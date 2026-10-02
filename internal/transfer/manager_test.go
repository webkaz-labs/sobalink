package transfer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func testEntry(id, path, data string) Entry {
	digest := sha256.Sum256([]byte(data))
	return Entry{ID: id, Path: path, Kind: File, Size: int64(len(data)), SHA256: hex.EncodeToString(digest[:])}
}

func testManifest(id string, entries ...Entry) Manifest { return Manifest{ID: id, Entries: entries} }

func testManager(t *testing.T, options Options) (*Manager, Peer) {
	t.Helper()
	m, err := NewManager(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := m.Close(); err != nil {
			t.Error(err)
		}
	})
	p := Peer{ID: "verified-peer", Generation: 1}
	if err = m.BindPeer(p); err != nil {
		t.Fatal(err)
	}
	return m, p
}

func testAccepted(t *testing.T, m *Manager, peer Peer, manifest Manifest) Batch {
	t.Helper()
	if _, err := m.Offer(peer, manifest); err != nil {
		t.Fatal(err)
	}
	b, err := m.Accept(manifest.ID, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return b
}

type unreadableReader struct{ t *testing.T }

func (r unreadableReader) Read([]byte) (int, error) {
	r.t.Error("unexpected payload read")
	return 0, errors.New("must not read")
}

func TestOfferAndLostAcknowledgementAreIdempotent(t *testing.T) {
	m, peer := testManager(t, Options{})
	manifest := testManifest("batch", testEntry("file", "folder/file.txt", "payload"))
	destination := t.TempDir()
	first, err := m.Offer(peer, manifest)
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := m.Offer(peer, manifest)
	if err != nil || !reflect.DeepEqual(first, duplicate) {
		t.Fatalf("duplicate offer = %+v, %v", duplicate, err)
	}
	entries, err := os.ReadDir(destination)
	if err != nil || len(entries) != 0 {
		t.Fatalf("offer wrote destination: %v, %v", entries, err)
	}
	// Both input and returned metadata belong to their callers, not the manager.
	manifest.Entries[0].Path = "changed"
	first.Files[0].Path = "mutated"
	got, err := m.Get("batch")
	if err != nil || got.Files[0].Path != "folder/file.txt" {
		t.Fatalf("aliased metadata: %+v, %v", got, err)
	}
	listed := m.List()
	listed[0].Files[0].Path = "mutated-again"
	if _, err = m.Offer(peer, manifest); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed manifest error = %v", err)
	}
	accepted, err := m.Accept("batch", destination)
	if err != nil {
		t.Fatal(err)
	}
	acceptedAgain, err := m.Accept("batch", destination)
	if err != nil || !reflect.DeepEqual(accepted, acceptedAgain) {
		t.Fatalf("duplicate accept = %+v, %v", acceptedAgain, err)
	}
	ack, err := m.ReceiveFile(context.Background(), peer, "batch", "file", strings.NewReader("payload"))
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(accepted.Destination, ack.StoredName))
	if err != nil {
		t.Fatal(err)
	}
	ackAgain, err := m.ReceiveFile(context.Background(), peer, "batch", "file", unreadableReader{t})
	if err != nil || ack != ackAgain {
		t.Fatalf("ACK retry = %+v, %v; want %+v", ackAgain, err, ack)
	}
	infoAgain, err := os.Stat(filepath.Join(accepted.Destination, ack.StoredName))
	if err != nil || !os.SameFile(info, infoAgain) {
		t.Fatalf("ACK retry rewrote file: %v", err)
	}
	completed, err := m.Offer(peer, testManifest("batch", testEntry("file", "folder/file.txt", "payload")))
	if err != nil || completed.State != Completed || completed.CompletedBytes != 7 {
		t.Fatalf("completed offer = %+v, %v", completed, err)
	}
	other := Peer{ID: "other-peer", Generation: 1}
	if err = m.BindPeer(other); err != nil {
		t.Fatal(err)
	}
	if _, err = m.Offer(other, testManifest("batch", testEntry("file", "folder/file.txt", "payload"))); !errors.Is(err, ErrConflict) {
		t.Fatalf("cross-peer ID reuse = %v", err)
	}
	if _, err = m.ReceiveFile(context.Background(), other, "batch", "file", unreadableReader{t}); !errors.Is(err, ErrPeerChanged) {
		t.Fatalf("cross-peer ACK access = %v", err)
	}
}

func TestPartialFailureRetryStartsAtZeroAndKeepsSavedFiles(t *testing.T) {
	m, peer := testManager(t, Options{})
	b := testAccepted(t, m, peer, testManifest("batch", testEntry("a", "a.txt", "first"), testEntry("b", "b.txt", "second")))
	a, err := m.ReceiveFile(context.Background(), peer, "batch", "a", strings.NewReader("first"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.ReceiveFile(context.Background(), peer, "batch", "b", strings.NewReader("sec")); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("partial receive = %v", err)
	}
	partial, _ := m.Get("batch")
	if partial.State != Partial || partial.Files[0].State != FileSaved || partial.Files[1].State != FileFailed || partial.CompletedBytes != 8 {
		t.Fatalf("partial state = %+v", partial)
	}
	if _, err = os.Stat(filepath.Join(b.Destination, "b.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial file visible: %v", err)
	}
	if _, err = m.ReceiveFile(context.Background(), peer, "batch", "b", unreadableReader{t}); !errors.Is(err, ErrState) {
		t.Fatalf("implicit retry = %v", err)
	}
	retry, err := m.RetryFile("batch", "b")
	if err != nil || retry.CompletedBytes != 5 || retry.Files[1].CompletedBytes != 0 || retry.Files[1].State != FilePending {
		t.Fatalf("retry = %+v, %v", retry, err)
	}
	if _, err = m.ReceiveFile(context.Background(), peer, "batch", "b", strings.NewReader("second")); err != nil {
		t.Fatal(err)
	}
	aAgain, err := m.ReceiveFile(context.Background(), peer, "batch", "a", unreadableReader{t})
	if err != nil || a != aAgain {
		t.Fatalf("successful file ACK changed: %+v, %v", aAgain, err)
	}
	completed, _ := m.Get("batch")
	if completed.State != Completed || completed.CompletedBytes != 11 {
		t.Fatalf("completed = %+v", completed)
	}
	data, err := os.ReadFile(filepath.Join(b.Destination, "b.txt"))
	if err != nil || string(data) != "second" {
		t.Fatalf("retried bytes = %q, %v", data, err)
	}
	for _, entry := range mustReadDir(t, b.Destination) {
		if strings.HasPrefix(entry.Name(), ".incoming-") {
			t.Error("staging directory retained after completion")
		}
	}
}

func mustReadDir(t *testing.T, path string) []os.DirEntry {
	t.Helper()
	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func TestReservationsReleaseButHistoryKeysRequireExplicitForget(t *testing.T) {
	m, peer := testManager(t, Options{Limits: Limits{MaxBatches: 2, MaxReservedBytes: 4}})
	one := testManifest("one", testEntry("file", "one", "1234"))
	if _, err := m.Offer(peer, one); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Offer(peer, one); err != nil {
		t.Fatalf("duplicate consumes budget: %v", err)
	}
	two := testManifest("two", testEntry("file", "two", "1234"))
	if _, err := m.Offer(peer, two); !errors.Is(err, ErrLimit) {
		t.Fatalf("reservation overflow = %v", err)
	}
	if err := m.Forget("one"); !errors.Is(err, ErrState) {
		t.Fatalf("forget active = %v", err)
	}
	if _, err := m.Reject("one"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Offer(peer, two); err != nil {
		t.Fatalf("reservation not released: %v", err)
	}
	if _, err := m.Cancel("two"); err != nil {
		t.Fatal(err)
	}
	three := testManifest("three", testEntry("file", "three", "1234"))
	if _, err := m.Offer(peer, three); !errors.Is(err, ErrLimit) {
		t.Fatalf("history silently evicted = %v", err)
	}
	if err := m.Forget("one"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Offer(peer, three); err != nil {
		t.Fatalf("forget did not release history: %v", err)
	}
}

func TestMetadataAndPendingLimitsRemainBounded(t *testing.T) {
	for name, limits := range map[string]Limits{
		"pending all peers": {MaxPendingBatches: 1},
		"pending one peer":  {MaxPendingPerPeer: 1},
		"metadata":          {MaxMetadataBytes: metadataSize(testManifest("first", testEntry("file", "file", "")))},
	} {
		t.Run(name, func(t *testing.T) {
			m, peer := testManager(t, Options{Limits: limits})
			if _, err := m.Offer(peer, testManifest("first", testEntry("file", "file", ""))); err != nil {
				t.Fatal(err)
			}
			if _, err := m.Offer(peer, testManifest("second", testEntry("file", "file", ""))); !errors.Is(err, ErrLimit) {
				t.Fatalf("limit error = %v", err)
			}
		})
	}
	m, peer := testManager(t, Options{Limits: Limits{MaxPendingPerPeer: 1}})
	_ = testAccepted(t, m, peer, testManifest("first", testEntry("file", "file", "")))
	if _, err := m.Offer(peer, testManifest("second", testEntry("file", "file", ""))); !errors.Is(err, ErrLimit) {
		t.Fatalf("accepted batch bypassed per-peer bound = %v", err)
	}
}

type testPolicyStore struct {
	policies []ReceivePolicy
	saveErr  error
}

func (s *testPolicyStore) LoadPolicies() ([]ReceivePolicy, error) {
	return append([]ReceivePolicy(nil), s.policies...), nil
}
func (s *testPolicyStore) SavePolicies(p []ReceivePolicy) error {
	if s.saveErr != nil {
		return s.saveErr
	}
	s.policies = append([]ReceivePolicy(nil), p...)
	return nil
}

func TestReceivePolicyRequiresExactTrustGenerationAndDurableSave(t *testing.T) {
	store := &testPolicyStore{}
	m, peer := testManager(t, Options{PolicyStore: store})
	destination := t.TempDir()
	policy := ReceivePolicy{Peer: peer, Destination: destination, AutoAccept: true}
	if _, err := m.Offer(peer, testManifest("pending", testEntry("file", "file", ""))); err != nil {
		t.Fatal(err)
	}
	store.saveErr = errors.New("storage unavailable")
	if err := m.SetReceivePolicy(policy); !errors.Is(err, store.saveErr) {
		t.Fatalf("policy save failure = %v", err)
	}
	if len(m.ReceivePolicies()) != 0 {
		t.Fatal("failed durable policy became effective")
	}
	store.saveErr = nil
	if err := m.SetReceivePolicy(policy); err != nil {
		t.Fatal(err)
	}
	pending, _ := m.Get("pending")
	if pending.State != Pending {
		t.Fatalf("policy retroactively accepted batch: %+v", pending)
	}
	auto, err := m.Offer(peer, testManifest("auto", testEntry("file", "file", "")))
	if err != nil || auto.State != Accepted {
		t.Fatalf("auto accept = %+v, %v", auto, err)
	}
	newPeer := Peer{ID: peer.ID, Generation: 2}
	if err := m.BindPeer(newPeer); err != nil {
		t.Fatal(err)
	}
	if len(m.ReceivePolicies()) != 0 || len(store.policies) != 0 {
		t.Fatal("renewal retained previous generation approval")
	}
	old, _ := m.Get("auto")
	if old.State != Cancelled {
		t.Fatalf("old generation batch = %+v", old)
	}
	if _, err := m.Offer(peer, testManifest("old", testEntry("file", "file", ""))); !errors.Is(err, ErrPeerChanged) {
		t.Fatalf("old generation offer = %v", err)
	}
	if err := m.SetReceivePolicy(policy); !errors.Is(err, ErrPeerChanged) {
		t.Fatalf("old generation policy = %v", err)
	}
	fresh, err := m.Offer(newPeer, testManifest("fresh", testEntry("file", "file", "")))
	if err != nil || fresh.State != Pending {
		t.Fatalf("renewal implicitly approved new generation: %+v, %v", fresh, err)
	}
	if err := m.RevokePeer(peer.ID); err != nil {
		t.Fatal(err)
	}
	if err := m.BindPeer(newPeer); !errors.Is(err, ErrPeerChanged) {
		t.Fatalf("revoked generation resurrected = %v", err)
	}
}

func TestLoadedPolicyCannotApproveOlderOrNewerGeneration(t *testing.T) {
	for _, generation := range []uint64{1, 2, 3} {
		t.Run(fmt.Sprint(generation), func(t *testing.T) {
			policy := ReceivePolicy{Peer: Peer{ID: "peer", Generation: 2}, Destination: t.TempDir(), AutoAccept: true}
			m, err := NewManager(Options{PolicyStore: &testPolicyStore{policies: []ReceivePolicy{policy}}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = m.Close() })
			peer := Peer{ID: "peer", Generation: generation}
			err = m.BindPeer(peer)
			if generation < 2 {
				if !errors.Is(err, ErrPeerChanged) {
					t.Fatalf("older binding = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			b, err := m.Offer(peer, testManifest("batch", testEntry("file", "file", "")))
			want := Pending
			if generation == 2 {
				want = Accepted
			}
			if err != nil || b.State != want {
				t.Fatalf("offer state = %s, %v; want %s", b.State, err, want)
			}
		})
	}
}

func TestDirectoryOnlyBatchCompletesAndCannotReceiveBytes(t *testing.T) {
	m, peer := testManager(t, Options{})
	b := testAccepted(t, m, peer, testManifest("batch", Entry{ID: "dir", Path: "empty", Kind: Directory}))
	if b.State != Completed || b.Files[0].State != FileSaved {
		t.Fatalf("directory state = %+v", b)
	}
	if _, err := m.ReceiveFile(context.Background(), peer, "batch", "dir", unreadableReader{t}); !errors.Is(err, ErrState) {
		t.Fatalf("directory payload = %v", err)
	}
	if info, err := os.Stat(filepath.Join(b.Destination, "empty")); err != nil || !info.IsDir() {
		t.Fatalf("empty directory = %v, %v", info, err)
	}
}

var _ io.Reader = unreadableReader{}
