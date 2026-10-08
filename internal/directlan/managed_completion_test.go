//go:build directlan_managed_completion_tls

package directlan

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/endpointmeta"
)

// Opt-in, in-memory TLS composition fixtures only. Writing these source tests
// does not authorize their execution. They never start a listener, use an OS
// socket, run a transport preparer, or establish durable Core authority.
type managedCompletionFixture struct {
	n      *Node
	g      *runtimeGeneration
	p      *peerState
	remote Identity
}

func newManagedCompletionFixture(t *testing.T, completion func(context.Context, *ManagedCompletionRequest) (ContextResponse, error)) *managedCompletionFixture {
	t.Helper()
	cfg, remote := managedFixtureConfig()
	cfg.CompletionAdmission = completion
	n, g, p := managedFixtureOwnerConfig(t, cfg)
	return &managedCompletionFixture{n: n, g: g, p: p, remote: remote}
}

func (f *managedCompletionFixture) request(operation string) endpointmeta.BoundRequest {
	return endpointmeta.BoundRequest{Version: 2, Operation: operation, PairBinding: f.p.binding}
}

func managedCompletionResponse(request *ManagedCompletionRequest) ContextResponse {
	bound := request.Request()
	return ContextResponse{
		Reply: endpointmeta.ContextReply{Version: 2, Operation: bound.Operation, OK: true, PairBinding: bound.PairBinding, State: "committed"},
		Epoch: NewContextEpoch(),
		Admit: func() bool { return true },
	}
}

type managedCompletionExchangeOptions struct {
	wrapServer     func(net.Conn) net.Conn
	afterHandshake func()
	beforeRead     func(*tls.Conn)
	beforeJoin     func()
}

// Only the production ordinary managed dispatcher may invoke CompletionAdmission.
// Pipe cleanup and the real handler join precede any callback-owned assertions.
func (f *managedCompletionFixture) exchange(t *testing.T, data []byte, options managedCompletionExchangeOptions) ([]byte, error) {
	t.Helper()
	serverRaw, clientRaw := net.Pipe()
	var done chan struct{}
	defer func() {
		if options.beforeJoin != nil {
			options.beforeJoin()
		}
		_ = clientRaw.Close()
		_ = serverRaw.Close()
		if done != nil {
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("completion handler cleanup remains unjoined")
			}
		}
	}()
	var raw net.Conn = serverRaw
	if options.wrapServer != nil {
		raw = options.wrapServer(raw)
	}
	deadline := time.Now().Add(2 * time.Second)
	work, err := f.g.acquireWork(nil, true)
	if err != nil {
		t.Fatal(err)
	}
	w := &wire{raw: newControlStream(f.g, raw), g: f.g, work: work, control: true, contextDeadline: deadline}
	f.n.mu.Lock()
	f.n.wires[w] = struct{}{}
	f.n.wg.Add(1)
	f.n.mu.Unlock()
	done = make(chan struct{})
	go func() { defer close(done); defer f.n.wg.Done(); f.n.handle(w) }()
	cert, err := certificate(f.remote, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	client := tls.Client(clientRaw, tlsConfigProtocol(cert, f.n.PublicKey(), false, contextProtocolName))
	if err := client.SetDeadline(deadline); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	if err := client.HandshakeContext(ctx); err != nil {
		return nil, err
	}
	if options.afterHandshake != nil {
		options.afterHandshake()
	}
	if err := writeFrame(client, data, endpointmeta.MaxFrameBytes); err != nil {
		return nil, err
	}
	if options.beforeRead != nil {
		options.beforeRead(client)
	}
	return readFrame(client, endpointmeta.MaxFrameBytes)
}

func (f *managedCompletionFixture) send(t *testing.T, operation string, options managedCompletionExchangeOptions) ([]byte, error) {
	t.Helper()
	data, err := endpointmeta.Encode(f.request(operation))
	if err != nil {
		t.Fatal(err)
	}
	return f.exchange(t, data, options)
}

func (f *managedCompletionFixture) checkUnauthenticatedAndIdle(t *testing.T) {
	t.Helper()
	if f.p.authenticated.Load() != nil || f.g.applicationPeer(f.p) {
		t.Error("completion response granted application/session authentication")
	}
	f.g.mu.Lock()
	count, work := f.g.controlCount, len(f.g.work)
	f.g.mu.Unlock()
	f.n.mu.Lock()
	wires := len(f.n.wires)
	f.n.mu.Unlock()
	if count != 0 || work != 0 || wires != 0 {
		t.Errorf("completion retained finished work: control=%d work=%d wires=%d", count, work, wires)
	}
}

func TestManagedCompletionNilCallbackFailsClosed(t *testing.T) {
	for _, operation := range []string{"pair-context-commit", "pair-context-status"} {
		t.Run(operation, func(t *testing.T) {
			f := newManagedCompletionFixture(t, nil)
			if _, err := f.send(t, operation, managedCompletionExchangeOptions{}); err == nil {
				t.Fatal("completion succeeded without an admission owner")
			}
			f.checkUnauthenticatedAndIdle(t)
		})
	}
}

func TestManagedCompletionZeroRequestCannotBeCurrent(t *testing.T) {
	var absent *ManagedCompletionRequest
	for _, request := range []*ManagedCompletionRequest{absent, {}} {
		if request.Current() || request.Node() != nil || request.PeerKey() != "" || request.Request() != (endpointmeta.BoundRequest{}) {
			t.Fatal("zero completion request exposed authority")
		}
	}
}

func TestManagedCompletionExactCommittedReplyWithoutSessionAuthority(t *testing.T) {
	for _, operation := range []string{"pair-context-commit", "pair-context-status"} {
		t.Run(operation, func(t *testing.T) {
			var f *managedCompletionFixture
			var held *ManagedCompletionRequest
			var calls, admits atomic.Int32
			f = newManagedCompletionFixture(t, func(ctx context.Context, request *ManagedCompletionRequest) (ContextResponse, error) {
				calls.Add(1)
				held = request
				if request.Node() != f.n || request.PeerKey() != f.p.peer.Key || request.Request() != f.request(operation) || !request.Current() {
					t.Error("callback did not receive exact current managed TLS request")
				}
				// Copy the observation without copying the atomic's noCopy marker.
				copied := &ManagedCompletionRequest{self: request.self, node: request.node, capture: request.capture,
					request: request.request, ctx: request.ctx, deadline: request.deadline}
				copied.live.Store(request.live.Load())
				if copied.Current() {
					t.Error("copied callback observation gained current authority")
				}
				for _, gate := range []string{"node", "generation"} {
					if gate == "node" {
						f.n.mu.Lock()
					} else {
						f.g.mu.Lock()
					}
					contended := request.Current()
					if gate == "node" {
						f.n.mu.Unlock()
					} else {
						f.g.mu.Unlock()
					}
					if contended {
						t.Error("contended owner observation did not fail closed", gate)
					}
				}
				if deadline, ok := ctx.Deadline(); !ok || !deadline.After(time.Now()) || deadline.After(time.Now().Add(2*time.Second)) {
					t.Error("callback escaped the accepted wire deadline")
				}
				copyRequest := request.Request()
				copyRequest.PairBinding = strings.Repeat("0", 64)
				if request.Request() != f.request(operation) {
					t.Error("request accessor changed the captured binding")
				}
				f.g.mu.Lock()
				count, work := f.g.controlCount, len(f.g.work)
				f.g.mu.Unlock()
				if count != 1 || work == 0 {
					t.Error("callback lost its bounded control reservation")
				}
				response := managedCompletionResponse(request)
				response.Admit = func() bool { admits.Add(1); return request.Current() }
				return response, nil
			})
			data, err := f.send(t, operation, managedCompletionExchangeOptions{})
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := endpointmeta.ParseReply(data, operation)
			if err != nil {
				t.Fatal(err)
			}
			reply, ok := parsed.(*endpointmeta.ContextReply)
			if !ok || reply.PairBinding != f.p.binding || reply.State != "committed" || !reply.OK {
				t.Fatal("completion reply did not preserve exact committed binding")
			}
			if calls.Load() != 1 || admits.Load() != 1 || held == nil || held.Current() {
				t.Fatal("completion callback/admission repeated or request escaped its lifetime")
			}
			f.checkUnauthenticatedAndIdle(t)
		})
	}
}

func TestManagedCompletionInvalidRequestNeverReachesAdmission(t *testing.T) {
	for _, mode := range []string{"wrong-binding", "wrong-operation", "noncanonical", "old-version"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			f := newManagedCompletionFixture(t, func(_ context.Context, request *ManagedCompletionRequest) (ContextResponse, error) {
				calls.Add(1)
				return managedCompletionResponse(request), nil
			})
			request := f.request("pair-context-commit")
			if mode == "wrong-binding" {
				request.PairBinding = strings.Repeat("0", 64)
			}
			data, err := endpointmeta.Encode(request)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "wrong-operation":
				data = []byte(strings.Replace(string(data), "pair-context-commit", "pair-context-unknown", 1))
			case "noncanonical":
				data = append([]byte(" "), data...)
			case "old-version":
				data = []byte(strings.Replace(string(data), `"version":2`, `"version":1`, 1))
			}
			if _, err := f.exchange(t, data, managedCompletionExchangeOptions{}); err == nil {
				t.Fatal("invalid completion request received a reply")
			}
			if calls.Load() != 0 {
				t.Fatal("invalid completion request reached admission")
			}
			f.checkUnauthenticatedAndIdle(t)
		})
	}
}

func TestManagedCompletionInvalidResponseFailsClosed(t *testing.T) {
	for _, mode := range []string{"callback-error", "nil-reply", "session-reply", "wrong-binding", "wrong-operation", "prepared-status", "nil-epoch", "invalid-epoch", "nil-admit", "denied-admit"} {
		t.Run(mode, func(t *testing.T) {
			var calls, admits atomic.Int32
			f := newManagedCompletionFixture(t, func(_ context.Context, request *ManagedCompletionRequest) (ContextResponse, error) {
				calls.Add(1)
				response := managedCompletionResponse(request)
				response.Admit = func() bool { admits.Add(1); return mode != "denied-admit" }
				reply := response.Reply.(endpointmeta.ContextReply)
				switch mode {
				case "callback-error":
					return response, ErrUnavailable
				case "nil-reply":
					response.Reply = nil
				case "session-reply":
					response.Reply = endpointmeta.SessionReply{Version: 2, Operation: "session", OK: true, PairBinding: reply.PairBinding}
				case "wrong-binding":
					reply.PairBinding = strings.Repeat("0", 64)
					response.Reply = reply
				case "wrong-operation":
					reply.Operation = "pair-context-commit"
					response.Reply = reply
				case "prepared-status":
					reply.State = "prepared"
					response.Reply = reply
				case "nil-epoch":
					response.Epoch = nil
				case "invalid-epoch":
					response.Epoch.Invalidate()
				case "nil-admit":
					response.Admit = nil
				}
				return response, nil
			})
			if _, err := f.send(t, "pair-context-status", managedCompletionExchangeOptions{}); err == nil {
				t.Fatal("invalid completion response was sent")
			}
			wantAdmits := int32(0)
			if mode == "denied-admit" {
				wantAdmits = 1
			}
			if calls.Load() != 1 || admits.Load() != wantAdmits {
				t.Fatalf("wrong callback/admission count: %d/%d", calls.Load(), admits.Load())
			}
			f.checkUnauthenticatedAndIdle(t)
		})
	}
}

func TestManagedCompletionRevalidatesCapturedOwner(t *testing.T) {
	for _, timing := range []string{"callback", "admit"} {
		for _, mode := range []string{"registration", "policy", "generation", "peer", "binding", "cancel", "stop"} {
			t.Run(timing+"/"+mode, func(t *testing.T) {
				var f *managedCompletionFixture
				var held *ManagedCompletionRequest
				var calls, admits atomic.Int32
				invalidate := func() {
					switch mode {
					case "registration":
						f.p.session.registration.Add(1)
					case "policy":
						f.g.refreshBindPolicy(f.g.peers)
					case "generation":
						f.n.generation.Store(nil)
					case "peer":
						f.n.mu.Lock()
						f.n.peers[f.p.peer.Key] = newPeerStateForGeneration(f.g, f.n.PublicKey(), f.p.peer)
						f.n.mu.Unlock()
					case "binding":
						f.p.binding = strings.Repeat("0", 64)
					case "cancel":
						f.n.cancel()
					case "stop":
						f.n.RequestClose()
					}
				}
				f = newManagedCompletionFixture(t, func(_ context.Context, request *ManagedCompletionRequest) (ContextResponse, error) {
					calls.Add(1)
					held = request
					response := managedCompletionResponse(request)
					if timing == "callback" {
						invalidate()
						if request.Current() {
							t.Error("stale captured owner remained current")
						}
					}
					response.Admit = func() bool {
						admits.Add(1)
						if timing == "admit" {
							invalidate()
						}
						return true
					}
					return response, nil
				})
				if _, err := f.send(t, "pair-context-commit", managedCompletionExchangeOptions{}); err == nil {
					t.Fatal("stale completion request received a reply")
				}
				if calls.Load() != 1 || admits.Load() > 1 || held == nil || held.Current() {
					t.Fatal("stale completion escaped its owner or repeated admission")
				}
				if timing == "admit" && admits.Load() != 1 {
					t.Fatal("post-admission invalidation was not exercised")
				}
				f.checkUnauthenticatedAndIdle(t)
			})
		}
	}
}

func TestManagedCompletionEpochInvalidatedAtAdmission(t *testing.T) {
	var admits atomic.Int32
	f := newManagedCompletionFixture(t, func(_ context.Context, request *ManagedCompletionRequest) (ContextResponse, error) {
		response := managedCompletionResponse(request)
		response.Admit = func() bool {
			admits.Add(1)
			response.Epoch.Invalidate()
			return true
		}
		return response, nil
	})
	if _, err := f.send(t, "pair-context-status", managedCompletionExchangeOptions{}); err == nil {
		t.Fatal("invalidated response epoch was sent")
	}
	if admits.Load() != 1 {
		t.Fatal("response admission was not exactly once")
	}
	f.checkUnauthenticatedAndIdle(t)
}

func TestManagedCompletionLateCallbackKeepsReservationUntilReturn(t *testing.T) {
	var f *managedCompletionFixture
	var held *ManagedCompletionRequest
	var admits atomic.Int32
	entered, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	releaseCallback := func() { releaseOnce.Do(func() { close(release) }) }
	f = newManagedCompletionFixture(t, func(ctx context.Context, request *ManagedCompletionRequest) (ContextResponse, error) {
		held = request
		response := managedCompletionResponse(request)
		response.Admit = func() bool { admits.Add(1); return true }
		close(entered)
		<-ctx.Done()
		close(cancelled)
		<-release
		return response, nil
	})
	options := managedCompletionExchangeOptions{beforeJoin: releaseCallback, beforeRead: func(*tls.Conn) {
		defer releaseCallback()
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("completion callback did not start")
		}
		f.n.cancel()
		select {
		case <-cancelled:
		case <-time.After(time.Second):
			t.Fatal("wire cancellation did not reach callback")
		}
		f.g.mu.Lock()
		count, work := f.g.controlCount, len(f.g.work)
		f.g.mu.Unlock()
		if count != 1 || work == 0 {
			t.Error("cancelled callback released its reservation before returning")
		}
		if held.Current() {
			t.Error("cancelled callback retained current authority")
		}
	}}
	if _, err := f.send(t, "pair-context-commit", options); err == nil {
		t.Fatal("late cancelled callback emitted completion success")
	}
	if admits.Load() != 0 || held == nil || held.Current() {
		t.Fatal("late callback reached admission or retained authority")
	}
	f.checkUnauthenticatedAndIdle(t)
}

func TestManagedCompletionDeadlineRefusesLateCallback(t *testing.T) {
	var calls, admits atomic.Int32
	var expired atomic.Bool
	f := newManagedCompletionFixture(t, func(ctx context.Context, request *ManagedCompletionRequest) (ContextResponse, error) {
		calls.Add(1)
		response := managedCompletionResponse(request)
		response.Admit = func() bool { admits.Add(1); return true }
		<-ctx.Done()
		expired.Store(errors.Is(ctx.Err(), context.DeadlineExceeded))
		if request.Current() {
			t.Error("expired callback observation remained current")
		}
		return response, nil
	})
	if _, err := f.send(t, "pair-context-commit", managedCompletionExchangeOptions{}); err == nil {
		t.Fatal("deadline-expired callback emitted a completion reply")
	}
	if calls.Load() != 1 || admits.Load() != 0 || !expired.Load() {
		t.Fatal("bounded callback expiration did not fail before admission")
	}
	f.checkUnauthenticatedAndIdle(t)
}

type managedCompletionFaultConn struct {
	net.Conn
	failWrite     atomic.Bool
	observeWrite  atomic.Bool
	writeObserved atomic.Bool
	writeStarted  chan struct{}
	closed        chan struct{}
	closeNotify   sync.Once
	writeErr      error
	closeErr      error
}

func (c *managedCompletionFaultConn) Write(data []byte) (int, error) {
	if c.observeWrite.Load() && c.writeObserved.CompareAndSwap(false, true) {
		close(c.writeStarted)
	}
	if c.failWrite.Load() {
		return 0, c.writeErr
	}
	return c.Conn.Write(data)
}
func (c *managedCompletionFaultConn) Close() error {
	err := c.Conn.Close()
	if c.closed != nil {
		c.closeNotify.Do(func() { close(c.closed) })
	}
	if c.closeErr != nil {
		return c.closeErr
	}
	return err
}

func TestManagedCompletionEpochClosesBlockedResponseWrite(t *testing.T) {
	var raw *managedCompletionFaultConn
	var admits atomic.Int32
	epoch := NewContextEpoch()
	f := newManagedCompletionFixture(t, func(_ context.Context, request *ManagedCompletionRequest) (ContextResponse, error) {
		response := managedCompletionResponse(request)
		response.Epoch = epoch
		response.Admit = func() bool { admits.Add(1); raw.observeWrite.Store(true); return true }
		return response, nil
	})
	options := managedCompletionExchangeOptions{
		wrapServer: func(conn net.Conn) net.Conn {
			raw = &managedCompletionFaultConn{Conn: conn, writeStarted: make(chan struct{}), closed: make(chan struct{})}
			return raw
		},
		beforeRead: func(*tls.Conn) {
			// The client is not reading, so the response's first pipe write
			// cannot complete before its result epoch is invalidated.
			select {
			case <-raw.writeStarted:
			case <-time.After(time.Second):
				t.Fatal("completion response write did not start")
			}
			epoch.Invalidate()
			select {
			case <-raw.closed:
			case <-time.After(time.Second):
				t.Fatal("response epoch did not close the blocked wire")
			}
		},
	}
	if _, err := f.send(t, "pair-context-status", options); err == nil {
		t.Fatal("invalidated epoch allowed a blocked completion reply to finish")
	}
	if admits.Load() != 1 || !raw.writeObserved.Load() {
		t.Fatal("mid-write epoch invalidation was not exercised")
	}
	f.checkUnauthenticatedAndIdle(t)
}

func TestManagedCompletionWriteFailureDoesNotAuthenticate(t *testing.T) {
	var raw *managedCompletionFaultConn
	var calls, admits atomic.Int32
	f := newManagedCompletionFixture(t, func(_ context.Context, request *ManagedCompletionRequest) (ContextResponse, error) {
		calls.Add(1)
		response := managedCompletionResponse(request)
		response.Admit = func() bool { admits.Add(1); raw.failWrite.Store(true); return true }
		return response, nil
	})
	options := managedCompletionExchangeOptions{wrapServer: func(conn net.Conn) net.Conn {
		raw = &managedCompletionFaultConn{Conn: conn, writeErr: errors.New("synthetic completion write failure")}
		return raw
	}}
	if _, err := f.send(t, "pair-context-commit", options); err == nil {
		t.Fatal("failed completion write produced a reply")
	}
	if calls.Load() != 1 || admits.Load() != 1 || !raw.failWrite.Load() {
		t.Fatal("completion write failure was not exercised")
	}
	f.checkUnauthenticatedAndIdle(t)
}

func TestManagedCompletionFailedCloseRetainsAndSealsOwner(t *testing.T) {
	failure := errors.New("synthetic completion close failure")
	var calls atomic.Int32
	f := newManagedCompletionFixture(t, func(_ context.Context, request *ManagedCompletionRequest) (ContextResponse, error) {
		calls.Add(1)
		return managedCompletionResponse(request), nil
	})
	options := managedCompletionExchangeOptions{wrapServer: func(conn net.Conn) net.Conn {
		return &managedCompletionFaultConn{Conn: conn, closeErr: failure}
	}}
	if _, err := f.send(t, "pair-context-status", options); err != nil {
		t.Fatal("completion frame was not delivered before close failure", err)
	}
	if calls.Load() != 1 || f.p.authenticated.Load() != nil || f.g.applicationPeer(f.p) || f.g.open() {
		t.Fatal("failed completion close left authority or admission open")
	}
	f.g.mu.Lock()
	count, work, retained := f.g.controlCount, len(f.g.work), len(f.g.failedControl)
	for _, err := range f.g.failedControl {
		if !errors.Is(err, failure) {
			t.Error("completion close failure lost its cause")
		}
	}
	f.g.mu.Unlock()
	if count != 1 || work != 0 || retained != 1 {
		t.Fatalf("completion close failure was not retained and charged: %d/%d/%d", count, work, retained)
	}
}
