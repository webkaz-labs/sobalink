package directlan

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"net/netip"
	"reflect"
	"testing"
	"time"

	"github.com/tailscale/wireguard-go/device"
	"github.com/webkaz-labs/sobalink/internal/endpointmeta"
)

// Synthetic source acceptance only. These helpers never use Start, an OS
// socket, a transport preparer, context publication, or persisted authority.
func managedFixtureConfig() (Config, Identity) {
	local, remote := controlLifecycleIdentity(111), controlLifecycleIdentity(112)
	scope := endpointmeta.Scope{Family: "ipv4", Prefixes: []string{"127.0.0.0/8"}}
	pair := endpointmeta.PairContext{Version: 1, HostKey: local.PublicKey(), JoinerKey: remote.PublicKey(), HostTunnelKey: local.TunnelKey(), JoinerTunnelKey: remote.TunnelKey(), HostNonce: base64.RawURLEncoding.EncodeToString(make([]byte, 32)), JoinerNonce: base64.RawURLEncoding.EncodeToString(make([]byte, 32)), HostEndpoint: "127.0.0.1:45101", JoinerEndpoint: "127.0.0.1:45102", HostScope: scope, JoinerScope: scope}
	cfg := Config{Identity: local, Listen: netip.MustParseAddrPort(pair.HostEndpoint), AllowedPrefixes: []netip.Prefix{netip.MustParsePrefix(scope.Prefixes[0])}, Peers: []Peer{{Key: remote.PublicKey(), TunnelKey: remote.TunnelKey(), Endpoint: netip.MustParseAddrPort(pair.JoinerEndpoint)}}, PairContexts: map[string]endpointmeta.PairContext{remote.PublicKey(): pair}}
	return cfg, remote
}
func managedFixtureOwner(t *testing.T) (*Node, *runtimeGeneration, *peerState, Identity) {
	t.Helper()
	cfg, remote := managedFixtureConfig()
	n, g, peer := managedFixtureOwnerConfig(t, cfg)
	return n, g, peer, remote
}

func managedFixtureOwnerConfig(t *testing.T, cfg Config) (*Node, *runtimeGeneration, *peerState) {
	t.Helper()
	n, err := NewNode(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(n.cancel)
	bind := &lanBind{cfg: cloneGenerationConfig(cfg)}
	g := newRuntimeGeneration(n, bind, nil)
	g.cfg = cloneGenerationConfig(n.cfg)
	bind.owner = g
	peer := newPeerStateForGeneration(g, n.PublicKey(), cfg.Peers[0])
	peer.session.registration.Store(7)
	g.peers[peer.peer.Key] = peer
	n.peers = g.peers
	n.bind = bind
	n.generation.Store(g)
	n.started = true
	g.traffic.Store(true)
	g.refreshBindPolicy(g.peers)
	return n, g, peer
}
func TestManagedConfigExactProjectionAndCopies(t *testing.T) {
	cfg, remote := managedFixtureConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	clone := cloneGenerationConfig(cfg)
	n, err := NewNode(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer n.cancel()
	pair := cfg.PairContexts[remote.PublicKey()]
	pair.HostScope.Prefixes[0] = "10.0.0.0/8"
	pair.JoinerScope.Prefixes[0] = "10.0.0.0/8"
	delete(cfg.PairContexts, remote.PublicKey())
	if !reflect.DeepEqual(n.cfg.PairContexts, clone.PairContexts) {
		t.Fatal("constructor aliases input")
	}
	for _, mutate := range []func(*Config){
		func(c *Config) { c.Peers = nil },
		func(c *Config) { c.Peers[0].Endpoint = netip.MustParseAddrPort("127.0.0.1:45103") },
		func(c *Config) { c.Listen = netip.MustParseAddrPort("127.0.0.1:45104") },
		func(c *Config) { c.AllowedPrefixes = []netip.Prefix{netip.MustParsePrefix("127.0.0.0/9")} },
		func(c *Config) {
			pair := c.PairContexts[remote.PublicKey()]
			pair.JoinerTunnelKey = c.Identity.TunnelKey()
			c.PairContexts[remote.PublicKey()] = pair
		},
	} {
		invalid := cloneGenerationConfig(clone)
		mutate(&invalid)
		if invalid.Validate() == nil {
			t.Fatal("changed projection accepted")
		}
	}
}
func TestManagedAuthenticationExactRegistrationPolicyAndOwner(t *testing.T) {
	n, g, p, _ := managedFixtureOwner(t)
	p.session.transition(device.PeerSessionEstablished)
	p.session.confirm()
	if g.applicationPeer(p) {
		t.Fatal("WG readiness authenticated a managed binding")
	}
	captured := n.captureManagedSession(p)
	if captured == nil {
		t.Fatal("missing identity")
	}
	// Synthetic admission test, not evidence of a completed TLS exchange.
	if err := n.commitManagedSession(context.Background(), captured); err != nil {
		t.Fatal(err)
	}
	if !g.applicationPeer(p) {
		t.Fatal("exact authentication rejected")
	}
	p.session.registration.Store(8)
	if g.applicationPeer(p) || n.commitManagedSession(context.Background(), captured) == nil {
		t.Fatal("registration replacement accepted")
	}
	p.session.registration.Store(7)
	g.refreshBindPolicy(g.peers)
	if g.applicationPeer(p) || n.commitManagedSession(context.Background(), captured) == nil {
		t.Fatal("policy replacement accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if n.commitManagedSession(ctx, n.captureManagedSession(p)) == nil {
		t.Fatal("cancelled commit accepted")
	}
	fresh := n.captureManagedSession(p)
	n.peers[p.peer.Key] = newPeerStateForGeneration(g, n.PublicKey(), p.peer)
	if n.commitManagedSession(context.Background(), fresh) == nil {
		t.Fatal("peer replacement accepted")
	}
	n.closing.Store(true)
	if g.applicationPeer(p) {
		t.Fatal("stopped owner accepted")
	}
}
func TestManagedMutationAndDowngradeStayRefused(t *testing.T) {
	n, g, p, remote := managedFixtureOwner(t)
	saved := false
	n.cfg.Persist = func([]Peer) error { saved = true; return nil }
	if n.Revoke(p.peer.Key) == nil || n.commitPeerLocked(p.peer) == nil || saved {
		t.Fatal("legacy managed mutation reached persistence")
	}
	delete(n.peers, p.peer.Key)
	if !n.managedKey(p.peer.Key) || n.commitPeerLocked(p.peer) == nil {
		t.Fatal("retired identity lost managed classification")
	}
	n.peers[p.peer.Key] = p
	cert, err := certificate(remote, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	verify := n.ordinaryServerTLS(g).VerifyConnection
	for _, protocol := range []string{"", protocolName, contextProtocolName} {
		err := verify(tls.ConnectionState{PeerCertificates: []*x509.Certificate{parsed}, NegotiatedProtocol: protocol})
		if (err == nil) != (protocol == contextProtocolName) {
			t.Fatalf("ALPN %q: %v", protocol, err)
		}
	}
	if _, err := n.candidateConfigLocked(nil, TransportEndpoints{}); err == nil {
		t.Fatal("managed preparer admitted")
	}
}

func TestManagedBoundedCommitAndControlCapacity(t *testing.T) {
	n, g, p, _ := managedFixtureOwner(t)
	captured := n.captureManagedSession(p)
	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if err := n.commitManagedSession(expired, captured); err == nil || g.applicationPeer(p) {
		t.Fatal("late bounded reply authenticated")
	}
	g.controlLimit = 1
	first, err := g.acquireWork(nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.acquireWork(nil, true); !errors.Is(err, ErrCapacity) {
		t.Fatal("control capacity not enforced", err)
	}
	app, err := g.acquireWork(nil, false)
	if err != nil {
		t.Fatal("control capacity consumed application work", err)
	}
	app.finish()
	first.finish()
}
