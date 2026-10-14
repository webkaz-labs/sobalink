package core

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/resource"
)

// Core.mu is the sole lock for this store. The retained root and native file
// identity bind every transaction to the owned profile, never a replacement.
type messageStore struct {
	state            messageEnvelope
	candidate        *messageEnvelope
	candidateDisk    []byte
	frozen           string
	root             *os.Root
	info             os.FileInfo
	confirmedDisk    []byte
	snapshotIdentity string
	firstUse         bool
	now              func() time.Time
	// Private deterministic fault seams; production uses the existing native
	// atomic lease and directory barriers.
	write         func(string, []byte) error
	afterWrite    func() error
	syncDirectory func(*os.Root) error
}

func (c *Core) initializeMessageStore(initialize bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.messageStore != nil || c.resourceLock == nil || c.resourceIdentity == "" || c.profile.DurableFiles == nil {
		return messageStoreError("message_store_recovery_required")
	}
	s := &messageStore{now: time.Now}
	err := c.resourceLock.TryWithOwnershipInfo(c.dir, func(profile, lock os.FileInfo) error {
		c.messageStore = s
		root, e := os.OpenRoot(c.dir)
		if e != nil {
			return e
		}
		s.root = root
		s.info, e = root.Stat(".")
		if e != nil {
			return e
		}
		if !os.SameFile(profile, s.info) {
			return messageStoreError("message_store_recovery_required")
		}
		data, identity, e := s.readSnapshot()
		if errors.Is(e, os.ErrNotExist) {
			if !initialize {
				return messageStoreError("message_store_recovery_required")
			}
			s.firstUse = true
			s.state = messageEnvelope{Version: messageStoreVersion, Identity: c.resourceIdentity, Epoch: c.profile.DurableFiles.Epoch, HighWater: s.now().UTC(), Immediate: []Message{}, Outgoing: map[string]*messageOutgoing{}, Incoming: map[string]*messageIncoming{}}
		} else if e != nil {
			return e
		} else {
			var state messageEnvelope
			if e = resource.Decode(data, messageStoreHardBytes, &state); e != nil {
				return e
			}
			if e = c.validateMessageEnvelopeLocked(state); e != nil {
				return e
			}
			if initialize && (len(state.Immediate) != 0 || len(state.Outgoing) != 0 || len(state.Incoming) != 0) {
				return messageStoreError("message_store_recovery_required")
			}
			s.state = state
			s.confirmedDisk = data
			s.snapshotIdentity = identity
		}
		if e = c.validateMessageEnvelopeLocked(s.state); e != nil {
			return e
		}
		if e = c.checkMessageCapacityLocked(s.state, s.state); e != nil {
			return e
		}
		if s.now().UTC().Before(s.state.HighWater) {
			return messageStoreError("message_clock_rollback")
		}
		// Re-certify the exact saved envelope on reopen before any receipt or send
		// authority. This also closes a previous process's uncertain rename window.
		if s.firstUse {
			data, e = json.Marshal(s.state)
		} else {
			data = s.confirmedDisk
		}
		if e != nil {
			return e
		}
		identity, e = c.writeOwnedMessageSnapshotLocked(s, data, profile, lock)
		if e != nil {
			if errors.Is(e, config.ErrAtomicCommitted) {
				candidate := cloneMessageEnvelope(s.state)
				s.candidate = &candidate
				s.candidateDisk = append([]byte(nil), data...)
			}
			return e
		}
		s.firstUse = false
		s.confirmedDisk = append([]byte(nil), data...)
		s.snapshotIdentity = identity

		c.messages = c.messageProjectionLocked()
		return nil
	})
	if err != nil {
		s.frozen = "message_store_recovery_required"
		if errors.Is(err, config.ErrLifecycleBusy) {
			return messageStoreError("message_storage_busy")
		}
		if errors.Is(err, config.ErrAtomicCommitted) {
			s.frozen = "message_storage_uncertain"
		}
		return errors.Join(messageStoreError(s.frozen), err)
	}
	return nil
}

func cloneMessageEnvelope(state messageEnvelope) messageEnvelope {
	data, _ := json.Marshal(state)
	var out messageEnvelope
	_ = json.Unmarshal(data, &out)
	return out
}

// The callback runs only after nonblocking lifecycle admission and exact disk
// verification. It must not reacquire Core.mu, network, join, recursively save,
// or take file-outbox/Manager locks. Only a certified write promotes the clone.
func (c *Core) messageStoreReadyLocked() error {
	if c.messageStore == nil || c.messageStore.root == nil || c.resourceLock == nil || c.profile.DurableFiles == nil || c.profile.DurableFiles.Phase != "ready" || c.durableProfileError != "" {
		return messageStoreError("message_store_recovery_required")
	}
	if c.messageStore.frozen != "" {
		return messageStoreError(c.messageStore.frozen)
	}
	return nil
}
func (c *Core) mutateMessageStoreLocked(change func(*messageEnvelope) error) error {
	if err := c.messageStoreReadyLocked(); err != nil {
		return err
	}
	s := c.messageStore
	if s == nil || s.root == nil || c.resourceLock == nil {
		return messageStoreError("message_store_recovery_required")
	}
	if s.frozen != "" {
		return messageStoreError(s.frozen)
	}
	err := c.resourceLock.TryWithOwnershipInfo(c.dir, func(profile, lock os.FileInfo) error {
		if err := c.checkMessageSnapshotLocked(s, profile, lock); err != nil {
			s.frozen = "message_store_recovery_required"
			return err
		}
		now := s.now().UTC()
		if now.Before(s.state.HighWater) {
			s.frozen = "message_clock_rollback"
			return messageStoreError(s.frozen)
		}
		next := cloneMessageEnvelope(s.state)
		if err := change(&next); err != nil {
			return err
		}
		// Detach values introduced by the callback from process-local preparation
		// or request pointers before either certification or candidate retention.
		next = cloneMessageEnvelope(next)
		next.HighWater = now
		if err := c.validateMessageEnvelopeLocked(next); err != nil {
			return err
		}
		if err := validateMessageTransition(s.state, next); err != nil {
			return err
		}
		if err := c.checkMessageCapacityLocked(s.state, next); err != nil {
			return err
		}
		data, err := json.Marshal(next)
		if err != nil {
			return err
		}
		identity, err := c.writeOwnedMessageSnapshotLocked(s, data, profile, lock)
		if err != nil {
			if errors.Is(err, config.ErrAtomicCommitted) {
				s.candidate = &next
				s.candidateDisk = append([]byte(nil), data...)
				s.frozen = "message_storage_uncertain"
				c.messageHistoryUncertain = true
			}
			return err
		}
		s.state = next
		s.confirmedDisk = append([]byte(nil), data...)
		s.snapshotIdentity = identity
		s.firstUse = false
		c.messages = c.messageProjectionLocked()
		c.messageHistoryUncertain = false
		return nil
	})
	if errors.Is(err, config.ErrLifecycleBusy) {
		return messageStoreError("message_storage_busy")
	}
	if s.frozen != "" {
		return errors.Join(messageStoreError(s.frozen), err)
	}
	return err
}

// Reconcile only the exact retained publication; it never reruns a mutation or
// reopens admission based on an absent/replaced file. The caller holds Core.mu.
func (c *Core) reconcileMessageStoreLocked() error {
	s := c.messageStore
	if s == nil || s.candidate == nil || s.frozen != "message_storage_uncertain" {
		if s != nil && s.frozen == "" {
			return nil
		}
		return messageStoreError("message_store_recovery_required")
	}
	err := c.resourceLock.TryWithOwnershipInfo(c.dir, func(profile, lock os.FileInfo) error {
		if err := c.checkMessageBindingLocked(s, profile, lock); err != nil {
			return err
		}
		if s.now().UTC().Before(s.candidate.HighWater) {
			return messageStoreError("message_clock_rollback")
		}
		data, _, err := s.readSnapshot()
		if err != nil || !bytes.Equal(data, s.candidateDisk) {
			return messageStoreError("message_storage_uncertain")
		}
		identity, err := c.writeOwnedMessageSnapshotLocked(s, s.candidateDisk, profile, lock)
		if err != nil {
			return err
		}
		s.state = *s.candidate
		s.confirmedDisk = append([]byte(nil), s.candidateDisk...)
		s.snapshotIdentity = identity
		s.firstUse = false
		s.candidate = nil
		s.candidateDisk = nil
		s.frozen = ""
		c.messages = c.messageProjectionLocked()
		c.messageHistoryUncertain = false
		return nil
	})
	if errors.Is(err, config.ErrLifecycleBusy) {
		return messageStoreError("message_storage_busy")
	}
	if err != nil {
		return errors.Join(messageStoreError("message_storage_uncertain"), err)
	}
	return nil
}

func (s *messageStore) readSnapshot() ([]byte, string, error) {
	info, err := s.root.Lstat("messages.json")
	if err != nil {
		return nil, "", err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > messageStoreHardBytes {
		return nil, "", messageStoreError("message_store_recovery_required")
	}
	f, err := s.root.Open("messages.json")
	if err != nil {
		return nil, "", err
	}
	actual, err := f.Stat()
	if err != nil || !os.SameFile(info, actual) {
		f.Close()
		return nil, "", messageStoreError("message_store_recovery_required")
	}
	identity, err := outboxNativeIdentity(f)
	if err != nil {
		f.Close()
		return nil, "", err
	}
	data, readErr := io.ReadAll(io.LimitReader(f, messageStoreHardBytes+1))
	closeErr := f.Close()
	if err = errors.Join(readErr, closeErr); err != nil {
		return nil, "", err
	}
	after, err := s.root.Lstat("messages.json")
	if err != nil || !os.SameFile(info, after) || len(data) > messageStoreHardBytes {
		return nil, "", messageStoreError("message_store_recovery_required")
	}
	return data, identity, nil
}
func (c *Core) checkMessageBindingLocked(s *messageStore, profile, lock os.FileInfo) error {
	current, err := os.Lstat(c.dir)
	if err != nil || !current.IsDir() || current.Mode()&os.ModeSymlink != 0 || !os.SameFile(profile, current) || !os.SameFile(s.info, current) {
		return messageStoreError("message_store_recovery_required")
	}
	current, err = os.Lstat(filepath.Join(c.dir, "process.lock"))
	if err != nil || !current.Mode().IsRegular() || !os.SameFile(lock, current) {
		return messageStoreError("message_store_recovery_required")
	}
	opened, err := s.root.Stat(".")
	if err != nil || !os.SameFile(profile, opened) {
		return messageStoreError("message_store_recovery_required")
	}
	return nil
}
func (c *Core) checkMessageSnapshotLocked(s *messageStore, profile, lock os.FileInfo) error {
	if err := c.checkMessageBindingLocked(s, profile, lock); err != nil {
		return err
	}
	if s.firstUse {
		if _, err := s.root.Lstat("messages.json"); !errors.Is(err, os.ErrNotExist) {
			return messageStoreError("message_store_recovery_required")
		}
		return nil
	}
	data, identity, err := s.readSnapshot()
	if err != nil || identity != s.snapshotIdentity || !bytes.Equal(data, s.confirmedDisk) {
		return messageStoreError("message_store_recovery_required")
	}
	return nil
}

func (c *Core) writeOwnedMessageSnapshotLocked(s *messageStore, data []byte, profile, lock os.FileInfo) (identity string, err error) {
	published := false
	defer func() {
		if err != nil && published {
			err = errors.Join(config.ErrAtomicCommitted, err)
		}
	}()
	if len(data) > messageStoreHardBytes {
		return "", messageStoreError("message_evidence_capacity")
	}
	if err = c.checkMessageBindingLocked(s, profile, lock); err != nil {
		return "", err
	}
	parent, err := s.root.Open(".")
	if err != nil {
		return "", err
	}
	defer func() { err = errors.Join(err, parent.Close()) }()
	lease, err := config.AcquireAtomicWriteLease(filepath.Join(c.dir, "messages.json"))
	if err != nil {
		return "", err
	}
	defer func() { err = errors.Join(err, lease.Close()) }()
	if err = lease.CheckParent(parent); err != nil {
		return "", err
	}
	write := lease.Write
	if c.atomicWrite != nil {
		write = c.atomicWrite
	}
	if s.write != nil {
		write = s.write
	}
	err = write(filepath.Join(c.dir, "messages.json"), data)
	published = atomicPublished(err)
	if err != nil {
		return "", err
	}
	if s.afterWrite != nil {
		if err = s.afterWrite(); err != nil {
			return "", err
		}
	}
	saved, identity, err := s.readSnapshot()
	if err != nil {
		return "", err
	}
	if !bytes.Equal(saved, data) {
		return "", messageStoreError("message_store_recovery_required")
	}
	syncDirectory := outboxSyncDirectory
	if s.syncDirectory != nil {
		syncDirectory = s.syncDirectory
	}
	if err = syncDurableProfileNamespaces(s.root, syncDirectory); err != nil {
		return "", err
	}
	if err = lease.CheckParent(parent); err != nil {
		return "", err
	}
	if err = c.checkMessageBindingLocked(s, profile, lock); err != nil {
		return "", err
	}
	after, afterIdentity, err := s.readSnapshot()
	if err != nil {
		return "", err
	}
	if identity != afterIdentity || !bytes.Equal(after, data) {
		return "", messageStoreError("message_store_recovery_required")
	}
	return identity, nil
}

func (c *Core) messageProjectionLocked() []Message {
	if c.messageStore == nil {
		return []Message{}
	}
	return projectMessageEnvelope(c.messageStore.state)
}
func projectMessageEnvelope(state messageEnvelope) []Message {
	rows := append([]Message{}, state.Immediate...)
	ids := make([]string, 0, len(state.Outgoing))
	for id := range state.Outgoing {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		row := state.Outgoing[id]
		if row.Visible && row.BodyRetained {
			rows = append(rows, Message{ID: id, PeerID: row.View.PeerID, Direction: "outgoing", Text: row.Text, CreatedAt: row.View.CreatedAt, Status: row.View.Status})
		}
	}
	ids = ids[:0]
	for id := range state.Incoming {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		row := state.Incoming[id]
		if row.Visible && row.BodyRetained && row.Receipt != nil {
			rows = append(rows, Message{ID: id, PeerID: row.PeerID, Direction: "incoming", Text: row.Text, CreatedAt: row.Receipt.SavedAt, Status: "received"})
		}
	}
	// Preserve immediate-history insertion order for equal timestamps. Durable
	// map rows enter the stable sort in deterministic key order.
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].CreatedAt.Before(rows[j].CreatedAt) })
	return rows
}

func (c *Core) messageQueueViewLocked(id string) (TextOutboxRecord, error) {
	s := c.messageStore
	if s == nil {
		return TextOutboxRecord{}, messageStoreError("message_store_recovery_required")
	}
	row := s.state.Outgoing[id]
	if s.candidate != nil && s.candidate.Outgoing[id] != nil {
		row = s.candidate.Outgoing[id]
	}
	if row == nil {
		if s.frozen != "" {
			return TextOutboxRecord{}, messageStoreError(s.frozen)
		}
		return TextOutboxRecord{}, messageStoreError("message_not_found")
	}
	data, _ := json.Marshal(row.View)
	var view TextOutboxRecord
	_ = json.Unmarshal(data, &view)
	if row.BodyRetained {
		view.Text = row.Text
	}
	if row.Receipt != nil {
		receipt := *row.Receipt
		view.Receipt = &receipt
	}
	if s.frozen != "" {
		view.ReasonCode = s.frozen
		return view, messageStoreError(s.frozen)
	}
	return view, nil
}

func (c *Core) closeMessageStoreLocked() error {
	if c.messageStore == nil || c.messageStore.root == nil {
		return nil
	}
	err := c.messageStore.root.Close()
	c.messageStore.root = nil
	return err
}
