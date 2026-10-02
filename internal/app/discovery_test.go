package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/webkaz-labs/tsnet-bridge/internal/config"
	"github.com/webkaz-labs/tsnet-bridge/internal/discovery"
	"github.com/webkaz-labs/tsnet-bridge/internal/policy"
)

// All discovery tests use synthetic peer identities and local in-memory or
// loopback transports. They do not enroll a node or verify real tailnet policy.
type discoveryFake struct {
	*fakeNode
	who  func(context.Context, netip.AddrPort) (string, error)
	dial func(context.Context, string, netip.AddrPort) (net.Conn, error)
}

func (n *discoveryFake) WhoIs(ctx context.Context, source netip.AddrPort) (string, error) {
	if n.who != nil {
		return n.who(ctx, source)
	}
	return "peer1", nil
}
func (n *discoveryFake) DialIP(ctx context.Context, network string, address netip.AddrPort) (net.Conn, error) {
	if n.dial != nil {
		return n.dial(ctx, network, address)
	}
	return nil, errors.New("no discovery endpoint")
}

func discoveryFixture(t *testing.T) (*Service, *discoveryFake, netip.AddrPort) {
	t.Helper()
	s, base := ruleFixture(t)
	n := &discoveryFake{fakeNode: base}
	s.Node = n
	now := time.Now()
	s.discovery = &localDiscovery{state: "listening", shares: []advertisedShare{{
		service: discovery.Service{ID: strings.Repeat("a", 32), Purpose: "web", Network: "tcp", Port: 8080, ExpiresAt: now.Add(time.Minute).UTC(), Application: "unverified"},
		allowed: []config.PeerRef{{ID: "peer1", Host: "server.example.ts.net"}},
		pins:    map[netip.Addr]string{netip.MustParseAddr("100.64.1.2"): "peer1"},
		life:    &lifetime{expires: now.Add(time.Minute)},
		done:    make(chan struct{}),
	}}}
	return s, n, netip.MustParseAddrPort("100.64.1.2:12345")
}

func TestDiscoveryAuthorizationRequiresCurrentWhoIsAndStartPins(t *testing.T) {
	cases := []struct {
		name   string
		change func(*Service, *discoveryFake, *netip.AddrPort)
	}{
		{"whois-error", func(_ *Service, n *discoveryFake, _ *netip.AddrPort) {
			n.who = func(context.Context, netip.AddrPort) (string, error) { return "peer1", errors.New("unavailable") }
		}},
		{"whois-empty", func(_ *Service, n *discoveryFake, _ *netip.AddrPort) {
			n.who = func(context.Context, netip.AddrPort) (string, error) { return "", nil }
		}},
		{"whois-invalid", func(_ *Service, n *discoveryFake, _ *netip.AddrPort) {
			n.who = func(context.Context, netip.AddrPort) (string, error) { return "peer1\nforged", nil }
		}},
		{"whois-wrong-peer", func(_ *Service, n *discoveryFake, _ *netip.AddrPort) {
			n.who = func(context.Context, netip.AddrPort) (string, error) { return "peer2", nil }
		}},
		{"non-tailnet-source", func(_ *Service, _ *discoveryFake, p *netip.AddrPort) { *p = netip.MustParseAddrPort("127.0.0.1:12345") }},
		{"zero-port", func(_ *Service, _ *discoveryFake, p *netip.AddrPort) { *p = netip.MustParseAddrPort("100.64.1.2:0") }},
		{"offline", func(_ *Service, n *discoveryFake, _ *netip.AddrPort) { n.state.Snapshot.Running = false }},
		{"peer-missing", func(_ *Service, n *discoveryFake, _ *netip.AddrPort) { n.state.Snapshot.Peers = nil }},
		{"peer-expired", func(_ *Service, n *discoveryFake, _ *netip.AddrPort) { n.state.Snapshot.Peers[0].Expired = true }},
		{"source-reassigned", func(_ *Service, n *discoveryFake, _ *netip.AddrPort) { n.state.Snapshot.Peers[0].ID = "peer2" }},
		{"ambiguous-source", func(_ *Service, n *discoveryFake, _ *netip.AddrPort) {
			n.state.Snapshot.Peers = append(n.state.Snapshot.Peers, policy.Peer{ID: "peer2", IPs: []netip.Addr{netip.MustParseAddr("100.64.1.2")}})
		}},
		{"not-allowed", func(s *Service, _ *discoveryFake, _ *netip.AddrPort) {
			s.discovery.shares[0].allowed = []config.PeerRef{{ID: "peer2"}}
		}},
		{"not-pinned", func(s *Service, _ *discoveryFake, _ *netip.AddrPort) { s.discovery.shares[0].pins = nil }},
		{"pinned-to-other-peer", func(s *Service, _ *discoveryFake, _ *netip.AddrPort) {
			s.discovery.shares[0].pins = map[netip.Addr]string{netip.MustParseAddr("100.64.1.2"): "peer2"}
		}},
		{"peer-moved-since-start", func(_ *Service, n *discoveryFake, p *netip.AddrPort) {
			n.state.Snapshot.Peers[0].IPs = []netip.Addr{netip.MustParseAddr("100.64.1.3")}
			*p = netip.MustParseAddrPort("100.64.1.3:12345")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, n, source := discoveryFixture(t)
			before, err := s.discoveryServices(t.Context(), source)
			if err != nil || len(before) != 1 {
				t.Fatal("invalid fixture", before, err)
			}
			tc.change(s, n, &source)
			list, _ := s.discoveryServices(t.Context(), source)
			if len(list) != 0 {
				t.Fatal("unauthorized metadata disclosed", list)
			}
		})
	}
}

func TestDiscoveryLifetimeAndClosedServerFailClosed(t *testing.T) {
	for _, reason := range []string{"stop", "ttl", "lease", "failure", "server", "advertised-expiry"} {
		t.Run(reason, func(t *testing.T) {
			s, _, source := discoveryFixture(t)
			share := &s.discovery.shares[0]
			switch reason {
			case "stop":
				share.life.stop()
			case "ttl":
				share.life.expires = time.Now().Add(-time.Second)
			case "lease":
				share.life.lease = time.Now().Add(-time.Second)
			case "failure":
				share.life.fail("peer-identity-changed")
			case "server":
				done := make(chan struct{})
				close(done)
				share.done = done
			case "advertised-expiry":
				share.service.ExpiresAt = time.Now().Add(-time.Second)
			}
			list, err := s.discoveryServices(t.Context(), source)
			if err != nil || len(list) != 0 {
				t.Fatal("inactive share advertised", list, err)
			}
		})
	}
}

func TestDiscoveryRechecksGrantAfterBlockedIdentityLookup(t *testing.T) {
	for _, action := range []string{"stop", "expire", "cancel"} {
		t.Run(action, func(t *testing.T) {
			s, n, source := discoveryFixture(t)
			entered, release := make(chan struct{}), make(chan struct{})
			n.who = func(ctx context.Context, _ netip.AddrPort) (string, error) {
				close(entered)
				select {
				case <-release:
					return "peer1", nil
				case <-ctx.Done():
					return "", ctx.Err()
				}
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan []discovery.Service, 1)
			go func() { list, _ := s.discoveryServices(ctx, source); done <- list }()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("identity lookup did not start")
			}
			life := s.discovery.shares[0].life
			switch action {
			case "stop":
				life.stop()
			case "expire":
				life.mu.Lock()
				life.expires = time.Now().Add(-time.Second)
				life.mu.Unlock()
			case "cancel":
				cancel()
			}
			close(release)
			select {
			case list := <-done:
				if len(list) != 0 {
					t.Fatal("grant survived concurrent change", list)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("lookup did not finish")
			}
		})
	}
}

func TestDiscoveryResponseOnlyContainsCallerServicesAndMinimalDTO(t *testing.T) {
	s, n, source := discoveryFixture(t)
	other := s.discovery.shares[0]
	other.service.ID = strings.Repeat("b", 32)
	other.allowed = []config.PeerRef{{ID: "peer2", Host: "other.example.ts.net"}}
	other.pins = map[netip.Addr]string{netip.MustParseAddr("100.64.1.3"): "peer2"}
	s.discovery.shares = append(s.discovery.shares, other)
	n.state.Snapshot.Peers = append(n.state.Snapshot.Peers, policy.Peer{ID: "peer2", DNSName: "other.example.ts.net", IPs: []netip.Addr{netip.MustParseAddr("100.64.1.3")}})
	list, err := s.discoveryServices(t.Context(), source)
	if err != nil || len(list) != 1 || list[0].ID != strings.Repeat("a", 32) {
		t.Fatal(list, err)
	}
	data, err := json.Marshal(list[0])
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	want := []string{"id", "purpose", "network", "port", "expires_at", "application"}
	if len(fields) != len(want) {
		t.Fatal("unexpected remote metadata", string(data))
	}
	for _, key := range want {
		if _, ok := fields[key]; !ok {
			t.Fatal("missing metadata", key)
		}
	}
	for _, secret := range []string{"server.example.ts.net", "other.example.ts.net", "peer1", "peer2", "127.0.0.1", "allowed", "owner", "target"} {
		if strings.Contains(string(data), secret) {
			t.Fatal("private metadata leaked", secret)
		}
	}
}

func TestDiscoveryOptInIsExplicitAndBoundToReview(t *testing.T) {
	old := `{"name":"web","purpose":"web","direction":"share","network":"tcp","listen_port":8080,"target_host":"127.0.0.1","target_port":8080,"allowed_peers":[{"id":"peer1","host":"server.example.ts.net"}],"enabled":false}`
	var r config.Rule
	if err := json.Unmarshal([]byte(old), &r); err != nil {
		t.Fatal(err)
	}
	if r.Discoverable {
		t.Fatal("old profile silently opted in")
	}
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	before := config.RulesDigest([]config.Rule{r})
	r.Discoverable = true
	if config.RulesDigest([]config.Rule{r}) == before {
		t.Fatal("discovery exposure not bound to reviewed scope")
	}
	r.Direction = "forward"
	r.TargetHost, r.PeerID, r.AllowedPeers = "server.example.ts.net", "peer1", nil
	if r.Validate() == nil {
		t.Fatal("forward rule can publish discovery metadata")
	}
}

func TestDiscoveryQueryPinsTransportAndRejectsChangedIdentity(t *testing.T) {
	for _, replace := range []bool{false, true} {
		t.Run(fmt.Sprint("replace=", replace), func(t *testing.T) {
			s, n, _ := discoveryFixture(t)
			var seen []netip.AddrPort
			n.dial = func(ctx context.Context, network string, endpoint netip.AddrPort) (net.Conn, error) {
				if network != "tcp" {
					t.Errorf("unexpected network %q", network)
				}
				seen = append(seen, endpoint)
				client, server := net.Pipe()
				go func() {
					defer server.Close()
					server.SetDeadline(time.Now().Add(5 * time.Second))
					buf := make([]byte, 4096)
					if _, err := server.Read(buf); err != nil {
						return
					}
					if replace {
						n.mu.Lock()
						// Replace the slice rather than mutating a snapshot in use.
						n.state.Snapshot.Peers = []policy.Peer{{ID: "replacement", DNSName: "server.example.ts.net", IPs: []netip.Addr{netip.MustParseAddr("100.64.1.2")}}}
						n.mu.Unlock()
					}
					body, _ := json.Marshal(discovery.Response{Version: discovery.Version, Services: []discovery.Service{s.discovery.shares[0].service}})
					fmt.Fprintf(server, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(body), body)
				}()
				return client, nil
			}
			peer := n.state.Snapshot.Peers[0]
			result := s.queryDiscoveryPeer(t.Context(), peer)
			if !reflect.DeepEqual(seen, []netip.AddrPort{netip.MustParseAddrPort("100.64.1.2:54543")}) {
				t.Fatal("unexpected destination", seen)
			}
			if replace {
				if result.peer.State != "unavailable" || len(result.services) != 0 {
					t.Fatal("reassigned identity trusted", result)
				}
			} else if result.peer.State != "confirmed" || len(result.services) != 1 || result.services[0].PeerID != "peer1" || result.services[0].PeerHost != "server.example.ts.net" || result.services[0].Application != "unverified" {
				t.Fatal(result)
			}
		})
	}
}

func TestDiscoveryConcurrentQueriesAndCancellationAreBounded(t *testing.T) {
	s, n, _ := discoveryFixture(t)
	started := make(chan struct{}, discoveryConcurrency)
	var mu sync.Mutex
	active, peak, calls := 0, 0, 0
	n.state.Snapshot.Peers = nil
	for i := 1; i <= discoveryPeerLimit+7; i++ {
		n.state.Snapshot.Peers = append(n.state.Snapshot.Peers, policy.Peer{ID: fmt.Sprintf("peer%03d", i), DNSName: fmt.Sprintf("peer%d.example.ts.net", i), IPs: []netip.Addr{netip.AddrFrom4([4]byte{100, 64, 2, byte(i)})}})
	}
	n.dial = func(ctx context.Context, _ string, _ netip.AddrPort) (net.Conn, error) {
		mu.Lock()
		active++
		calls++
		if active > peak {
			peak = active
		}
		mu.Unlock()
		select {
		case started <- struct{}{}:
		default:
		}
		<-ctx.Done()
		mu.Lock()
		active--
		mu.Unlock()
		return nil, ctx.Err()
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan ServiceCatalog, 1)
	go func() { result, _ := s.discoverServices(ctx, ""); done <- result }()
	for range discoveryConcurrency {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("bounded workers did not start")
		}
	}
	if _, err := s.discoverServices(t.Context(), ""); err == nil {
		t.Fatal("concurrent catalog admitted")
	}
	cancel()
	select {
	case result := <-done:
		if !result.Truncated || len(result.Peers) != discoveryPeerLimit || len(result.Services) != 0 {
			t.Fatal("incorrect bounded result", len(result.Peers), result.Truncated)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("catalog cancellation blocked")
	}
	mu.Lock()
	defer mu.Unlock()
	if active != 0 || peak > discoveryConcurrency || calls > discoveryPeerLimit {
		t.Fatal("unbounded discovery", active, peak, calls)
	}
}

func TestDiscoveryPeerFilteringDoesNotProbeInvalidOrExpiredPeers(t *testing.T) {
	s, n, _ := discoveryFixture(t)
	n.state.Snapshot.Peers = []policy.Peer{
		{ID: "expired", DNSName: "expired.example.ts.net", IPs: []netip.Addr{netip.MustParseAddr("100.64.1.3")}, Expired: true},
		{ID: "bad\nidentity", DNSName: "bad.example.ts.net", IPs: []netip.Addr{netip.MustParseAddr("100.64.1.4")}},
		{ID: "no-endpoint"},
		{ID: "valid", DNSName: "valid.example.ts.net", IPs: []netip.Addr{netip.MustParseAddr("100.64.1.5")}},
	}
	calls := 0
	n.dial = func(context.Context, string, netip.AddrPort) (net.Conn, error) { calls++; return nil, io.EOF }
	result, err := s.discoverServices(t.Context(), "valid")
	if err != nil || len(result.Peers) != 1 || result.Peers[0].PeerID != "valid" || calls != 1 {
		t.Fatal(result, calls, err)
	}
	if _, err := s.discoverServices(t.Context(), "bad\nidentity"); err == nil {
		t.Fatal("invalid selection accepted")
	}
	result, err = s.discoverServices(t.Context(), "missing")
	if err != nil || len(result.Peers) != 0 || len(result.Services) != 0 || calls != 1 {
		t.Fatal(result, calls, err)
	}
}

func TestDiscoveryObservedRevocationCannotResumeWithoutRestart(t *testing.T) {
	for _, change := range []string{"missing", "expired", "reassigned"} {
		t.Run(change, func(t *testing.T) {
			s, n, source := discoveryFixture(t)
			original := n.state.Snapshot
			switch change {
			case "missing":
				n.state.Snapshot.Peers = nil
			case "expired":
				n.state.Snapshot.Peers = []policy.Peer{{ID: "peer1", IPs: []netip.Addr{source.Addr()}, Expired: true}}
			case "reassigned":
				n.state.Snapshot.Peers = []policy.Peer{{ID: "peer1", IPs: []netip.Addr{netip.MustParseAddr("100.64.1.3")}}, {ID: "peer2", IPs: []netip.Addr{source.Addr()}}}
			}
			if list, _ := s.discoveryServices(t.Context(), source); len(list) != 0 {
				t.Fatal("revoked service visible", list)
			}
			n.state.Snapshot = original
			if list, _ := s.discoveryServices(t.Context(), source); len(list) != 0 {
				t.Fatal("observed revocation resumed without new grant", list)
			}
			if s.discovery.shares[0].life.check() == nil {
				t.Fatal("revocation was not terminal")
			}
		})
	}
}

func TestDiscoveryUnallowedCallerCannotRevokeHealthyGrant(t *testing.T) {
	s, n, source := discoveryFixture(t)
	n.state.Snapshot.Peers = append(n.state.Snapshot.Peers, policy.Peer{ID: "peer2", IPs: []netip.Addr{netip.MustParseAddr("100.64.1.3")}})
	n.who = func(context.Context, netip.AddrPort) (string, error) { return "peer2", nil }
	if list, _ := s.discoveryServices(t.Context(), netip.MustParseAddrPort("100.64.1.3:12345")); len(list) != 0 {
		t.Fatal("unallowed caller saw metadata", list)
	}
	// Neither lying about an address nor a different valid caller can revoke an
	// otherwise healthy grant. Only current backend identity evidence can do so.
	if list, _ := s.discoveryServices(t.Context(), source); len(list) != 0 {
		t.Fatal("WhoIs mismatch accepted", list)
	}
	if err := s.discovery.shares[0].life.check(); err != nil {
		t.Fatal("unauthorized caller revoked healthy grant", err)
	}
	n.who = nil
	if list, err := s.discoveryServices(t.Context(), source); err != nil || len(list) != 1 {
		t.Fatal(list, err)
	}
}

type discoveryInboundFake struct {
	*discoveryFake
	listenMu          sync.Mutex
	listeners         []net.Listener
	discoveryAttempts int
	discoveryFailure  bool
}

type discoveryAdvertisedListener struct {
	net.Listener
	address net.Addr
}

func (l discoveryAdvertisedListener) Addr() net.Addr { return l.address }
func (l discoveryAdvertisedListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return discoveryAdvertisedConn{Conn: c, address: l.address}, nil
}

type discoveryAdvertisedConn struct {
	net.Conn
	address net.Addr
}

func (c discoveryAdvertisedConn) LocalAddr() net.Addr { return c.address }
func (c discoveryAdvertisedConn) RemoteAddr() net.Addr {
	return net.TCPAddrFromAddrPort(netip.MustParseAddrPort("100.64.1.2:12345"))
}
func (n *discoveryInboundFake) Listen(network, address string) (net.Listener, error) {
	ap, err := netip.ParseAddrPort(address)
	if err != nil || !config.TailnetIP(ap.Addr()) || network != "tcp" {
		return nil, errors.New("invalid test listener")
	}
	n.listenMu.Lock()
	defer n.listenMu.Unlock()
	if ap.Port() == discovery.Port {
		n.discoveryAttempts++
		if n.discoveryFailure {
			return nil, errors.New("discovery port unavailable")
		}
	}
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	n.listeners = append(n.listeners, l)
	return discoveryAdvertisedListener{Listener: l, address: net.TCPAddrFromAddrPort(ap)}, nil
}
func (n *discoveryInboundFake) ListenPacket(string, string) (net.PacketConn, error) {
	return nil, errors.New("unused test packet listener")
}
func discoveryRuleFixture(t *testing.T) (*Service, *discoveryInboundFake, config.Rule) {
	t.Helper()
	s, base := ruleFixture(t)
	n := &discoveryInboundFake{discoveryFake: &discoveryFake{fakeNode: base}}
	s.Node = n
	t.Cleanup(func() {
		s.closeDiscovery()
		n.listenMu.Lock()
		defer n.listenMu.Unlock()
		for _, l := range n.listeners {
			l.Close()
		}
	})
	r := config.Rule{Name: "web-share", Purpose: "web", Direction: "share", Network: "tcp", ListenPort: 8080, TargetHost: "127.0.0.1", TargetPort: 8081, AllowedPeers: []config.PeerRef{{ID: "peer1", Host: "server.example.ts.net"}}, Discoverable: true}
	return s, n, r
}

func TestDiscoveryShareStartRepeatStopRestartAndExpiry(t *testing.T) {
	s, n, rule := discoveryRuleFixture(t)
	saveRule(t, s, rule)
	if s.discovery != nil || n.discoveryAttempts != 0 {
		t.Fatal("saved rule opened discovery")
	}
	startRules(t, s, RuleCommand{Names: []string{rule.Name}, TTLSeconds: 60})
	source := netip.MustParseAddrPort("100.64.1.2:12345")
	list, err := s.discoveryServices(t.Context(), source)
	if err != nil || len(list) != 1 || len(list[0].ID) != 32 || list[0].Port != rule.ListenPort {
		t.Fatal("started rule not visible", list, err)
	}
	first := list[0]
	startRules(t, s, RuleCommand{Names: []string{rule.Name}, TTLSeconds: 60})
	list, err = s.discoveryServices(t.Context(), source)
	if err != nil || len(list) != 1 || list[0].ID != first.ID || list[0].ExpiresAt != first.ExpiresAt {
		t.Fatal("identical start changed grant", list, err)
	}
	if _, err = s.rules.command(t.Context(), RuleCommand{Action: "stop", Names: []string{rule.Name}}); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.discoveryServices(t.Context(), source); len(list) != 0 {
		t.Fatal("stopped service visible", list)
	}
	startRules(t, s, RuleCommand{Names: []string{rule.Name}, TTLSeconds: 60})
	list, err = s.discoveryServices(t.Context(), source)
	if err != nil || len(list) != 1 || list[0].ID == first.ID {
		t.Fatal("restart reused former publication", list, err)
	}
	entry := s.rules.entries[rule.Name]
	entry.life.mu.Lock()
	entry.life.expires = time.Now().Add(-time.Second)
	entry.life.mu.Unlock()
	if list, _ := s.discoveryServices(t.Context(), source); len(list) != 0 {
		t.Fatal("expired service visible before manager tick", list)
	}
	s.rules.expire()
	if s.Status().Discovery != "disabled" || entry.desired {
		t.Fatal("expired discovery remains enabled", s.Status())
	}
}

func TestDiscoveryPrivateShareRemainsPrivateOnStart(t *testing.T) {
	s, n, rule := discoveryRuleFixture(t)
	rule.Discoverable = false
	saveRule(t, s, rule)
	startRules(t, s, RuleCommand{Names: []string{rule.Name}, TTLSeconds: 60})
	if s.discovery != nil || n.discoveryAttempts != 0 {
		t.Fatal("private share opened discovery")
	}
	if s.rules.entries[rule.Name].status.State != "ready" {
		t.Fatal("private share failed")
	}
}

func TestDiscoveryBindFailurePreservesSharingAndHealthRetries(t *testing.T) {
	s, n, rule := discoveryRuleFixture(t)
	n.discoveryFailure = true
	saveRule(t, s, rule)
	startRules(t, s, RuleCommand{Names: []string{rule.Name}, TTLSeconds: 60})
	entry := s.rules.entries[rule.Name]
	if entry.status.State != "ready" || entry.server == nil || s.Status().Discovery != "unavailable" {
		t.Fatal("metadata port failure blocked share", s.Status())
	}
	originalServer := entry.server
	n.listenMu.Lock()
	n.discoveryFailure = false
	n.listenMu.Unlock()
	s.check(t.Context())
	if entry.server != originalServer || s.Status().Discovery != "listening" {
		t.Fatal("discovery did not recover independently", s.Status())
	}
	n.listenMu.Lock()
	attempts := n.discoveryAttempts
	n.listenMu.Unlock()
	if attempts != 2 {
		t.Fatal("unexpected bind retries", attempts)
	}
	s.discovery.mu.RLock()
	listener := s.discovery.listener
	s.discovery.mu.RUnlock()
	listener.Close()
	deadline := time.After(5 * time.Second)
	for {
		s.discovery.mu.RLock()
		failed := s.discovery.listener == nil
		s.discovery.mu.RUnlock()
		if failed {
			break
		}
		select {
		case <-deadline:
			t.Fatal("listener failure not observed")
		case <-time.After(time.Millisecond):
		}
	}
	s.check(t.Context())
	if entry.server != originalServer || s.Status().Discovery != "listening" {
		t.Fatal("listener failure did not recover", s.Status())
	}
	n.listenMu.Lock()
	attempts = n.discoveryAttempts
	n.listenMu.Unlock()
	if attempts != 3 {
		t.Fatal("unexpected listener-failure retries", attempts)
	}
}

func TestDiscoveryReconnectCannotSilentlyReplaceStartPins(t *testing.T) {
	s, n, rule := discoveryRuleFixture(t)
	saveRule(t, s, rule)
	startRules(t, s, RuleCommand{Names: []string{rule.Name}, TTLSeconds: 60})
	n.mu.Lock()
	n.state.Snapshot.Running = false
	n.mu.Unlock()
	s.check(t.Context())
	n.mu.Lock()
	n.state.Snapshot.Running = true
	n.state.Snapshot.Peers = []policy.Peer{{ID: "peer1", DNSName: "server.example.ts.net", IPs: []netip.Addr{netip.MustParseAddr("100.64.1.3")}}}
	n.mu.Unlock()
	s.check(t.Context())
	list, _ := s.discoveryServices(t.Context(), netip.MustParseAddrPort("100.64.1.3:12345"))
	if len(list) != 0 {
		t.Fatal("new source IP silently gained old start authorization", list)
	}
}

func TestDiscoveryReportsEffectiveLeaseExpiryAndFreshRenewal(t *testing.T) {
	s, _, source := discoveryFixture(t)
	share := &s.discovery.shares[0]
	now := time.Now()
	share.life.expires = now.Add(time.Hour)
	share.life.lease = now.Add(30 * time.Second)
	share.service.ExpiresAt = share.life.expires.UTC()
	list, err := s.discoveryServices(t.Context(), source)
	if err != nil || len(list) != 1 || !list[0].ExpiresAt.Equal(share.life.lease) {
		t.Fatal("shorter lease not advertised", list, err)
	}
	originalID := list[0].ID
	if err := share.life.renew(time.Minute); err != nil {
		t.Fatal(err)
	}
	list, err = s.discoveryServices(t.Context(), source)
	if err != nil || len(list) != 1 || list[0].ID != originalID || !list[0].ExpiresAt.Equal(share.life.lease) {
		t.Fatal("renewed effective expiry not refreshed", list, err)
	}
	share.life.expires = now.Add(45 * time.Second)
	share.service.ExpiresAt = share.life.expires.UTC()
	list, err = s.discoveryServices(t.Context(), source)
	if err != nil || len(list) != 1 || !list[0].ExpiresAt.Equal(share.life.expires) {
		t.Fatal("TTL cap not advertised", list, err)
	}
}

func TestCanceledRuleCommandDoesNotSaveOrStart(t *testing.T) {
	s, _, rule := discoveryRuleFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.rules.command(ctx, RuleCommand{Action: "save", Rule: &rule}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if len(s.rules.entries) != 0 {
		t.Fatal("canceled command saved rule")
	}
}
