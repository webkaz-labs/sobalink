package directlan

import (
	"context"
	"encoding/base64"
	"errors"
	"net/netip"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Source-only synthetic acceptance: no Node/transport constructor, listener,
// TLS exchange, WireGuard registration, preparer, persisted receipt or runtime.
func deniedFixtureOwner() (*Node, *runtimeGeneration, Peer) {
	cfg, _ := managedFixtureConfig()
	denied := testIdentity(113)
	peer := Peer{Key: denied.PublicKey(), TunnelKey: denied.TunnelKey(), Endpoint: netip.MustParseAddrPort("127.0.0.1:45103")}
	cfg.DeniedPeerKeys = []string{peer.Key}
	n := &Node{cfg: cloneGenerationConfig(cfg), started: true, peers: map[string]*peerState{}}
	g := &runtimeGeneration{n: n, cfg: cloneGenerationConfig(n.cfg), peers: n.peers}
	g.traffic.Store(true)
	n.generation.Store(g)
	return n, g, peer
}

func TestDeniedConfigCanonicalDisjointAndCopied(t *testing.T) {
	cfg, remote := managedFixtureConfig()
	key := testIdentity(113).PublicKey()
	cfg.DeniedPeerKeys = []string{key}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, denied := range [][]string{{""}, {"bad"}, {strings.ToUpper(key)}, {key, key}, {cfg.Identity.PublicKey()}, {remote.PublicKey()}} {
		bad := cloneGenerationConfig(cfg)
		bad.DeniedPeerKeys = denied
		if err := bad.Validate(); !errors.Is(err, ErrIdentity) {
			t.Fatalf("invalid denial accepted: %v", err)
		}
	}
	constructorCopy := cloneGenerationConfig(cfg)
	generationCopy := cloneGenerationConfig(constructorCopy)
	cfg.DeniedPeerKeys[0] = testIdentity(114).PublicKey()
	constructorCopy.DeniedPeerKeys[0] = testIdentity(115).PublicKey()
	if !reflect.DeepEqual(generationCopy.DeniedPeerKeys, []string{key}) || !generationCopy.deniedKey(key) {
		t.Fatal("generation denial aliases constructor input")
	}
	generationCopy.Peers, generationCopy.PairContexts = nil, nil
	if err := generationCopy.Validate(); err != nil || !generationCopy.protectedPairs() {
		t.Fatal("all-terminal config lost protection", err)
	}
}

func TestDeniedKeysNeverBecomeUnknownLegacyOrPairingAuthority(t *testing.T) {
	n, g, denied := deniedFixtureOwner()
	saves := 0
	n.cfg.Persist = func([]Peer) error { saves++; return nil }
	for _, protocol := range []string{"", protocolName, contextProtocolName} {
		if err := n.ordinaryPeerProtocolLocked(g, denied.Key, protocol); !errors.Is(err, ErrUntrusted) {
			t.Fatalf("terminal TLS/frame dispatch accepted %q: %v", protocol, err)
		}
	}
	if n.managedKey(denied.Key) || n.peers[denied.Key] != nil || g.peers[denied.Key] != nil {
		t.Fatal("terminal key acquired managed context or peer state")
	}
	inv := Invitation{Version: 1, Host: denied, RecipientKey: n.PublicKey(), Token: base64.RawURLEncoding.EncodeToString(make([]byte, 32)), Expires: time.Now().Add(time.Minute)}
	if err := inv.ValidateFor(n.PublicKey(), time.Now()); err != nil {
		t.Fatal("invalid synthetic invitation", err)
	}
	for _, operation := range []struct {
		name string
		run  func() error
		want error
	}{
		{"issue", func() error { _, err := n.IssueInvitation(context.Background(), denied, time.Minute); return err }, ErrUntrusted},
		{"join", func() error { return n.PairInvitation(context.Background(), inv) }, ErrUntrusted},
		{"accept", func() error {
			return n.acceptPair(context.Background(), &wire{key: denied.Key, g: g}, request{Peer: &denied})
		}, ErrRecovery},
		{"commit", func() error { return n.commitPeerLocked(denied) }, ErrUntrusted},
		{"revoke", func() error { return n.Revoke(denied.Key) }, ErrUntrusted},
		{"connect", func() error { _, _, err := n.connect(context.Background(), denied, nil); return err }, ErrUntrusted},
	} {
		if err := operation.run(); !errors.Is(err, operation.want) {
			t.Fatalf("terminal %s missed its denial gate: %v", operation.name, err)
		}
	}
	if saves != 0 || len(n.peers) != 0 || len(g.peers) != 0 || len(n.invites) != 0 || len(n.attempts) != 0 || len(n.dials) != 0 || g.controlCount != 0 || g.engine.Load() != nil {
		t.Fatal("terminal denial mutated persistence or allocated transport authority")
	}
}

func TestDeniedMixedClassificationPreservesLegacyAndManaged(t *testing.T) {
	n, g, denied := deniedFixtureOwner()
	managed := n.cfg.Peers[0]
	n.peers[managed.Key] = &peerState{peer: managed, g: g}
	legacy := testIdentity(114).PublicKey()
	n.peers[legacy] = &peerState{peer: Peer{Key: legacy}, g: g}
	unknown := testIdentity(115).PublicKey()
	for _, test := range []struct {
		key, protocol string
		allowed       bool
	}{
		{managed.Key, contextProtocolName, true}, {managed.Key, protocolName, false},
		{legacy, protocolName, true}, {legacy, contextProtocolName, false},
		{unknown, protocolName, false}, {denied.Key, protocolName, false},
	} {
		if err := n.ordinaryPeerProtocolLocked(g, test.key, test.protocol); (err == nil) != test.allowed {
			t.Fatalf("classification mismatch: %v", err)
		}
	}
	n.cfg.Persist = func([]Peer) error { return nil }
	if err := n.ordinaryPeerProtocolLocked(g, unknown, protocolName); err != nil {
		t.Fatal("genuine unknown legacy pairing contract changed", err)
	}
	if err := n.ordinaryPeerProtocolLocked(g, denied.Key, protocolName); err == nil {
		t.Fatal("legacy persistence callback bypassed terminal denial")
	}
	// All-terminal owners keep the movement/preparer guard even without an
	// active managed context. Exercise only its early pure classification.
	n.cfg.PairContexts = nil
	if _, err := n.candidateConfigLocked(nil, TransportEndpoints{}); !errors.Is(err, ErrUnavailable) {
		t.Fatal("all-terminal owner lost preparer guard", err)
	}
}
