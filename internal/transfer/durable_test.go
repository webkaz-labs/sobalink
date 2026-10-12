package transfer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
)

const testDurableEpoch = "1234567890abcdef1234567890abcdef"

var testDurablePeer = Peer{ID: "synthetic-sender", Generation: 1}

func durableTestManager(t *testing.T, dir string, initialize bool) *Manager {
	t.Helper()
	m, err := NewManager(Options{Limits: Limits{DiskReserveBytes: 1}, Durable: &DurableOptions{Path: filepath.Join(dir, "receiver.json"), Epoch: testDurableEpoch, Initialize: initialize}})
	if err != nil {
		t.Fatal(err)
	}
	if err = m.BindPeer(testDurablePeer); err != nil {
		t.Fatal(err)
	}
	if err = m.BindDurableRelationship(testDurablePeer, "synthetic", "epoch-one"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m
}
func durableTestOffer(t *testing.T, m *Manager, id string, entries ...Entry) DurableStatus {
	t.Helper()
	now := time.Now().UTC()
	o := DurableOffer{Version: 1, DeliveryID: id, RecipientID: "synthetic-receiver", Manifest: Manifest{ID: id, Entries: entries}, CreatedAt: now, SentAt: now, ExpiresAt: now.Add(time.Hour)}
	var err error
	o.Digest, err = DurableDigest(o)
	if err != nil {
		t.Fatal(err)
	}
	status, err := m.OfferDurable(testDurablePeer, o.RecipientID, o)
	if err != nil {
		t.Fatal(err)
	}
	return status
}
func durableTestEntry() Entry {
	data := []byte("synthetic payload")
	sum := sha256.Sum256(data)
	return Entry{ID: "file-one", Path: "folder/file.txt", Kind: File, Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}
}
func durableTestAccepted(t *testing.T, m *Manager) (DurableStatus, Batch) {
	t.Helper()
	s := durableTestOffer(t, m, "delivery-one", durableTestEntry())
	b, err := m.Accept(s.Lease.DeliveryID, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.StartDurable(context.Background(), testDurablePeer, s.Lease); err != nil {
		t.Fatal(err)
	}
	b, err = m.Get(s.Lease.DeliveryID)
	if err != nil {
		t.Fatal(err)
	}
	return s, b
}

func TestDurableDigestAndClock(t *testing.T) {
	now := time.Now().UTC()
	o := DurableOffer{Version: 1, DeliveryID: "delivery", RecipientID: "receiver", Manifest: Manifest{ID: "delivery", Entries: []Entry{durableTestEntry()}}, CreatedAt: now, ExpiresAt: now.Add(time.Hour), SentAt: now}
	first, _ := DurableDigest(o)
	o.SentAt = now.Add(time.Minute)
	again, _ := DurableDigest(o)
	if first != again {
		t.Fatal("attempt clock changed immutable digest")
	}
	o.ExpiresAt = o.ExpiresAt.Add(time.Second)
	changed, _ := DurableDigest(o)
	if changed == first {
		t.Fatal("deadline not bound")
	}
	m := durableTestManager(t, t.TempDir(), true)
	o.SentAt = now.Add(6 * time.Minute)
	o.Digest, _ = DurableDigest(o)
	if _, err := m.OfferDurable(testDurablePeer, "receiver", o); !errors.Is(err, ErrDurableClock) {
		t.Fatalf("clock: %v", err)
	}
	m.durable.now = func() time.Time { return now.Add(-6 * time.Minute) }
	if m.DurableAvailable() {
		t.Fatal("rollback admitted")
	}
}
func TestDurableRestartSavedAndDeletedOutput(t *testing.T) {
	dir := t.TempDir()
	m := durableTestManager(t, dir, true)
	s, b := durableTestAccepted(t, m)
	result, err := m.ReceiveDurableFile(context.Background(), testDurablePeer, s.Lease, "file-one", strings.NewReader("synthetic payload"))
	if err != nil || result.State != Completed || result.Receipt == nil {
		t.Fatalf("save: %+v %v", result, err)
	}
	if m.durableReservedLocked() != 0 {
		t.Fatal("saved anchors remained charged")
	}
	if _, err = os.Stat(filepath.Join(b.Destination, durableStage)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stage not cleaned: %v", err)
	}
	_ = m.Close()
	if err = os.RemoveAll(b.Destination); err != nil {
		t.Fatal(err)
	}
	m = durableTestManager(t, dir, false)
	if err = m.MaintainDurable(); err != nil {
		t.Fatal(err)
	}
	result, err = m.StatusDurable(testDurablePeer, s.Lease)
	if err != nil || result.State != Completed || result.Receipt.Files[0].StoredName != "folder/file.txt" {
		t.Fatalf("historical receipt: %+v %v", result, err)
	}
	if _, err = m.ReceiveDurableFile(context.Background(), testDurablePeer, s.Lease, "file-one", failReader{}); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(b.Destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("saved output recreated")
	}
}

type failReader struct{}

func (failReader) Read([]byte) (int, error) { return 0, errors.New("saved replay read payload") }
func TestDurableRecoverSameInodeAndCancelPending(t *testing.T) {
	for _, phase := range []string{"before_link", "after_link", "after_final_sync"} {
		t.Run(phase, func(t *testing.T) {
			dir := t.TempDir()
			m := durableTestManager(t, dir, true)
			s, b := durableTestAccepted(t, m)
			m.durable.fault = func(at string) error {
				if at == phase {
					return errors.New("synthetic crash")
				}
				return nil
			}
			if _, err := m.ReceiveDurableFile(context.Background(), testDurablePeer, s.Lease, "file-one", strings.NewReader("synthetic payload")); err == nil {
				t.Fatal("missing crash")
			}
			final := filepath.Join(b.Destination, "folder/file.txt")
			before, statErr := os.Stat(final)
			_ = m.Close()
			m = durableTestManager(t, dir, false)
			result, err := m.StatusDurable(testDurablePeer, s.Lease)
			if err != nil {
				t.Fatal(err)
			}
			if phase == "before_link" {
				if !errors.Is(statErr, os.ErrNotExist) || result.Receipt != nil {
					t.Fatal("invented effect")
				}
				if _, err = m.Cancel(s.Lease.DeliveryID); err != nil {
					t.Fatal(err)
				}
				if err = m.MaintainDurable(); err != nil {
					t.Fatal(err)
				}
				if m.durableReservedLocked() != 0 {
					t.Fatal("cancel retained proven-unpublished anchor")
				}
			} else {
				after, err := os.Stat(final)
				if err != nil || !os.SameFile(before, after) || result.State != Completed {
					t.Fatalf("recovery duplicated or lost receipt: %v", err)
				}
				if err = m.MaintainDurable(); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
func TestDurableUncertainSaveProjectionAndRestart(t *testing.T) {
	dir := t.TempDir()
	m := durableTestManager(t, dir, true)
	s, _ := durableTestAccepted(t, m)
	originalWrite := m.durable.write
	m.durable.write = func(path string, data []byte) error {
		err := originalWrite(path, data)
		if err == nil && bytes.Contains(data, []byte(`"phase":"saved"`)) {
			return config.ErrAtomicCommitted
		}
		return err
	}
	if _, err := m.ReceiveDurableFile(context.Background(), testDurablePeer, s.Lease, "file-one", strings.NewReader("synthetic payload")); !errors.Is(err, config.ErrAtomicCommitted) {
		t.Fatalf("uncertainty lost: %v", err)
	}
	b, err := m.Get(s.Lease.DeliveryID)
	if err != nil || b.State == Completed || b.Files[0].State == FileSaved {
		t.Fatal("unconfirmed save exposed as completed")
	}
	_ = m.Close()
	m = durableTestManager(t, dir, false)
	status, err := m.StatusDurable(testDurablePeer, s.Lease)
	if err != nil || status.State != Completed {
		t.Fatalf("published state lost: %+v %v", status, err)
	}
}
func TestDurableDirectoryOnlyAndLeaseFences(t *testing.T) {
	dir := t.TempDir()
	m := durableTestManager(t, dir, true)
	s := durableTestOffer(t, m, "directories", Entry{ID: "empty", Path: "a/b", Kind: Directory})
	b, err := m.Accept(s.Lease.DeliveryID, t.TempDir())
	if err != nil || b.State != Accepted {
		t.Fatalf("directory approval: %+v %v", b, err)
	}
	if _, err = m.StartDurable(context.Background(), testDurablePeer, s.Lease); err != nil {
		t.Fatal(err)
	}
	b, err = m.Get(s.Lease.DeliveryID)
	if err != nil || b.State != Completed {
		t.Fatalf("directory batch: %+v %v", b, err)
	}
	status, err := m.StatusDurable(testDurablePeer, s.Lease)
	if err != nil || status.Receipt == nil || len(status.Receipt.Files) != 1 {
		t.Fatal("directory receipt missing")
	}
	if err = m.BindDurableRelationship(testDurablePeer, "synthetic", "epoch-two"); err != nil {
		t.Fatal(err)
	}
	if _, err = m.StatusDurable(testDurablePeer, s.Lease); !errors.Is(err, ErrDurableUnknownLease) {
		t.Fatalf("reused generation authorized old lease: %v", err)
	}
	_ = m.Close()
	if err = os.Remove(filepath.Join(dir, "receiver.json")); err != nil {
		t.Fatal(err)
	}
	m = durableTestManager(t, dir, false)
	if m.DurableAvailable() {
		t.Fatal("missing ledger recreated")
	}
	if _, err = os.Stat(filepath.Join(dir, "receiver.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("missing ledger written")
	}
}
func TestDurableCorruptLedgerNeverPanics(t *testing.T) {
	dir := t.TempDir()
	m := durableTestManager(t, dir, true)
	_ = m.Close()
	data, err := os.ReadFile(filepath.Join(dir, "receiver.json"))
	if err != nil {
		t.Fatal(err)
	}
	data = bytes.Replace(data, []byte(`"records":[]`), []byte(`"records":[null]`), 1)
	if err = config.AtomicWritePrivate(filepath.Join(dir, "receiver.json"), data); err != nil {
		t.Fatal(err)
	}
	m = durableTestManager(t, dir, false)
	if m.DurableAvailable() {
		t.Fatal("corrupt admitted")
	}
	_ = m.List()
	_, _ = m.Get("missing")
}

var _ io.Reader = failReader{}

func TestDurableSavedRootLostBeforeCleanupStillHasReceipt(t *testing.T) {
	dir := t.TempDir()
	m := durableTestManager(t, dir, true)
	s, b := durableTestAccepted(t, m)
	m.durable.fault = func(at string) error {
		if at == "before_cleanup" {
			return errors.New("synthetic cleanup unavailable")
		}
		return nil
	}
	result, err := m.ReceiveDurableFile(context.Background(), testDurablePeer, s.Lease, "file-one", strings.NewReader("synthetic payload"))
	if err != nil || result.State != Completed {
		t.Fatalf("durable receipt affected by cleanup: %v", err)
	}
	if m.durableReservedLocked() == 0 {
		t.Fatal("uncleaned anchor not charged")
	}
	if err = os.Rename(b.Destination, b.Destination+"-moved"); err != nil {
		t.Fatal(err)
	}
	_ = m.Close()
	m = durableTestManager(t, dir, false)
	if err = m.MaintainDurable(); err == nil {
		t.Fatal("missing cleanup location ignored")
	}
	result, err = m.StatusDurable(testDurablePeer, s.Lease)
	if err != nil || result.State != Completed {
		t.Fatalf("certified history lost: %v", err)
	}
	if _, err = os.Stat(b.Destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("output recreated")
	}
}
func TestDurableForeignStageBlocksNewAdmission(t *testing.T) {
	dir := t.TempDir()
	m := durableTestManager(t, dir, true)
	s, b := durableTestAccepted(t, m)
	if err := os.WriteFile(filepath.Join(b.Destination, durableStage, "foreign"), []byte("synthetic"), 0600); err != nil {
		t.Fatal(err)
	}
	_ = m.Close()
	m = durableTestManager(t, dir, false)
	if m.DurableAvailable() {
		t.Fatal("unknown stage bytes admitted")
	}
	if _, err := m.ReceiveDurableFile(context.Background(), testDurablePeer, s.Lease, "file-one", failReader{}); !errors.Is(err, ErrDurableRecovery) {
		t.Fatalf("payload admitted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(b.Destination, durableStage, "foreign")); err != nil {
		t.Fatal("foreign data removed")
	}
}
func TestDurableFinalConflictNeverAdopted(t *testing.T) {
	dir := t.TempDir()
	m := durableTestManager(t, dir, true)
	s, b := durableTestAccepted(t, m)
	final := filepath.Join(b.Destination, "folder/file.txt")
	if err := os.WriteFile(final, []byte("synthetic payload"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ReceiveDurableFile(context.Background(), testDurablePeer, s.Lease, "file-one", strings.NewReader("synthetic payload")); err == nil {
		t.Fatal("unrelated matching content adopted")
	}
	_ = m.Close()
	m = durableTestManager(t, dir, false)
	if _, err := m.StatusDurable(testDurablePeer, s.Lease); !errors.Is(err, ErrDurableRecovery) {
		t.Fatalf("conflict generated receipt: %v", err)
	}
	data, err := os.ReadFile(final)
	if err != nil || string(data) != "synthetic payload" {
		t.Fatal("existing output changed")
	}
}
func TestDurableReceiptPrunesOnlyAfterHorizon(t *testing.T) {
	dir := t.TempDir()
	m := durableTestManager(t, dir, true)
	s, _ := durableTestAccepted(t, m)
	if _, err := m.ReceiveDurableFile(context.Background(), testDurablePeer, s.Lease, "file-one", strings.NewReader("synthetic payload")); err != nil {
		t.Fatal(err)
	}
	m.durable.now = func() time.Time { return s.Lease.ReceiptUntil }
	if err := m.MaintainDurable(); err != nil {
		t.Fatal(err)
	}
	if len(m.List()) != 1 {
		t.Fatal("receipt pruned at retention boundary")
	}
	m.durable.now = func() time.Time { return s.Lease.ReceiptUntil.Add(time.Second) }
	if err := m.MaintainDurable(); err != nil {
		t.Fatal(err)
	}
	if len(m.List()) != 0 {
		t.Fatal("eligible receipt not pruned")
	}
	if _, err := m.StatusDurable(testDurablePeer, s.Lease); !errors.Is(err, ErrDurableUnknownLease) {
		t.Fatalf("old token reconstructed: %v", err)
	}
}

type durableBlockedReader struct {
	entered, closed chan struct{}
	once            sync.Once
}

func (r *durableBlockedReader) Read([]byte) (int, error) {
	r.once.Do(func() { close(r.entered) })
	<-r.closed
	return 0, io.EOF
}
func (r *durableBlockedReader) Close() error {
	select {
	case <-r.closed:
	default:
		close(r.closed)
	}
	return nil
}
func TestDurableConcurrentFileCancelAndEpochFence(t *testing.T) {
	for _, cancelKind := range []string{"cancel", "relationship"} {
		t.Run(cancelKind, func(t *testing.T) {
			m := durableTestManager(t, t.TempDir(), true)
			s, _ := durableTestAccepted(t, m)
			reader := &durableBlockedReader{entered: make(chan struct{}), closed: make(chan struct{})}
			done := make(chan error, 1)
			go func() {
				_, err := m.ReceiveDurableFile(context.Background(), testDurablePeer, s.Lease, "file-one", reader)
				done <- err
			}()
			<-reader.entered
			if _, err := m.ReceiveDurableFile(context.Background(), testDurablePeer, s.Lease, "file-one", failReader{}); !errors.Is(err, ErrBusy) {
				t.Fatalf("duplicate physical owner: %v", err)
			}
			if cancelKind == "cancel" {
				if _, err := m.Cancel(s.Lease.DeliveryID); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := m.BindDurableRelationship(testDurablePeer, "synthetic", "new-epoch"); err != nil {
					t.Fatal(err)
				}
			}
			if err := <-done; err == nil {
				t.Fatal("cancelled stream acknowledged")
			}
			if cancelKind == "cancel" {
				if err := m.MaintainDurable(); err != nil {
					t.Fatal(err)
				}
				if m.durableReservedLocked() != 0 {
					t.Fatal("cancel leaked owned anchor")
				}
			}
		})
	}
}

func TestDurableDirectoryEffectsRequirePersistedLeaseStart(t *testing.T) {
	dir := t.TempDir()
	destination := t.TempDir()
	m := durableTestManager(t, dir, true)
	s := durableTestOffer(t, m, "directory-handshake", Entry{ID: "empty", Path: "empty/directory", Kind: Directory})
	if _, err := m.Accept(s.Lease.DeliveryID, destination); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(destination)
	if err != nil || len(entries) != 0 {
		t.Fatal("accept created directory output before lease start")
	}
	status, err := m.StatusDurable(testDurablePeer, s.Lease)
	if err != nil || status.Receipt != nil {
		t.Fatal("read-only status published directories")
	}
	entries, _ = os.ReadDir(destination)
	if len(entries) != 0 {
		t.Fatal("status created output")
	}
	bad := s.Lease
	bad.Token = strings.Repeat("0", 32)
	if _, err = m.StartDurable(context.Background(), testDurablePeer, bad); !errors.Is(err, ErrDurableUnknownLease) {
		t.Fatal("unknown token started")
	}
	_ = m.Close()
	m = durableTestManager(t, dir, false)
	status, err = m.StartDurable(context.Background(), testDurablePeer, s.Lease)
	if err != nil || status.State != Completed || status.Receipt == nil {
		t.Fatalf("directory start: %+v %v", status, err)
	}
	first, _ := os.ReadDir(destination)
	if len(first) != 1 {
		t.Fatal("missing output root")
	}
	if _, err = m.StartDurable(context.Background(), testDurablePeer, s.Lease); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadDir(destination)
	if len(second) != 1 || first[0].Name() != second[0].Name() {
		t.Fatal("duplicate start created output")
	}
}
func TestDurableInitializationCannotAdoptRecordsOrForeignNamespace(t *testing.T) {
	dir := t.TempDir()
	m := durableTestManager(t, dir, true)
	durableTestOffer(t, m, "known", durableTestEntry())
	_ = m.Close()
	m = durableTestManager(t, dir, true)
	if m.DurableAvailable() {
		t.Fatal("initializing profile adopted exposed records")
	}
	foreign := t.TempDir()
	if err := os.WriteFile(filepath.Join(foreign, "foreign"), []byte("synthetic"), 0600); err != nil {
		t.Fatal(err)
	}
	m = durableTestManager(t, foreign, true)
	if m.DurableAvailable() {
		t.Fatal("foreign namespace adopted")
	}
}

func TestDurableLifecycleBusyIsTransientAndPreservesLockOrder(t *testing.T) {
	profile := t.TempDir()
	owner, err := config.AcquireLock(profile)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	namespace := filepath.Join(profile, "durable")
	if err = os.Mkdir(namespace, 0700); err != nil {
		t.Fatal(err)
	}
	owned := func(fn func() error) error {
		err := owner.TryWithOwnershipInfo(profile, func(_, _ os.FileInfo) error { return fn() })
		if errors.Is(err, config.ErrLifecycleBusy) {
			return ErrBusy
		}
		return err
	}
	m, err := NewManager(Options{Limits: Limits{DiskReserveBytes: 1}, Durable: &DurableOptions{Path: filepath.Join(namespace, "receiver.json"), Epoch: testDurableEpoch, Initialize: true, OwnerCheck: func() error { return owned(func() error { return nil }) }, OwnerOperation: owned}})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if err = m.BindPeer(testDurablePeer); err != nil {
		t.Fatal(err)
	}
	if err = m.BindDurableRelationship(testDurablePeer, "synthetic", "epoch-one"); err != nil {
		t.Fatal(err)
	}
	s, _ := durableTestAccepted(t, m)
	held, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- owner.WithOwnershipInfo(profile, func(_, _ os.FileInfo) error {
			close(held)
			<-release
			return m.UpdateLimits(Limits{DiskReserveBytes: 1})
		})
	}()
	<-held
	if _, err = m.ReceiveDurableFile(context.Background(), testDurablePeer, s.Lease, "file-one", failReader{}); !errors.Is(err, ErrBusy) {
		t.Fatalf("busy: %v", err)
	}
	if !m.DurableAvailable() || m.durable.blocked != nil {
		t.Fatal("transient lifecycle contention removed protocol or poisoned ledger")
	}
	close(release)
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	if result, err := m.ReceiveDurableFile(context.Background(), testDurablePeer, s.Lease, "file-one", strings.NewReader("synthetic payload")); err != nil || result.State != Completed {
		t.Fatalf("retry: %+v %v", result, err)
	}
}
func TestDurableBusyCandidateNeverBecomesLocalReceipt(t *testing.T) {
	m := durableTestManager(t, t.TempDir(), true)
	now := time.Now().UTC()
	o := DurableOffer{Version: 1, DeliveryID: "busy-offer", RecipientID: "receiver", Manifest: Manifest{ID: "busy-offer", Entries: []Entry{durableTestEntry()}}, CreatedAt: now, SentAt: now, ExpiresAt: now.Add(time.Hour)}
	o.Digest, _ = DurableDigest(o)
	m.durable.ownerOperation = func(func() error) error { return ErrBusy }
	if _, err := m.OfferDurable(testDurablePeer, "receiver", o); !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
	if len(m.List()) != 0 || m.durable.blocked != nil {
		t.Fatal("uncommitted offer leaked or permanently blocked")
	}
	m.durable.ownerOperation = nil
	s, err := m.OfferDurable(testDurablePeer, "receiver", o)
	if err != nil {
		t.Fatal(err)
	}
	destination := t.TempDir()
	if _, err = m.Accept(s.Lease.DeliveryID, destination); err != nil {
		t.Fatal(err)
	}
	if _, err = m.ReceiveDurableFile(context.Background(), testDurablePeer, s.Lease, "file-one", failReader{}); !errors.Is(err, ErrState) {
		t.Fatalf("file before start poisoned ledger: %v", err)
	}
	m.durable.ownerOperation = func(func() error) error { return ErrBusy }
	if _, err = m.StartDurable(context.Background(), testDurablePeer, s.Lease); !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(destination)
	if len(entries) != 0 {
		t.Fatal("busy start created output")
	}
	m.durable.ownerOperation = nil
	if _, err = m.StartDurable(context.Background(), testDurablePeer, s.Lease); err != nil {
		t.Fatal(err)
	}
}

func TestDurableOuterOwnershipFailureNeverCertifiesSaved(t *testing.T) {
	dir := t.TempDir()
	m := durableTestManager(t, dir, true)
	s, _ := durableTestAccepted(t, m)
	m.durable.ownerOperation = func(fn func() error) error {
		err := fn()
		if err == nil && m.durable.state.Records[0].State == Completed {
			return errors.Join(config.ErrAtomicCommitted, errors.New("synthetic final owner check"))
		}
		return err
	}
	if _, err := m.ReceiveDurableFile(context.Background(), testDurablePeer, s.Lease, "file-one", strings.NewReader("synthetic payload")); !errors.Is(err, config.ErrAtomicCommitted) {
		t.Fatalf("postcheck uncertainty: %v", err)
	}
	b, err := m.Get(s.Lease.DeliveryID)
	if err != nil || b.State == Completed || b.Files[0].State == FileSaved {
		t.Fatal("outer owner failure certified a saved projection")
	}
	_ = m.Close()
	m = durableTestManager(t, dir, false)
	status, err := m.StatusDurable(testDurablePeer, s.Lease)
	if err != nil || status.State != Completed {
		t.Fatalf("restart did not reconcile actual durable effect: %v", err)
	}
}

func TestDurableBoundsConflictAndAuthenticatedLease(t *testing.T) {
	m := durableTestManager(t, t.TempDir(), true)
	if err := m.UpdateLimits(Limits{MaxPendingPerPeer: 1, DiskReserveBytes: 1}); err != nil {
		t.Fatal(err)
	}
	first := durableTestOffer(t, m, "one", durableTestEntry())
	o := m.durable.state.Records[0].Offer
	o.SentAt = time.Now().UTC()
	o.DeliveryID = "two"
	o.Manifest.ID = "two"
	o.Digest, _ = DurableDigest(o)
	if _, err := m.OfferDurable(testDurablePeer, o.RecipientID, o); !errors.Is(err, ErrDurableCapacity) {
		t.Fatalf("per-peer reservation exceeded: %v", err)
	}
	o.DeliveryID = "one"
	o.Manifest.ID = "one"
	o.Manifest.Entries = append([]Entry(nil), o.Manifest.Entries...)
	o.Manifest.Entries[0].Path = "different.txt"
	o.Digest, _ = DurableDigest(o)
	if _, err := m.OfferDurable(testDurablePeer, o.RecipientID, o); !errors.Is(err, ErrDurableConflict) {
		t.Fatalf("same identity changed content: %v", err)
	}
	other := Peer{ID: "synthetic-other", Generation: 1}
	if err := m.BindPeer(other); err != nil {
		t.Fatal(err)
	}
	if err := m.BindDurableRelationship(other, "synthetic", "epoch-one"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.StatusDurable(other, first.Lease); !errors.Is(err, ErrDurableUnknownLease) {
		t.Fatalf("token alone granted authority: %v", err)
	}
}
func TestDurableRestoredReservationOverflowFailsClosed(t *testing.T) {
	dir := t.TempDir()
	m := durableTestManager(t, dir, true)
	durableTestOffer(t, m, "large-one", durableTestEntry())
	durableTestOffer(t, m, "large-two", durableTestEntry())
	for _, r := range m.durable.state.Records {
		r.Offer.Manifest.Entries[0].Size = math.MaxInt64/2 + 1
		r.Files[0].Entry = r.Offer.Manifest.Entries[0]
		r.Offer.Digest, _ = DurableDigest(r.Offer)
		r.Lease.Digest = r.Offer.Digest
		r.ReservedMetadata = durableMetadataReservation(r.Offer)
	}
	data, err := json.Marshal(m.durable.state)
	if err != nil {
		t.Fatal(err)
	}
	_ = m.Close()
	if err = config.AtomicWritePrivate(filepath.Join(dir, "receiver.json"), data); err != nil {
		t.Fatal(err)
	}
	m = durableTestManager(t, dir, false)
	if m.DurableAvailable() {
		t.Fatal("overflowed restored reservation admitted")
	}
	if durableCapacityLeft(1, math.MaxInt64, math.MaxInt64) != -1 {
		t.Fatal("checked admission subtraction overflowed")
	}
}

func TestDurableCleanupProgressSurvivesLaterFailureAndBusy(t *testing.T) {
	m := durableTestManager(t, t.TempDir(), true)
	one := durableTestEntry()
	two := one
	two.ID = "file-two"
	two.Path = "folder/two.txt"
	s := durableTestOffer(t, m, "cleanup-progress", one, two)
	if _, err := m.Accept(s.Lease.DeliveryID, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if _, err := m.StartDurable(context.Background(), testDurablePeer, s.Lease); err != nil {
		t.Fatal(err)
	}
	m.durable.fault = func(at string) error {
		if at == "before_cleanup" {
			return errors.New("synthetic delayed cleanup")
		}
		return nil
	}
	for _, id := range []string{one.ID, two.ID} {
		if _, err := m.ReceiveDurableFile(context.Background(), testDurablePeer, s.Lease, id, strings.NewReader("synthetic payload")); err != nil {
			t.Fatal(err)
		}
	}
	m.durable.fault = nil
	r := m.durable.record(s.Lease.DeliveryID)
	anchor := filepath.Join(r.Destination, r.Root, durableStage, r.Files[1].Anchor)
	if err := os.Rename(anchor, anchor+".retained"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(anchor, []byte("synthetic payload"), 0600); err != nil {
		t.Fatal(err)
	}
	m.durable.ownerOperation = func(fn func() error) error { return fn() }
	if err := m.MaintainDurable(); err == nil {
		t.Fatal("replaced second anchor accepted")
	}
	if m.durable.blocked != nil {
		t.Fatal("ordinary cleanup failure poisoned receipts")
	}
	if r.Files[0].Anchor != "" {
		t.Fatal("first cleanup did not progress")
	}
	m.durable.ownerOperation = func(func() error) error { return ErrBusy }
	now := time.Now().UTC()
	o := DurableOffer{Version: 1, DeliveryID: "busy-after-cleanup", RecipientID: "receiver", Manifest: Manifest{ID: "busy-after-cleanup", Entries: []Entry{one}}, CreatedAt: now, SentAt: now, ExpiresAt: now.Add(time.Hour)}
	o.Digest, _ = DurableDigest(o)
	if _, err := m.OfferDurable(testDurablePeer, "receiver", o); !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
	if r.Files[0].Anchor != "" {
		t.Fatal("busy rollback resurrected deleted first anchor")
	}
	m.durable.ownerOperation = func(fn func() error) error { return fn() }
	if err := os.Remove(anchor); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(anchor+".retained", anchor); err != nil {
		t.Fatal(err)
	}
	if err := m.MaintainDurable(); err != nil {
		t.Fatal(err)
	}
	if m.durableReservedLocked() != 0 {
		t.Fatal("remaining cleanup not reclaimed")
	}
}
