package core

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/policy"
	"github.com/webkaz-labs/sobalink/internal/transport"
)

type mockProxyServer struct {
	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once
}

func (s *mockProxyServer) Close() error          { s.once.Do(func() { s.cancel(); close(s.done) }); return nil }
func (s *mockProxyServer) Done() <-chan struct{} { return s.done }
func (s *mockProxyServer) Addr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1080}
}
func installProxyStarter(t *testing.T, c *Core) (*transport.SOCKSConfig, *transport.Dialer, *atomic.Int32) {
	t.Helper()
	cfg := new(transport.SOCKSConfig)
	dial := new(transport.Dialer)
	count := new(atomic.Int32)
	c.proxyStart = func(ctx context.Context, config transport.SOCKSConfig, d transport.Dialer) (proxyServer, error) {
		count.Add(1)
		*cfg = config
		*dial = d
		_, cancel := context.WithCancel(ctx)
		s := &mockProxyServer{cancel: cancel, done: make(chan struct{})}
		context.AfterFunc(ctx, func() { _ = s.Close() })
		return s, nil
	}
	return cfg, dial, count
}
func proxyFixture() ProxyScope {
	return ProxyScope{Name: "example-proxy", Backend: "tailnet", LoopbackHost: "127.0.0.1", LocalPort: 1080, Lifetime: "until-stopped", Targets: []ProxyTarget{{PeerID: "peer-b", Port: 443}, {PeerID: "peer-b", Port: 8443}}}
}
func previewProxy(t *testing.T, c *Core, scope ProxyScope) proxyReview {
	t.Helper()
	return mustCommand(t, c, "proxy.preview", map[string]any{"scope": scope}).(proxyReview)
}
func startProxy(t *testing.T, c *Core, scope ProxyScope) map[string]any {
	t.Helper()
	review := previewProxy(t, c, scope)
	return mustCommand(t, c, "proxy.start", map[string]any{"scope": review.Scope, "expectedRevision": review.Revision, "username": "fixture-user", "password": "fixture-private-password"}).(map[string]any)
}

func TestProxyReviewCredentialsAndRuntimeOnlyState(t *testing.T) {
	p := newCorePair(t)
	cfg, _, count := installProxyStarter(t, p.a)
	scope := proxyFixture()
	review := previewProxy(t, p.a, scope)
	if count.Load() != 0 || review.Scope.Lifetime != "until-stopped" || review.Endpoint != "127.0.0.1:1080" || len(review.Revision) != 64 {
		t.Fatal("preview opened a listener or omitted reviewed scope")
	}
	input := map[string]any{"scope": review.Scope, "username": "fixture-user", "password": "fixture-private-password"}
	if _, err := command(p.a, randomID(), "proxy.start", input); networkErrorCode(err) != "proxy_review_required" {
		t.Fatal("start bypassed review", err)
	}
	input["expectedRevision"] = review.Revision
	changed := review.Scope
	changed.TTLSeconds = 72 * 3600
	changed.Lifetime = "finite"
	input["scope"] = changed
	if _, err := command(p.a, randomID(), "proxy.start", input); networkErrorCode(err) != "proxy_review_required" {
		t.Fatal("changed lifetime retained stale approval", err)
	}
	input["scope"] = review.Scope
	started := mustCommand(t, p.a, "proxy.start", input).(map[string]any)
	if count.Load() != 1 || cfg.Username != "fixture-user" || cfg.Password != "fixture-private-password" || cfg.Controller != p.a.serviceResources() {
		t.Fatal("authenticated controller-backed listener was not configured")
	}
	if started["expiresAt"] != nil || started["lifetime"] != "until-stopped" || p.a.materializedCount() != 1 {
		t.Fatal("proxy lifetime or resource reservation changed")
	}
	snapshot, err := p.a.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	public, _ := json.Marshal([]any{review, started, snapshot, p.a.permittedServices("peer-b"), p.a.profileCopy()})
	disk, err := os.ReadFile(filepath.Join(p.a.dir, "sobalink.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"fixture-user", "fixture-private-password"} {
		if strings.Contains(string(public), secret) || strings.Contains(string(disk), secret) {
			t.Fatal("proxy credentials escaped runtime input")
		}
	}
	if len(p.a.profileCopy().Services) != 0 {
		t.Fatal("proxy permission was persisted as a service")
	}
	mustCommand(t, p.a, "proxy.stop", map[string]string{"id": started["id"].(string)})
	if len(p.a.proxyViews()) != 0 || p.a.materializedCount() != 0 {
		t.Fatal("stop retained proxy listener reservation")
	}
	finite := previewProxy(t, p.a, changed)
	if finite.Scope.TTLSeconds != 72*3600 {
		t.Fatal("finite proxy lifetime was capped at 24 hours")
	}
}

func TestProxyRejectsScopeAndDoesNotGenerateCredentials(t *testing.T) {
	p := newCorePair(t)
	_, _, count := installProxyStarter(t, p.a)
	for _, mutate := range []func(*ProxyScope){func(s *ProxyScope) { s.LoopbackHost = "0.0.0.0" }, func(s *ProxyScope) { s.LoopbackHost = "localhost" }, func(s *ProxyScope) { s.LocalPort = 80 }, func(s *ProxyScope) { s.LocalPort = PeerPort }, func(s *ProxyScope) { s.Targets[0].Port = PeerPort }, func(s *ProxyScope) { s.Targets[0].PeerID = "missing" }, func(s *ProxyScope) { s.Targets = nil }, func(s *ProxyScope) { s.Targets = append(s.Targets, s.Targets[0]) }, func(s *ProxyScope) { s.Lifetime = "until-revoked" }, func(s *ProxyScope) { s.Backend = "lan" }} {
		scope := proxyFixture()
		mutate(&scope)
		if _, err := command(p.a, randomID(), "proxy.preview", map[string]any{"scope": scope}); err == nil {
			t.Fatal("invalid proxy scope accepted", scope)
		}
	}
	review := previewProxy(t, p.a, proxyFixture())
	for _, credentials := range [][2]string{{"", ""}, {"user", ""}, {"", "password"}, {"user", strings.Repeat("p", 256)}} {
		_, err := command(p.a, randomID(), "proxy.start", map[string]any{"scope": review.Scope, "expectedRevision": review.Revision, "username": credentials[0], "password": credentials[1]})
		if networkErrorCode(err) != "proxy_credentials_required" {
			t.Fatal("missing/invalid credentials were synthesized or accepted", err)
		}
	}
	if count.Load() != 0 {
		t.Fatal("invalid review or credentials reached listener creation")
	}
}

type proxyDialBackend struct {
	NetworkBackend
	dials   atomic.Int32
	remotes []net.Conn
}

func (n *proxyDialBackend) DialIP(_ context.Context, network string, endpoint netip.AddrPort) (net.Conn, error) {
	n.dials.Add(1)
	if network != "tcp" || endpoint.Addr() != netip.MustParseAddr("100.64.0.2") || (endpoint.Port() != 443 && endpoint.Port() != 8443) {
		return nil, errors.New("unexpected approved destination")
	}
	a, b := net.Pipe()
	n.remotes = append(n.remotes, b)
	return a, nil
}
func TestProxyOnlyDialsCurrentApprovedPeerPortsAndRevokes(t *testing.T) {
	p := newCorePair(t)
	backend := &proxyDialBackend{NetworkBackend: p.na}
	p.a.mu.Lock()
	p.a.node = backend
	p.a.mu.Unlock()
	_, dial, _ := installProxyStarter(t, p.a)
	view := startProxy(t, p.a, proxyFixture())
	_ = view
	for _, address := range []string{"example.org:443", "127.0.0.1:443", "192.168.1.4:443", "peer-b.invalid:22", "100.64.0.9:443"} {
		if conn, err := (*dial)(context.Background(), "tcp", address); err == nil || conn != nil {
			t.Fatal("destination escaped exact allowlist", address)
		}
	}
	if backend.dials.Load() != 0 {
		t.Fatal("unapproved destination reached backend")
	}
	conn, err := (*dial)(context.Background(), "tcp", "peer-b.invalid:443")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	defer func() {
		for _, conn := range backend.remotes {
			_ = conn.Close()
		}
	}()
	if backend.dials.Load() != 1 {
		t.Fatal("approved TCP did not use netstack")
	}
	p.na.mu.Lock()
	p.na.state.Snapshot.Peers[0].ID = "replacement-peer"
	p.na.mu.Unlock()
	state, _ := p.na.State(context.Background())
	withServiceOperation(p.a, func() { p.a.revalidateProxies(state) })
	if len(p.a.proxyViews()) != 0 {
		t.Fatal("reassigned peer retained proxy")
	}
	if _, err := (*dial)(context.Background(), "tcp", "peer-b.invalid:443"); err == nil {
		t.Fatal("revoked permission dialed")
	}
	if backend.dials.Load() != 1 {
		t.Fatal("revoked destination reached backend")
	}
}
func TestProxyStopsOnRevokeShutdownAndExpiry(t *testing.T) {
	for _, why := range []string{"revoke", "shutdown", "expiry", "network"} {
		t.Run(why, func(t *testing.T) {
			p := newCorePair(t)
			installProxyStarter(t, p.a)
			view := startProxy(t, p.a, proxyFixture())
			p.a.mu.RLock()
			a := p.a.proxies[view["id"].(string)]
			p.a.mu.RUnlock()
			switch why {
			case "revoke":
				mustCommand(t, p.a, "peer.trust", map[string]any{"peerId": "peer-b", "trusted": false})
			case "shutdown":
				if err := p.a.Close(); err != nil {
					t.Fatal(err)
				}
			case "expiry":
				withServiceOperation(p.a, func() { a.cancel(); p.a.expireProxies() })
			case "network":
				state, _ := p.na.State(context.Background())
				state.Snapshot.Running = false
				withServiceOperation(p.a, func() { p.a.revalidateProxies(state) })
			}
			if a.ctx.Err() == nil || len(p.a.proxyViews()) != 0 {
				t.Fatal("proxy survived", why)
			}
			select {
			case <-a.server.Done():
			case <-time.After(time.Second):
				t.Fatal("proxy listener did not close")
			}
		})
	}
}
func TestProxyCannotBeSharedAndUsesListenerBudget(t *testing.T) {
	p := newCorePair(t)
	installProxyStarter(t, p.a)
	p.a.mu.Lock()
	p.a.capacity = capacity.Defaults()
	p.a.capacity.Resources["materializedListeners"] = capacity.Limited(1)
	p.a.mu.Unlock()
	startProxy(t, p.a, proxyFixture())
	scope := proxyFixture()
	scope.Name = "other-proxy"
	scope.LocalPort = 1081
	review := previewProxy(t, p.a, scope)
	if _, err := command(p.a, randomID(), "proxy.start", map[string]any{"scope": review.Scope, "expectedRevision": review.Revision, "username": "user", "password": "password"}); networkErrorCode(err) != "proxy_listener_capacity" {
		t.Fatal("proxy exceeded shared listener capacity", err)
	}
	if _, err := command(p.a, randomID(), "service.share", map[string]any{"name": "proxy-share", "network": "tcp", "ports": "1080", "peerIds": []string{"peer-b"}, "ttlSeconds": 60}); err == nil {
		t.Fatal("proxy listener was exposed through share")
	}
}

func TestProxyPeerBudgetCountsIdentitiesAndDoesNotRevokeExistingScope(t *testing.T) {
	p := newCorePair(t)
	installProxyStarter(t, p.a)
	p.a.mu.Lock()
	p.a.capacity = capacity.Defaults()
	p.a.capacity.Logical["sharePeers"] = capacity.Limited(1)
	p.a.mu.Unlock()
	scope := proxyFixture()
	scope.Targets = nil
	for port := 8000; port < 8033; port++ {
		scope.Targets = append(scope.Targets, ProxyTarget{PeerID: "peer-b", Port: port})
	}
	if got := previewProxy(t, p.a, scope); len(got.Targets) != 33 {
		t.Fatal("one peer's ports consumed peer identity capacity")
	}
	p.na.mu.Lock()
	p.na.state.Snapshot.Peers = append(p.na.state.Snapshot.Peers, policy.Peer{ID: "peer-c", DNSName: "peer-c.invalid", IPs: []netip.Addr{netip.MustParseAddr("100.64.0.3")}})
	p.na.mu.Unlock()
	scope.Targets = append(scope.Targets, ProxyTarget{PeerID: "peer-c", Port: 443})
	if _, err := command(p.a, randomID(), "proxy.preview", map[string]any{"scope": scope}); networkErrorCode(err) != "proxy_scope_invalid" {
		t.Fatal("distinct peer exceeded identity policy", err)
	}
	p.a.mu.Lock()
	p.a.capacity.Logical["sharePeers"] = capacity.Limited(2)
	p.a.mu.Unlock()
	startProxy(t, p.a, scope)
	p.a.mu.Lock()
	p.a.capacity.Logical["sharePeers"] = capacity.Limited(1)
	p.a.mu.Unlock()
	st, _ := p.na.State(context.Background())
	withServiceOperation(p.a, func() { p.a.revalidateProxies(st) })
	if len(p.a.proxyViews()) != 1 {
		t.Fatal("lowering admission policy revoked a reviewed active scope")
	}
	if _, err := command(p.a, randomID(), "proxy.preview", map[string]any{"scope": scope}); err == nil {
		t.Fatal("lowered peer policy admitted a new expanded scope")
	}
}

func TestProxyRejectsListenerCoveredByExistingShare(t *testing.T) {
	p := newCorePair(t)
	_, _, count := installProxyStarter(t, p.a)
	shared := mustCommand(t, p.a, "service.share", map[string]any{"name": "existing-share", "network": "tcp", "ports": "1080", "peerIds": []string{"peer-b"}, "ttlSeconds": 60}).(map[string]any)
	if _, err := command(p.a, randomID(), "proxy.preview", map[string]any{"scope": proxyFixture()}); networkErrorCode(err) != "proxy_scope_invalid" {
		t.Fatal("proxy could become an already-shared application target", err)
	}
	if count.Load() != 0 || !savedService(t, p.a, shared["id"].(string)).Active {
		t.Fatal("rejected preview mutated an existing permission")
	}
}
func TestProxyDropsStoppedListenerWithoutAutomaticRestart(t *testing.T) {
	p := newCorePair(t)
	_, _, count := installProxyStarter(t, p.a)
	view := startProxy(t, p.a, proxyFixture())
	p.a.mu.RLock()
	a := p.a.proxies[view["id"].(string)]
	p.a.mu.RUnlock()
	_ = a.server.Close()
	if p.a.proxyViews()[0]["status"] != "failed" {
		t.Fatal("stopped listener still reported ready")
	}
	st, _ := p.na.State(context.Background())
	withServiceOperation(p.a, func() { p.a.revalidateProxies(st) })
	if len(p.a.proxyViews()) != 0 || count.Load() != 1 {
		t.Fatal("failed proxy restarted or kept a listener reservation")
	}
}
