package directlan

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"net"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/endpointmeta"
)

// These fixtures run TLS only over net.Pipe. They never call Node.Start,
// buildContextGeneration or outbound Exchange, whose production paths open OS
// sockets. Lifecycle setup is synthetic; authentication is always production
// handleContext plus the real v2 pinned mutual TLS path, never a proof factory.
type contextExchangeFixture struct {
	n              *Node
	g              *runtimeGeneration
	remote         Identity
	peer           Peer
	local, request endpointmeta.PrepareRequest
	reply          endpointmeta.PrepareReply
	epoch          *ContextEpoch
}

func contextExchangeNew(t *testing.T, completion ContextCompletion) *contextExchangeFixture {
	t.Helper()
	local := Identity{Seed: strings.Repeat("11", 32)}
	remote := Identity{Seed: strings.Repeat("22", 32)}
	peer := Peer{Key: remote.PublicKey(), TunnelKey: remote.TunnelKey(), Endpoint: netip.MustParseAddrPort("127.0.0.2:21002")}
	n, err := NewContextControl(ContextControlConfig{Identity: local, Listen: netip.MustParseAddrPort("127.0.0.1:21001"),
		AllowedPrefixes: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}, Peers: []Peer{peer}, ControlLimit: 4, Completion: completion})
	if err != nil {
		t.Fatal(err)
	}
	g := newRuntimeGeneration(n, nil, nil)
	g.controlPeers = map[string]*contextPeerRegistration{peer.Key: {g: g, peer: peer}}
	g.failedControl = make(map[*controlStream]error)
	g.controlOpen.Store(true)
	n.generation.Store(g)
	n.started = true
	close(g.built)
	close(g.published)
	go g.supervise()
	t.Cleanup(func() {
		closed := make(chan error, 1)
		go func() { closed <- n.Close() }()
		select {
		case err := <-closed:
			if err != nil {
				t.Errorf("synthetic owner close: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("synthetic owner cleanup remains unjoined")
		}
	})
	scope := endpointmeta.Scope{Family: "ipv4", Prefixes: []string{"127.0.0.0/8"}}
	ownNonce := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))
	remoteNonce := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{2}, 32))
	own := endpointmeta.PrepareRequest{Version: 2, Operation: "pair-context-prepare", Sender: local.PublicKey(), Recipient: peer.Key,
		SenderTunnelKey: local.TunnelKey(), RecipientTunnelKey: peer.TunnelKey, SenderNonce: ownNonce,
		SenderEndpoint: n.Endpoint().String(), RecipientEndpoint: peer.Endpoint.String(), SenderScope: scope}
	request := endpointmeta.PrepareRequest{Version: 2, Operation: "pair-context-prepare", Sender: peer.Key, Recipient: local.PublicKey(),
		SenderTunnelKey: peer.TunnelKey, RecipientTunnelKey: local.TunnelKey(), SenderNonce: remoteNonce,
		SenderEndpoint: peer.Endpoint.String(), RecipientEndpoint: n.Endpoint().String(), SenderScope: scope}
	p := endpointmeta.PairContext{Version: 1, HostKey: local.PublicKey(), JoinerKey: peer.Key, HostTunnelKey: local.TunnelKey(), JoinerTunnelKey: peer.TunnelKey,
		HostNonce: ownNonce, JoinerNonce: remoteNonce, HostEndpoint: n.Endpoint().String(), JoinerEndpoint: peer.Endpoint.String(), HostScope: scope, JoinerScope: scope}
	if p.HostKey > p.JoinerKey {
		p.HostKey, p.JoinerKey = p.JoinerKey, p.HostKey
		p.HostTunnelKey, p.JoinerTunnelKey = p.JoinerTunnelKey, p.HostTunnelKey
		p.HostNonce, p.JoinerNonce = p.JoinerNonce, p.HostNonce
		p.HostEndpoint, p.JoinerEndpoint = p.JoinerEndpoint, p.HostEndpoint
		p.HostScope, p.JoinerScope = p.JoinerScope, p.HostScope
	}
	binding, err := p.Binding()
	if err != nil {
		t.Fatal(err)
	}
	return &contextExchangeFixture{n: n, g: g, remote: remote, peer: peer, local: own, request: request,
		reply: endpointmeta.PrepareReply{Version: 2, Operation: "pair-context-prepare", OK: true, PairContext: p, PairBinding: binding}, epoch: NewContextEpoch()}
}
func (f *contextExchangeFixture) arm(t *testing.T) *ContextAttempt {
	t.Helper()
	a, err := f.n.ArmContextAttempt(f.peer.Key, f.local, f.epoch)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// afterAccept runs after the wire's immutable cutoff and before client TLS starts;
// afterHandshake runs after authentication and before the client writes a frame.
func (f *contextExchangeFixture) exchange(t *testing.T, data []byte, protocol string, identity Identity, afterAccept, afterHandshake func()) ([]byte, error) {
	t.Helper()
	serverRaw, clientRaw := net.Pipe()
	var done chan struct{}
	// Install pipe cleanup before any assertion/callback can call Goexit.
	// Once registered, only the real handler may release its work lease.
	defer func() {
		if err := clientRaw.Close(); err != nil {
			t.Errorf("client pipe close: %v", err)
		}
		if err := serverRaw.Close(); err != nil {
			t.Errorf("server pipe close: %v", err)
		}
		if done != nil {
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				// Do not return to callers that inspect callback-owned values.
				t.Fatal("synthetic TLS owner cleanup remains unjoined")
			}
		}
	}()
	deadline := time.Now().Add(2 * time.Second)
	work, err := f.g.acquireWork(nil, true)
	if err != nil {
		t.Fatal(err)
	}
	w := &wire{raw: newControlStream(f.g, serverRaw), g: f.g, work: work, control: true, contextDeadline: deadline}
	f.n.mu.Lock()
	w.contextArmCutoff = f.n.contextArmRevision
	f.n.wires[w] = struct{}{}
	f.n.wg.Add(1)
	f.n.mu.Unlock()
	done = make(chan struct{})
	go func() { defer close(done); defer f.n.wg.Done(); f.n.handleContext(w) }()
	// The release owner and joined cleanup already exist if this callback or
	// subsequent certificate/assertion code terminates the test goroutine.
	if afterAccept != nil {
		afterAccept()
	}
	cert, err := certificate(identity, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	client := tls.Client(clientRaw, tlsConfigProtocol(cert, f.n.PublicKey(), false, protocol))
	_ = client.SetDeadline(deadline)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	if err := client.HandshakeContext(ctx); err != nil {
		return nil, err
	}
	if afterHandshake != nil {
		afterHandshake()
	}
	if err := writeFrame(client, data, endpointmeta.MaxFrameBytes); err != nil {
		return nil, err
	}
	return readFrame(client, endpointmeta.MaxFrameBytes)
}
func (f *contextExchangeFixture) send(t *testing.T) ([]byte, error) {
	t.Helper()
	data, err := endpointmeta.Encode(f.request)
	if err != nil {
		t.Fatal(err)
	}
	return f.exchange(t, data, contextProtocolName, f.remote, nil, nil)
}

func TestContextClaimZeroCopyAndWrongAttemptReject(t *testing.T) {
	if _, err := (VerifiedContextExchange{}).TryClaimFor(&ContextAttempt{}); err == nil {
		t.Fatal("zero evidence accepted")
	}
	for _, mode := range []string{"copy_then_repeat", "wrong_pointer", "copied_attempt"} {
		t.Run(mode, func(t *testing.T) {
			called := false
			f := contextExchangeNew(t, func(_ context.Context, a *ContextAttempt, v VerifiedContextExchange) (ContextResponse, error) {
				called = true
				copyProof := v
				switch mode {
				case "copy_then_repeat":
					if _, err := v.TryClaimFor(a); err != nil {
						t.Errorf("first claim: %v", err)
					}
				case "wrong_pointer":
					if _, err := v.TryClaimFor(&ContextAttempt{}); err == nil {
						t.Error("wrong pointer accepted")
					}
				case "copied_attempt":
					copyAttempt := *a
					if _, err := v.TryClaimFor(&copyAttempt); err == nil {
						t.Error("copied attempt accepted")
					}
				}
				if _, err := copyProof.TryClaimFor(a); err == nil {
					t.Error("shared consumption cell reused")
				}
				return ContextResponse{}, ErrUnavailable
			})
			f.arm(t)
			_, _ = f.send(t)
			if !called {
				t.Fatal("authenticated callback not reached")
			}
		})
	}
}

func TestContextClaimCurrentRegistrationAndEpoch(t *testing.T) {
	for _, mode := range []string{"epoch", "cancel", "registration", "generation", "replacement", "stop"} {
		t.Run(mode, func(t *testing.T) {
			var f *contextExchangeFixture
			called := false
			f = contextExchangeNew(t, func(_ context.Context, a *ContextAttempt, v VerifiedContextExchange) (ContextResponse, error) {
				called = true
				switch mode {
				case "epoch":
					f.epoch.Invalidate()
				case "cancel":
					a.Cancel()
				case "registration":
					old := f.g.controlPeers[f.peer.Key]
					f.g.controlPeers[f.peer.Key] = &contextPeerRegistration{g: f.g, peer: f.peer}
					defer func() { f.g.controlPeers[f.peer.Key] = old }()
				case "generation":
					f.n.generation.Store(nil)
					defer f.n.generation.Store(f.g)
				case "replacement":
					if _, err := f.n.ArmContextAttempt(f.peer.Key, f.local, NewContextEpoch()); err != nil {
						t.Errorf("replacement: %v", err)
					}
				case "stop":
					f.n.RequestClose()
				}
				if _, err := v.TryClaimFor(a); err == nil {
					t.Error("stale/terminal evidence accepted")
				}
				return ContextResponse{}, ErrUnavailable
			})
			f.arm(t)
			_, _ = f.send(t)
			if !called {
				t.Fatal("callback not reached")
			}
		})
	}
}

func TestContextClaimTryLockContention(t *testing.T) {
	for _, gate := range []string{"node", "generation"} {
		t.Run(gate, func(t *testing.T) {
			var f *contextExchangeFixture
			called := false
			var claimUnjoined atomic.Bool
			f = contextExchangeNew(t, func(_ context.Context, a *ContextAttempt, v VerifiedContextExchange) (ContextResponse, error) {
				called = true
				if gate == "node" {
					f.n.mu.Lock()
				} else {
					f.g.mu.Lock()
				}
				claimed := make(chan error, 1)
				go func() { _, err := v.TryClaimFor(a); claimed <- err }()
				var err error
				returned := false
				select {
				case err = <-claimed:
					returned = true
				case <-time.After(200 * time.Millisecond):
					t.Error("claim waited for a contended gate")
				}
				if gate == "node" {
					f.n.mu.Unlock()
				} else {
					f.g.mu.Unlock()
				}
				if !returned {
					// The prompt-TryLock assertion above has already failed.
					// Allow bounded cleanup after releasing the held gate, but
					// never reinterpret that late return as a prompt rejection.
					select {
					case err = <-claimed:
					case <-time.After(3 * time.Second):
						claimUnjoined.Store(true)
						t.Error("contended claim cleanup remains unjoined")
						return ContextResponse{}, ErrUnavailable
					}
				}
				if err == nil {
					t.Error("contended claim accepted")
				}
				if _, err := v.TryClaimFor(a); err == nil {
					t.Error("contended proof not burned")
				}
				return ContextResponse{}, ErrUnavailable
			})
			f.arm(t)
			_, _ = f.send(t)
			if claimUnjoined.Load() {
				t.Fatal("cannot inspect an unjoined claim")
			}
			if !called {
				t.Fatal("callback not reached")
			}
		})
	}
}

func TestContextInboundArmsPredateReadAndTLSBuffering(t *testing.T) {
	for _, timing := range []string{"after_accept", "after_handshake", "replacement_after_accept"} {
		t.Run(timing, func(t *testing.T) {
			var calls atomic.Int32
			f := contextExchangeNew(t, func(context.Context, *ContextAttempt, VerifiedContextExchange) (ContextResponse, error) {
				calls.Add(1)
				return ContextResponse{}, ErrUnavailable
			})
			if timing == "replacement_after_accept" {
				f.arm(t)
			}
			arm := func() { f.arm(t) }
			var before, after func()
			if timing == "after_handshake" {
				after = arm
			} else {
				before = arm
			}
			data, err := endpointmeta.Encode(f.request)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = f.exchange(t, data, contextProtocolName, f.remote, before, after)
			if calls.Load() != 0 {
				t.Fatal("new arm relabeled an accepted connection")
			}
		})
	}
}

func TestContextWireClosedOperationsAndExactTranscript(t *testing.T) {
	for _, mode := range []string{"v1", "unknown_pin", "session", "pair", "endpoint_update", "changed_binding", "changed_endpoint", "noncanonical"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			f := contextExchangeNew(t, func(context.Context, *ContextAttempt, VerifiedContextExchange) (ContextResponse, error) {
				calls.Add(1)
				return ContextResponse{}, ErrUnavailable
			})
			f.arm(t)
			var request endpointmeta.Request = f.request
			proto, identity := contextProtocolName, f.remote
			switch mode {
			case "v1":
				proto = protocolName
			case "unknown_pin":
				identity = Identity{Seed: strings.Repeat("33", 32)}
			case "session":
				request = endpointmeta.BoundRequest{Version: 2, Operation: "session", PairBinding: f.reply.PairBinding}
			case "pair":
				request = endpointmeta.PairRequest{Version: 2, Operation: "pair", Token: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{3}, 32)), Peer: endpointmeta.PeerWire{Key: f.peer.Key, TunnelKey: f.peer.TunnelKey, Endpoint: f.peer.Endpoint.String()}, JoinerNonce: f.request.SenderNonce, JoinerScope: f.request.SenderScope}
			case "endpoint_update":
				// Structural envelope with synthetic signature bytes only. No
				// signing, update verification, adoption or endpoint move runs.
				request = endpointmeta.Envelope{Update: endpointmeta.UpdateBody{Version: 1, Domain: endpointmeta.UpdateDomain, PairBinding: f.reply.PairBinding, Issuer: f.peer.Key, Recipient: f.n.PublicKey(), IssuerTunnelKey: f.peer.TunnelKey, RecipientTunnelKey: f.n.cfg.Identity.TunnelKey(), Sequence: "1", PriorEndpoint: f.peer.Endpoint.String(), Operation: "set", Endpoint: f.peer.Endpoint.String(), ScopeDigest: strings.Repeat("a", 64), Issued: "2030-01-01T00:00:00Z", Lifetime: "finite", Expires: "2030-01-01T01:00:00Z"}, Signature: base64.RawURLEncoding.EncodeToString(make([]byte, 64))}
			case "changed_binding":
				bound := endpointmeta.BoundRequest{Version: 2, Operation: "pair-context-status", PairBinding: f.reply.PairBinding}
				if _, err := f.n.ArmContextAttempt(f.peer.Key, bound, f.epoch); err != nil {
					t.Fatal(err)
				}
				bound.PairBinding = strings.Repeat("0", 64)
				request = bound
			case "changed_endpoint":
				copy := f.request
				copy.SenderEndpoint = "127.0.0.3:21003"
				request = copy
			}
			data, err := endpointmeta.Encode(request)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "noncanonical" {
				data = append([]byte(" "), data...)
			}
			_, _ = f.exchange(t, data, proto, identity, nil, nil)
			if calls.Load() != 0 {
				t.Fatal("unsupported or mismatched transcript reached callback")
			}
		})
	}
}

func TestContextCompletionOwnsProofAndDetaches(t *testing.T) {
	t.Run("unclaimed", func(t *testing.T) {
		var held VerifiedContextExchange
		var attempt *ContextAttempt
		f := contextExchangeNew(t, func(_ context.Context, a *ContextAttempt, v VerifiedContextExchange) (ContextResponse, error) {
			held, attempt = v, a
			return ContextResponse{}, nil
		})
		f.arm(t)
		_, _ = f.send(t)
		if attempt == nil {
			t.Fatal("callback missing")
		}
		if _, err := held.TryClaimFor(attempt); err == nil {
			t.Fatal("unclaimed evidence survived completion")
		}
		if attempt.data() != nil {
			t.Fatal("unclaimed attempt retained runtime graph")
		}
	})
	var f *contextExchangeFixture
	var held VerifiedContextExchange
	var attempt *ContextAttempt
	called := false
	f = contextExchangeNew(t, func(_ context.Context, a *ContextAttempt, v VerifiedContextExchange) (ContextResponse, error) {
		called = true
		held = v
		attempt = a
		f.g.mu.Lock()
		count, work := f.g.controlCount, len(f.g.work)
		f.g.mu.Unlock()
		if count != 1 || work == 0 {
			t.Error("callback lost control reservation")
		}
		transcript, err := v.TryClaimFor(a)
		if err != nil {
			t.Errorf("claim: %v", err)
			return ContextResponse{}, err
		}
		copyRequest := transcript.Request().(*endpointmeta.PrepareRequest)
		copyRequest.SenderScope.Prefixes[0] = "127.0.0.0/16"
		if transcript.Request().(*endpointmeta.PrepareRequest).SenderScope.Prefixes[0] != "127.0.0.0/8" {
			t.Error("transcript accessor aliased stored scope")
		}
		return ContextResponse{Reply: f.reply, Epoch: f.epoch, Admit: func() bool { return true }}, nil
	})
	f.arm(t)
	if reply, err := f.send(t); err != nil || len(reply) == 0 {
		t.Fatalf("owned reply: %v", err)
	}
	if !called || attempt == nil {
		t.Fatal("callback missing")
	}
	if _, err := held.TryClaimFor(attempt); err == nil {
		t.Fatal("proof escaped completion")
	}
	if attempt.data() != nil {
		t.Fatal("attempt retained runtime graph after cleanup")
	}
	f.g.mu.Lock()
	count, work := f.g.controlCount, len(f.g.work)
	f.g.mu.Unlock()
	if count != 0 || work != 0 {
		t.Fatalf("reservation survived actual cleanup: %d/%d", count, work)
	}
}

func TestContextResponseUsesResultEpochAndOneAdmission(t *testing.T) {
	for _, mode := range []string{"fresh", "old_epoch", "later_write", "cancel", "stop"} {
		t.Run(mode, func(t *testing.T) {
			var f *contextExchangeFixture
			var admits atomic.Int32
			called := false
			f = contextExchangeNew(t, func(_ context.Context, a *ContextAttempt, v VerifiedContextExchange) (ContextResponse, error) {
				called = true
				if _, err := v.TryClaimFor(a); err != nil {
					return ContextResponse{}, err
				}
				f.epoch.Invalidate() // simulate the completed owner's own write
				current := NewContextEpoch()
				if mode == "old_epoch" {
					current = f.epoch
				}
				return ContextResponse{Reply: f.reply, Epoch: current, Admit: func() bool {
					admits.Add(1)
					switch mode {
					case "later_write":
						current.Invalidate()
					case "cancel":
						a.Cancel()
					case "stop":
						f.n.RequestClose()
					}
					return true
				}}, nil
			})
			f.arm(t)
			reply, err := f.send(t)
			if !called {
				t.Fatal("callback missing")
			}
			if mode == "fresh" {
				if err != nil || len(reply) == 0 {
					t.Fatalf("fresh response: %v", err)
				}
			} else if err == nil {
				t.Fatal("invalid response sent")
			}
			if admits.Load() > 1 {
				t.Fatal("response admission repeated")
			}
			if mode == "old_epoch" && admits.Load() != 0 {
				t.Fatal("old epoch reached response gate")
			}
		})
	}
}
