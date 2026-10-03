package transfer

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type receiveResult struct {
	ack FileAck
	err error
}

// gateReader supplies the full payload, then blocks before EOF. Its barrier
// proves the bytes reached the staging file before a competing operation.
type gateReader struct {
	data        *bytes.Reader
	entered     chan struct{}
	release     chan struct{}
	closed      chan struct{}
	enterOnce   sync.Once
	releaseOnce sync.Once
	closeOnce   sync.Once
	closeCount  atomic.Int32
}

func newGateReader(data string) *gateReader {
	return &gateReader{data: bytes.NewReader([]byte(data)), entered: make(chan struct{}), release: make(chan struct{}), closed: make(chan struct{})}
}

func (r *gateReader) Read(p []byte) (int, error) {
	if r.data.Len() > 0 {
		return r.data.Read(p)
	}
	r.enterOnce.Do(func() { close(r.entered) })
	select {
	case <-r.release:
		return 0, io.EOF
	case <-r.closed:
		return 0, io.ErrClosedPipe
	}
}
func (r *gateReader) Close() error {
	r.closeOnce.Do(func() { r.closeCount.Add(1); close(r.closed) })
	return nil
}
func (r *gateReader) finish() { r.releaseOnce.Do(func() { close(r.release) }) }

func waitBarrier(t *testing.T, ready <-chan struct{}) {
	t.Helper()
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("stream did not reach read barrier")
	}
}

func waitReceive(t *testing.T, done <-chan receiveResult) receiveResult {
	t.Helper()
	select {
	case result := <-done:
		return result
	case <-time.After(5 * time.Second):
		t.Fatal("receive did not finish")
		return receiveResult{}
	}
}

func startBlocked(t *testing.T, m *Manager, ctx context.Context, peer Peer, batchID, fileID string, reader *gateReader, closable bool) <-chan receiveResult {
	t.Helper()
	var input io.Reader = reader
	if !closable {
		input = struct{ io.Reader }{reader}
	}
	t.Cleanup(reader.finish)
	done := make(chan receiveResult, 1)
	go func() { ack, err := m.ReceiveFile(ctx, peer, batchID, fileID, input); done <- receiveResult{ack, err} }()
	waitBarrier(t, reader.entered)
	return done
}

func TestCancellationAndTrustChangesPreventInFlightCommit(t *testing.T) {
	for _, action := range []string{"cancel batch", "revoke peer", "renew trust", "pause peer", "close manager", "cancel context"} {
		t.Run(action, func(t *testing.T) {
			m, peer := testManager(t, Options{})
			b := testAccepted(t, m, peer, testManifest("batch", testEntry("file", "file.txt", "payload")))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			reader := newGateReader("payload")
			done := startBlocked(t, m, ctx, peer, "batch", "file", reader, true)
			var err error
			switch action {
			case "cancel batch":
				_, err = m.Cancel("batch")
			case "revoke peer":
				err = m.RevokePeer(peer.ID)
			case "renew trust":
				err = m.BindPeer(Peer{ID: peer.ID, Generation: peer.Generation + 1})
			case "pause peer":
				err = m.PausePeer(peer.ID, true)
			case "close manager":
				err = m.Close()
			case "cancel context":
				cancel()
			}
			if err != nil {
				t.Fatal(err)
			}
			result := waitReceive(t, done)
			if !errors.Is(result.err, ErrCancelled) {
				t.Fatalf("in-flight result = %v", result.err)
			}
			if result.ack != (FileAck{}) {
				t.Fatalf("cancelled receive acknowledged: %+v", result.ack)
			}
			if reader.closeCount.Load() != 1 {
				t.Fatal("cancellation did not close blocking reader")
			}
			if _, err = os.Stat(filepath.Join(b.Destination, "file.txt")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("cancelled file became visible: %v", err)
			}
			got, _ := m.Get("batch")
			if action == "pause peer" || action == "cancel context" {
				if got.State != Partial || got.Files[0].State != FileFailed {
					t.Fatalf("recoverable cancellation = %+v", got)
				}
				if action == "pause peer" {
					if _, err = m.RetryFile("batch", "file"); !errors.Is(err, ErrPeerPaused) {
						t.Fatalf("paused retry = %v", err)
					}
					if err = m.PausePeer(peer.ID, false); err != nil {
						t.Fatal(err)
					}
				}
				if _, err = m.RetryFile("batch", "file"); err != nil {
					t.Fatal(err)
				}
				if _, err = m.ReceiveFile(context.Background(), peer, "batch", "file", strings.NewReader("payload")); err != nil {
					t.Fatalf("explicit retry = %v", err)
				}
			} else if got.State != Cancelled || got.Files[0].State != FileCancelled {
				t.Fatalf("terminal cancellation = %+v", got)
			}
		})
	}
}

func TestNonclosableCancelledStreamRetainsReservationUntilCleanup(t *testing.T) {
	m, peer := testManager(t, Options{Limits: Limits{MaxReservedBytes: 7}})
	b := testAccepted(t, m, peer, testManifest("batch", testEntry("file", "file.txt", "payload")))
	reader := newGateReader("payload")
	done := startBlocked(t, m, context.Background(), peer, "batch", "file", reader, false)
	if _, err := m.Cancel("batch"); err != nil {
		t.Fatal(err)
	}
	if err := m.Forget("batch"); !errors.Is(err, ErrState) {
		t.Fatalf("forgot active cleanup = %v", err)
	}
	next := testManifest("next", testEntry("file", "next.txt", "payload"))
	if _, err := m.Offer(peer, next); !errors.Is(err, ErrLimit) {
		t.Fatalf("active cancelled bytes lost reservation: %v", err)
	}
	reader.finish()
	if result := waitReceive(t, done); !errors.Is(result.err, ErrCancelled) {
		t.Fatalf("cancelled stream = %v", result.err)
	}
	if _, err := os.Stat(filepath.Join(b.Destination, "file.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled file became visible: %v", err)
	}
	if _, err := m.Offer(peer, next); err != nil {
		t.Fatalf("cleanup retained reservation: %v", err)
	}
	if entries := mustReadDir(t, b.Destination); len(entries) != 0 {
		t.Fatalf("cancelled staging files retained: %v", entries)
	}
}

func TestGlobalAndPerPeerConcurrentStreamsAreBounded(t *testing.T) {
	m, firstPeer := testManager(t, Options{Limits: Limits{MaxConcurrentFiles: 2, MaxConcurrentPerPeer: 1}})
	secondPeer, thirdPeer := Peer{ID: "second-peer", Generation: 1}, Peer{ID: "third-peer", Generation: 1}
	for _, peer := range []Peer{secondPeer, thirdPeer} {
		if err := m.BindPeer(peer); err != nil {
			t.Fatal(err)
		}
	}
	_ = testAccepted(t, m, firstPeer, testManifest("first", testEntry("a", "a", "x"), testEntry("b", "b", "x")))
	_ = testAccepted(t, m, secondPeer, testManifest("second", testEntry("a", "a", "x")))
	_ = testAccepted(t, m, thirdPeer, testManifest("third", testEntry("a", "a", "x")))
	firstReader, secondReader := newGateReader("x"), newGateReader("x")
	firstDone := startBlocked(t, m, context.Background(), firstPeer, "first", "a", firstReader, true)
	if _, err := m.ReceiveFile(context.Background(), firstPeer, "first", "b", unreadableReader{t}); !errors.Is(err, ErrBusy) {
		t.Fatalf("per-peer stream overflow = %v", err)
	}
	if _, err := m.ReceiveFile(context.Background(), firstPeer, "first", "a", unreadableReader{t}); !errors.Is(err, ErrState) {
		t.Fatalf("concurrent duplicate stream = %v", err)
	}
	secondDone := startBlocked(t, m, context.Background(), secondPeer, "second", "a", secondReader, true)
	if _, err := m.ReceiveFile(context.Background(), thirdPeer, "third", "a", unreadableReader{t}); !errors.Is(err, ErrBusy) {
		t.Fatalf("global stream overflow = %v", err)
	}
	firstReader.finish()
	secondReader.finish()
	if r := waitReceive(t, firstDone); r.err != nil {
		t.Fatal(r.err)
	}
	if r := waitReceive(t, secondDone); r.err != nil {
		t.Fatal(r.err)
	}
	if _, err := m.ReceiveFile(context.Background(), firstPeer, "first", "b", strings.NewReader("x")); err != nil {
		t.Fatalf("per-peer slot leaked: %v", err)
	}
	if _, err := m.ReceiveFile(context.Background(), thirdPeer, "third", "a", strings.NewReader("x")); err != nil {
		t.Fatalf("global slot leaked: %v", err)
	}
}

type trackedReader struct {
	reader            io.Reader
	bytes, maxRequest int
}

func (r *trackedReader) Read(p []byte) (int, error) {
	if len(p) > r.maxRequest {
		r.maxRequest = len(p)
	}
	n, err := r.reader.Read(p)
	r.bytes += n
	return n, err
}

func TestStreamingReadsUseFixedBufferAndAtMostDeclaredSizePlusOne(t *testing.T) {
	for _, oversized := range []bool{false, true} {
		name := "exact"
		if oversized {
			name = "oversized"
		}
		t.Run(name, func(t *testing.T) {
			m, peer := testManager(t, Options{})
			payload := strings.Repeat("x", 3*streamBufferBytes+17)
			b := testAccepted(t, m, peer, testManifest("batch", testEntry("file", "file", payload)))
			input := payload
			if oversized {
				input += strings.Repeat("x", 1<<20)
			}
			reader := &trackedReader{reader: strings.NewReader(input)}
			_, err := m.ReceiveFile(context.Background(), peer, "batch", "file", reader)
			if oversized {
				if !errors.Is(err, ErrIntegrity) || reader.bytes != len(payload)+1 {
					t.Fatalf("oversized result = %v, bytes = %d", err, reader.bytes)
				}
				if _, err := os.Stat(filepath.Join(b.Destination, "file")); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("oversized file visible: %v", err)
				}
			} else if err != nil || reader.bytes != len(payload) {
				t.Fatalf("exact receive = %v, bytes = %d", err, reader.bytes)
			}
			if reader.maxRequest > 32<<10 {
				t.Fatalf("unbounded read buffer: %d", reader.maxRequest)
			}
		})
	}
}

type invalidReader struct{ result int }

func (r invalidReader) Read(p []byte) (int, error) {
	if r.result == 0 {
		return len(p) + 1, nil
	}
	return r.result, nil
}

type errorReader struct{ err error }

func (r errorReader) Read([]byte) (int, error) { return 0, r.err }

type noProgressReader struct{ calls int }

func (r *noProgressReader) Read([]byte) (int, error) { r.calls++; return 0, nil }

func TestMalformedAndFailingReadersCannotCommitOrLeakErrorDetails(t *testing.T) {
	secret := "private-source-path-for-test"
	for name, reader := range map[string]io.Reader{
		"negative count": invalidReader{result: -1}, "oversized count": invalidReader{},
		"read error": errorReader{errors.New(secret)}, "wrong checksum": strings.NewReader("different"),
	} {
		t.Run(name, func(t *testing.T) {
			m, peer := testManager(t, Options{})
			b := testAccepted(t, m, peer, testManifest("batch", testEntry("file", "file", "expected!")))
			if _, err := m.ReceiveFile(context.Background(), peer, "batch", "file", reader); err == nil {
				t.Fatal("invalid payload succeeded")
			}
			if _, err := os.Stat(filepath.Join(b.Destination, "file")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("failed file visible: %v", err)
			}
			failed, _ := m.Get("batch")
			if strings.Contains(failed.Files[0].Error, secret) {
				t.Fatal("stored status disclosed arbitrary reader error")
			}
		})
	}
	m, peer := testManager(t, Options{})
	_ = testAccepted(t, m, peer, testManifest("batch", testEntry("file", "file", "")))
	reader := &noProgressReader{}
	if _, err := m.ReceiveFile(context.Background(), peer, "batch", "file", reader); !errors.Is(err, io.ErrNoProgress) {
		t.Fatalf("empty reads = %v", err)
	}
	if reader.calls > 100 {
		t.Fatalf("unbounded empty reads: %d", reader.calls)
	}
}
