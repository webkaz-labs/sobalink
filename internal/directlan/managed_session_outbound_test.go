//go:build directlan_managed_session_outbound_tcp

package directlan

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/endpointmeta"
)

// This opt-in source gate uses one fixed synthetic loopback TCP listener per
// case because requestManagedSession owns its real net.Dialer. It creates no
// WG/UDP owner, Core store, endpoint movement, restart, or durable authority.
func TestManagedOutboundBoundReplyComposition(t *testing.T) {
	for _, kind := range []string{"exact", "wrong-binding", "wrong-operation", "v1", "noncanonical", "oversize", "truncated", "lost-reply", "wrong-pin", "cancelled", "stale-registration", "stale-peer", "stopped-owner"} {
		t.Run(kind, func(t *testing.T) {
			listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.ParseIP("127.0.0.1")})
			if err != nil {
				t.Fatal(err)
			}
			var listenerClose sync.Once
			var listenerCloseErr error
			closeListener := func() error {
				listenerClose.Do(func() { listenerCloseErr = listener.Close() })
				return listenerCloseErr
			}
			t.Cleanup(func() {
				if err := closeListener(); err != nil && !errors.Is(err, net.ErrClosed) {
					t.Errorf("fixed listener cleanup failure: %v", err)
				}
			})
			endpoint := listener.Addr().(*net.TCPAddr).AddrPort()
			if endpoint.Addr().String() != "127.0.0.1" || endpoint.Port() < 1024 {
				t.Fatal("unexpected fixed endpoint")
			}
			cfg, remote := managedFixtureConfig()
			cfg.Peers[0].Endpoint = endpoint
			pair := cfg.PairContexts[remote.PublicKey()]
			pair.JoinerEndpoint = endpoint.String()
			cfg.PairContexts[remote.PublicKey()] = pair
			n, g, p := managedFixtureOwnerConfig(t, cfg)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			deadline, _ := ctx.Deadline()
			if err := listener.SetDeadline(deadline); err != nil {
				t.Fatal(err)
			}
			serverDone := make(chan struct{})
			var active atomic.Pointer[net.TCPConn]
			var socketClose sync.Once
			var socketCloseErr error
			closeSocket := func() error {
				raw := active.Load()
				if raw == nil {
					return nil
				}
				socketClose.Do(func() { socketCloseErr = raw.Close() })
				return socketCloseErr
			}
			outcome := struct {
				stage         string
				err, closeErr error
			}{}
			t.Cleanup(func() {
				select {
				case <-serverDone:
					return
				default:
				}
				cancel()
				_ = closeListener()
				if err := closeSocket(); err != nil && !errors.Is(err, net.ErrClosed) {
					t.Errorf("outbound socket cleanup failure: %v", err)
				}
				select {
				case <-serverDone:
				case <-time.After(4 * time.Second):
					t.Error("outbound fixture cleanup did not join")
				}
			})
			go func() {
				defer close(serverDone)
				outcome.stage = "accept"
				raw, err := listener.AcceptTCP()
				if err != nil {
					outcome.err = err
					return
				}
				active.Store(raw)
				defer func() { outcome.closeErr = closeSocket() }()
				if ctx.Err() != nil {
					outcome.err = ctx.Err()
					return
				}
				identity := remote
				if kind == "wrong-pin" {
					identity = controlLifecycleIdentity(113)
				}
				cert, err := certificate(identity, time.Now())
				if err != nil {
					outcome.err = err
					return
				}
				server := tls.Server(raw, tlsConfigProtocol(cert, n.PublicKey(), true, contextProtocolName))
				if err := server.SetDeadline(deadline); err != nil {
					outcome.err = err
					return
				}
				outcome.stage = "tls"
				if err := server.HandshakeContext(ctx); err != nil {
					outcome.err = err
					return
				}
				outcome.stage = "request"
				request, err := readFrame(server, endpointmeta.MaxFrameBytes)
				if err != nil {
					outcome.err = err
					return
				}
				parsed, err := endpointmeta.ParseRequest(request)
				bound, ok := parsed.(*endpointmeta.BoundRequest)
				if err != nil || !ok || bound.Operation != "session" || bound.PairBinding != p.binding {
					outcome.err = ErrUntrusted
					return
				}
				reply, err := endpointmeta.Encode(endpointmeta.SessionReply{Version: 2, Operation: "session", OK: true, PairBinding: p.binding})
				if err != nil {
					outcome.err = err
					return
				}
				outcome.stage = kind
				switch kind {
				case "wrong-binding":
					reply, _ = endpointmeta.Encode(endpointmeta.SessionReply{Version: 2, Operation: "session", OK: true, PairBinding: n.PublicKey()})
				case "wrong-operation":
					reply, _ = endpointmeta.Encode(endpointmeta.ContextReply{Version: 2, Operation: "pair-context-status", OK: true, PairBinding: p.binding, State: "committed"})
				case "v1":
					reply = []byte(`{"version":1,"ok":true}`)
				case "noncanonical":
					reply = append([]byte(" "), reply...)
				case "oversize":
					outcome.err = writeAll(server, []byte{0x7f, 0xff, 0xff, 0xff})
					return
				case "truncated":
					outcome.err = writeAll(server, []byte{0, 0, 0, 20, '{'})
					return
				case "lost-reply":
					outcome.err = nil
					return
				case "cancelled":
					cancel()
				case "stale-registration":
					p.session.registration.Store(8)
				case "stale-peer":
					n.mu.Lock()
					replacement := newPeerStateForGeneration(g, n.PublicKey(), p.peer)
					replacement.session.registration.Store(9)
					n.peers[p.peer.Key] = replacement
					g.refreshBindPolicy(n.peers)
					n.mu.Unlock()
				case "stopped-owner":
					g.requestStop(ErrRecovery)
				}
				outcome.err = writeFrame(server, reply, endpointmeta.MaxFrameBytes)
			}()
			result := n.requestManagedSession(ctx, p)
			select {
			case <-serverDone:
			case <-time.After(4 * time.Second):
				t.Fatal("fixed TCP server did not join")
			}
			if outcome.closeErr != nil {
				t.Fatal("server socket cleanup", outcome.closeErr)
			}
			if kind == "wrong-pin" {
				if outcome.stage != "tls" || outcome.err == nil || !(strings.Contains(outcome.err.Error(), "bad certificate") || managedExpectedPeerClose(outcome.err)) {
					t.Fatal("wrong-pin stage not reached", outcome.stage, outcome.err)
				}
			} else {
				if outcome.stage != kind {
					t.Fatal("intended negative exchange not reached", kind, outcome.stage, outcome.err)
				}
				if outcome.err != nil && !((kind == "cancelled" || kind == "stopped-owner") && managedExpectedPeerClose(outcome.err)) {
					t.Fatal("scripted server exchange failed", outcome.err)
				}
			}
			if kind == "exact" {
				if result != nil || outcome.err != nil || !g.applicationPeer(p) {
					t.Fatal("exact outbound exchange", result, outcome.err)
				}
			} else if result == nil || g.applicationPeer(p) {
				t.Fatalf("%s authenticated: %v", kind, result)
			}
			n.mu.Lock()
			wires, dials := len(n.wires), len(n.dials)
			n.mu.Unlock()
			g.mu.Lock()
			work := len(g.work)
			g.mu.Unlock()
			if wires != 0 || dials != 0 || work != 0 {
				t.Fatal("outbound completion retained live work", wires, dials, work)
			}
			// Cancellation is expected to surface as cancellation or closed transport;
			// it is never accepted as success merely because a valid frame existed.
			if kind == "cancelled" && !errors.Is(ctx.Err(), context.Canceled) {
				t.Fatal("cancellation fixture did not cancel")
			}
		})
	}
}

func managedExpectedPeerClose(err error) bool {
	return errors.Is(err, net.ErrClosed) || errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) || errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.ECONNRESET)
}
