package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"sync"
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/resource"
	"github.com/webkaz-labs/sobalink/internal/transfer"
)

const outboxNamespace = "durable-outbox-v1"
const outboxMaxMetadata = 16 << 20
const outboxMaxChildren = 256
const outboxReceiptReserve = 2048
const outboxMarkerBytes = int64(len("sobalink durable payload v1\n") + 32 + 1)

// Local-control receipts distinguish admission, work, and exact remote evidence.
// Neither cancellation nor expiry asserts that previously exposed bytes vanished.
type OutboxFile struct {
	BatchID string `json:"batchId"`
	FileID  string `json:"fileId"`
	Size    int64  `json:"size"`
	SHA256  string `json:"sha256"`
}
type OutboxChild struct {
	PeerID          string       `json:"peerId"`
	DeliveryID      string       `json:"deliveryId"`
	Status          string       `json:"status"`
	DeliveryOutcome string       `json:"deliveryOutcome"`
	ReasonCode      string       `json:"reasonCode"`
	Files           []OutboxFile `json:"files"`
}
type OutboxRecord struct {
	Version         int              `json:"version"`
	Kind            string           `json:"kind"`
	OutboxID        string           `json:"outboxId"`
	RequestID       string           `json:"requestId"`
	ProcessID       string           `json:"processId,omitempty"`
	ReviewRevision  string           `json:"reviewRevision"`
	Admission       string           `json:"admission"`
	Status          string           `json:"status"`
	DeliveryOutcome string           `json:"deliveryOutcome"`
	ReasonCode      string           `json:"reasonCode"`
	PeerIDs         []string         `json:"peerIds"`
	Entries         []transfer.Entry `json:"entries"`
	Children        []OutboxChild    `json:"children"`
	TotalBytes      int64            `json:"totalBytes"`
	CreatedAt       time.Time        `json:"createdAt"`
	ExpiresAt       time.Time        `json:"expiresAt"`
	UpdatedAt       time.Time        `json:"updatedAt"`
}
type outboxDelivery struct {
	Trust     Trust                    `json:"trust"`
	Offer     transfer.DurableOffer    `json:"offer"`
	Lease     *transfer.DurableLease   `json:"lease,omitempty"`
	Receipt   *transfer.DurableReceipt `json:"receipt,omitempty"`
	Attempted bool                     `json:"attempted"`
	Exposure  bool                     `json:"exposure"`
	Cancelled bool                     `json:"cancelled"`
}
type outboxIntent struct {
	View       OutboxRecord     `json:"view"`
	Identity   string           `json:"identity"`
	Network    string           `json:"network"`
	SenderID   string           `json:"senderId"`
	Signature  string           `json:"signature"`
	PayloadID  string           `json:"payloadId"`
	Deliveries []outboxDelivery `json:"deliveries"`
}
type outboxPayload struct {
	ID               string            `json:"id"`
	RootIdentity     string            `json:"rootIdentity"`
	MarkerIdentity   string            `json:"markerIdentity"`
	FileIdentities   map[string]string `json:"fileIdentities"`
	Marker           string            `json:"marker"`
	Bytes            int64             `json:"bytes"`
	Manifest         transfer.Manifest `json:"manifest"`
	Ready            bool              `json:"ready"`
	Released         bool              `json:"released"`
	NamespaceRemoval bool              `json:"namespaceRemoval"`
	CleanupPending   bool              `json:"cleanupPending"`
}
type outboxEnvelope struct {
	Version   int                       `json:"version"`
	Identity  string                    `json:"identity"`
	Epoch     string                    `json:"epoch"`
	HighWater time.Time                 `json:"highWater"`
	Intents   map[string]*outboxIntent  `json:"intents"`
	Payloads  map[string]*outboxPayload `json:"payloads"`
}
type outboxPreparation struct {
	intent    *outboxIntent
	ctx       context.Context
	cancel    context.CancelFunc
	staging   bool
	discarded bool
}
type outboxCoordinator struct {
	owned             *outboxOwnedScope
	firstUse          bool
	confirmedDisk     []byte
	snapshotIdentity  string
	mu                sync.Mutex
	core              *Core
	state             outboxEnvelope
	root              *os.Root
	info              os.FileInfo
	frozen            string
	prepared          map[string]*outboxPreparation
	completions       map[string]outboxCompletion
	runs              map[string]*outgoingBatch
	next              map[string]time.Time
	certified         map[string]OutboxRecord
	capabilities      map[string]bool
	orphanBytes       int64
	now               func() time.Time
	write             func(string, []byte) error
	afterWrite        func() error
	syncDirectory     func(*os.Root) error
	stagingRootOpened func(*os.Root) // test-only observation of the physical staging owner
}

func outboxError(code string) error {
	return &localCommandError{code, "outbox requires review; inspect the original request and its retained delivery evidence"}
}
func cloneOutboxRecord(r OutboxRecord) OutboxRecord {
	b, _ := json.Marshal(r)
	var v OutboxRecord
	_ = json.Unmarshal(b, &v)
	return v
}
func (o *outboxCoordinator) path() string { return filepath.Join(o.core.dir, outboxNamespace) }
func (o *outboxCoordinator) checkLocked() error {
	if o.frozen != "" {
		return outboxError(o.frozen)
	}
	if o.root == nil || o.core.resourceIdentity != o.state.Identity || o.core.resourceLock == nil {
		return outboxError("outbox_recovery_required")
	}
	return o.withOwnedInfoLocked(func(profile, lock os.FileInfo) error {
		s, e := os.Lstat(o.path())
		if e != nil || !s.IsDir() || !os.SameFile(s, o.info) {
			return outboxError("outbox_recovery_required")
		}
		return nil
	})
}
func (o *outboxCoordinator) clockLocked() error {
	now := o.now().UTC()
	if now.Before(o.state.HighWater) {
		o.frozen = "outbox_clock_rollback"
		return outboxError(o.frozen)
	}
	return nil
}
func (o *outboxCoordinator) saveLocked() error {
	if err := o.checkLocked(); err != nil {
		return err
	}
	if err := o.clockLocked(); err != nil {
		return err
	}
	o.state.HighWater = o.now().UTC()
	data, err := json.Marshal(o.state)
	if err != nil {
		return err
	}
	// Reserve worst-case exact receipts and cancellation/cleanup state at admission.
	reserved := len(data)
	for _, p := range o.prepared {
		reserved += int(outboxPreparationMetadata(transfer.Manifest{Entries: p.intent.View.Entries}, len(p.intent.Deliveries)))
	}
	for _, v := range o.state.Intents {
		for range v.Deliveries {
			reserved += outboxReceiptReserve + len(v.View.Entries)*512
		}
	}
	if reserved > outboxMaxMetadata {
		o.frozen = "outbox_capacity"
		return outboxError("outbox_capacity")
	}
	var publishedIdentity string
	err = o.withOwnedInfoLocked(func(profile, lock os.FileInfo) (result error) {
		checkBinding := func() error {
			current, e := os.Lstat(o.core.dir)
			if e != nil || !current.IsDir() || !os.SameFile(profile, current) {
				return outboxError("outbox_recovery_required")
			}
			current, e = os.Lstat(filepath.Join(o.core.dir, "process.lock"))
			if e != nil || !current.Mode().IsRegular() || !os.SameFile(lock, current) {
				return outboxError("outbox_recovery_required")
			}
			current, e = os.Lstat(o.path())
			if e != nil || !current.IsDir() || !os.SameFile(o.info, current) {
				return outboxError("outbox_recovery_required")
			}
			return nil
		}
		if e := checkBinding(); e != nil {
			return e
		}
		if o.firstUse {
			if _, e := o.root.Lstat("state.json"); !errors.Is(e, os.ErrNotExist) {
				return outboxError("outbox_recovery_required")
			}
		} else {
			prior, identity, e := o.readSnapshotLocked()
			if e != nil || identity != o.snapshotIdentity || !bytes.Equal(prior, o.confirmedDisk) {
				return outboxError("outbox_recovery_required")
			}
		}
		path := filepath.Join(o.path(), "state.json")
		lease, e := config.AcquireAtomicWriteLease(path)
		if e != nil {
			return e
		}
		defer func() { result = errors.Join(result, lease.Close()) }()
		parent, e := o.root.Open(".")
		if e != nil {
			return e
		}
		defer parent.Close()
		if e = lease.CheckParent(parent); e != nil {
			return e
		}
		namespaceInfo, e := o.root.Lstat(".sobalink-atomic-v1")
		if e != nil || !namespaceInfo.IsDir() {
			return outboxError("outbox_recovery_required")
		}
		namespace, e := o.root.OpenRoot(".sobalink-atomic-v1")
		if e != nil {
			return e
		}
		defer namespace.Close()
		opened, e := namespace.Stat(".")
		if e != nil || !os.SameFile(opened, namespaceInfo) {
			return outboxError("outbox_recovery_required")
		}
		write := lease.Write
		if o.core.atomicWrite != nil {
			write = o.core.atomicWrite
		}
		if o.write != nil {
			write = o.write
		}
		if e = write(path, data); e != nil {
			return e
		}
		if o.afterWrite != nil {
			if e = o.afterWrite(); e != nil {
				return e
			}
		}
		info, e := o.root.Lstat("state.json")
		if e != nil || !info.Mode().IsRegular() || info.Size() != int64(len(data)) {
			return outboxError("outbox_recovery_required")
		}
		f, e := o.root.Open("state.json")
		if e != nil {
			return e
		}
		actual, e := f.Stat()
		if e != nil || !os.SameFile(info, actual) {
			f.Close()
			return outboxError("outbox_recovery_required")
		}
		publishedIdentity, e = outboxNativeIdentity(f)
		if e != nil {
			f.Close()
			return e
		}
		saved, e := io.ReadAll(io.LimitReader(f, outboxMaxMetadata+1))
		closeErr := f.Close()
		if e != nil || closeErr != nil || !bytes.Equal(saved, data) {
			return outboxError("outbox_recovery_required")
		}
		syncDirectory := outboxSyncDirectory
		if o.syncDirectory != nil {
			syncDirectory = o.syncDirectory
		}
		// Both rename namespaces remain exclusively owned through the barriers.
		if e = syncDirectory(namespace); e != nil {
			return e
		}
		if e = syncDirectory(o.root); e != nil {
			return e
		}
		current, e := o.root.Lstat("state.json")
		if e != nil || !os.SameFile(info, current) {
			return outboxError("outbox_recovery_required")
		}
		current, e = o.root.Lstat(".sobalink-atomic-v1")
		if e != nil || !os.SameFile(namespaceInfo, current) {
			return outboxError("outbox_recovery_required")
		}
		if e = lease.CheckParent(parent); e != nil {
			return e
		}
		return checkBinding()
	})
	if err != nil {
		o.frozen = "outbox_storage_uncertain"
		return outboxError(o.frozen)
	}
	o.firstUse = false
	o.confirmedDisk = append([]byte{}, data...)
	o.snapshotIdentity = publishedIdentity
	for id, v := range o.state.Intents {
		o.certified[id] = cloneOutboxRecord(v.View)
	}
	o.updateAccountingLocked()
	return nil
}
func (o *outboxCoordinator) updateAccountingLocked() {
	var bytes, children, metadata int64
	for _, p := range o.state.Payloads {
		// Marker reservation is released only when its namespace row is durably retired.
		bytes += outboxMarkerBytes
		if !p.Released {
			bytes += p.Bytes
		}
	}
	for _, v := range o.state.Intents {
		children += int64(len(v.Deliveries))
		metadata += transfer.ManifestMetadataBytes(transfer.Manifest{Entries: v.View.Entries})*int64(len(v.Deliveries)) + 1024
	}
	for _, p := range o.prepared {
		if !p.discarded {
			children += int64(len(p.intent.Deliveries))
		}
		metadata += transfer.ManifestMetadataBytes(transfer.Manifest{Entries: p.intent.View.Entries})*int64(len(p.intent.Deliveries)) + 1024
	}
	o.core.outboxReserved.Store(bytes + o.orphanBytes)
	o.core.outboxChildren.Store(children)
	o.core.outboxMetadata.Store(metadata)
}
func (c *Core) initializeOutbox(initialize bool) error {
	// Startup owns exactly one coordinator. Replacing it could lose the retained
	// root handle (notably preventing Windows cleanup) or abandon live owners.
	// Recovery requires closing this Core and opening a new one under its lock.
	if c.outboxCopy() != nil {
		return outboxError("outbox_recovery_required")
	}
	o := &outboxCoordinator{core: c, prepared: map[string]*outboxPreparation{}, runs: map[string]*outgoingBatch{}, completions: map[string]outboxCompletion{}, next: map[string]time.Time{}, certified: map[string]OutboxRecord{}, capabilities: map[string]bool{}, now: time.Now}
	defer func() { c.mu.Lock(); c.outbox = o; c.mu.Unlock() }()
	if c.resourceLock == nil || c.resourceIdentity == "" {
		o.frozen = "outbox_recovery_required"
		return outboxError(o.frozen)
	}
	err := c.resourceLock.WithOwnershipInfo(c.dir, func(profile, lock os.FileInfo) error {
		if err := config.SecureChildDirectoryBound(c.dir, outboxNamespace, profile); err != nil {
			return err
		}
		root, err := os.OpenRoot(o.path())
		if err != nil {
			return err
		}
		o.root = root
		o.info, err = root.Stat(".")
		if err != nil {
			return err
		}
		stateInfo, stateErr := root.Lstat("state.json")
		if stateErr == nil && !stateInfo.Mode().IsRegular() {
			return outboxError("outbox_recovery_required")
		}
		f, err := root.Open("state.json")
		if errors.Is(err, os.ErrNotExist) {
			if !initialize {
				return outboxError("outbox_recovery_required")
			}
			entries, e := root.Open(".")
			if e != nil {
				return e
			}
			names, e := entries.ReadDir(4)
			entries.Close()
			if e != nil && e != io.EOF {
				return e
			}
			for _, name := range names {
				if name.Name() != ".sobalink-atomic-v1" || !name.IsDir() {
					return outboxError("outbox_recovery_required")
				}
			}
			o.firstUse = true
			o.state = outboxEnvelope{Version: 1, Identity: c.resourceIdentity, Epoch: c.profileCopy().DurableFiles.Epoch, HighWater: o.now().UTC(), Intents: map[string]*outboxIntent{}, Payloads: map[string]*outboxPayload{}}
		} else if err != nil {
			return err
		} else {
			defer f.Close()
			info, e := f.Stat()
			if e != nil || !info.Mode().IsRegular() || info.Size() > outboxMaxMetadata {
				return outboxError("outbox_recovery_required")
			}
			data, e := io.ReadAll(io.LimitReader(f, outboxMaxMetadata+1))
			if e != nil {
				return e
			}
			var candidate outboxEnvelope
			if e = resource.Decode(data, outboxMaxMetadata, &candidate); e != nil {
				return e
			}
			validator := &outboxCoordinator{core: c, state: candidate}
			if e = validator.validateLocked(); e != nil {
				return e
			}
			o.state = candidate
			o.confirmedDisk = append([]byte{}, data...)
			o.snapshotIdentity, e = outboxNativeIdentity(f)
			if e != nil {
				return e
			}
		}
		if initialize && (len(o.state.Intents) != 0 || len(o.state.Payloads) != 0) {
			return outboxError("outbox_recovery_required")
		}
		return o.validateLocked()
	})
	if err == nil {
		err = o.saveLocked()
	}
	if err != nil {
		if o.frozen == "" {
			o.frozen = "outbox_recovery_required"
		}
		return outboxError(o.frozen)
	}
	if err = o.inventoryLocked(); err != nil {
		o.frozen = "outbox_payload_recovery_required"
		o.updateAccountingLocked()
		return outboxError(o.frozen)
	}
	if err = o.resumeNamespaceRemovalLocked(); err != nil {
		o.frozen = "outbox_payload_recovery_required"
		o.updateAccountingLocked()
		return outboxError(o.frozen)
	}
	for _, p := range o.state.Payloads {
		if p.Released {
			continue
		}
		referenced := false
		for _, row := range o.state.Intents {
			referenced = referenced || row.PayloadID == p.ID
		}
		if p.CleanupPending || !referenced {
			if err = o.cleanupPayloadLocked(p); err != nil {
				o.frozen = "outbox_payload_recovery_required"
				o.updateAccountingLocked()
				return outboxError(o.frozen)
			}
		}
	}
	o.updateAccountingLocked()
	return nil
}
func (o *outboxCoordinator) validateLocked() error {
	s := o.state
	if s.Version != 1 || s.Identity != o.core.resourceIdentity || o.core.profileCopy().DurableFiles == nil || s.Epoch != o.core.profileCopy().DurableFiles.Epoch || s.Intents == nil || s.Payloads == nil || len(s.Intents) > outboxMaxChildren || len(s.Payloads) > outboxMaxChildren || s.HighWater.IsZero() {
		return outboxError("outbox_recovery_required")
	}
	count := 0
	seen := map[string]bool{}
	for id, v := range s.Intents {
		if v == nil {
			return outboxError("outbox_recovery_required")
		}
		r := v.View
		idCreated, idOK := outboxRequestCreated(id)
		if !idOK || r.ExpiresAt.After(idCreated.Add(outboxRequestMaximumLifetime)) {
			return outboxError("outbox_recovery_required")
		}
		if !config.ValidPeerID(id) || len(id) > 128 || r.OutboxID != id || r.RequestID != id || r.Version != 1 || r.Kind != "outbox" || r.Admission != "enqueued" || r.ProcessID != "" || r.ReviewRevision == "" || len(r.Children) != len(v.Deliveries) || len(r.PeerIDs) != len(v.Deliveries) || len(v.Deliveries) == 0 || v.Identity != s.Identity || v.SenderID == "" || s.Payloads[v.PayloadID] == nil || !r.ExpiresAt.After(r.CreatedAt) || r.ExpiresAt.Sub(r.CreatedAt) > 7*24*time.Hour {
			return outboxError("outbox_recovery_required")
		}
		payload := s.Payloads[v.PayloadID]
		if !payload.Ready || !reflect.DeepEqual(payload.Manifest.Entries, r.Entries) || !validOutboxStatus(r.Status) {
			return outboxError("outbox_recovery_required")
		}
		var bytes int64
		for _, entry := range r.Entries {
			if entry.Size < 0 || entry.Size > 1<<60-bytes {
				return outboxError("outbox_recovery_required")
			}
			bytes += entry.Size
		}
		if bytes != r.TotalBytes || bytes != payload.Bytes {
			return outboxError("outbox_recovery_required")
		}
		// Absolute hard parser limits avoid treating a lowered runtime quota as corruption.
		if err := transfer.ValidateManifest(payload.Manifest, transfer.Limits{MaxBatches: outboxMaxChildren, MaxEntries: 65536, MaxBatchBytes: 1 << 60, MaxFileBytes: 1 << 60, MaxReservedBytes: 1 << 60, MaxMetadataBytes: outboxMaxMetadata, MaxManifestBytes: outboxMaxMetadata, MaxDepth: 256}); err != nil {
			return outboxError("outbox_recovery_required")
		}
		count += len(v.Deliveries)
		for i, d := range v.Deliveries {
			child := r.Children[i]
			if !validOutboxStatus(child.Status) || child.Files == nil || d.Exposure && (d.Lease == nil || !d.Attempted) || d.Cancelled && (child.Status != "cancelled" && child.Status != "cancel_requested" && child.Status != "completed") {
				return outboxError("outbox_recovery_required")
			}
			switch child.DeliveryOutcome {
			case "not_attempted":
				if d.Attempted || d.Exposure || len(child.Files) > 0 {
					return outboxError("outbox_recovery_required")
				}
			case "unknown":
				if !d.Attempted {
					return outboxError("outbox_recovery_required")
				}
			case "partial":
				if len(child.Files) == 0 {
					return outboxError("outbox_recovery_required")
				}
			case "confirmed":
				if d.Receipt == nil || d.Receipt.State != transfer.Completed {
					return outboxError("outbox_recovery_required")
				}
			default:
				return outboxError("outbox_recovery_required")
			}
			if d.Receipt != nil {
				files, err := validateOutboxEvidence(d.Offer, d.Lease, transfer.DurableStatus{Lease: d.Receipt.Lease, State: d.Receipt.State, Receipt: d.Receipt}, nil)
				if err != nil || !reflect.DeepEqual(files, child.Files) {
					return outboxError("outbox_recovery_required")
				}
			} else if len(child.Files) > 0 {
				return outboxError("outbox_recovery_required")
			}

			digest, e := transfer.DurableDigest(d.Offer)
			if e != nil || digest != d.Offer.Digest || child.PeerID != d.Trust.ID || child.DeliveryID != d.Offer.DeliveryID || r.PeerIDs[i] != child.PeerID || seen[child.DeliveryID] || d.Trust.Network != v.Network || d.Trust.Generation == 0 || d.Offer.RecipientID != child.PeerID || !d.Offer.ExpiresAt.Equal(r.ExpiresAt) {
				return outboxError("outbox_recovery_required")
			}
			if !reflect.DeepEqual(d.Offer.Manifest.Entries, r.Entries) || !d.Offer.CreatedAt.Equal(r.CreatedAt) {
				return outboxError("outbox_recovery_required")
			}
			seen[child.DeliveryID] = true
			if d.Lease != nil && !outboxLeaseMatches(d.Offer, *d.Lease) {
				return outboxError("outbox_recovery_required")
			}
		}
	}
	if count > outboxMaxChildren {
		return outboxError("outbox_capacity")
	}
	var retainedBytes int64
	for id, p := range s.Payloads {
		if p == nil {
			return outboxError("outbox_recovery_required")
		}

		if p.Bytes > 1<<60-retainedBytes {
			return outboxError("outbox_recovery_required")
		}
		retainedBytes += p.Bytes
		for id, identity := range p.FileIdentities {
			if !config.ValidPeerID(id) || id == "owner" || len(id) > 128 || identity == "" || len(identity) > 128 {
				return outboxError("outbox_recovery_required")
			}
		}
		if p.NamespaceRemoval && (!p.Released || p.CleanupPending || p.RootIdentity == "" || p.MarkerIdentity == "") {
			return outboxError("outbox_recovery_required")
		}
		if id != p.ID || !outboxOwnedID(id) || !outboxOwnedID(p.Marker) || p.Bytes < 0 || p.FileIdentities == nil || p.Ready && (p.RootIdentity == "" || p.MarkerIdentity == "") {
			return outboxError("outbox_recovery_required")
		}
	}
	for _, payload := range s.Payloads {
		if !payload.Released && !payload.CleanupPending {
			continue
		}
		for _, row := range s.Intents {
			if row.PayloadID != payload.ID {
				continue
			}
			if payload.NamespaceRemoval && (row.View.DeliveryOutcome != "confirmed" && row.View.DeliveryOutcome != "not_attempted") {
				return outboxError("outbox_recovery_required")
			}
			for _, child := range row.View.Children {
				if payload.NamespaceRemoval && o.runs[child.DeliveryID] != nil {
					return outboxError("outbox_recovery_required")
				}
				if child.Status != "completed" && child.Status != "cancelled" && child.Status != "expired" {
					return outboxError("outbox_recovery_required")
				}
			}
		}
	}
	return nil
}
func outboxOwnedID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, v := range id {
		if !(v >= '0' && v <= '9' || v >= 'a' && v <= 'f') {
			return false
		}
	}
	return true
}
func (o *outboxCoordinator) inventoryLocked() error {
	var live int64
	for _, p := range o.state.Payloads {
		live += outboxMarkerBytes
		if p.Released {
			continue
		}
		live += p.Bytes
		if p.Ready && !p.CleanupPending {
			if err := o.verifyPayloadLocked(p); err != nil {
				return err
			}
		}
	}
	// Inventory includes unknown objects; no unknown path is removed or adopted.
	bytes, _, err := inspectStaging(filepath.Join(o.path(), "payloads"), o.core.limit("resources", "stagingInventoryEntries"), o.core.limit("resources", "stagingInventoryDepth"))
	if err != nil {
		return err
	}
	// Exact marker reservations live with payload rows. Only additional inventory
	// is orphan storage, so retiring a known marker cannot leak an orphan charge.
	o.orphanBytes = 0
	if bytes > live {
		o.orphanBytes = bytes - live
	}
	dir, e := o.root.Open("payloads")
	if errors.Is(e, os.ErrNotExist) {
		return nil
	}
	if e != nil {
		return e
	}
	defer dir.Close()
	names, e := dir.ReadDir(outboxMaxChildren + 1)
	if e != nil && e != io.EOF {
		return e
	}
	for _, name := range names {
		p := o.state.Payloads[name.Name()]
		if p == nil || !name.IsDir() {
			return outboxError("outbox_payload_recovery_required")
		}
	}
	return nil
}
func (o *outboxCoordinator) listLocked() []OutboxRecord {
	rows := make([]OutboxRecord, 0, len(o.state.Intents))
	for id, r := range o.state.Intents {
		if o.frozen != "" {
			if _, ok := o.certified[id]; !ok {
				continue
			}
		}
		rows = append(rows, o.viewLocked(r))
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].CreatedAt.Before(rows[j].CreatedAt) })
	return rows
}
func (o *outboxCoordinator) viewLocked(v *outboxIntent) OutboxRecord {
	r := cloneOutboxRecord(v.View)
	if o.frozen != "" {
		if old, ok := o.certified[v.View.OutboxID]; ok {
			r = cloneOutboxRecord(old)
		}
		r.Status = "blocked_recovery"
		r.ReasonCode = o.frozen
	}
	return r
}

func (o *outboxCoordinator) readSnapshotLocked() ([]byte, string, error) {
	info, err := o.root.Lstat("state.json")
	if err != nil || !info.Mode().IsRegular() || info.Size() > outboxMaxMetadata {
		return nil, "", outboxError("outbox_recovery_required")
	}
	f, err := o.root.Open("state.json")
	if err != nil {
		return nil, "", err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, "", outboxError("outbox_recovery_required")
	}
	identity, err := outboxNativeIdentity(f)
	if err != nil {
		return nil, "", err
	}
	data, err := io.ReadAll(io.LimitReader(f, outboxMaxMetadata+1))
	if err != nil || len(data) > outboxMaxMetadata {
		return nil, "", outboxError("outbox_recovery_required")
	}
	current, err := o.root.Lstat("state.json")
	if err != nil || !os.SameFile(opened, current) {
		return nil, "", outboxError("outbox_recovery_required")
	}
	return data, identity, nil
}

func outboxPreparationMetadata(manifest transfer.Manifest, recipients int) int64 {
	return 6*transfer.ManifestMetadataBytes(manifest)*int64(2+recipients) + int64(recipients*(outboxReceiptReserve+len(manifest.Entries)*512))
}
