package core

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/ranges"
	"github.com/webkaz-labs/sobalink/internal/transfer"
	"github.com/webkaz-labs/sobalink/internal/transport"
	"github.com/webkaz-labs/sobalink/internal/webui"
)

type logoutBackend struct {
	NetworkBackend
	logout func(context.Context) error
}

func (n logoutBackend) Logout(ctx context.Context) error { return n.logout(ctx) }

func TestTailnetLogoutStopsTrafficBeforeBackendAndPreservesProfile(t *testing.T) {
	for _, confirmed := range []bool{true, false} {
		t.Run(map[bool]string{true: "confirmed", false: "unconfirmed"}[confirmed], func(t *testing.T) {
			p := newCorePair(t)
			trustPair(t, p)
			mustCommand(t, p.a, "peer.autosave", map[string]any{"peerId": "peer-b", "enabled": true, "directory": t.TempDir()})
			mustCommand(t, p.a, "service.share", map[string]any{"name": "example", "network": "tcp", "ports": "8080", "peerIds": []string{"peer-b"}, "ttlSeconds": 60})
			before, err := os.ReadFile(filepath.Join(p.a.dir, "sobalink.json"))
			if err != nil {
				t.Fatal(err)
			}
			outbound, cancel := context.WithCancel(context.Background())
			defer cancel()
			runDone := make(chan struct{})
			context.AfterFunc(outbound, func() { close(runDone) })
			p.a.mu.Lock()
			p.a.outgoing["fixture"] = &outgoingBatch{ID: "fixture", cancel: cancel, running: true, runDone: runDone}
			ps := p.a.peerServer
			calls := 0
			p.a.node = logoutBackend{NetworkBackend: p.na, logout: func(ctx context.Context) error {
				calls++
				if ctx.Err() != nil || p.a.ctx.Err() != nil {
					t.Fatal("management context was cancelled before backend logout")
				}
				if p.a.networkReady.Load() || len(p.a.active) != 0 || p.a.peerServer != nil || outbound.Err() == nil {
					t.Fatal("local work remained enabled during backend logout")
				}
				select {
				case <-runDone:
				default:
					t.Fatal("backend logout began before outgoing worker stopped")
				}
				if _, err := p.a.current(context.Background()); err == nil {
					t.Fatal("new peer work remained available")
				}
				if err := p.a.transfers.BindPeer(transfer.Peer{ID: "new-peer", Generation: 1}); !errors.Is(err, transfer.ErrClosed) {
					t.Fatal("receiver remained available", err)
				}
				for _, listener := range ps.listeners {
					if _, err := listener.Accept(); err == nil {
						t.Fatal("peer listener remained available")
					}
				}
				if !confirmed {
					return errors.New("private backend detail")
				}
				return nil
			}}
			p.a.mu.Unlock()
			value, err := command(p.a, "logout-request", "network.logout", map[string]any{})
			result := value.(LogoutResult)
			if calls != 1 || result.LogoutConfirmed != confirmed || !result.LocalTrafficStopped || result.ApplicationState != "stopping" {
				t.Fatalf("incorrect logout result: %+v, %v", result, err)
			}
			if confirmed && err != nil || !confirmed && networkErrorCode(err) != "tailnet_logout_unconfirmed" {
				t.Fatal("backend outcome was misreported", err)
			}
			select {
			case <-p.a.Done():
			default:
				t.Fatal("logout did not stop the application")
			}
			_, repeatErr := command(p.a, "logout-request", "network.logout", map[string]any{})
			if calls != 1 || networkErrorCode(repeatErr) != networkErrorCode(err) {
				t.Fatal("repeat logout changed outcome or repeated backend action")
			}
			after, err := os.ReadFile(filepath.Join(p.a.dir, "sobalink.json"))
			if err != nil || string(before) != string(after) {
				t.Fatal("logout changed saved approvals or selected network", err)
			}
		})
	}
}

func TestLogoutDoesNotClaimTransferDrainOrBackendLogoutOnCancellation(t *testing.T) {
	p := newCorePair(t)
	called := false
	p.a.mu.Lock()
	p.a.node = logoutBackend{NetworkBackend: p.na, logout: func(context.Context) error { called = true; return nil }}
	p.a.outgoing["blocked"] = &outgoingBatch{ID: "blocked", running: true, runDone: make(chan struct{}), cancel: func() {}}
	p.a.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	value, err := p.a.Command(ctx, webui.Command{RequestID: "blocked-logout", Name: "network.logout", Payload: []byte("{}")})
	result, ok := value.(LogoutResult)
	if !ok || called || result.LogoutConfirmed || result.LocalTrafficStopped || networkErrorCode(err) != "tailnet_logout_unconfirmed" || p.a.ctx.Err() == nil {
		t.Fatalf("drain failure misreported: %+v, %v; backend called=%v", value, err, called)
	}
}

func TestStartupRestoresSavedReceiversButNeverServiceGrants(t *testing.T) {
	p := newCorePair(t)
	trustPair(t, p)
	mustCommand(t, p.a, "peer.autosave", map[string]any{"peerId": "peer-b", "enabled": true, "directory": t.TempDir()})
	mustCommand(t, p.a, "service.share", map[string]any{"name": "saved-service", "network": "tcp", "ports": "8080", "peerIds": []string{"peer-b"}, "ttlSeconds": 60})
	if err := p.a.Close(); err != nil {
		t.Fatal(err)
	}
	for _, offline := range []bool{false, true} {
		attempts := 0
		c, err := Open(context.Background(), Options{Directory: p.a.dir, SkipNetworkStart: offline, NodeFactory: func(string, string) (NetworkBackend, error) {
			attempts++
			return nil, errors.New("injected unavailable backend")
		}})
		if err != nil {
			t.Fatal(err)
		}
		if offline && attempts != 0 || !offline && attempts != 1 || len(c.active) != 0 || len(c.profileCopy().Services) != 1 || len(c.transfers.ReceivePolicies()) != 1 {
			t.Fatal("startup network/receiver/service contract changed", offline, attempts)
		}
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

type delayedCloseListener struct {
	*pipeListener
	release <-chan struct{}
}

func (l delayedCloseListener) Close() error {
	<-l.release
	return l.pipeListener.Close()
}

func TestLogoutBoundsEveryServiceDrainAndStillCancelsOtherTraffic(t *testing.T) {
	for _, kind := range []string{"compact", "materialized"} {
		t.Run(kind, func(t *testing.T) {
			p := newCorePair(t)
			release := make(chan struct{})
			var drained <-chan struct{}
			if kind == "compact" {
				entered := make(chan struct{}, 1)
				engine, err := ranges.New(context.Background(), ranges.Options{Authorize: func(context.Context, ranges.Request) (string, error) {
					entered <- struct{}{}
					<-release
					return "peer-b", nil
				}, DialLoopback: func(context.Context, netip.AddrPort) (net.Conn, error) {
					t.Error("cancelled service reached dial")
					return nil, errors.New("cancelled")
				}})
				if err != nil {
					t.Fatal(err)
				}
				ports, _ := ranges.NewSet([]ranges.Interval{{First: 8080, Last: 8080}})
				if err := engine.Replace([]netip.Addr{p.na.ip}, []ranges.Policy{{ID: "fixture", Network: "tcp", Address: p.na.ip, Ports: ports, Loopback: netip.MustParseAddr("127.0.0.1"), PeerIDs: []string{"peer-b"}, ExpiresAt: time.Now().Add(time.Minute)}}); err != nil {
					t.Fatal(err)
				}
				client, server := net.Pipe()
				defer client.Close()
				handle, _ := engine.Handle(netip.AddrPortFrom(p.nb.ip, 30000), netip.AddrPortFrom(p.na.ip, 8080))
				if handle == nil {
					t.Fatal("fixture service not selected")
				}
				go handle(server)
				select {
				case <-entered:
				case <-time.After(time.Second):
					t.Fatal("fixture authorizer did not start")
				}
				p.a.rangeState = &rangeState{engine: engine, unregister: func() {}}
				drained = engine.Done()
			} else {
				listener := delayedCloseListener{pipeListener: &pipeListener{address: netip.AddrPortFrom(p.na.ip, 8080), queue: make(chan net.Conn), done: make(chan struct{})}, release: release}
				ctx, cancel := context.WithCancel(context.Background())
				server, err := transport.StartInboundTCP(ctx, transport.TCPConfig{Target: "127.0.0.1:8080"}, listener, func(context.Context, netip.AddrPort) error { return nil }, func() error { return nil })
				if err != nil {
					cancel()
					t.Fatal(err)
				}
				p.a.mu.Lock()
				p.a.active["fixture"] = &activeService{spec: ServiceSpec{ID: "fixture"}, ctx: ctx, cancel: cancel, servers: []*transport.Server{server}}
				p.a.mu.Unlock()
				drained = server.Done()
			}
			defer func() {
				close(release)
				select {
				case <-drained:
				case <-time.After(time.Second):
					t.Error("fixture service did not drain")
				}
			}()
			called := false
			outbound, stop := context.WithCancel(context.Background())
			defer stop()
			p.a.mu.Lock()
			p.a.node = logoutBackend{NetworkBackend: p.na, logout: func(context.Context) error { called = true; return nil }}
			p.a.outgoing["queued"] = &outgoingBatch{ID: "queued", cancel: stop}
			p.a.mu.Unlock()
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			_, err := p.a.Command(ctx, webui.Command{RequestID: "blocked-service", Name: "network.logout", Payload: []byte("{}")})
			if called || networkErrorCode(err) != "tailnet_logout_unconfirmed" || p.a.ctx.Err() == nil || outbound.Err() == nil {
				t.Fatal("blocked service prevented bounded shutdown/cancellation", err, called)
			}
			if err := p.a.transfers.BindPeer(transfer.Peer{ID: "new-peer", Generation: 1}); !errors.Is(err, transfer.ErrClosed) {
				t.Fatal("blocked service left receiver enabled", err)
			}
		})
	}
}

func TestLogoutRejectsInactiveNetworkAndMalformedPayloadWithoutStopping(t *testing.T) {
	c, err := Open(context.Background(), Options{Directory: t.TempDir(), SkipNetworkStart: true})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := command(c, "idle", "network.logout", map[string]any{}); networkErrorCode(err) != "tailnet_logout_unavailable" {
		t.Fatal(err)
	}
	if _, err := command(c, "bad", "network.logout", map[string]any{"force": true}); err == nil {
		t.Fatal("unknown logout option accepted")
	}
	if c.ctx.Err() != nil || c.closing {
		t.Fatal("invalid request stopped application")
	}
}
