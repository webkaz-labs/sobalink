//go:build lanlink_integration && lanlink_independent_relay_experiment

package lanlink

// Experimental test-only admission. Nothing in this file is compiled into the
// product. Local review is represented by an exact public, pair-bound receipt;
// remote grant exchange, durable revocation and multi-grant transport eviction
// are intentionally not claimed by this communication feasibility fixture.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/netip"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
	"tailscale.com/derp/derphttp"
	"tailscale.com/feature/buildfeatures"
	"tailscale.com/net/netmon"
	"tailscale.com/tailcfg"
	"tailscale.com/types/key"
	"tailscale.com/types/logger"
)

type experimentalRelayGrant struct {
	Host        string
	CandidateID string
	Devices     [2]string
	Roles       [2]string
	PairBinding string
	Expires     time.Time
}

func (g experimentalRelayGrant) valid(host, candidate string, now time.Time) bool {
	if g.Host != host || g.CandidateID != candidate || !g.Expires.After(now) {
		return false
	}
	seen := map[string]bool{host: true}
	for _, k := range append(g.Devices[:], g.Roles[:]...) {
		if !validKey(k) || seen[k] {
			return false
		}
		seen[k] = true
	}
	bindings := []string{g.Devices[0] + "=" + g.Roles[0], g.Devices[1] + "=" + g.Roles[1]}
	sort.Strings(bindings)
	return g.PairBinding == digest([]byte("sobalink paired routes v1\x00"+strings.Join(bindings, "\x00")))
}

type experimentalReview struct {
	Grant    experimentalRelayGrant
	Revision uint64
	Digest   string
}
type experimentalAdmission struct {
	mu              sync.Mutex
	host, candidate string
	revision        uint64
	grant           *experimentalRelayGrant
}

func (a *experimentalAdmission) review(g experimentalRelayGrant) (experimentalReview, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !g.valid(a.host, a.candidate, time.Now()) {
		return experimentalReview{}, ErrUntrusted
	}
	r := experimentalReview{Grant: g, Revision: a.revision}
	raw, _ := json.Marshal(r)
	r.Digest = digest(raw)
	return r, nil
}
func (a *experimentalAdmission) approve(ctx context.Context, r experimentalReview) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	proof := r
	proof.Digest = ""
	raw, _ := json.Marshal(proof)
	if r.Revision != a.revision || r.Digest != digest(raw) || !r.Grant.valid(a.host, a.candidate, time.Now()) {
		return ErrUntrusted
	}
	g := r.Grant
	a.grant = &g
	a.revision++
	return nil
}
func (a *experimentalAdmission) allow(k string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.grant == nil || !a.grant.valid(a.host, a.candidate, time.Now()) {
		return false
	}
	for _, v := range append(a.grant.Devices[:], a.grant.Roles[:]...) {
		if k == v {
			return true
		}
	}
	return false
}
func (a *experimentalAdmission) revoke() { a.mu.Lock(); a.grant = nil; a.revision++; a.mu.Unlock() }

func TestIndependentRelayAdmissionContractPrototype(t *testing.T) {
	a, b, ab, _ := routePairFixture()
	c := GenerateIdentity()
	binding, err := PairRouteBinding(a, ab)
	if err != nil {
		t.Fatal(err)
	}
	grant := experimentalRelayGrant{Host: c.PublicKey(), CandidateID: strings.Repeat("a", 64), Devices: [2]string{a.PublicKey(), b.PublicKey()}, Roles: [2]string{keyString(ab.ClientPrivate.Public()), ab.IncomingClientKey}, PairBinding: binding, Expires: time.Now().Add(time.Minute)}
	gate := &experimentalAdmission{host: c.PublicKey(), candidate: grant.CandidateID}
	review, err := gate.review(grant)
	if err != nil {
		t.Fatal("public pair-bound review rejected")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if !errors.Is(gate.approve(cancelled, review), context.Canceled) || gate.allow(grant.Roles[0]) {
		t.Fatal("cancelled review authorized admission")
	}
	for _, mutate := range []func(*experimentalRelayGrant){
		func(g *experimentalRelayGrant) { g.Host = GenerateIdentity().PublicKey() },
		func(g *experimentalRelayGrant) { g.CandidateID = strings.Repeat("b", 64) },
		func(g *experimentalRelayGrant) { g.PairBinding = strings.Repeat("b", 64) },
		func(g *experimentalRelayGrant) { g.Roles[1] = keyString(key.NewNode().Public()) },
		func(g *experimentalRelayGrant) { g.Expires = time.Now().Add(-time.Second) },
	} {
		changed := review
		mutate(&changed.Grant)
		if _, err := gate.review(changed.Grant); err == nil {
			t.Fatal("unverified changed host, candidate, binding, role or expiry was reviewable")
		}
		if gate.approve(context.Background(), changed) == nil {
			t.Fatal("changed review granted authority")
		}
	}
	if err := gate.approve(context.Background(), review); err != nil {
		t.Fatal(err)
	}
	if !gate.allow(grant.Devices[0]) || !gate.allow(grant.Roles[1]) || gate.allow(keyString(key.NewNode().Public())) {
		t.Fatal("admission exceeded exact reviewed roles")
	}
	changed := ab
	changed.ClientPrivate = key.NewNode()
	nextBinding, _ := PairRouteBinding(a, changed)
	if nextBinding == binding || gate.allow(keyString(changed.ClientPrivate.Public())) {
		t.Fatal("re-pair inherited old relay authority")
	}
	gate.revoke()
	if gate.allow(grant.Roles[0]) || gate.approve(context.Background(), review) == nil {
		t.Fatal("revocation or stale review allowed admission")
	}
	grant.Expires = time.Now().Add(30 * time.Millisecond)
	review, _ = gate.review(grant)
	if err := gate.approve(context.Background(), review); err != nil {
		t.Fatal(err)
	}
	time.Sleep(40 * time.Millisecond)
	if gate.allow(grant.Roles[0]) {
		t.Fatal("expired grant still admitted role")
	}
	t.Log("cancel, exact host/pin-candidate, pair-generation, role scope, stale review, revoke and expiry checks passed; no remote exchange/durability claim")
}

// This uses the current product admission logic, with separately scoped A-C
// and B-C pair records. Their existence must not authorize A-B client roles.
func TestIndependentRelayCurrentNeighborAdmissionPrototype(t *testing.T) {
	a, b, _, _ := routeNodesFixture(t)
	c := testNode()
	defer c.Close()
	oldA, oldB := a.RemoteSnapshot()[0], b.RemoteSnapshot()[0]
	for _, peer := range []*Node{a, b} {
		rolePeer, roleC := key.NewNode(), key.NewNode()
		pc := RemotePeer{Peer: Peer{c.PublicKey(), "fixture-c"}, Address: c.Address(), ClientPrivate: rolePeer, IncomingClientKey: keyString(roleC.Public())}
		cp := RemotePeer{Peer: Peer{peer.PublicKey(), "fixture-neighbor"}, Address: peer.Address(), ClientPrivate: roleC, IncomingClientKey: keyString(rolePeer.Public())}
		if err := peer.commitPair(context.Background(), pc, "", registeredPairAttemptFixture(t, peer, c.PublicKey())); err != nil {
			t.Fatal(err)
		}
		if err := c.commitPair(context.Background(), cp, "", registeredPairAttemptFixture(t, c, peer.PublicKey())); err != nil {
			t.Fatal(err)
		}
		if !c.AllowRelayKey(keyString(rolePeer.Public())) {
			t.Fatal("fixture neighbor was not separately paired")
		}
	}
	if !c.AllowRelayKey(a.PublicKey()) || !c.AllowRelayKey(b.PublicKey()) {
		t.Fatal("paired device keys unavailable")
	}
	if c.AllowRelayKey(keyString(oldA.ClientPrivate.Public())) || c.AllowRelayKey(keyString(oldB.ClientPrivate.Public())) {
		t.Fatal("neighbor pairing implicitly admitted other pair roles")
	}
	t.Log("CURRENT PRODUCT NEGATIVE PASS: A-C and B-C pairing does not authorize A-B directional transport roles")
}

func TestIndependentRelayCommunicationPrototype(t *testing.T) {
	if os.Getenv("SOBALINK_RUN_LAN_INTEGRATION") != "1" {
		t.Skip("requires isolated loopback opt-in")
	}
	if buildfeatures.HasUDPTransport {
		t.Fatal("fixture requires UDP omitted to prove relay transport")
	}
	if err := ValidateBuild(); err != nil {
		t.Fatal("unsafe build")
	}
	clearRouteEnvironment(t)
	if err := validateEnvironment(os.Environ()); err != nil {
		t.Fatal("unsafe environment")
	}
	// Run only in a fresh focused test process. The upstream global setter
	// has no prior-getter accessor: cleanup restores the default, not an
	// arbitrary preinstalled getter. This is not a package-wide fixture.
	// The execution environment disallows interface netlink snapshots. Use the
	// supported upstream getter with synthetic metadata; sockets, pinned DERP,
	// authenticated pairing, WireGuard and TCP remain real. The documentation
	// address is never dialed or bound and UDP transport is compiled out.
	netmon.RegisterInterfaceGetter(func() ([]netmon.Interface, error) {
		return []netmon.Interface{{Interface: &net.Interface{Index: 1, Name: "relay-fixture", MTU: 1500, Flags: net.FlagUp | net.FlagRunning}, AltAddrs: []net.Addr{&net.IPNet{IP: net.IPv4(192, 0, 2, 1), Mask: net.CIDRMask(32, 32)}}}}, nil
	})
	t.Cleanup(func() { netmon.RegisterInterfaceGetter(nil) })
	ctx, cancel := context.WithTimeout(context.Background(), 110*time.Second)
	defer cancel()
	reserve := func() (net.Listener, netip.AddrPort) {
		t.Helper()
		ln, e := net.Listen("tcp4", "127.0.0.1:0")
		if e != nil {
			t.Fatal(e)
		}
		return ln, ln.Addr().(*net.TCPAddr).AddrPort()
	}
	heldA, apA := reserve()
	defer heldA.Close()
	heldC, apC := reserve()
	defer heldC.Close()
	relayIdentityA, err := GenerateRelayIdentity(apA.Addr())
	if err != nil {
		t.Fatal(err)
	}
	relayIdentityC, err := GenerateRelayIdentity(apC.Addr())
	if err != nil {
		t.Fatal(err)
	}
	endpointA, _ := relayIdentityA.Endpoint(apA)
	endpointC, _ := relayIdentityC.Endpoint(apC)
	candidates := []RouteCandidate{{Relay: endpointA, Scope: "local"}, {Relay: endpointC, Scope: "local"}}
	identities := []Identity{GenerateIdentity(), GenerateIdentity(), GenerateIdentity()}
	nodes := make([]*Node, 3)
	for i, id := range identities {
		path := t.TempDir() + "/pair.json"
		persist := func(trust Snapshot, remotes []RemotePeer) error {
			raw, e := json.Marshal(struct {
				Trust   Snapshot
				Remotes []RemotePeer
			}{trust, remotes})
			if e != nil {
				return e
			}
			return config.AtomicWritePrivate(path, raw)
		}
		selected := endpointA
		set := candidates
		if i == 2 {
			selected = endpointC
			set = []RouteCandidate{candidates[1]}
		}
		nodes[i], err = NewNode(NodeConfig{Identity: id, Relay: selected, Candidates: set, Trust: NewBook(), Persist: persist, EmbeddedRelay: i != 1})
		if err != nil {
			t.Fatal("create independent fixture node", err)
		}
		defer nodes[i].Close()
	}
	a, b, c := nodes[0], nodes[1], nodes[2]
	heldA.Close()
	relayA, err := StartLocalRelay(ctx, apA, relayIdentityA, a.AllowRelayKey, a.AuthorizeRelayBootstrap)
	if err != nil {
		t.Fatal("start original relay", err)
	}
	defer relayA.Close()
	admission := &experimentalAdmission{host: c.PublicKey(), candidate: candidates[1].ID()}
	type denialProof struct {
		key    string
		denied atomic.Bool
	}
	var deniedTarget atomic.Pointer[denialProof]
	allowC := func(k string) bool {
		ok := c.AllowRelayKey(k) || admission.allow(k)
		if target := deniedTarget.Load(); !ok && target != nil && k == target.key {
			target.denied.Store(true)
		}
		return ok
	}
	heldC.Close()
	relayC, err := StartLocalRelay(ctx, apC, relayIdentityC, allowC, c.AuthorizeRelayBootstrap)
	if err != nil {
		t.Fatal("start independent relay", err)
	}
	defer relayC.Close()
	if _, err := a.ServePairing(ctx); err != nil {
		t.Fatal("start pairing", err)
	}
	invitation, err := a.IssueInvitation(ctx, Peer{b.PublicKey(), "fixture-b"}, "fixture-a", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.PairInvitation(ctx, invitation); err != nil {
		t.Fatal("real original pair", err)
	}
	beforeA, beforeB := a.RemoteSnapshot()[0], b.RemoteSnapshot()[0]
	binding, err := PairRouteBinding(identities[0], beforeA)
	if err != nil {
		t.Fatal(err)
	}
	grant := experimentalRelayGrant{Host: c.PublicKey(), CandidateID: candidates[1].ID(), Devices: [2]string{a.PublicKey(), b.PublicKey()}, Roles: [2]string{keyString(beforeA.ClientPrivate.Public()), beforeA.IncomingClientKey}, PairBinding: binding, Expires: time.Now().Add(time.Minute)}
	if c.AllowRelayKey(grant.Roles[0]) || c.AllowRelayKey(grant.Roles[1]) {
		t.Fatal("current C admission unexpectedly admitted unrelated pair")
	}
	proveDenied := func(role key.NodePrivate) {
		t.Helper()
		target := &denialProof{key: keyString(role.Public())}
		deniedTarget.Store(target)
		probeMonitor := netmon.NewStatic()
		defer probeMonitor.Close()
		deniedClient := derphttp.NewRegionClient(role, logger.Discard, probeMonitor, func() *tailcfg.DERPRegion { return endpointC.region() })
		denyCtx, stopDeny := context.WithTimeout(ctx, 5*time.Second)
		deniedClient.BaseContext = func() context.Context { return denyCtx }
		stopClose := context.AfterFunc(denyCtx, func() { deniedClient.Close() })
		err := deniedClient.Connect(denyCtx)
		if err == nil {
			_, err = deniedClient.Recv()
		}
		deniedClient.Close()
		stopClose()
		stopDeny()
		if err == nil || !target.denied.Load() || relayC.derp.IsClientConnectedForTest(role.Public()) {
			t.Fatal("exact-role native admission rejection proof failed")
		}
	}
	proveDenied(beforeB.ClientPrivate)
	t.Log("NEGATIVE PASS: independent C rejected the existing A-B transport role over real pinned TLS/DERP")
	review, err := admission.review(grant)
	if err != nil {
		t.Fatal(err)
	}
	if err := admission.approve(ctx, review); err != nil {
		t.Fatal(err)
	}
	proveDenied(key.NewNode())
	t.Log("UNRELATED ROLE PASS: exact grant does not admit an unrelated transport key")
	for _, pair := range [][2]*Node{{a, b}, {b, a}} {
		raw, e := pair[0].ExportRouteUpdate(pair[1].PublicKey(), candidates, time.Now().Add(time.Minute))
		if e != nil {
			t.Fatal(e)
		}
		checked, e := pair[1].InspectRouteUpdate(pair[0].PublicKey(), raw)
		if e != nil {
			t.Fatal(e)
		}
		if e := pair[1].ApplyRouteUpdate(pair[0].PublicKey(), raw, checked.Digest, []string{candidates[1].ID()}, checked.Update.Expires); e != nil {
			t.Fatal(e)
		}
	}
	listener, err := a.ListenPeer(ctx, "tcp", 54546)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan error, 1)
	go func() {
		defer close(accepted)
		conn, e := listener.Accept()
		if e != nil {
			accepted <- e
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(20 * time.Second))
		peer, ok := a.PeerKey(conn.RemoteAddr())
		if !ok || peer != b.PublicKey() {
			accepted <- ErrUntrusted
			return
		}
		_, e = io.Copy(conn, conn)
		accepted <- e
	}()
	defer func() {
		listener.Close()
		a.Close()
		b.Close()
		select {
		case <-accepted:
		case <-time.After(5 * time.Second):
			t.Error("application handler did not terminate after owned teardown")
		}
	}()
	// Wait only for the independently approved C registration, then remove A's
	// relay. UDP is absent, so successful payload must traverse C.
	registration, stopRegistration := context.WithTimeout(ctx, 35*time.Second)
	defer stopRegistration()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for !relayC.derp.IsClientConnectedForTest(a.cfg.Identity.Key.Public()) {
		select {
		case <-registration.Done():
			t.Fatal("A failed to register at independently approved C")
		case <-ticker.C:
		}
	}
	relayA.Close()
	dialCtx, stopDial := context.WithTimeout(ctx, 25*time.Second)
	defer stopDial()
	conn, err := b.DialPeer(dialCtx, a.PublicKey(), "tcp", 54546)
	if err != nil {
		t.Fatal("application connection through C", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	payload := []byte("independent relay feasibility fixture")
	if _, err := conn.Write(payload); err != nil {
		t.Fatal(err)
	}
	received := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, received); err != nil || string(received) != string(payload) {
		t.Fatal("payload through C", err)
	}
	observed, err := b.TransportSnapshot(a.PublicKey())
	if err != nil || observed.CandidateID != candidates[1].ID() {
		t.Fatal("actual route was not C")
	}
	afterA, afterB := a.RemoteSnapshot()[0], b.RemoteSnapshot()[0]
	if afterA.Address != beforeA.Address || !afterA.ClientPrivate.Equal(beforeA.ClientPrivate) || afterA.IncomingClientKey != beforeA.IncomingClientKey || afterB.Address != beforeB.Address || !afterB.ClientPrivate.Equal(beforeB.ClientPrivate) || afterB.IncomingClientKey != beforeB.IncomingClientKey || len(c.RemoteSnapshot()) != 0 {
		t.Fatal("relay use changed pair identities or paired C implicitly")
	}
	t.Log("POSITIVE PASS: unchanged real A-B pair carried application TCP through independent C; original relay closed and UDP omitted; C has no A/B pairs")
	// The fixture's only hosted grant is revoked. Closing C is explicit whole-host
	// teardown, not a claim of implemented per-grant eviction for shared hosts.
	admission.revoke()
	// This previously granted role has no active client in the one-way echo
	// fixture. Prove C rejects a new handshake before listener teardown.
	proveDenied(beforeA.ClientPrivate)
	t.Log("REVOKE PASS: still-running C rejected a newly connected formerly granted role")
	relayC.Close()
	_ = conn.SetDeadline(time.Now().Add(time.Second))
	if _, writeErr := conn.Write(payload); writeErr == nil {
		if _, readErr := io.ReadFull(conn, received); readErr == nil {
			t.Fatal("new roundtrip survived independent host teardown")
		}
	}
	if admission.allow(grant.Roles[1]) || admission.approve(ctx, review) == nil {
		t.Fatal("revoke accepted an old review")
	}
	conn.Close()
	listener.Close()
	a.Close()
	b.Close()
	select {
	case <-accepted:
	case <-time.After(5 * time.Second):
		t.Fatal("application handler leaked during teardown")
	}
	t.Log("TEARDOWN PASS: C grant revoked, stale approval rejected, owned relay/listeners/application closed")
}
