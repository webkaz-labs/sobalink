package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/transfer"
)

func (o *outboxCoordinator) payloadRootLocked(p *outboxPayload) (*os.Root, error) {
	if !outboxOwnedID(p.ID) || !outboxOwnedID(p.Marker) {
		return nil, outboxError("outbox_payload_recovery_required")
	}
	root, err := o.root.OpenRoot(filepath.Join("payloads", p.ID))
	if err != nil {
		return nil, err
	}
	identity, err := outboxIdentity(root, ".", true)
	if err != nil || identity != p.RootIdentity {
		root.Close()
		return nil, outboxError("outbox_payload_recovery_required")
	}
	markerInfo, err := root.Lstat("owner")
	if err != nil || !markerInfo.Mode().IsRegular() {
		root.Close()
		return nil, outboxError("outbox_payload_recovery_required")
	}
	f, err := root.Open("owner")
	if err != nil {
		root.Close()
		return nil, err
	}
	opened, err := f.Stat()
	identity, idErr := outboxNativeIdentity(f)
	if err != nil || idErr != nil || !os.SameFile(markerInfo, opened) || identity != p.MarkerIdentity {
		f.Close()
		root.Close()
		return nil, outboxError("outbox_payload_recovery_required")
	}
	data, err := io.ReadAll(io.LimitReader(f, 128))
	f.Close()
	if err != nil || string(data) != "sobalink durable payload v1\n"+p.Marker+"\n" {
		root.Close()
		return nil, outboxError("outbox_payload_recovery_required")
	}
	return root, nil
}
func (o *outboxCoordinator) createPayloadLocked(p *outboxPayload) error {
	// The ownership reservation is durable before creating any payload entry.
	if err := o.saveLocked(); err != nil {
		return err
	}
	if err := config.SecureChildDirectory(o.path(), "payloads"); err != nil {
		return err
	}
	parent := filepath.Join(o.path(), "payloads")
	parentInfo, err := o.root.Lstat("payloads")
	if err != nil {
		return err
	}
	// Exclusive native creation establishes the protected private DACL on
	// Windows as well as 0700 on Unix. os.Mkdir followed by validation cannot
	// do this: an inherited Windows DACL is intentionally not accepted.
	created, err := config.CreatePrivateChildDirectoryBound(parent, p.ID, parentInfo)
	if err != nil {
		return err
	}
	createdIdentity, identityErr := outboxNativeIdentity(created)
	root, openErr := o.root.OpenRoot(filepath.Join("payloads", p.ID))
	closeErr := created.Close()
	if err := errors.Join(identityErr, openErr, closeErr); err != nil {
		if root != nil {
			_ = root.Close()
		}
		return err
	}
	defer root.Close()
	identity, err := outboxIdentity(root, ".", true)
	if err != nil || identity != createdIdentity {
		return outboxError("outbox_payload_recovery_required")
	}
	p.RootIdentity = identity
	f, err := root.OpenFile("owner", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	p.MarkerIdentity, err = outboxNativeIdentity(f)
	if err != nil {
		f.Close()
		return err
	}
	_, err = f.WriteString("sobalink durable payload v1\n" + p.Marker + "\n")
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	if err = outboxSyncDirectory(root); err != nil {
		return err
	}
	return o.saveLocked()
}
func (o *outboxCoordinator) verifyPayloadLocked(p *outboxPayload) error {
	if p.Released {
		return outboxError("outbox_payload_missing")
	}
	root, err := o.payloadRootLocked(p)
	if err != nil {
		return err
	}
	defer root.Close()
	expected := map[string]bool{"owner": true}
	for _, entry := range p.Manifest.Entries {
		if entry.Kind != transfer.File {
			continue
		}
		expected[entry.ID] = true
		info, err := root.Lstat(entry.ID)
		if err != nil || !info.Mode().IsRegular() || info.Size() != entry.Size {
			return outboxError("outbox_payload_corrupt")
		}
		f, err := root.Open(entry.ID)
		if err != nil {
			return err
		}
		opened, e := f.Stat()
		identity, idErr := outboxNativeIdentity(f)
		if e != nil || idErr != nil || identity != p.FileIdentities[entry.ID] || !os.SameFile(info, opened) {
			f.Close()
			return outboxError("outbox_payload_corrupt")
		}
		hash := sha256.New()
		n, e := io.Copy(hash, io.LimitReader(stagingContextReader{o.core.ctx, f}, entry.Size+1))
		f.Close()
		if e != nil || n != entry.Size || hex.EncodeToString(hash.Sum(nil)) != entry.SHA256 {
			return outboxError("outbox_payload_corrupt")
		}
	}
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	entries, err := dir.ReadDir(len(expected) + 1)
	if err != nil && err != io.EOF {
		return err
	}
	if len(entries) != len(expected) {
		return outboxError("outbox_payload_recovery_required")
	}
	for _, entry := range entries {
		if !expected[entry.Name()] {
			return outboxError("outbox_payload_recovery_required")
		}
	}
	return nil
}
func (c *Core) stageOutboxPlan(ctx context.Context, p *outboxPreparation, plan sourcePlan) error {
	manifest, sources, err := plan.Hash(ctx)
	if err != nil {
		return err
	}
	o := c.outboxCopy()
	o.mu.Lock()
	payload := o.state.Payloads[p.intent.PayloadID]
	root, err := o.payloadRootLocked(payload)
	if err == nil && o.stagingRootOpened != nil {
		o.stagingRootOpened(root)
	}
	o.mu.Unlock()
	if err != nil {
		return err
	}
	rootOpen := true
	defer func() {
		if rootOpen {
			_ = root.Close()
		}
	}()
	for _, source := range sources {
		from, err := source.Open()
		if err != nil {
			return err
		}
		var entry transfer.Entry
		for _, e := range manifest.Entries {
			if e.ID == source.EntryID {
				entry = e
				break
			}
		}
		err = c.stageOutboxFile(ctx, root, payload, entry, from)
		from.Close()
		if err != nil {
			return err
		}
	}
	if err = outboxSyncDirectory(root); err != nil {
		return err
	}
	// The prepared state releases the physical staging-owner fence. Join its
	// directory handle before another local request can discard and retire it.
	rootOpen = false
	if err = root.Close(); err != nil {
		return err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if p.discarded || ctx.Err() != nil {
		return outboxError("outbox_stale")
	}
	return o.withOwnedLocked(func() error {
		payload.Manifest = manifest
		payload.Ready = true
		p.intent.View.Entries = append([]transfer.Entry{}, manifest.Entries...)
		return o.finishPreparationLocked(p)
	})
}
func (c *Core) stageOutboxFile(ctx context.Context, root *os.Root, payload *outboxPayload, entry transfer.Entry, from io.Reader) error {
	if err := c.checkStagingDirectory(ctx, root.Name()); err != nil {
		return err
	}
	var f *os.File
	o := c.outboxCopy()
	o.mu.Lock()
	err := o.withOwnedLocked(func() error {
		if err := o.checkLocked(); err != nil {
			return err
		}
		var err error
		f, err = root.OpenFile(entry.ID, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		identity, err := outboxNativeIdentity(f)
		if err != nil {
			return err
		}
		payload.FileIdentities[entry.ID] = identity
		return o.saveLocked()
	})
	o.mu.Unlock()
	if err != nil {
		if f != nil {
			f.Close()
		}
		return err
	}

	hash := sha256.New()
	n, err := copyStaging(ctx, io.MultiWriter(stagingDiskWriter{c, ctx, f}, hash), from, entry.Size)
	if err == nil && (n != entry.Size || entry.SHA256 != "" && hex.EncodeToString(hash.Sum(nil)) != entry.SHA256) {
		err = transfer.ErrIntegrity
	}
	if err == nil {
		err = f.Sync()
	}
	return errors.Join(err, f.Close())
}
func (o *outboxCoordinator) cleanupPayloadOwnedLocked(p *outboxPayload) error {
	if p.Released {
		return nil
	}
	for _, v := range o.state.Intents {
		if v.PayloadID != p.ID {
			continue
		}
		for i, d := range v.Deliveries {
			if o.runs[d.Offer.DeliveryID] != nil {
				return nil
			}
			s := v.View.Children[i].Status
			if s != "completed" && s != "cancelled" && s != "expired" {
				return nil
			}
		}
	}
	for _, prep := range o.prepared {
		if prep.intent.PayloadID == p.ID && !prep.discarded {
			return nil
		}
	}
	// Preserve a durable non-runnable disposition before removing owned bytes.
	p.CleanupPending = true
	if err := o.saveLocked(); err != nil {
		return err
	}
	root, err := o.payloadRootLocked(p)
	if err != nil {
		return err
	}
	defer root.Close()
	allowed := map[string]bool{"owner": true}
	for id := range p.FileIdentities {
		if !config.ValidPeerID(id) || id == "owner" {
			return outboxError("outbox_payload_recovery_required")
		}
		allowed[id] = true
	}
	for _, e := range p.Manifest.Entries {
		if e.Kind == transfer.File {
			allowed[e.ID] = true
		}
	}
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	entries, err := dir.ReadDir(len(allowed) + 1)
	dir.Close()
	if err != nil && err != io.EOF {
		return err
	}
	for _, entry := range entries {
		if !allowed[entry.Name()] {
			return outboxError("outbox_payload_recovery_required")
		}
		info, err := root.Lstat(entry.Name())
		if err != nil || !info.Mode().IsRegular() {
			return outboxError("outbox_payload_recovery_required")
		}
		expected := p.FileIdentities[entry.Name()]
		if entry.Name() == "owner" {
			expected = p.MarkerIdentity
		}
		identity, e := outboxIdentity(root, entry.Name(), false)
		if e != nil || identity != expected {
			return outboxError("outbox_payload_recovery_required")
		}
	}
	for _, entry := range entries {
		if entry.Name() == "owner" {
			continue
		}
		if err := root.Remove(entry.Name()); err != nil {
			return err
		}
	}
	if err = outboxSyncDirectory(root); err != nil {
		return err
	}
	// Keep the tiny immutable ownership marker: restart never has to guess whether
	// a missing object was ours. Reclaimed byte accounting requires confirmed sync.
	p.Released = true
	p.CleanupPending = false
	return o.saveLocked()
}

func outboxIdentity(root *os.Root, name string, directory bool) (string, error) {
	info, err := root.Lstat(name)
	if err != nil || directory && !info.IsDir() || !directory && !info.Mode().IsRegular() {
		return "", outboxError("outbox_payload_recovery_required")
	}
	f, err := root.Open(name)
	if err != nil {
		return "", err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return "", outboxError("outbox_payload_recovery_required")
	}
	return outboxNativeIdentity(f)
}

func (o *outboxCoordinator) cleanupPayloadLocked(p *outboxPayload) error {
	return o.withOwnedLocked(func() error { return o.cleanupPayloadOwnedLocked(p) })
}
