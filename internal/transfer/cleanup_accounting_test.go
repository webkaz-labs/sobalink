package transfer

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type accountingFailureStore struct {
	FileReceiveAccountingStore
	fail bool
}

type saveThenFailAccountingStore struct {
	FileReceiveAccountingStore
	saves int
}

func (s *saveThenFailAccountingStore) SaveReceiveAccounting(state ReceiveAccounting) error {
	s.saves++
	if err := s.FileReceiveAccountingStore.SaveReceiveAccounting(state); err != nil {
		return err
	}
	if s.saves == 3 {
		return errors.New("response lost after accounting save")
	}
	return nil
}

func TestReceiveAccountingRetirementFailurePreservesIdempotentRetries(t *testing.T) {
	dir := t.TempDir()
	store := &saveThenFailAccountingStore{FileReceiveAccountingStore: FileReceiveAccountingStore{Path: filepath.Join(dir, "receive-accounting.json")}}
	m, peer := testManager(t, Options{AccountingStore: store})
	manifest := testManifest("saved", testEntry("file", "payload", "payload"))
	if _, err := m.Offer(peer, manifest); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(dir, "receive")
	if err := os.Mkdir(destination, 0700); err != nil {
		t.Fatal(err)
	}
	accepted, err := m.Accept(manifest.ID, destination)
	if err != nil {
		t.Fatal(err)
	}
	pending := testManifest("pending", testEntry("next", "next", "next"))
	if _, err := m.Offer(peer, pending); err != nil {
		t.Fatal(err)
	}
	firstAck, err := m.ReceiveFile(context.Background(), peer, manifest.ID, "file", strings.NewReader("payload"))
	if err != nil || firstAck.FileID != "file" {
		t.Fatalf("saved payload result: %+v %v", firstAck, err)
	}
	if view := m.ReceiveRecovery(); view.Code != "index_unavailable" {
		t.Fatalf("retirement failure did not block receiving: %+v", view)
	}
	if duplicate, err := m.Offer(peer, manifest); err != nil || duplicate.ID != manifest.ID {
		t.Fatalf("identical offer retry = %+v, %v", duplicate, err)
	}
	ack, err := m.ReceiveFile(context.Background(), peer, manifest.ID, "file", unreadableReader{t})
	if err != nil || ack != (FileAck{BatchID: manifest.ID, FileID: "file", StoredName: "payload", Size: 7, SHA256: testEntry("file", "payload", "payload").SHA256}) {
		t.Fatalf("saved ACK retry = %+v, %v", ack, err)
	}
	if _, err := m.Offer(peer, testManifest("new", testEntry("new-file", "new", "x"))); !errors.Is(err, ErrReceiveRecovery) {
		t.Fatalf("new offer escaped recovery gate: %v", err)
	}
	if _, err := m.ReceiveFile(context.Background(), peer, pending.ID, "next", unreadableReader{t}); !errors.Is(err, ErrReceiveRecovery) {
		t.Fatalf("new payload escaped recovery gate: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(accepted.Destination, "payload")); err != nil || string(data) != "payload" {
		t.Fatalf("saved output lost: %q %v", data, err)
	}
	state, err := store.LoadReceiveAccounting()
	if err != nil || len(state.Roots) != 0 {
		t.Fatalf("fixture did not persist retirement before lost response: %+v %v", state, err)
	}
}

func (s *accountingFailureStore) SaveReceiveAccounting(state ReceiveAccounting) error {
	if s.fail {
		return errors.New("private-path/secret-token")
	}
	return s.FileReceiveAccountingStore.SaveReceiveAccounting(state)
}

func TestReceiveAccountingPersistenceFailurePrecedesApproval(t *testing.T) {
	dir := t.TempDir()
	store := &accountingFailureStore{FileReceiveAccountingStore: FileReceiveAccountingStore{Path: filepath.Join(dir, "receive-accounting.json")}}
	m, peer := testManager(t, Options{AccountingStore: store})
	store.fail = true
	if _, err := m.Offer(peer, testManifest("fixture", testEntry("file", "payload", "12345678"))); err != nil {
		t.Fatal(err)
	}
	if b, err := m.Accept("fixture", t.TempDir()); !errors.Is(err, ErrReceiveRecovery) || strings.Contains(err.Error(), "secret-token") || b.State != Pending {
		t.Fatalf("accepted unsaved accounting: %+v %v", b, err)
	}
	if view := m.ReceiveRecovery(); view.State != "blocked" || view.ReservedBytes != nil {
		t.Fatal("storage failure did not stop receiving")
	}
}

type cleanupFailureReader struct {
	t     *testing.T
	root  *os.Root
	stage string
	read  bool
}

func (r *cleanupFailureReader) Read(p []byte) (int, error) {
	if !r.read {
		r.read = true
		return copy(p, "1234567"), nil
	}
	dir, err := r.root.Open(r.stage)
	if err != nil {
		r.t.Fatal(err)
	}
	allNames, err := dir.Readdirnames(-1)
	dir.Close()
	var names []string
	for _, name := range allNames {
		if name != receiveOwnerMarker {
			names = append(names, name)
		}
	}
	if err != nil || len(names) != 1 {
		r.t.Fatalf("private fixture: %v %v", names, err)
	}
	name := r.stage + "/" + names[0]
	if err := r.root.Remove(name); err != nil {
		r.t.Fatal(err)
	}
	if err := r.root.Mkdir(name, 0700); err != nil {
		r.t.Fatal(err)
	}
	file, err := r.root.OpenFile(name+"/unknown", os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		r.t.Fatal(err)
	}
	file.WriteString("1234567")
	file.Close()
	return 0, io.ErrUnexpectedEOF
}

func TestReceiveCleanupUnknownCancelForgetCannotReleaseCapacity(t *testing.T) {
	dir := t.TempDir()
	options := crashOptions(dir)
	m, peer := testManager(t, options)
	if _, err := m.Offer(peer, testManifest("fixture", testEntry("file", "payload", "12345678"))); err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir()
	if _, err := m.Accept("fixture", dest); err != nil {
		t.Fatal(err)
	}
	b := m.batches["fixture"]
	if _, err := m.ReceiveFile(context.Background(), peer, "fixture", "file", &cleanupFailureReader{t: t, root: b.root, stage: b.stage}); err == nil {
		t.Fatal("fixture unexpectedly completed")
	}
	if m.reserved != 8 || len(b.cleanup) != 1 {
		t.Fatal("unknown cleanup released reservation")
	}
	if _, err := m.Cancel("fixture"); err != nil {
		t.Fatal(err)
	}
	if err := m.Forget("fixture"); !errors.Is(err, ErrState) {
		t.Fatalf("forgot cleanup ownership: %v", err)
	}
	if m.reserved != 8 || len(m.accounting.Roots) != 1 {
		t.Fatal("cancel forgot retained storage")
	}
	if view := m.ReceiveRecovery(); view.ReservedBytes != nil {
		t.Fatal("unknown residual published as known")
	}
	if _, err := m.Offer(peer, testManifest("new", testEntry("a", "a", "1"))); !errors.Is(err, ErrReceiveRecovery) {
		t.Fatal("unknown cleanup allowed another stream")
	}
	if _, err := m.RetryFile("fixture", "file"); !errors.Is(err, ErrState) {
		t.Fatal("cancelled transfer retried")
	}
}
