package transfer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
)

const durableLedgerBytes = 32 << 20
const durableLedgerRecords = 1024
const durableStage = ".sobalink-durable-stage-v1"

// DurableOptions enables only the separate durable receiver. The owner must
// retain its profile singleton lock. Initialize is allowed only during
// same-epoch first initialization, never as a missing-ledger recovery operation.
type DurableOptions struct {
	Path           string
	Initialize     bool
	Epoch          string
	OwnerCheck     func() error
	OwnerOperation func(func() error) error
}

type durableFile struct {
	Entry    Entry     `json:"entry"`
	Phase    string    `json:"phase"`
	Anchor   string    `json:"anchor"`
	Identity string    `json:"identity"`
	SavedAt  time.Time `json:"savedAt"`
	Cleanup  bool      `json:"cleanup"`
}
type durableDirectory struct {
	Path     string `json:"path"`
	Identity string `json:"identity"`
}
type durableRecord struct {
	Offer               DurableOffer       `json:"offer"`
	Peer                Peer               `json:"peer"`
	Network             string             `json:"network"`
	RevocationEpoch     string             `json:"revocationEpoch"`
	Lease               DurableLease       `json:"lease"`
	State               BatchState         `json:"state"`
	Destination         string             `json:"destination"`
	DestinationIdentity string             `json:"destinationIdentity"`
	Root                string             `json:"root"`
	RootIdentity        string             `json:"rootIdentity"`
	StageIdentity       string             `json:"stageIdentity"`
	Directories         []durableDirectory `json:"directories"`
	Files               []durableFile      `json:"files"`
	ReservedMetadata    int64              `json:"reservedMetadata"`
	Started             bool               `json:"started"`
	Preparing           bool               `json:"preparing"`
	StageCleanup        bool               `json:"stageCleanup"`
	Reason              string             `json:"reason"`
	active              map[string]context.CancelFunc
}
type durableSnapshot struct {
	Version   int              `json:"version"`
	Epoch     string           `json:"epoch"`
	HighWater time.Time        `json:"highWater"`
	Records   []*durableRecord `json:"records"`
}
type durableReceiver struct {
	path             string
	parent           *os.Root
	parentIdentity   string
	state            durableSnapshot
	confirmed        durableSnapshot
	pendingConfirmed []byte
	hash             [32]byte
	loaded           bool
	blocked          error
	admissionBlocked error
	now              func() time.Time
	write            func(string, []byte) error
	ownerCheck       func() error
	ownerOperation   func(func() error) error
	ownerHeld        bool
	writer           *config.AtomicWriteLease
	// Fault points are per-receiver and package-private; production never reads
	// environment switches or globally installed crash hooks.
	fault func(string) error
}

func openDurable(options DurableOptions) *durableReceiver {
	d := &durableReceiver{path: options.Path, now: time.Now, ownerCheck: options.OwnerCheck, ownerOperation: options.OwnerOperation}
	valid := false
	defer func() {
		if !valid {
			d.state.Records = nil
		}
	}()
	if !durableStorageSupported() {
		d.blocked = ErrDurableStorage
		return d
	}
	if !validDestination(options.Path) {
		d.blocked = ErrDurableRecovery
		return d
	}
	var err error
	d.parent, err = openDestination(filepath.Dir(options.Path))
	if err != nil {
		d.blocked = ErrDurableRecovery
		return d
	}
	d.write = d.writeBound
	d.parentIdentity, err = durableIdentity(d.parent, ".", true)
	if err != nil {
		d.blocked = ErrDurableRecovery
		return d
	}
	marker := filepath.Base(options.Path) + ".epoch"
	epoch, markerErr := d.read(marker, 128)
	data, loadErr := d.read(filepath.Base(options.Path), durableLedgerBytes)
	if errors.Is(markerErr, os.ErrNotExist) && errors.Is(loadErr, os.ErrNotExist) && options.Initialize {
		directory, e := d.parent.Open(".")
		if e != nil {
			d.blocked = ErrDurableRecovery
			return d
		}
		names, e := directory.Readdirnames(1)
		_ = directory.Close()
		if e != io.EOF || len(names) != 0 {
			d.blocked = ErrDurableRecovery
			return d
		}

		token := options.Epoch
		if !hexToken(token, 32) {
			d.blocked = ErrDurableRecovery
			return d
		}
		if e = d.withOwner(func() error { return d.write(filepath.Join(filepath.Dir(d.path), marker), []byte(token)) }); e != nil {
			d.blocked = errors.Join(ErrDurableRecovery, e)
			return d
		}
		valid = true
		d.state = durableSnapshot{Version: DurableVersion, Epoch: token, HighWater: d.now().UTC(), Records: []*durableRecord{}}
		if e = d.save(); e != nil {
			d.blocked = e
		}
		return d
	}
	if markerErr != nil || loadErr != nil || !hexToken(string(epoch), 32) {
		d.blocked = ErrDurableRecovery
		return d
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&d.state); err != nil {
		d.blocked = ErrDurableRecovery
		return d
	}
	canonical, e := json.Marshal(d.state)
	if e != nil || !bytes.Equal(canonical, data) || d.state.Version != DurableVersion || d.state.Epoch != string(epoch) || d.state.Epoch != options.Epoch || d.state.Records == nil || len(d.state.Records) > durableLedgerRecords || d.state.HighWater.IsZero() {
		d.blocked = ErrDurableRecovery
		return d
	}
	if d.now().Before(d.state.HighWater) {
		d.blocked = ErrDurableClock
		return d
	}
	var reserved, reservedBytes int64
	seen := map[string]bool{}
	for _, r := range d.state.Records {
		if r == nil || seen[r.Offer.DeliveryID] || d.validateRecord(r) != nil {
			d.blocked = ErrDurableRecovery
			return d
		}
		if n := durableBytes(r); n > math.MaxInt64-1-reservedBytes {
			d.blocked = ErrDurableRecovery
			return d
		}
		reservedBytes += durableBytes(r)
		seen[r.Offer.DeliveryID] = true
		if r.ReservedMetadata > durableLedgerBytes-reserved {
			d.blocked = ErrDurableRecovery
			return d
		}
		reserved += r.ReservedMetadata
	}
	if reserved > durableLedgerBytes {
		d.blocked = ErrDurableRecovery
		return d
	}
	if options.Initialize && len(d.state.Records) != 0 {
		d.blocked = ErrDurableRecovery
		return d
	}
	valid = true
	d.hash = sha256.Sum256(data)
	d.loaded = true
	// A successfully reopened, exact snapshot is reconciled to a confirmed file
	// and directory barrier before any receipt can be returned.
	if err = d.withOwner(func() error {
		return d.withWriter(d.path, func() error {
			actual, err := d.read(filepath.Base(d.path), durableLedgerBytes)
			if err != nil || !bytes.Equal(actual, data) {
				return ErrDurableRecovery
			}
			return d.syncLedger()
		})
	}); err != nil {
		d.blocked = errors.Join(ErrDurableRecovery, err)
	} else {
		d.captureConfirmed(data)
	}
	return d
}
func (d *durableReceiver) read(name string, limit int64) ([]byte, error) {
	f, err := openAccountingFile(d.parent, name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() > limit || !accountingPrivate(f, info) {
		return nil, ErrDurableRecovery
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, ErrDurableRecovery
	}
	return data, nil
}
func (d *durableReceiver) verify() error {
	if d == nil {
		return ErrDurableStorage
	}
	if d.blocked != nil {
		return d.blocked
	}
	if d.ownerCheck != nil && !d.ownerHeld {
		if err := d.ownerCheck(); err != nil {
			if errors.Is(err, ErrBusy) {
				return err
			}
			return d.fail(err)
		}
	}
	if err := verifyPreparationParent(filepath.Dir(d.path), d.parentIdentity); err != nil {
		return d.fail(err)
	}
	epoch, err := d.read(filepath.Base(d.path)+".epoch", 128)
	if err != nil || string(epoch) != d.state.Epoch {
		return d.fail(ErrDurableRecovery)
	}
	if d.loaded {
		data, err := d.read(filepath.Base(d.path), durableLedgerBytes)
		if err != nil || sha256.Sum256(data) != d.hash {
			return d.fail(ErrDurableRecovery)
		}
	}
	if d.now().Before(d.state.HighWater) {
		return d.fail(ErrDurableClock)
	}
	return nil
}
func (d *durableReceiver) fail(err error) error {
	d.blocked = errors.Join(ErrDurableRecovery, err)
	return d.blocked
}
func (d *durableReceiver) syncLedger() error {
	f, err := durableOpenWritable(d.parent, filepath.Base(d.path))
	if err != nil {
		return err
	}
	err = errors.Join(f.Sync(), f.Close())
	if err != nil {
		return err
	}
	namespace, err := d.parent.OpenRoot(".sobalink-atomic-v1")
	if err != nil {
		return err
	}
	info, err := d.parent.Lstat(".sobalink-atomic-v1")
	actual, actualErr := namespace.Stat(".")
	if err != nil || actualErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !os.SameFile(info, actual) {
		_ = namespace.Close()
		return ErrDurableRecovery
	}
	err = retirementSyncDirectory(namespace)
	_ = namespace.Close()
	if err != nil {
		return err
	}
	return retirementSyncDirectory(d.parent)
}
func (d *durableReceiver) save() error {
	if err := d.verify(); err != nil {
		if errors.Is(err, ErrBusy) {
			d.restoreConfirmed()
		}
		return err
	}
	now := d.now().UTC()
	if now.After(d.state.HighWater) {
		d.state.HighWater = now
	}
	data, err := json.Marshal(d.state)
	if err != nil || len(data) > durableLedgerBytes {
		return d.fail(ErrDurableCapacity)
	}
	if err := d.withOwner(func() error { return d.saveEncoded(data) }); err != nil {
		if errors.Is(err, ErrBusy) {
			d.restoreConfirmed()
			return err
		}
		return d.fail(err)
	}
	if !d.ownerHeld {
		d.promoteConfirmed()
	}
	return nil
}
func (d *durableReceiver) saveEncoded(data []byte) error {
	err := d.withWriter(d.path, func() error { return d.savePublished(data) })
	if err != nil {
		return err
	}
	d.pendingConfirmed = append([]byte(nil), data...)
	return nil
}
func (d *durableReceiver) savePublished(data []byte) error {
	var err error
	if d.fault != nil {
		if err = d.fault("before_ledger"); err != nil {
			return d.fail(err)
		}
	}
	err = d.write(d.path, data)
	if err != nil {
		return d.fail(err)
	} // Keep uncertain in-memory intent and every anchor.
	actual, readErr := d.read(filepath.Base(d.path), durableLedgerBytes)
	if readErr != nil || !bytes.Equal(actual, data) {
		return d.fail(errors.Join(config.ErrAtomicCommitted, ErrDurableRecovery))
	}
	d.hash = sha256.Sum256(data)
	d.loaded = true
	if err = d.syncLedger(); err != nil {
		return d.fail(errors.Join(config.ErrAtomicCommitted, err))
	}
	if d.fault != nil {
		if err = d.fault("after_ledger"); err != nil {
			return d.fail(errors.Join(config.ErrAtomicCommitted, err))
		}
	}
	return nil
}
func (d *durableReceiver) record(id string) *durableRecord {
	if d == nil {
		return nil
	}
	for _, r := range d.state.Records {
		if r != nil && r.Offer.DeliveryID == id {
			return r
		}
	}
	return nil
}
func (d *durableReceiver) validateRecord(r *durableRecord) error {
	digest, err := DurableDigest(r.Offer)
	if err != nil || digest != r.Offer.Digest || !validPeer(r.Peer) || r.Lease.Version != DurableVersion || r.Lease.DeliveryID != r.Offer.DeliveryID || r.Lease.Digest != digest || r.Lease.RecipientID != r.Offer.RecipientID || r.Lease.ReceiverEpoch != d.state.Epoch || !hexToken(r.Lease.Token, 32) || !r.Lease.ExpiresAt.Equal(r.Offer.ExpiresAt) || !r.Lease.ReceiptUntil.Equal(r.Offer.ExpiresAt.Add(DurableReceiptGrace)) || len(r.Files) != len(r.Offer.Manifest.Entries) || r.ReservedMetadata < 1 {
		return ErrDurableRecovery
	}
	if r.State != Pending && r.State != Accepted && r.State != Partial && r.State != Completed && r.State != Rejected && r.State != Cancelled {
		return ErrDurableRecovery
	}
	if r.Offer.CreatedAt.IsZero() || !r.Offer.ExpiresAt.After(r.Offer.CreatedAt) || r.Offer.ExpiresAt.Sub(r.Offer.CreatedAt) > DurableMaxLifetime {
		return ErrDurableRecovery
	}
	limits := DefaultLimits()
	limits.MaxManifestBytes = durableLedgerBytes
	limits.MaxMetadataBytes = durableLedgerBytes
	limits.MaxFileBytes = math.MaxInt64 - 1
	limits.MaxBatchBytes = math.MaxInt64 - 1
	limits.MaxEntries = durableLedgerBytes
	limits.MaxPathBytes = 4096
	limits.MaxDepth = 2048
	if ValidateManifest(r.Offer.Manifest, limits) != nil {
		return ErrDurableRecovery
	}
	if r.Destination != "" && (!validDestination(r.Destination) || r.DestinationIdentity == "") {
		return ErrDurableRecovery
	}
	for i, f := range r.Files {
		if f.Entry != r.Offer.Manifest.Entries[i] || f.Phase != "pending" && f.Phase != "receiving" && f.Phase != "publish_pending" && f.Phase != "saved" {
			return ErrDurableRecovery
		}
		if f.Anchor != "" && (filepath.Base(f.Anchor) != f.Anchor || !validID(f.Anchor)) {
			return ErrDurableRecovery
		}
		if f.Phase == "publish_pending" && f.Entry.Kind == File && (f.Anchor == "" || f.Identity == "") {
			return ErrDurableRecovery
		}
	}

	encoded, encodeErr := json.Marshal(r)
	if encodeErr != nil || int64(len(encoded)) > r.ReservedMetadata || len(r.RootIdentity) > 128 || len(r.StageIdentity) > 128 || len(r.DestinationIdentity) > 128 || len(r.Reason) > 128 {
		return ErrDurableRecovery
	}
	if r.Network == "" || len(r.Network) > 32 || len(r.RevocationEpoch) > 128 || r.ReservedMetadata != durableMetadataReservation(r.Offer) || r.ReservedMetadata > durableLedgerBytes {
		return ErrDurableRecovery
	}
	if r.Destination == "" && (r.Root != "" || r.RootIdentity != "" || r.StageIdentity != "" || r.Preparing || len(r.Directories) > 0 || r.State == Accepted || r.State == Completed) {
		return ErrDurableRecovery
	}
	if r.Started && (!hexToken(strings.TrimPrefix(r.Root, "sobalink-durable-"), 32) || !strings.HasPrefix(r.Root, "sobalink-durable-") || r.Destination == "") {
		return ErrDurableRecovery
	}
	if !r.Started && (r.Root != "" || r.RootIdentity != "" || r.StageIdentity != "" || len(r.Directories) > 0 || r.Preparing) {
		return ErrDurableRecovery
	}
	if r.Started && !r.Preparing && (r.RootIdentity == "" || r.StageIdentity == "" && !r.StageCleanup) {
		return ErrDurableRecovery
	}
	allSaved := true
	for _, f := range r.Files {
		if f.Phase == "saved" {
			if f.SavedAt.IsZero() || r.Destination == "" || r.Preparing || !r.Started {
				return ErrDurableRecovery
			}
		} else {
			allSaved = false
			if !f.SavedAt.IsZero() {
				return ErrDurableRecovery
			}
		}
		if f.Entry.Kind == Directory && (f.Anchor != "" || f.Identity != "" || f.Cleanup) {
			return ErrDurableRecovery
		}
		if f.Cleanup && f.Anchor == "" {
			return ErrDurableRecovery
		}
		if len(f.Identity) > 128 {
			return ErrDurableRecovery
		}
	}
	if r.State == Completed && !allSaved || allSaved && !terminal(r.State) {
		return ErrDurableRecovery
	}
	dirs := map[string]bool{}
	for _, dir := range r.Directories {
		if validatePath(dir.Path, limits) != nil || dir.Identity == "" || len(dir.Identity) > 128 || dirs[canonical(dir.Path)] || strings.Split(dir.Path, "/")[0] == durableStage {
			return ErrDurableRecovery
		}
		dirs[canonical(dir.Path)] = true
	}
	if len(r.Directories) > len(r.Files)*2048 {
		return ErrDurableRecovery
	}
	return nil
}
func durableIdentity(root *os.Root, name string, directory bool) (string, error) {
	var f *os.File
	var err error
	if directory {
		info, e := root.Lstat(name)
		if e != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", ErrUnsafePath
		}
		f, err = root.Open(name)
		if err == nil {
			actual, e := f.Stat()
			if e != nil || !os.SameFile(info, actual) {
				_ = f.Close()
				return "", ErrUnsafePath
			}
		}
	} else {
		f, err = accountingOpen(root, name)
	}
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || directory && !info.IsDir() || !directory && !info.Mode().IsRegular() {
		return "", ErrUnsafePath
	}
	return accountingIdentity(f)
}

// Sync requires writable handles on Windows. Retain and compare the exact
// no-symlink regular-file identity before using a writable reopening.
func durableOpenWritable(root *os.Root, name string) (*os.File, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, ErrUnsafePath
	}
	f, err := root.OpenFile(name, os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	actual, err := f.Stat()
	if err != nil || !actual.Mode().IsRegular() || !os.SameFile(info, actual) {
		_ = f.Close()
		return nil, ErrUnsafePath
	}
	return f, nil
}
func (d *durableReceiver) project(r *durableRecord) Batch {
	if d.blocked == nil {
		return durableBatch(r)
	}
	for _, previous := range d.confirmed.Records {
		if previous.Offer.DeliveryID == r.Offer.DeliveryID {
			out := durableBatch(previous)
			for i := range out.Files {
				if out.Files[i].State != FileSaved {
					out.Files[i].Error = "durable_recovery_required"
				}
			}
			return out
		}
	}
	out := Batch{ID: r.Offer.DeliveryID, Peer: r.Peer, State: Pending, CreatedAt: r.Offer.CreatedAt}
	for _, f := range r.Offer.Manifest.Entries {
		out.Files = append(out.Files, FileStatus{Entry: f, State: FilePending, Error: "durable_recovery_required"})
	}
	return out
}

// Atomic admission is tied to the retained private namespace, not merely to a
// pathname that could now name a replacement profile.
func (d *durableReceiver) writeBound(target string, data []byte) error {
	if d.writer != nil {
		return d.writer.Write(target, data)
	}
	return d.withWriter(target, func() error { return d.writer.Write(target, data) })
}
func (d *durableReceiver) withWriter(target string, operation func() error) error {
	if err := verifyPreparationParent(filepath.Dir(d.path), d.parentIdentity); err != nil {
		return err
	}
	lease, err := config.AcquireAtomicWriteLease(target)
	if err != nil {
		return err
	}
	defer lease.Close()
	parent, err := d.parent.Open(".")
	if err != nil {
		return err
	}
	defer parent.Close()
	if err = lease.CheckParent(parent); err != nil {
		return err
	}
	d.writer = lease
	defer func() { d.writer = nil }()
	err = operation()
	if check := lease.CheckParent(parent); check != nil {
		return errors.Join(err, config.ErrAtomicCommitted, check)
	}
	return err
}
func (d *durableReceiver) withOwner(operation func() error) error {
	if d.ownerHeld || d.ownerOperation == nil {
		return operation()
	}
	var operationErr error
	ran := false
	err := d.ownerOperation(func() error {
		ran = true
		d.ownerHeld = true
		defer func() { d.ownerHeld = false }()
		operationErr = operation()
		return operationErr
	})
	if err != nil {
		if errors.Is(err, config.ErrAtomicCommitted) {
			d.pendingConfirmed = nil
			return d.fail(err)
		}
		// The callback's own later cleanup error does not invalidate completed
		// earlier snapshots when the enclosing owner's final binding check passed.
		if ran && operationErr != nil {
			d.promoteConfirmed()
		} else {
			d.pendingConfirmed = nil
			if ran {
				return d.fail(err)
			}
		}
		return err
	}
	d.promoteConfirmed()
	return nil
}
func (d *durableReceiver) promoteConfirmed() {
	if d.pendingConfirmed != nil {
		d.captureConfirmed(d.pendingConfirmed)
		d.pendingConfirmed = nil
	}
}

// Busy means lifecycle admission was refused before publication. Restore the
// certified snapshot in place so active readers retain their exact file objects
// and no local view can mistake an uncommitted candidate for saved evidence.
func (d *durableReceiver) restoreConfirmed() {
	byID := map[string]*durableRecord{}
	for _, r := range d.state.Records {
		byID[r.Offer.DeliveryID] = r
	}
	next := make([]*durableRecord, 0, len(d.confirmed.Records))
	for _, previous := range d.confirmed.Records {
		r := byID[previous.Offer.DeliveryID]
		if r == nil {
			r = &durableRecord{Files: append([]durableFile(nil), previous.Files...)}
		}
		active, files := r.active, r.Files
		for i := range files {
			p := previous.Files[i]
			f := &files[i]
			if f.Phase != p.Phase {
				f.Phase = p.Phase
			}
			if f.Anchor != p.Anchor {
				f.Anchor = p.Anchor
			}
			if f.Identity != p.Identity {
				f.Identity = p.Identity
			}
			if f.SavedAt != p.SavedAt {
				f.SavedAt = p.SavedAt
			}
			if f.Cleanup != p.Cleanup {
				f.Cleanup = p.Cleanup
			}
		}
		*r = *previous
		r.Offer.Manifest.Entries = append([]Entry(nil), previous.Offer.Manifest.Entries...)
		r.Directories = append([]durableDirectory(nil), previous.Directories...)
		r.Files = files
		r.active = active
		next = append(next, r)
	}
	d.state.Records = next
	d.state.HighWater = d.confirmed.HighWater
}
func (d *durableReceiver) captureConfirmed(data []byte) {
	var confirmed durableSnapshot
	_ = json.Unmarshal(data, &confirmed)
	d.confirmed = confirmed
}
