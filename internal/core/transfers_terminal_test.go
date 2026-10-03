package core

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/transfer"
)

func terminalTransferSources(t *testing.T) []string {
	t.Helper()
	dir := t.TempDir()
	paths := []string{filepath.Join(dir, "first.txt"), filepath.Join(dir, "second.txt")}
	for _, path := range paths {
		if err := os.WriteFile(path, []byte("data"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return paths
}

func waitIncomingOffer(t *testing.T, c *Core, id string) transfer.Batch {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if batch, err := c.transfers.Get(id); err == nil {
			return batch
		}
		if time.Now().After(deadline) {
			t.Fatal("receiver did not get the offer")
		}
		time.Sleep(time.Millisecond)
	}
}

func assertTerminalOutgoing(t *testing.T, c *Core, id, spool, state string, completed int64) *outgoingBatch {
	t.Helper()
	b := waitOutgoingIdle(t, c, id)
	if b.State != state || b.Completed != completed || b.reserved != 0 || b.Spool != "" {
		t.Fatalf("terminal outgoing = state %s, completed %d, reserved %d, spool %q, error %s", b.State, b.Completed, b.reserved, b.Spool, b.Error)
	}
	if _, err := os.Stat(spool); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("terminal staged files remain: %v", err)
	}
	for _, view := range c.transferViews() {
		if view["id"] == id && (view["status"] != state || view["completedBytes"] != completed) {
			t.Fatalf("terminal UI/CLI view = %+v", view)
		}
	}
	if _, err := command(c, randomID(), "transfer.retry", map[string]string{"transferId": id}); err == nil || !strings.Contains(err.Error(), "only a failed inactive transfer") {
		t.Fatalf("terminal transfer retry = %v", err)
	}
	if state == "declined" {
		b.stop()
		if b.State != state {
			t.Fatalf("later peer shutdown changed declined history to %s", b.State)
		}
	}
	return b
}

func TestOutgoingReceiverDecisionReleasesStaging(t *testing.T) {
	for _, decision := range []string{"transfer.decline", "transfer.cancel"} {
		t.Run(decision, func(t *testing.T) {
			p := newCorePair(t)
			trustPair(t, p)
			paths := terminalTransferSources(t)
			lim := transferLimits()
			lim.MaxReservedBytes = 8
			value, err := p.a.sendPaths(context.Background(), "peer-b", paths, lim)
			if err != nil {
				t.Fatal(err)
			}
			id := value.(map[string]string)["id"]
			waitIncomingOffer(t, p.b, id)
			p.a.mu.RLock()
			b := p.a.outgoing[id]
			p.a.mu.RUnlock()
			b.mu.Lock()
			spool := b.Spool
			b.mu.Unlock()
			if _, err := os.Stat(spool); err != nil {
				t.Fatal(err)
			}
			if _, err := p.a.sendPaths(context.Background(), "peer-b", paths, lim); !errors.Is(err, errOutgoingStaging) {
				t.Fatalf("pending transfer did not reserve staging: %v", err)
			}
			mustCommand(t, p.b, decision, map[string]string{"transferId": id})
			waitOutgoingIdle(t, p.a, id)
			p.hub.mu.Lock()
			wireBytes := p.hub.wire.Len()
			p.hub.mu.Unlock()
			assertTerminalOutgoing(t, p.a, id, spool, "declined", 0)
			p.hub.mu.Lock()
			afterRetry := p.hub.wire.Len()
			p.hub.mu.Unlock()
			if afterRetry != wireBytes {
				t.Fatal("retry of receiver-declined batch contacted the peer")
			}
			mustCommand(t, p.b, "peer.autosave", map[string]any{"peerId": "peer-a", "enabled": true, "directory": t.TempDir()})
			value, err = p.a.sendPaths(context.Background(), "peer-b", paths, lim)
			if err != nil {
				t.Fatalf("receiver decision retained staging quota: %v", err)
			}
			next := waitOutgoingIdle(t, p.a, value.(map[string]string)["id"])
			if next.State != "completed" || next.Completed != 8 || next.reserved != 0 {
				t.Fatalf("subsequent batch = %s, %d, %d: %s", next.State, next.Completed, next.reserved, next.Error)
			}
		})
	}
}

// Replace only the receiver's in-memory HTTP listener. The ordinary handler
// still performs identity checks, receives files and supplies saved-file acks.
func interceptTransferPeer(t *testing.T, p corePair, intercept func(http.ResponseWriter, *http.Request, http.Handler)) {
	t.Helper()
	original := p.b.peerServer.http.Handler
	if err := p.b.peerServer.Close(); err != nil {
		t.Fatal(err)
	}
	listener, err := p.nb.Listen("tcp", config.Address(p.nb.ip.String(), PeerPort))
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { intercept(w, r, original) })}
	t.Cleanup(func() { _ = server.Close() })
	go func() { _ = server.Serve(listener) }()
}

func TestOutgoingInterruptedFilePreservesSavedBytes(t *testing.T) {
	for _, interruption := range []string{"receiver-cancel", "sender-cancel", "transport-failure", "foreign-status", "missing-status-id", "status-unavailable"} {
		t.Run(interruption, func(t *testing.T) {
			p := newCorePair(t)
			trustPair(t, p)
			paths := terminalTransferSources(t)
			mustCommand(t, p.b, "peer.autosave", map[string]any{"peerId": "peer-a", "enabled": true, "directory": t.TempDir()})
			var puts atomic.Int32
			var spool string
			interceptTransferPeer(t, p, func(w http.ResponseWriter, r *http.Request, next http.Handler) {
				if r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/v1/batches/") && puts.Load() == 2 {
					switch interruption {
					case "foreign-status":
						reply(w, 200, wireBatch{ID: "different-batch", State: transfer.Cancelled})
						return
					case "missing-status-id":
						reply(w, 200, map[string]any{"state": transfer.Cancelled})
						return
					case "status-unavailable":
						peerFailure(w, http.StatusServiceUnavailable)
						return
					}
				}
				if r.Method == "PUT" && puts.Add(1) == 2 {
					id := strings.Split(r.URL.Path, "/")[3]
					p.a.mu.RLock()
					b := p.a.outgoing[id]
					p.a.mu.RUnlock()
					b.mu.Lock()
					spool = b.Spool
					b.mu.Unlock()
					switch interruption {
					case "receiver-cancel":
						if _, err := p.b.transfers.Cancel(id); err != nil {
							t.Error(err)
						}
					case "sender-cancel":
						if _, err := command(p.a, randomID(), "transfer.cancel", map[string]string{"transferId": id}); err != nil {
							t.Error(err)
						}
					default:
						conn, _, err := w.(http.Hijacker).Hijack()
						if err != nil {
							t.Error(err)
							return
						}
						_ = conn.Close()
						return
					}
				}
				next.ServeHTTP(w, r)
			})
			lim := transferLimits()
			lim.MaxReservedBytes = 8
			value, err := p.a.sendPaths(context.Background(), "peer-b", paths, lim)
			if err != nil {
				t.Fatal(err)
			}
			id := value.(map[string]string)["id"]
			b := waitOutgoingIdle(t, p.a, id)
			if puts.Load() != 2 || b.Completed != 4 {
				t.Fatalf("interruption lost acknowledged bytes: puts %d, completed %d", puts.Load(), b.Completed)
			}
			if interruption != "receiver-cancel" && interruption != "sender-cancel" {
				if b.State != "failed" || b.reserved != 8 || b.Spool != spool {
					t.Fatalf("transport failure lost retry data: %s, %d, %q", b.State, b.reserved, b.Spool)
				}
				if !strings.Contains(b.Error, "peer did not respond") {
					t.Fatalf("status check replaced the original transport failure: %s", b.Error)
				}
				for _, path := range b.Files {
					if data, err := os.ReadFile(path); err != nil || string(data) != "data" {
						t.Fatalf("retry payload = %q, %v", data, err)
					}
				}
				if _, err := p.a.sendPaths(context.Background(), "peer-b", paths, lim); !errors.Is(err, errOutgoingStaging) {
					t.Fatalf("retryable transfer lost staging reservation: %v", err)
				}
				mustCommand(t, p.a, "transfer.retry", map[string]string{"transferId": id})
				assertTerminalOutgoing(t, p.a, id, spool, "completed", 8)
				if puts.Load() != 3 {
					t.Fatalf("retry resent a saved file: %d PUTs", puts.Load())
				}
			} else {
				state := "declined"
				if interruption == "sender-cancel" {
					state = "cancelled"
				}
				assertTerminalOutgoing(t, p.a, id, spool, state, 4)
			}
			received, err := p.b.transfers.Get(id)
			if err != nil {
				t.Fatal(err)
			}
			if data, err := os.ReadFile(filepath.Join(received.Destination, "first.txt")); err != nil || string(data) != "data" {
				t.Fatalf("interruption removed a saved file: %q, %v", data, err)
			}
		})
	}
}

func TestOutgoingCancelledFinalStatusIsTerminal(t *testing.T) {
	for _, statusID := range []string{"matching-batch", "different-batch", "missing-batch"} {
		t.Run(statusID, func(t *testing.T) {
			p := newCorePair(t)
			trustPair(t, p)
			paths := terminalTransferSources(t)
			mustCommand(t, p.b, "peer.autosave", map[string]any{"peerId": "peer-a", "enabled": true, "directory": t.TempDir()})
			var spool string
			interceptTransferPeer(t, p, func(w http.ResponseWriter, r *http.Request, next http.Handler) {
				if r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/v1/batches/") {
					id := strings.TrimPrefix(r.URL.Path, "/v1/batches/")
					p.a.mu.RLock()
					b := p.a.outgoing[id]
					p.a.mu.RUnlock()
					b.mu.Lock()
					spool = b.Spool
					b.mu.Unlock()
					if statusID == "different-batch" {
						id = "different-batch"
					}
					// Exercise a terminal reply at the final protocol boundary.
					if statusID == "missing-batch" {
						reply(w, 200, map[string]any{"state": transfer.Cancelled})
					} else {
						reply(w, 200, wireBatch{ID: id, State: transfer.Cancelled})
					}
					return
				}
				next.ServeHTTP(w, r)
			})
			value, err := p.a.sendPaths(context.Background(), "peer-b", paths, transferLimits())
			if err != nil {
				t.Fatal(err)
			}
			id := value.(map[string]string)["id"]
			b := waitOutgoingIdle(t, p.a, id)
			if statusID != "matching-batch" {
				if b.State != "failed" || b.reserved != 8 || b.Spool != spool {
					t.Fatalf("foreign terminal status discarded retry data: %s, %d, %q", b.State, b.reserved, b.Spool)
				}
			} else {
				assertTerminalOutgoing(t, p.a, id, spool, "declined", 8)
			}
		})
	}
}

func TestOutgoingReceiverCancelsFileRetry(t *testing.T) {
	p := newCorePair(t)
	trustPair(t, p)
	paths := terminalTransferSources(t)
	mustCommand(t, p.b, "peer.autosave", map[string]any{"peerId": "peer-a", "enabled": true, "directory": t.TempDir()})
	var puts atomic.Int32
	interceptTransferPeer(t, p, func(w http.ResponseWriter, r *http.Request, next http.Handler) {
		if r.Method == "PUT" && puts.Add(1) == 2 {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			_ = conn.Close()
			return
		}
		if r.Method == "POST" && strings.Contains(r.URL.Path, "/retry/") {
			if _, err := p.b.transfers.Cancel(strings.Split(r.URL.Path, "/")[3]); err != nil {
				t.Error(err)
			}
		}
		next.ServeHTTP(w, r)
	})
	value, err := p.a.sendPaths(context.Background(), "peer-b", paths, transferLimits())
	if err != nil {
		t.Fatal(err)
	}
	id := value.(map[string]string)["id"]
	b := waitOutgoingIdle(t, p.a, id)
	if b.State != "failed" || b.Completed != 4 {
		t.Fatalf("initial dropped file = %s, %d", b.State, b.Completed)
	}
	spool := b.Spool
	remote, err := p.b.transfers.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	// A zero-byte failed receive makes the next run use the file retry endpoint.
	if _, err := p.b.transfers.ReceiveFile(context.Background(), remote.Peer, id, remote.Files[1].ID, strings.NewReader("")); !errors.Is(err, transfer.ErrIntegrity) {
		t.Fatalf("failed receive setup = %v", err)
	}
	mustCommand(t, p.a, "transfer.retry", map[string]string{"transferId": id})
	assertTerminalOutgoing(t, p.a, id, spool, "declined", 4)
	if puts.Load() != 2 {
		t.Fatalf("receiver cancellation allowed another file PUT: %d", puts.Load())
	}
}

func TestOutgoingDeclinedCleanupFailureRetainsReservation(t *testing.T) {
	p := newCorePair(t)
	trustPair(t, p)
	paths := terminalTransferSources(t)
	lim := transferLimits()
	lim.MaxReservedBytes = 8
	value, err := p.a.sendPaths(context.Background(), "peer-b", paths, lim)
	if err != nil {
		t.Fatal(err)
	}
	id := value.(map[string]string)["id"]
	waitIncomingOffer(t, p.b, id)
	p.a.mu.RLock()
	b := p.a.outgoing[id]
	p.a.mu.RUnlock()
	b.mu.Lock()
	spool := b.Spool
	// RemoveAll refuses a final dot component on every supported platform.
	b.Spool += string(os.PathSeparator) + "."
	b.mu.Unlock()
	mustCommand(t, p.b, "transfer.decline", map[string]string{"transferId": id})
	waitOutgoingIdle(t, p.a, id)
	if b.State != "declined" || b.reserved != 8 || !strings.Contains(b.Error, "could not remove staged files") {
		t.Fatalf("failed cleanup lost terminal state or capacity: %s, %d, %s", b.State, b.reserved, b.Error)
	}
	if _, err := os.Stat(spool); err != nil {
		t.Fatal(err)
	}
	if _, err := p.a.sendPaths(context.Background(), "peer-b", paths, lim); !errors.Is(err, errOutgoingStaging) {
		t.Fatalf("failed cleanup released staging quota: %v", err)
	}
	if _, err := command(p.a, randomID(), "transfer.retry", map[string]string{"transferId": id}); err == nil {
		t.Fatal("cleanup failure made declined transfer retryable")
	}
	b.mu.Lock()
	b.Spool = spool
	b.mu.Unlock()
	mustCommand(t, p.a, "transfer.forget", map[string]string{"transferId": id})
	if b.reserved != 0 {
		t.Fatal("successful cleanup retained reservation")
	}
	if _, err := os.Stat(spool); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("forget retained staging files: %v", err)
	}
}
