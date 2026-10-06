//go:build lanlink_integration

package lanlink

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
	tailcat "github.com/webkaz-labs/sobalink/internal/routecat"
	"tailscale.com/feature/buildfeatures"
)

// This opt-in native fixture runs the real pairing/bootstrap, protected saved
// state, coordinator and application path. UDP transport is compiled out and
// both exact pinned relays use numeric loopback addresses. The external scope
// label is synthetic: this test neither contacts the Internet nor establishes
// direct-UDP, WAN, suspend/resume or unmoved-TCP acceptance.
func TestManagedRelayCandidateFailoverIntegration(t *testing.T) {
	if os.Getenv("SOBALINK_RUN_LAN_INTEGRATION") != "1" {
		t.Skip("requires explicitly enabled isolated native CI")
	}
	if buildfeatures.HasUDPTransport {
		t.Fatal("isolated integration requires ts_omit_udptransport")
	}
	if err := ValidateBuild(); err != nil {
		t.Fatal("unsafe integration build")
	}
	if err := validateEnvironment(os.Environ()); err != nil {
		t.Fatal("unsafe integration environment")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	// Reserve both at once to guarantee distinct ports. Canonical map ordering
	// chooses the lower endpoint first, which this fixture assigns to the LAN.
	reserved := make([]net.Listener, 0, 2)
	for range 2 {
		ln, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			for _, held := range reserved {
				held.Close()
			}
			t.Fatal("reserve loopback endpoint")
		}
		reserved = append(reserved, ln)
	}
	endpoints := []netip.AddrPort{reserved[0].Addr().(*net.TCPAddr).AddrPort(), reserved[1].Addr().(*net.TCPAddr).AddrPort()}
	if endpoints[0].Port() > endpoints[1].Port() {
		endpoints[0], endpoints[1] = endpoints[1], endpoints[0]
	}
	// Keep B reserved until its TLS relay starts. The unserved TCP listener is
	// not a functioning DERP endpoint, and prevents unrelated ephemeral-port
	// allocations during pairing/cold start from taking its configured address.
	var alternativeReservation net.Listener
	for _, held := range reserved {
		if held.Addr().(*net.TCPAddr).AddrPort() == endpoints[1] {
			alternativeReservation = held
		} else {
			held.Close()
		}
	}
	defer alternativeReservation.Close()
	identities := make([]RelayIdentity, 2)
	candidates := make([]RouteCandidate, 2)
	for i, ap := range endpoints {
		identity, err := GenerateRelayIdentity(ap.Addr())
		if err != nil {
			t.Fatal("generate synthetic relay identity")
		}
		relay, err := identity.Endpoint(ap)
		if err != nil {
			t.Fatal("construct pinned loopback candidate")
		}
		scope := "local"
		if i == 1 {
			scope = "external"
		}
		identities[i], candidates[i] = identity, RouteCandidate{Relay: relay, Scope: scope}
	}
	a, b := candidates[0], candidates[1]
	type persisted struct {
		Identity   Identity         `json:"identity"`
		Relay      TrustedRelay     `json:"relay"`
		Candidates []RouteCandidate `json:"candidates"`
		Trust      Snapshot         `json:"trust"`
		Remotes    []RemotePeer     `json:"remotes"`
	}
	dir := t.TempDir()
	hostPath, clientPath := filepath.Join(dir, "host.json"), filepath.Join(dir, "client.json")
	save := func(path string, state persisted) error {
		raw, err := json.Marshal(state)
		if err != nil {
			return err
		}
		return config.AtomicWritePrivate(path, raw)
	}
	openNode := func(path string, state persisted, embedded bool) *Node {
		t.Helper()
		book := NewBook()
		if err := book.Restore(state.Trust); err != nil {
			t.Fatal("restore protected trust snapshot")
		}
		persist := func(trust Snapshot, remotes []RemotePeer) error {
			next := state
			next.Trust, next.Remotes = trust, CloneRemotePeers(remotes)
			return save(path, next)
		}
		n, err := NewNode(NodeConfig{Identity: state.Identity, Relay: state.Relay, Candidates: state.Candidates, Trust: book, Remotes: state.Remotes, EmbeddedRelay: embedded, Persist: persist})
		if err != nil {
			t.Fatal("open saved node state")
		}
		return n
	}
	hostState := persisted{Identity: GenerateIdentity(), Relay: a.Relay, Candidates: candidates, Trust: NewBook().Snapshot()}
	clientState := persisted{Identity: GenerateIdentity(), Relay: a.Relay, Candidates: candidates, Trust: NewBook().Snapshot()}
	if err := save(hostPath, hostState); err != nil {
		t.Fatal("save initial protected host state")
	}
	if err := save(clientPath, clientState); err != nil {
		t.Fatal("save initial protected client state")
	}
	host, client := openNode(hostPath, hostState, true), openNode(clientPath, clientState, false)
	var currentHost atomic.Pointer[Node]
	currentHost.Store(host)
	defer func() { currentHost.Store(nil); client.Close(); host.Close() }()
	allow := func(role string) bool { n := currentHost.Load(); return n != nil && n.AllowRelayKey(role) }
	bootstrap := func(frame []byte) error {
		n := currentHost.Load()
		if n == nil {
			return ErrUntrusted
		}
		return n.AuthorizeRelayBootstrap(frame)
	}
	relayA, err := StartLocalRelay(ctx, a.Relay.Address, identities[0], allow, bootstrap)
	if err != nil {
		t.Fatal("start LAN fixture relay")
	}
	defer relayA.Close()
	// Alternative remains unavailable during pairing, approval and cold start.
	if _, err := host.ServePairing(ctx); err != nil {
		t.Fatal("start native pairing service")
	}
	invitation, err := host.IssueInvitation(ctx, Peer{client.PublicKey(), "client"}, "host", time.Minute)
	if err != nil {
		t.Fatal("issue synthetic invitation")
	}
	if err := client.PairInvitation(ctx, invitation); err != nil {
		t.Fatal("native pairing failed")
	}
	frame, err := host.ExportRouteUpdate(client.PublicKey(), candidates, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal("export authenticated candidate update")
	}
	review, err := client.InspectRouteUpdate(host.PublicKey(), frame)
	if err != nil {
		t.Fatal("inspect authenticated candidate update")
	}
	if err := client.ApplyRouteUpdate(host.PublicKey(), frame, review.Digest, []string{a.ID(), b.ID()}, review.Update.Expires); err != nil {
		t.Fatal("approve finite exact candidate permissions")
	}
	before := client.RemoteSnapshot()[0]
	currentHost.Store(nil)
	client.Close()
	host.Close()
	load := func(path string) persisted {
		t.Helper()
		var state persisted
		if err := config.ReadJSON(path, &state); err != nil {
			t.Fatal("reload protected saved state")
		}
		return state
	}
	savedHost, savedClient := load(hostPath), load(clientPath)
	if !savedHost.Identity.Key.Equal(hostState.Identity.Key) || !savedHost.Identity.PSK.Equal(hostState.Identity.PSK) || !savedClient.Identity.Key.Equal(clientState.Identity.Key) || !savedClient.Identity.PSK.Equal(clientState.Identity.PSK) {
		t.Fatal("protected cold-start state changed saved identity keys")
	}
	host, client = openNode(hostPath, savedHost, true), openNode(clientPath, savedClient, false)
	currentHost.Store(host)
	if host.PublicKey() != hostState.Identity.PublicKey() || client.PublicKey() != clientState.Identity.PublicKey() {
		t.Fatal("cold start changed public identity")
	}
	after := client.RemoteSnapshot()[0]
	if after.Address != before.Address || !after.ClientPrivate.Equal(before.ClientPrivate) || after.IncomingClientKey != before.IncomingClientKey {
		t.Fatal("saved-state cold start changed paired capability or role keys")
	}
	snapshot, err := client.RouteSnapshot(host.PublicKey())
	if err != nil || snapshot.Legacy || len(snapshot.Permitted) != 2 {
		t.Fatal("cold start lost finite candidate approvals")
	}
	ln, err := host.ListenPeer(ctx, "tcp", 54546)
	if err != nil {
		t.Fatal("start restored application service")
	}
	var handlers sync.WaitGroup
	acceptDone := make(chan struct{})
	peerErrors := make(chan struct{}, 4)
	go func() {
		defer close(acceptDone)
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			handlers.Add(1)
			go func() {
				defer handlers.Done()
				defer c.Close()
				peer, ok := host.PeerKey(c.RemoteAddr())
				if !ok || peer != client.PublicKey() {
					select {
					case peerErrors <- struct{}{}:
					default:
					}
					return
				}
				_ = c.SetDeadline(time.Now().Add(30 * time.Second))
				_, _ = io.Copy(c, c)
			}()
		}
	}()
	defer func() { ln.Close(); host.Close(); <-acceptDone; handlers.Wait() }()
	roundTrip := func(stage string) net.Conn {
		t.Helper()
		bounded, finish := context.WithTimeout(ctx, 25*time.Second)
		defer finish()
		c, err := client.DialPeer(bounded, host.PublicKey(), "tcp", 54546)
		if err != nil {
			var phase interface{ DERPFailurePhase() string }
			phaseName := "none"
			if errors.As(err, &phase) {
				phaseName = phase.DERPFailurePhase()
			}
			t.Fatalf("%s managed application dial failed (type=%T phase=%q eof=%t canceled=%t deadline=%t availability=%t)", stage, err, phaseName, errors.Is(err, io.EOF), errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded), relayAvailabilityError(err))
		}
		_ = c.SetDeadline(time.Now().Add(10 * time.Second))
		payload := []byte("managed synthetic application roundtrip")
		if _, err := c.Write(payload); err != nil {
			c.Close()
			t.Fatal(stage, "application write failed")
		}
		got := make([]byte, len(payload))
		if _, err := io.ReadFull(c, got); err != nil || string(got) != string(payload) {
			c.Close()
			t.Fatal(stage, "application echo failed")
		}
		select {
		case <-peerErrors:
			c.Close()
			t.Fatal("unexpected canonical application identity")
		default:
		}
		return c
	}
	first := roundTrip("saved-state LAN cold start with alternative unavailable")
	firstObservation, err := client.TransportSnapshot(host.PublicKey())
	if err != nil || firstObservation.CandidateID != a.ID() {
		first.Close()
		t.Fatal("cold start did not select the LAN candidate")
	}
	cw, ok := first.(interface{ CloseWrite() error })
	if !ok || cw.CloseWrite() != nil {
		first.Close()
		t.Fatal("TCP half-close failed")
	}
	if _, err := io.Copy(io.Discard, first); err != nil {
		first.Close()
		t.Fatal("TCP shutdown failed")
	}
	first.Close()
	r, err := client.client(ctx, host.PublicKey())
	if err != nil {
		t.Fatal("load existing client for orderly drain")
	}
	r.startMu.Lock()
	concrete, ok := r.client.(*tailcat.Client)
	if !ok {
		r.startMu.Unlock()
		t.Fatal("unexpected native transport")
	}
	drainCtx, finishDrain := context.WithTimeout(ctx, 10*time.Second)
	drainErr := concrete.DrainTCP(drainCtx)
	finishDrain()
	r.startMu.Unlock()
	if drainErr != nil {
		t.Fatal("TCP drain failed")
	}
	// Starting the approved alternative does not grant anything new. Wait for
	// the existing server's multi-region presence before withdrawing the LAN.
	alternativeReservation.Close()
	relayB, err := StartLocalRelay(ctx, b.Relay.Address, identities[1], allow, bootstrap)
	if err != nil {
		t.Fatal("start approved alternative fixture relay")
	}
	defer relayB.Close()
	registered, stopRegistration := context.WithTimeout(ctx, 45*time.Second)
	defer stopRegistration()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for !relayB.derp.IsClientConnectedForTest(host.cfg.Identity.Key.Public()) {
		select {
		case <-tick.C:
		case <-registered.Done():
			t.Fatal("server never registered at approved alternative")
		}
	}
	relayA.Close()
	second := roundTrip("new connection after LAN relay stopped")
	defer second.Close()
	selected, err := client.TransportSnapshot(host.PublicKey())
	if err != nil || selected.CandidateID != b.ID() || selected.ActiveFlows != 1 {
		t.Fatal("managed retry did not select the approved alternative")
	}
	recoveredRuntime, err := client.client(ctx, host.PublicKey())
	if err != nil {
		t.Fatal("load recovered runtime")
	}
	recoveredRuntime.startMu.Lock()
	freshClient := recoveredRuntime.client != concrete
	recoveredRuntime.startMu.Unlock()
	if !freshClient {
		t.Fatal("managed failover reused the failed client engine")
	}
	after = client.RemoteSnapshot()[0]
	if after.Address != before.Address || !after.ClientPrivate.Equal(before.ClientPrivate) || after.IncomingClientKey != before.IncomingClientKey {
		t.Fatal("automatic recovery changed the saved pairing")
	}
	if err := client.RevokeRoutes(host.PublicKey(), []string{a.ID(), b.ID()}); err != nil {
		t.Fatal("revoke saved route approvals")
	}
	if _, err := second.Write([]byte("must be blocked")); !errors.Is(err, ErrRoutePermission) && !errors.Is(err, ErrUntrusted) {
		t.Fatal("active traffic survived local route revocation")
	}
	if _, err := client.cfg.Trust.Epoch(host.PublicKey()); err != nil {
		t.Fatal("route revocation removed application trust")
	}
	if _, err := client.DialPeer(ctx, host.PublicKey(), "tcp", 54546); !errors.Is(err, ErrRoutePermission) {
		t.Fatal("revoked candidate allowed a new connection")
	}
	t.Log("saved-state LAN cold start, approved alternative recovery, stable pairing and active-flow revocation passed")
}
