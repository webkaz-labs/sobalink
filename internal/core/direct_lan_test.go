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
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/directlan"
)

func directLANTestSelection() *DirectLANSelection {
	return &DirectLANSelection{Listen: "127.0.0.1:55446", Prefixes: []string{"127.0.0.0/8"}}
}
func configureDirectLANTest(t *testing.T, c *Core) *directLANStore {
	t.Helper()
	if err := c.configureDirectLAN(directLANTestSelection()); err != nil {
		t.Fatal(err)
	}
	return c.directLANStoreCopy()
}
func directLANTestPeer(t *testing.T) directlan.Peer {
	t.Helper()
	id, err := directlan.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	return directlan.Peer{Key: id.PublicKey(), TunnelKey: id.TunnelKey(), Name: "test-peer", Endpoint: netip.MustParseAddrPort("127.0.0.1:55447")}
}
func TestDirectLANIdentityOnlyGeneratedByConfigure(t *testing.T) {
	c := openLANTestCore(t)
	path := filepath.Join(c.dir, "direct-lan.json")
	status := mustCommand(t, c, "direct-lan.status", map[string]any{}).(map[string]any)
	if status["configured"] != false || status["listenerReady"] != false {
		t.Fatal(status)
	}
	if _, err := c.directLANCommand(context.Background(), "direct-lan.identity", json.RawMessage(`{}`)); networkErrorCode(err) != "direct_lan_setup_required" {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("read created identity", err)
	}
	s := configureDirectLANTest(t, c)
	state := s.copy()
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.configureDirectLAN(directLANTestSelection()); err != nil {
		t.Fatal(err)
	}
	again, err := os.ReadFile(path)
	if err != nil || string(first) != string(again) {
		t.Fatal("repeat changed identity or state", err)
	}
	assertLANStatePrivate(t, path)
	if c.nodeCopy() != nil || c.profileCopy().Settings.Network != "none" {
		t.Fatal("configuration helper started network")
	}
	public, err := c.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(public)
	if strings.Contains(string(raw), state.Identity.Seed) || strings.Contains(string(raw), `"seed"`) {
		t.Fatal("secret leaked into status")
	}
	loaded, err := readDirectLANStore(path, 1<<20, 256)
	if err != nil || loaded.copy().Identity.PublicKey() != state.Identity.PublicKey() {
		t.Fatal("identity changed on reload", err)
	}
}
func TestDirectLANSelectionNeverExpandsScope(t *testing.T) {
	tests := []DirectLANSelection{
		{Listen: "localhost:55446", Prefixes: []string{"127.0.0.0/8"}},
		{Listen: "0.0.0.0:55446", Prefixes: []string{"0.0.0.0/0"}},
		{Listen: "127.0.0.1:55446"},
		{Listen: "127.0.0.1:55446", Prefixes: []string{"10.0.0.0/8"}},
		{Listen: "127.0.0.1:80", Prefixes: []string{"127.0.0.0/8"}},
		{Listen: "127.0.0.1:54544", Prefixes: []string{"127.0.0.0/8"}},
		{Listen: "127.0.0.1:55446", Prefixes: []string{"127.0.0.1/8"}},
		{Listen: "127.0.0.1:55446", Prefixes: []string{"127.0.0.0/8", "127.0.0.0/8"}},
		{Listen: "192.0.2.1:55446", Prefixes: []string{"192.0.2.0/24"}},
	}
	for _, s := range tests {
		if ValidateDirectLANSelection(s) == nil {
			t.Fatalf("accepted %#v", s)
		}
	}
	if err := ValidateDirectLANSelection(*directLANTestSelection()); err != nil {
		t.Fatal(err)
	}
}
func TestDirectLANPersistenceFailureStopsAdmission(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(map[bool]string{false: "before-replace", true: "after-replace"}[committed], func(t *testing.T) {
			c := openLANTestCore(t)
			s := configureDirectLANTest(t, c)
			peer := directLANTestPeer(t)
			s.write = func(path string, data []byte) error {
				if committed {
					if err := config.AtomicWrite(path, data); err != nil {
						return err
					}
					return config.ErrAtomicCommitted
				}
				return errors.New("disk unavailable")
			}
			err := s.persist([]directlan.Peer{peer})
			if err == nil || !s.needsRecovery() {
				t.Fatal("failed write remained available", err)
			}
			if committed != errors.Is(err, config.ErrAtomicCommitted) {
				t.Fatal("atomic outcome lost", err)
			}
			if err := s.persist(nil); !errors.Is(err, directlan.ErrRecovery) {
				t.Fatal("retry escaped recovery", err)
			}
			if err := c.configureDirectLAN(directLANTestSelection()); networkErrorCode(err) != "direct_lan_recovery_required" {
				t.Fatal(err)
			}
			loaded, err := readDirectLANStore(s.path, 1<<20, 256)
			if err != nil {
				t.Fatal(err)
			}
			if (len(loaded.copy().Peers) == 1) != committed {
				t.Fatal("wrong disk outcome")
			}
		})
	}
}
func TestDirectLANPairingDoesNotGrantApplicationTrust(t *testing.T) {
	c := openLANTestCore(t)
	s := configureDirectLANTest(t, c)
	peer := directLANTestPeer(t)
	if err := s.persist([]directlan.Peer{peer}); err != nil {
		t.Fatal(err)
	}
	if _, trusted := c.trust(peer.Key); trusted {
		t.Fatal("pair granted app trust")
	}
	p := c.profileCopy()
	p.Settings.Network = "direct-lan"
	p.Peers = []Trust{{ID: peer.Key, Name: peer.Name, Network: "direct-lan", Generation: 1}}
	if err := c.saveProfile(p); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	c.profile = p
	c.mu.Unlock()
	if err := c.revokeDirectLANPeer(peer.Key); err != nil {
		t.Fatal(err)
	}
	if len(s.copy().Peers) != 0 || len(c.profileCopy().Peers) != 0 {
		t.Fatal("revocation retained trust")
	}
	if err := s.persist([]directlan.Peer{peer}); err != nil {
		t.Fatal(err)
	}
	if _, trusted := c.trust(peer.Key); trusted {
		t.Fatal("re-pair restored app approval")
	}
}
func TestDirectLANInspectRecipientAndPrefix(t *testing.T) {
	c := openLANTestCore(t)
	s := configureDirectLANTest(t, c)
	peer := directLANTestPeer(t)
	inv := directlan.Invitation{Version: 1, Host: peer, RecipientKey: s.copy().Identity.PublicKey(), Token: strings.Repeat("A", 43), Expires: time.Now().Add(time.Minute)}
	encoded, err := inv.Encode()
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]string{"invitation": encoded})
	result, err := c.directLANCommand(context.Background(), "direct-lan.inspect", raw)
	if err != nil {
		t.Fatal(err)
	}
	view := result.(map[string]any)
	if view["endpoint"] != peer.Endpoint.String() || view["recipientMatches"] != true {
		t.Fatal(view)
	}
	data, _ := json.Marshal(view)
	if strings.Contains(string(data), inv.Token) {
		t.Fatal("inspect leaked invitation token")
	}
	inv.Host.Endpoint = netip.MustParseAddrPort("10.10.0.2:55447")
	encoded, err = inv.Encode()
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(map[string]string{"invitation": encoded})
	if _, err := c.directLANCommand(context.Background(), "direct-lan.inspect", raw); err == nil {
		t.Fatal("inspect admitted outside prefix")
	}
	inv.Host = peer
	inv.RecipientKey = directLANTestPeer(t).Key
	encoded, _ = inv.Encode()
	raw, _ = json.Marshal(map[string]string{"invitation": encoded})
	if _, err := c.directLANCommand(context.Background(), "direct-lan.inspect", raw); err == nil {
		t.Fatal("wrong recipient admitted")
	}
}
func TestDirectLANReadRejectsUnsafeOrMalformedState(t *testing.T) {
	c := openLANTestCore(t)
	s := configureDirectLANTest(t, c)
	data, _ := os.ReadFile(s.path)
	for name, raw := range map[string][]byte{"trailing": append(append([]byte{}, data...), []byte(`{}`)...), "unknown": []byte(`{"version":1,"unknown":true}`), "oversize": []byte(strings.Repeat(" ", 4097))} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "direct-lan.json")
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := readDirectLANStore(path, 4096, 256); err == nil {
				t.Fatal("invalid store accepted")
			}
		})
	}
}
func TestDirectLANBackendNeverAuthenticatesFromAddressAlone(t *testing.T) {
	c := openLANTestCore(t)
	s := configureDirectLANTest(t, c)
	peer := directLANTestPeer(t)
	if err := s.persist([]directlan.Peer{peer}); err != nil {
		t.Fatal(err)
	}
	cfg, err := directLANConfig(s.copy())
	if err != nil {
		t.Fatal(err)
	}
	n, err := directlan.NewNode(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	b := &directLANBackend{Node: n, ctx: c.ctx, store: s, ready: true}
	ap, _ := directlan.OverlayAddress(peer.Key)
	if _, err := b.WhoIs(context.Background(), netip.AddrPortFrom(ap, 31000)); err == nil {
		t.Fatal("saved pairing authenticated a fabricated address")
	}
	if _, err := b.Listen("tcp", net.JoinHostPort("0.0.0.0", "32100")); err == nil {
		t.Fatal("wildcard listener admitted")
	}
	if _, err := b.DialIP(context.Background(), "tcp", netip.MustParseAddrPort("127.0.0.1:32100")); err == nil {
		t.Fatal("application dial escaped virtual peer dispatch")
	}
}
func TestDirectLANBackendConstructionIsIsolated(t *testing.T) {
	c := openLANTestCore(t)
	s := configureDirectLANTest(t, c)
	node, err := c.newDirectLANBackend(s)
	if err != nil {
		t.Fatal(err)
	}
	defer node.Close()
	if _, ok := node.(*directLANBackend); !ok {
		t.Fatal("wrong network backend")
	}
	st, err := node.State(context.Background())
	if err != nil || st.Snapshot.Running || st.SelfID != s.copy().Identity.PublicKey() {
		t.Fatal("construction started network or changed identity", err)
	}
}

func TestDirectLANStartupJournalFailureStillRevokesAllAuthority(t *testing.T) {
	c := openLANTestCore(t)
	s := configureDirectLANTest(t, c)
	peer := directLANTestPeer(t)
	if e := s.persist([]directlan.Peer{peer}); e != nil {
		t.Fatal(e)
	}
	p := c.profileCopy()
	p.Settings.Network = "direct-lan"
	p.Peers = []Trust{{ID: peer.Key, Name: peer.Name, Network: "direct-lan", Generation: 1}}
	if e := c.saveProfile(p); e != nil {
		t.Fatal(e)
	}
	node, e := c.newDirectLANBackend(s)
	if e != nil {
		t.Fatal(e)
	}
	b := node.(*directLANBackend)
	c.mu.Lock()
	c.profile = p
	c.node = b
	c.mu.Unlock()
	original := c.atomicWrite
	c.atomicWrite = func(path string, data []byte) error {
		if filepath.Base(path) == "startup-revocations.json" {
			return errors.New("synthetic pre-replacement failure")
		}
		if original != nil {
			return original(path, data)
		}
		return config.AtomicWrite(path, data)
	}
	if e = c.revokeDirectLANPeer(peer.Key); networkErrorCode(e) != "direct_lan_recovery_required" {
		t.Fatal("startup failure was hidden", e)
	}
	if _, ok := c.trust(peer.Key); ok {
		t.Fatal("inbound application approval survived")
	}
	if len(b.Node.Peers()) != 0 || len(s.copy().Peers) != 0 {
		t.Fatal("paired transport survived failed journal")
	}
	if !errors.Is(b.Node.Ready(), net.ErrClosed) || !s.needsRecovery() || c.networkReady.Load() {
		t.Fatal("failed revoke did not close backend and latch recovery")
	}
}
func TestDirectLANRejectsComparisonOnlyStateVersion(t *testing.T) {
	c := openLANTestCore(t)
	s := configureDirectLANTest(t, c)
	old := s.copy()
	old.Version = 1
	raw, _ := json.Marshal(old)
	if e := os.WriteFile(s.path, raw, 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := readDirectLANStore(s.path, 1<<20, 0); networkErrorCode(e) != "direct_lan_state_invalid" {
		t.Fatal("old unpaired comparison-only state activated", e)
	}
}

func TestDirectLANFailedTransportRevokePreservesExplicitReloadBoundary(t *testing.T) {
	c := openLANTestCore(t)
	s := configureDirectLANTest(t, c)
	peer := directLANTestPeer(t)
	if e := s.persist([]directlan.Peer{peer}); e != nil {
		t.Fatal(e)
	}
	p := c.profileCopy()
	p.Settings.Network = "direct-lan"
	p.Peers = []Trust{{ID: peer.Key, Name: peer.Name, Network: "direct-lan", Generation: 1}}
	if e := c.saveProfile(p); e != nil {
		t.Fatal(e)
	}
	node, e := c.newDirectLANBackend(s)
	if e != nil {
		t.Fatal(e)
	}
	b := node.(*directLANBackend)
	c.mu.Lock()
	c.profile = p
	c.node = b
	c.mu.Unlock()
	s.write = func(string, []byte) error { return errors.New("synthetic private-state pre-replacement failure") }
	if e = c.revokeDirectLANPeer(peer.Key); networkErrorCode(e) != "direct_lan_recovery_required" {
		t.Fatal(e)
	}
	if len(b.Node.Peers()) != 0 || !errors.Is(b.Node.Ready(), net.ErrClosed) {
		t.Fatal("failed durable revoke retained live authority")
	}
	if _, ok := c.trust(peer.Key); ok {
		t.Fatal("failed transport persistence kept application approval")
	}
	restored, e := readDirectLANStore(s.path, s.currentCapacity().bytes, s.currentCapacity().peers)
	if e != nil || len(restored.copy().Peers) != 1 {
		t.Fatal("test must expose unchanged old disk pair after failed write", e)
	}
	if !s.needsRecovery() {
		t.Fatal("runtime did not require explicit saved-state reconciliation")
	}
}
