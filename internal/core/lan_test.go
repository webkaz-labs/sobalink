package core

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/lanlink"
	tailcat "github.com/webkaz-labs/sobalink/internal/routecat"
	"github.com/webkaz-labs/sobalink/internal/transfer"
)

func openLANTestCore(t *testing.T) *Core {
	t.Helper()
	c, err := Open(context.Background(), Options{Directory: t.TempDir(), Version: "test", NodeFactory: func(string, string) (NetworkBackend, error) {
		t.Error("test must never start a real network backend")
		return nil, errors.New("network forbidden")
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func testLANSelection() *LANSelection {
	return &LANSelection{Kind: "relay", Address: "192.168.50.10:54443", CertificateSHA256: strings.Repeat("a", 64)}
}

func TestLANIdentityIsExplicitPrivateAndStable(t *testing.T) {
	c := openLANTestCore(t)
	path := filepath.Join(c.dir, "lan.json")
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("startup created LAN identity: %v", err)
	}
	value := mustCommand(t, c, "lan.identity", map[string]any{})
	key := value.(map[string]string)["publicKey"]
	if len(key) != 64 {
		t.Fatalf("unexpected public key %q", key)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if next := mustCommand(t, c, "lan.identity", map[string]any{}).(map[string]string)["publicKey"]; next != key {
		t.Fatal("repeat setup rotated identity")
	}
	assertLANStatePrivate(t, path)
	loaded, err := readLANStore(path)
	if err != nil || loaded.copy().Identity.PublicKey() != key {
		t.Fatalf("identity did not survive private reload: %v", err)
	}
	status, err := c.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	public, _ := json.Marshal(status)
	state := c.lanStoreCopy().copy()
	privateKey, _ := json.Marshal(state.Identity.Key)
	psk, _ := json.Marshal(state.Identity.PSK)
	if strings.Contains(string(public), string(privateKey)) || strings.Contains(string(public), string(psk)) || strings.Contains(string(public), "private_key") {
		t.Fatal("private key or capability leaked into status")
	}
	if !strings.Contains(string(public), key) || string(before) == string(public) {
		t.Fatal("status did not present only public identity metadata")
	}
	if c.nodeCopy() != nil || c.profileCopy().Settings.Network != "none" {
		t.Fatal("identity initialization silently activated networking")
	}
}

func TestLANStoreStrictBoundsAndAtomicFailure(t *testing.T) {
	c := openLANTestCore(t)
	mustCommand(t, c, "lan.identity", map[string]any{})
	s := c.lanStoreCopy()
	before := s.copy()
	data, _ := os.ReadFile(s.path)
	for name, changed := range map[string][]byte{
		"trailing": append(append([]byte(nil), data...), []byte("{}")...),
		"unknown":  []byte(`{"version":1,"unexpected":true}`),
		"oversize": []byte(strings.Repeat(" ", (2<<20)+1)),
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "lan.json")
			if err := os.WriteFile(path, changed, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := readLANStore(path); err == nil {
				t.Fatal("unsafe state accepted")
			}
		})
	}
	link := filepath.Join(t.TempDir(), "lan.json")
	if err := os.Symlink(s.path, link); err == nil {
		if _, err := readLANStore(link); err == nil {
			t.Fatal("symlink state accepted")
		}
	}
	blocked := filepath.Join(t.TempDir(), "blocked")
	if err := os.Mkdir(blocked, 0700); err != nil {
		t.Fatal(err)
	}
	s.path = blocked
	next := before
	next.Identity = lanlink.GenerateIdentity()
	if err := s.save(next); err == nil {
		t.Fatal("non-regular state destination accepted")
	}
	if s.copy().Identity.PublicKey() != before.Identity.PublicKey() {
		t.Fatal("failed save published replacement identity")
	}
}

func TestLANSetupSelectsExactRelayAndPreservesHostIdentity(t *testing.T) {
	c := openLANTestCore(t)
	for _, selection := range []*LANSelection{
		{Kind: "relay", Address: "relay.invalid:443", CertificateSHA256: strings.Repeat("a", 64)},
		{Kind: "relay", Address: "192.168.1.2:443"},
		{Kind: "host", Address: "8.8.8.8:54443"},
		{Kind: "host", Address: "192.168.1.2:443"},
		{Kind: "host", Address: "192.168.1.2:54544"},
	} {
		if err := c.configureLAN(selection); err == nil {
			t.Fatalf("invalid relay selected: %+v", selection)
		}
	}
	if c.lanStoreCopy() != nil {
		t.Fatal("invalid configuration generated a saved identity")
	}
	host := &LANSelection{Kind: "host", Address: "192.168.1.2:54443"}
	if err := c.configureLAN(host); err != nil {
		t.Fatal(err)
	}
	first := c.lanStoreCopy().copy()
	if err := c.configureLAN(host); err != nil {
		t.Fatal(err)
	}
	next := c.lanStoreCopy().copy()
	if first.Identity.PublicKey() != next.Identity.PublicKey() || first.Selection.CertificateSHA256 != next.Selection.CertificateSHA256 || first.RelayIdentity.Key.Public() != next.RelayIdentity.Key.Public() {
		t.Fatal("repeat setup regenerated server or relay identities")
	}
	if c.nodeCopy() != nil {
		t.Fatal("saving explicit relay unexpectedly started sockets")
	}
}

type fakeLANEngine struct {
	lanEngine
	mu          sync.Mutex
	key         string
	self        netip.Addr
	peers       []lanlink.PublicPeerSnapshot
	role        netip.Addr
	dials       []string
	revoked     []string
	closed      bool
	revokeError error
	pairError   error
	persist     func(lanlink.Snapshot, []lanlink.RemotePeer) error
}

func (f *fakeLANEngine) PublicKey() string       { return f.key }
func (f *fakeLANEngine) OverlayAddr() netip.Addr { return f.self }
func (f *fakeLANEngine) PublicPeers() []lanlink.PublicPeerSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]lanlink.PublicPeerSnapshot(nil), f.peers...)
}
func (f *fakeLANEngine) PeerKey(remote net.Addr) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ap, err := netip.ParseAddrPort(remote.String())
	if !f.closed && err == nil && ap.Addr() == f.role && len(f.peers) > 0 {
		return f.peers[0].Key, true
	}
	return "", false
}
func (f *fakeLANEngine) DialPeer(_ context.Context, id, network string, port uint16) (net.Conn, error) {
	f.mu.Lock()
	f.dials = append(f.dials, id)
	f.mu.Unlock()
	a, b := net.Pipe()
	_ = b.Close()
	return a, nil
}
func (f *fakeLANEngine) Close() error {
	f.mu.Lock()
	f.closed = true
	f.mu.Unlock()
	return nil
}
func (f *fakeLANEngine) ListenPeer(context.Context, string, uint16) (net.Listener, error) {
	return nil, errors.New("test listener is intentionally unavailable")
}
func (f *fakeLANEngine) PairInvitation(context.Context, lanlink.Invitation) error {
	return f.pairError
}
func (f *fakeLANEngine) RegisterTCPFallback(func(netip.AddrPort, netip.AddrPort) (func(net.Conn), bool)) (func(), error) {
	return func() {}, nil
}
func (f *fakeLANEngine) Revoke(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.revoked = append(f.revoked, id)
	f.peers = nil
	if f.revokeError != nil {
		return f.revokeError
	}
	if f.persist != nil {
		return f.persist(lanlink.Snapshot{Version: 1, Peers: []lanlink.Peer{}}, nil)
	}
	return nil
}

func testLANBackend() (*lanBackend, *fakeLANEngine) {
	key := strings.Repeat("b", 64)
	endpoint := netip.MustParseAddr("fd7a:115c:a1e0:bb::1")
	role := netip.MustParseAddr("fd7a:115c:a1e0:cc::1")
	fake := &fakeLANEngine{key: strings.Repeat("a", 64), self: netip.MustParseAddr("fd7a:115c:a1e0:aa::1"), role: role, peers: []lanlink.PublicPeerSnapshot{{Key: key, Name: "Device B", Endpoint: endpoint, SourceIPs: []netip.Addr{endpoint, role}}}}
	b := &lanBackend{lanEngine: fake, ctx: context.Background(), start: func() (io.Closer, error) { return nil, nil }}
	return b, fake
}

func TestLANBackendSeparatesCanonicalEndpointAndAuthenticatedRole(t *testing.T) {
	b, fake := testLANBackend()
	defer b.Close()
	ctx := context.Background()
	peer := fake.peers[0]
	if _, err := b.DialIP(ctx, "tcp", netip.AddrPortFrom(peer.Endpoint, 8080)); err == nil {
		t.Fatal("dial succeeded before listener readiness")
	}
	if err := b.Start(); err != nil {
		t.Fatal(err)
	}
	state, err := b.State(ctx)
	if err != nil || !state.Snapshot.Running || state.Snapshot.Peers[0].Online || len(state.Snapshot.Peers[0].IPs) != 2 {
		t.Fatalf("wrong readiness or source identity metadata: %+v %v", state, err)
	}
	conn, err := b.DialIP(ctx, "tcp", netip.AddrPortFrom(peer.Endpoint, 8080))
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	if len(fake.dials) != 1 || fake.dials[0] != peer.Key {
		t.Fatal("outgoing dial was not bound to canonical public identity")
	}
	if _, err := b.DialIP(ctx, "tcp", netip.AddrPortFrom(fake.role, 8080)); err == nil || len(fake.dials) != 1 {
		t.Fatal("incoming role address was accepted as an outgoing endpoint")
	}
	if got, err := b.WhoIs(ctx, netip.AddrPortFrom(fake.role, 2222)); err != nil || got != peer.Key {
		t.Fatalf("authenticated role did not resolve canonical identity: %q %v", got, err)
	}
	if _, err := b.WhoIs(ctx, netip.AddrPortFrom(peer.Endpoint, 2222)); err == nil {
		t.Fatal("public endpoint metadata substituted for proved connection identity")
	}
	if _, err := b.Listen("tcp", "127.0.0.1:8080"); err == nil {
		t.Fatal("LAN adapter accepted an OS listener address")
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := b.WhoIs(ctx, netip.AddrPortFrom(fake.role, 2222)); err == nil {
		t.Fatal("closed adapter still authenticated an incoming role")
	}
}

func TestLANPairStateIsDurableBeforePublicationAndContainsNoAppTrust(t *testing.T) {
	c := openLANTestCore(t)
	if err := c.configureLAN(testLANSelection()); err != nil {
		t.Fatal(err)
	}
	store := c.lanStoreCopy()
	peer := lanlink.Peer{Key: strings.Repeat("b", 64), Name: "Peer B"}
	trust := lanlink.Snapshot{Version: 1, Peers: []lanlink.Peer{peer}}
	remotes := []lanlink.RemotePeer{{Peer: peer, Address: tailcat.Addr("private-pair-capability"), ClientPrivate: lanlink.GenerateIdentity().Key, IncomingClientKey: strings.Repeat("c", 64)}}
	path := store.path
	store.path = t.TempDir()
	if err := store.persist(trust, remotes); err == nil {
		t.Fatal("failed pairing persistence reported success")
	}
	if len(store.copy().Trust.Peers) != 0 {
		t.Fatal("failed pairing persistence published a peer")
	}
	store.path = path
	if err := store.persist(trust, remotes); err != nil {
		t.Fatal(err)
	}
	loaded, err := readLANStore(path)
	if err != nil || len(loaded.copy().Remotes) != 1 {
		t.Fatalf("pair was not durably saved: %v", err)
	}
	if _, trusted := c.trust(peer.Key); trusted {
		t.Fatal("transport pairing silently granted text/file or autosave permission")
	}
	status, _ := c.Snapshot(context.Background())
	public, _ := json.Marshal(status)
	if strings.Contains(string(public), "private-pair-capability") || strings.Contains(string(public), "client_private") {
		t.Fatal("paired capability leaked into status")
	}
}

func TestLANRevocationFailureStopsBackendAndDropsAppTrust(t *testing.T) {
	c := openLANTestCore(t)
	b, fake := testLANBackend()
	if err := b.Start(); err != nil {
		t.Fatal(err)
	}
	id := fake.peers[0].Key
	p := c.profileCopy()
	p.Peers = []Trust{{ID: id, Name: "Peer B", Network: "lan", Generation: 1}}
	p.Settings.Network = "lan"
	if err := c.saveProfile(p); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	c.node, c.profile = b, p
	c.confirmed[id] = time.Now()
	c.mu.Unlock()
	if err := c.bindTransferPeer(p.Peers[0]); err != nil {
		t.Fatal(err)
	}
	fake.revokeError = errors.New("private persistence failed")
	if err := c.revokeLANPeer(b, id); err == nil {
		t.Fatal("failed revocation reported success")
	}
	if _, trusted := c.trust(id); trusted {
		t.Fatal("revoked peer retained application trust")
	}
	if !fake.closed || len(fake.revoked) != 1 || c.networkReady.Load() {
		t.Fatal("revocation persistence failure did not stop transport")
	}
	var disk Profile
	if err := config.ReadJSON(filepath.Join(c.dir, "sobalink.json"), &disk); err != nil || len(disk.Peers) != 0 {
		t.Fatalf("app approval not durably removed: %v", err)
	}
}

func TestLANRestartPrunesStaleApplicationApproval(t *testing.T) {
	c := openLANTestCore(t)
	p := c.profileCopy()
	p.Peers = []Trust{{ID: strings.Repeat("b", 64), Name: "old pair", Network: "lan", Generation: 1}}
	if err := c.saveProfile(p); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(context.Background(), Options{Directory: c.dir, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if len(reopened.profileCopy().Peers) != 0 {
		t.Fatal("unpaired public identity retained old receive permission")
	}
}

func TestLANConfigureCommandsDoNotSwitchActiveEngines(t *testing.T) {
	c := openLANTestCore(t)
	starts := 0
	c.lanFactory = func(store *lanStore) (lanNetworkBackend, error) {
		b, fake := testLANBackend()
		fake.key = store.copy().Identity.PublicKey()
		b.start = func() (io.Closer, error) { starts++; return nil, nil }
		return b, nil
	}
	payload := map[string]any{"mode": "lan", "lan": testLANSelection()}
	mustCommand(t, c, "network.configure", payload)
	mustCommand(t, c, "network.configure", payload)
	if starts != 1 || c.profileCopy().Settings.Network != "lan" {
		t.Fatal("repeat configuration recreated backend")
	}
	if _, err := command(c, randomID(), "network.configure", map[string]any{"mode": "tailnet"}); err == nil {
		t.Fatal("active LAN silently switched engine")
	}
	changed := *testLANSelection()
	changed.Address = "192.168.50.11:54443"
	if _, err := command(c, randomID(), "network.configure", map[string]any{"mode": "lan", "lan": changed}); err == nil {
		t.Fatal("active LAN silently changed relay")
	}
	state, _ := c.Snapshot(context.Background())
	lan := state["lan"].(map[string]any)
	if lan["pairingReady"] != true || lan["path"] != "unknown" {
		t.Fatalf("readiness mistaken for a measured path: %+v", lan)
	}
	for _, peer := range state["peers"].([]map[string]any) {
		if peer["online"] != false {
			t.Fatal("saved pair was reported online without a successful probe")
		}
	}
}

func TestLANPairingRecoveryCodesPreserveUncertainty(t *testing.T) {
	c := openLANTestCore(t)
	b, fake := testLANBackend()
	_ = b.Start()
	c.mu.Lock()
	c.node = b
	c.mu.Unlock()
	for _, tc := range []struct {
		cause error
		code  string
	}{
		{lanlink.ErrCancelInviteFirst, "lan_cancel_invite_first"},
		{errors.Join(lanlink.ErrPairReplyUncertain, context.DeadlineExceeded), "lan_pair_reply_uncertain"},
		{lanlink.ErrRemotePairedLocalSave, "lan_remote_paired_local_save"},
		{lanlink.ErrRelayMismatch, "lan_relay_mismatch"},
	} {
		fake.pairError = tc.cause
		_, err := command(c, randomID(), "lan.join", map[string]any{"invitation": "{}"})
		var coded interface{ ErrorCode() string }
		if !errors.As(err, &coded) || coded.ErrorCode() != tc.code {
			t.Fatalf("wrong recovery code for %v: %v", tc.cause, err)
		}
	}
}

func TestLANBackendControlPortsAreExcludedFromCompactShare(t *testing.T) {
	c := openLANTestCore(t)
	b, fake := testLANBackend()
	b.reserved = []uint16{54443, 53421}
	_ = b.Start()
	c.mu.Lock()
	c.node = b
	c.mu.Unlock()
	mustCommand(t, c, "service.share", map[string]any{"name": "broad", "network": "tcp", "ports": "1024-65535", "peerIds": []string{fake.peers[0].Key}, "ttlSeconds": 60})
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, active := range c.active {
		for _, port := range []uint16{54543, 54544, 54545, 54443, 53421} {
			if active.effective.Contains(port) {
				t.Fatalf("reserved control port %d included in share", port)
			}
		}
		if !active.effective.Contains(8080) {
			t.Fatal("ordinary service port was lost")
		}
	}
}

func TestLANRevocationProfileFailureCannotRestoreAutosave(t *testing.T) {
	c := openLANTestCore(t)
	if err := c.configureLAN(testLANSelection()); err != nil {
		t.Fatal(err)
	}
	store := c.lanStoreCopy()
	b, fake := testLANBackend()
	_ = b.Start()
	id := fake.peers[0].Key
	paired := lanlink.Peer{Key: id, Name: "Peer B"}
	if err := store.persist(lanlink.Snapshot{Version: 1, Peers: []lanlink.Peer{paired}}, []lanlink.RemotePeer{{Peer: paired, Address: tailcat.Addr("private-capability")}}); err != nil {
		t.Fatal(err)
	}
	fake.persist = store.persist
	p := c.profileCopy()
	p.Peers = []Trust{{ID: id, Name: "Peer B", Network: "lan", Generation: 9, Autosave: true, Directory: t.TempDir()}}
	p.Settings.Network = "lan"
	if err := c.saveProfile(p); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	c.node, c.profile = b, p
	c.mu.Unlock()
	if err := c.bindTransferPeer(p.Peers[0]); err != nil {
		t.Fatal(err)
	}
	profilePath := filepath.Join(c.dir, "sobalink.json")
	oldProfile, _ := os.ReadFile(profilePath)
	if err := os.Remove(profilePath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(profilePath, 0700); err != nil {
		t.Fatal(err)
	}
	if err := c.revokeLANPeer(b, id); err == nil || !fake.closed {
		t.Fatal("profile failure did not fail closed")
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(profilePath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(profilePath, oldProfile, 0600); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(context.Background(), Options{Directory: c.dir, Version: "test", SkipNetworkStart: true})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if len(reopened.lanStoreCopy().copy().Trust.Peers) != 0 || len(reopened.profileCopy().Peers) != 0 {
		t.Fatal("stale app trust/autosave survived the durable transport removal")
	}
}

type failedStartBackend struct{ NetworkBackend }

func (failedStartBackend) Start() error { return errors.New("engine initialization failed") }
func (failedStartBackend) Close() error { return nil }

func TestFailedNetworkStartStillRequiresRestartForModeChange(t *testing.T) {
	c := openLANTestCore(t)
	c.factory = func(string, string) (NetworkBackend, error) { return failedStartBackend{}, nil }
	if _, err := command(c, randomID(), "network.configure", map[string]any{"mode": "tailnet"}); err == nil {
		t.Fatal("failed engine initialization reported success")
	}
	if c.nodeCopy() != nil || c.attemptedNetwork != "tailnet" {
		t.Fatal("failed network attempt was not retained as a process mode boundary")
	}
	for _, payload := range []map[string]any{
		{"mode": "lan", "lan": testLANSelection()},
		{"mode": "none"},
		{"mode": "tailnet", "hostname": "new-name"},
	} {
		if _, err := command(c, randomID(), "network.configure", payload); err == nil || !strings.Contains(err.Error(), "stop soba") {
			t.Fatalf("failed startup allowed another mode/name: %v", err)
		}
	}
	if c.lanStoreCopy() != nil {
		t.Fatal("rejected mode switch created LAN state")
	}
}

func TestOfflineStartupCanRevokeSavedPairsAndChooseNewRelay(t *testing.T) {
	c := openLANTestCore(t)
	if err := c.configureLAN(testLANSelection()); err != nil {
		t.Fatal(err)
	}
	peer := lanlink.Peer{Key: strings.Repeat("b", 64), Name: "saved peer"}
	if err := c.lanStoreCopy().persist(lanlink.Snapshot{Version: 1, Peers: []lanlink.Peer{peer}}, []lanlink.RemotePeer{{Peer: peer, Address: tailcat.Addr("private-capability")}}); err != nil {
		t.Fatal(err)
	}
	p := c.profileCopy()
	p.Settings.Network = "lan"
	if err := c.saveProfile(p); err != nil {
		t.Fatal(err)
	}
	_ = c.Close()
	factoryCalls := 0
	reopened, err := Open(context.Background(), Options{Directory: c.dir, Version: "test", SkipNetworkStart: true, NodeFactory: func(string, string) (NetworkBackend, error) {
		factoryCalls++
		return &pipeNode{hub: &pipeNetwork{listeners: map[netip.AddrPort]*pipeListener{}}}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.nodeCopy() != nil || factoryCalls != 0 || reopened.attemptedNetwork != "" || reopened.profileCopy().Settings.Network != "lan" {
		t.Fatal("offline management either started networking or forgot saved selection")
	}
	status, err := reopened.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	savedPeers := status["peers"].([]map[string]any)
	if len(savedPeers) != 1 || savedPeers[0]["id"] != peer.Key || savedPeers[0]["online"] != false || savedPeers[0]["verified"] != false {
		t.Fatal("offline management hid saved pairs or claimed current verification")
	}
	mustCommand(t, reopened, "lan.revoke", map[string]any{"peerId": peer.Key})
	if len(reopened.lanStoreCopy().copy().Remotes) != 0 {
		t.Fatal("offline revoke left saved transport capabilities")
	}
	next := *testLANSelection()
	next.Address = "192.168.50.20:54443"
	if err := reopened.configureLAN(&next); err != nil {
		t.Fatal(err)
	}
	mustCommand(t, reopened, "network.configure", map[string]any{"mode": "tailnet"})
	if factoryCalls != 1 || reopened.nodeCopy() == nil || reopened.profileCopy().Settings.Network != "tailnet" {
		t.Fatal("explicit activation after offline management did not select the new engine")
	}
}

func TestExpiredHostCertificateCanLoadForExplicitRecovery(t *testing.T) {
	c := openLANTestCore(t)
	host := &LANSelection{Kind: "host", Address: "192.168.1.2:54443"}
	if err := c.configureLAN(host); err != nil {
		t.Fatal(err)
	}
	store := c.lanStoreCopy()
	state := store.copy()
	block, _ := pem.Decode(state.RelayIdentity.CertificatePEM)
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	private, _ := pem.Decode(state.RelayIdentity.PrivateKeyPEM)
	key, err := x509.ParsePKCS8PrivateKey(private.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	certificate.NotBefore = time.Now().Add(-2 * time.Hour)
	certificate.NotAfter = time.Now().Add(-time.Hour)
	der, err := x509.CreateCertificate(rand.Reader, certificate, certificate, key.(crypto.Signer).Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	state.RelayIdentity.CertificatePEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	hash := sha256.Sum256(der)
	state.Selection.CertificateSHA256 = hex.EncodeToString(hash[:])
	if err := store.save(state); err != nil {
		t.Fatal(err)
	}
	if _, err := savedLANRelay(state); networkErrorCode(err) != "lan_certificate_expired" {
		t.Fatal("expired certificate did not provide actionable startup recovery")
	}
	if _, err := readLANStore(store.path); err != nil {
		t.Fatalf("expired state blocked recovery loading: %v", err)
	}
	before, _ := os.ReadFile(store.path)
	if err := c.configureLAN(host); networkErrorCode(err) != "lan_certificate_rotation_required" {
		t.Fatal("expired repeat setup must require explicit replacement", err)
	}
	after, _ := os.ReadFile(store.path)
	if string(before) != string(after) {
		t.Fatal("refused replacement changed saved state")
	}
	if err := c.configureLANWithOptions(host, true); err != nil {
		t.Fatal(err)
	}
	repaired := store.copy()
	if repaired.Identity.PublicKey() != state.Identity.PublicKey() || repaired.Selection.CertificateSHA256 == state.Selection.CertificateSHA256 {
		t.Fatal("explicit renewal did not preserve device identity and replace expired relay certificate")
	}
	if _, err := savedLANRelay(repaired); err != nil {
		t.Fatal(err)
	}
}

func TestLANEnvironmentRecoveryCodesAppearInStartupStatus(t *testing.T) {
	if lanlink.ValidateBuild() != nil {
		t.Skip("LAN build features are disabled")
	}
	for _, tc := range []struct{ name, variable, value, code string }{
		{"proxy", "HTTP_PROXY", "http://127.0.0.1:9", "lan_environment_proxy"},
		{"override", "TS_AUTHKEY", "test-not-a-credential", "lan_environment_override"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "TS_PROXY", "http_proxy", "https_proxy", "all_proxy"} {
				t.Setenv(name, "")
			}
			t.Setenv(tc.variable, tc.value)
			c := openLANTestCore(t)
			if err := c.configureLAN(testLANSelection()); err != nil {
				t.Fatal(err)
			}
			p := c.profileCopy()
			p.Settings.Network = "lan"
			if err := c.saveProfile(p); err != nil {
				t.Fatal(err)
			}
			_ = c.Close()
			// Constructor environment checks fail before any Start or socket.
			reopened, err := Open(context.Background(), Options{Directory: c.dir, Version: "test"})
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			status, err := reopened.Snapshot(context.Background())
			if err != nil || status["self"].(map[string]any)["errorCode"] != tc.code || reopened.nodeCopy() != nil {
				t.Fatalf("startup recovery code missing: %v", err)
			}
			encoded, _ := json.Marshal(status)
			if strings.Contains(string(encoded), tc.value) {
				t.Fatal("environment value leaked into status")
			}
		})
	}
}

func TestTrustGenerationSeedsFromSavedApprovalAndAdvancesAfterRevoke(t *testing.T) {
	c := openLANTestCore(t)
	id := strings.Repeat("b", 64)
	p := c.profileCopy()
	// This is deliberately far above the current Unix nanosecond clock. The
	// persisted generation must remain valid even if wall time goes backwards.
	p.Peers = []Trust{{ID: id, Name: "peer", Network: "tailnet", Generation: 1 << 63}}
	p.Settings.Network = "tailnet"
	if err := c.saveProfile(p); err != nil {
		t.Fatal(err)
	}
	_ = c.Close()
	reopened, err := Open(context.Background(), Options{Directory: c.dir, Version: "test", SkipNetworkStart: true})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	b, _ := testLANBackend()
	_ = b.Start()
	reopened.mu.Lock()
	reopened.node = b
	reopened.mu.Unlock()
	mustCommand(t, reopened, "peer.trust", map[string]any{"peerId": id, "trusted": false})
	mustCommand(t, reopened, "peer.trust", map[string]any{"peerId": id, "trusted": true})
	approved, ok := reopened.trust(id)
	if !ok || approved.Generation != (1<<63)+1 || approved.Autosave {
		t.Fatalf("reapproval reused an obsolete generation or autosave: %+v", approved)
	}
	mustCommand(t, reopened, "peer.trust", map[string]any{"peerId": id, "trusted": false})
	mustCommand(t, reopened, "peer.trust", map[string]any{"peerId": id, "trusted": true})
	approved, _ = reopened.trust(id)
	if approved.Generation != (1<<63)+2 {
		t.Fatal("in-process reapproval did not strictly advance")
	}
}

func TestCancelledCoreNeverAcceptsCachedPeerIdentity(t *testing.T) {
	pair := newCorePair(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := pair.a.current(ctx); err == nil {
		t.Fatal("cancelled request accepted cached network state")
	}
	source := netip.AddrPortFrom(pair.nb.ip, 10000)
	if _, err := pair.a.authenticated(ctx, source); err == nil {
		t.Fatal("cancelled request accepted cached identity")
	}
	pair.a.cancel()
	if _, err := pair.a.authenticated(context.Background(), source); err == nil {
		t.Fatal("stopping Core accepted cached identity")
	}
}

func TestOfflineLANRevocationCannotAffectActiveTailnet(t *testing.T) {
	pair := newCorePair(t)
	trustPair(t, pair)
	mustCommand(t, pair.a, "lan.identity", map[string]any{})
	for _, id := range []string{"peer-b", strings.Repeat("b", 64), strings.Repeat("0", 64), strings.Repeat("B", 64)} {
		if _, err := command(pair.a, randomID(), "lan.revoke", map[string]any{"peerId": id}); err == nil {
			t.Fatalf("saved LAN action accepted against active Tailnet for %q", id)
		}
	}
	if _, trusted := pair.a.trust("peer-b"); !trusted || pair.a.nodeCopy() != pair.na {
		t.Fatal("LAN maintenance affected active Tailnet approval or backend")
	}
}

func TestOfflineBackendSwitchDoesNotImportIdenticalPeerAutosave(t *testing.T) {
	c := openLANTestCore(t)
	id := strings.Repeat("b", 64)
	destination := t.TempDir()
	p := c.profileCopy()
	p.Settings.Network = "tailnet"
	p.Peers = []Trust{{ID: id, Name: "previous peer", Network: "tailnet", Generation: 7, Autosave: true, Directory: destination}}
	if err := c.saveProfile(p); err != nil {
		t.Fatal(err)
	}
	_ = c.Close()
	reopened, err := Open(context.Background(), Options{Directory: c.dir, Version: "test", SkipNetworkStart: true})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if policies, err := (receiveStore{reopened}).LoadPolicies(); err != nil || len(policies) != 1 {
		t.Fatalf("original selected namespace was not loaded: %v", err)
	}
	reopened.lanFactory = func(*lanStore) (lanNetworkBackend, error) { backend, _ := testLANBackend(); return backend, nil }
	mustCommand(t, reopened, "network.configure", map[string]any{"mode": "lan", "lan": testLANSelection()})
	if _, trusted := reopened.trust(id); trusted {
		t.Fatal("identical ID imported application trust across backend namespaces")
	}
	if _, err := reopened.transfers.Offer(transfer.Peer{ID: id, Generation: 7}, fileManifest("cross-network", "private")); !errors.Is(err, transfer.ErrUnknownPeer) {
		t.Fatalf("old namespace retained automatic receive binding: %v", err)
	}
	if _, err := command(reopened, randomID(), "peer.trust", map[string]any{"peerId": id, "trusted": true}); err == nil || !strings.Contains(err.Error(), "another network") {
		t.Fatalf("same-ID inactive approval was silently replaced: %v", err)
	}
	if err := (receiveStore{reopened}).SavePolicies(nil); err != nil {
		t.Fatal(err)
	}
	profile := reopened.profileCopy()
	if len(profile.Peers) != 1 || profile.Peers[0].Network != "tailnet" || !profile.Peers[0].Autosave || profile.Peers[0].Generation != 7 {
		t.Fatal("switching or saving current policies destroyed the inactive approval")
	}
	if policies, err := (receiveStore{reopened}).LoadPolicies(); err != nil || len(policies) != 0 {
		t.Fatal("inactive autosave leaked into current receive store")
	}
	entries, err := os.ReadDir(destination)
	if err != nil || len(entries) != 0 {
		t.Fatal("cross-network offer wrote to inactive receive destination")
	}
}
