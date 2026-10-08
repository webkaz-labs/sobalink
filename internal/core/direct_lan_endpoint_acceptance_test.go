//go:build endpoint_following_acceptance

package core

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/directlan"
	"github.com/webkaz-labs/sobalink/internal/endpointmeta"
	"github.com/webkaz-labs/sobalink/internal/webui"
)

// Source-only candidate. These positive fixtures are NOT execution clearance
// and do not cover the separately outstanding old-byte or revocation cases.
const endpointAcceptanceSelector = "^TestIntegratedEndpointMoveDelivery$"
const endpointReopenAcceptanceSelector = "^TestIntegratedEndpointStillValidReopen$"
const endpointAcceptanceOptIn = "reviewed-production-loopback-v1"

type endpointAcceptanceReservation struct {
	tcp       net.Listener
	udp       net.PacketConn
	endpoint  netip.AddrPort
	closeOnce sync.Once
	closeErr  error
}

func (r *endpointAcceptanceReservation) close() error {
	r.closeOnce.Do(func() {
		var errs []error
		if r.tcp != nil {
			if err := r.tcp.Close(); err != nil {
				errs = append(errs, err)
			} else {
				r.tcp = nil
			}
		}
		if r.udp != nil {
			if err := r.udp.Close(); err != nil {
				errs = append(errs, err)
			} else {
				r.udp = nil
			}
		}
		r.closeErr = errors.Join(errs...)
	})
	// A failed-close handle and its outcome remain retained; subsequent cleanup
	// cannot reinterpret a failed handoff as successfully joined.
	return r.closeErr
}

// One ephemeral allocation only. No port sweep, endpoint probing or retry.
func reserveEndpointAcceptance(t *testing.T) *endpointAcceptanceReservation {
	t.Helper()
	tcp, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal("could not reserve fixture TCP endpoint")
	}
	endpoint := tcp.Addr().(*net.TCPAddr).AddrPort()
	r := &endpointAcceptanceReservation{tcp: tcp, endpoint: endpoint}
	t.Cleanup(func() {
		if r.close() != nil {
			t.Error("reservation cleanup failed")
		}
	})
	if endpoint.Addr() != netip.MustParseAddr("127.0.0.1") || endpoint.Port() < 1024 || endpoint.Port() == DiscoveryPort || endpoint.Port() == PeerPort || endpoint.Port() == 54545 {
		t.Fatal("fixture endpoint allocation outside permitted set")
	}
	udp, err := net.ListenPacket("udp4", endpoint.String())
	if err != nil {
		t.Fatal("could not reserve same fixture UDP endpoint")
	}
	r.udp = udp
	return r
}

type endpointAcceptanceFixture struct {
	diagnosticTargets []endpointAcceptanceDiagnosticTarget
	t                 *testing.T
	ctx               context.Context
	cancel            context.CancelFunc
	cores             []*Core
	dirs              []string
	observer          *directlan.AcceptanceSessionLog
	deadline          string
	cleanupOnce       sync.Once
}

func newEndpointAcceptanceFixture(t *testing.T) *endpointAcceptanceFixture {
	t.Helper()
	// All guards precede Open, temporary state, observers, goroutines and sockets.
	if os.Getenv("SOBALINK_ENDPOINT_FOLLOWING_ACCEPTANCE") != endpointAcceptanceOptIn {
		t.Skip("requires separately reviewed production-path acceptance opt-in")
	}
	run := flag.Lookup("test.run")
	expected := map[string]string{"TestIntegratedEndpointMoveDelivery": endpointAcceptanceSelector, "TestIntegratedEndpointStillValidReopen": endpointReopenAcceptanceSelector}[t.Name()]
	if run == nil || expected == "" || run.Value.String() != expected {
		t.Fatal("exact acceptance selector required")
	}
	if !directLANNetstackReady {
		t.Fatal("userspace native transport build required")
	}
	for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy", "TS_PROXY"} {
		if os.Getenv(name) != "" {
			t.Fatal("proxy environment must be absent before fixture execution")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	observer, err := directlan.ObserveAcceptanceSessions()
	if err != nil {
		cancel()
		t.Fatal("acceptance observer already owned")
	}
	f := &endpointAcceptanceFixture{t: t, ctx: ctx, cancel: cancel, observer: observer, deadline: time.Now().Add(90 * time.Second).UTC().Format(time.RFC3339Nano)}
	t.Cleanup(f.cleanup)
	return f
}
func (f *endpointAcceptanceFixture) cleanup() {
	f.cleanupOnce.Do(func() {
		f.cancel()
		// Signal every independent owner before waiting. A blocked first close must
		// not prevent the other Core receiving its own shutdown request.
		outcomes := make(chan error, len(f.cores))
		for _, c := range f.cores {
			go func(c *Core) { outcomes <- c.Close() }(c)
		}
		watchdog := time.NewTimer(10 * time.Second)
		defer watchdog.Stop()
		joined := true
		for range f.cores {
			select {
			case err := <-outcomes:
				if err != nil {
					joined = false
					f.t.Error("real Core cleanup returned failure; private state retained")
				}
			case <-watchdog.C:
				f.t.Error("native Core cleanup unjoined; private state retained for the outer watchdog")
				return
			}
		}
		f.observer.Close()
		// Unlike t.TempDir, removal is conditional on actual successful close.
		// Timeout never erases evidence under live owners. Worker goroutines above
		// report only to a buffered channel and never call testing.T after return.
		if joined {
			for _, dir := range f.dirs {
				if os.RemoveAll(dir) != nil {
					f.t.Error("synthetic private-state cleanup failed")
				}
			}
		}
	})
}
func (f *endpointAcceptanceFixture) command(c *Core, name string, input any) any {
	f.t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		f.t.Fatal("fixture command encoding failed")
	}
	value, err := c.Command(f.ctx, webui.Command{RequestID: randomID(), Name: name, Payload: raw})
	if err != nil {
		f.dumpEndpointDiagnostics("command-failed")
		f.t.Fatalf("production command %s failed (class=%s)", name, networkErrorCode(err))
	}
	return value
}
func (f *endpointAcceptanceFixture) until(predicate func() bool, label string) {
	f.t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if predicate() {
			return
		}
		select {
		case <-f.ctx.Done():
			f.dumpEndpointDiagnostics(label)
			f.t.Fatal(label)
		case <-ticker.C:
		}
	}
}
func (f *endpointAcceptanceFixture) openSeed(local, remote directlan.Identity, own, peer netip.AddrPort) *Core {
	f.t.Helper()
	dir, dirErr := os.MkdirTemp("", "endpoint-acceptance-")
	if dirErr != nil {
		f.t.Fatal("private fixture directory allocation failed")
	}
	f.dirs = append(f.dirs, dir)
	// Synthetic legacy pair only: no confirmed context, approval, ready flag,
	// publication receipt, epoch or completed transport is fabricated here.
	scope := endpointmeta.Scope{Family: "ipv4", Prefixes: []string{"127.0.0.1/32"}}
	remotePeer := directlan.Peer{Key: remote.PublicKey(), Name: "synthetic-peer", Endpoint: peer, TunnelKey: remote.TunnelKey()}
	model := endpointmeta.Snapshot{Version: 3, Revision: "1", LocalPeer: endpointmeta.PeerWire{Key: local.PublicKey(), TunnelKey: local.TunnelKey(), Endpoint: own.String()}, LocalScope: scope, PreviousLocalEndpoint: own.String(), ObservedAt: time.Now().UTC().Format(time.RFC3339Nano), Peers: []endpointmeta.PeerRecord{{Peer: directLANPeerWire(remotePeer), Revision: "1"}}}
	state := directLANState{Version: 3, Identity: local, Selection: DirectLANSelection{Listen: own.String(), Prefixes: scope.Prefixes}, Peers: []directlan.Peer{remotePeer}, Metadata: &model}
	if validateDirectLANState(state) != nil {
		f.t.Fatal("synthetic legacy seed invalid")
	}
	profile := Profile{Version: 1, Settings: Settings{Locale: "en", Theme: "system", Network: "direct-lan", Hostname: "synthetic-endpoint"}, Peers: []Trust{}, Services: []ServiceSpec{}}
	for name, value := range map[string]any{"direct-lan.json": state, "sobalink.json": profile} {
		raw, err := json.MarshalIndent(value, "", "  ")
		if err != nil || config.AtomicWritePrivate(filepath.Join(dir, name), append(raw, '\n')) != nil {
			f.t.Fatal("synthetic private seed write failed")
		}
	}
	c, err := Open(f.ctx, Options{Directory: dir, Version: "synthetic-acceptance", SkipNetworkStart: true, NodeFactory: func(string, string) (NetworkBackend, error) { return nil, errors.New("non-direct backend forbidden") }})
	if err != nil {
		f.t.Fatal("real offline Core open failed")
	}
	f.cores = append(f.cores, c)
	if c.directLANStoreCopy().contextPublication != nil || c.nodeCopy() != nil {
		f.t.Fatal("read-only open invented runtime authority")
	}
	return c
}
func (f *endpointAcceptanceFixture) upgrade(a, b *Core, ar, br *endpointAcceptanceReservation) {
	f.t.Helper()
	for _, r := range []*endpointAcceptanceReservation{ar, br} {
		if r.close() != nil {
			f.t.Fatal("endpoint handoff failed")
		}
	}
	for _, item := range []struct{ c, peer *Core }{{a, b}, {b, a}} {
		in := UpgradeIntent{PeerID: item.peer.directLANStoreCopy().copy().Identity.PublicKey(), Deadline: f.deadline}
		review := f.command(item.c, "direct-lan.upgrade.review", in).(UpgradeReview)
		if review.RestartRequired {
			f.t.Fatal("offline seed unexpectedly requires restart")
		}
		in.ExpectedRevision = review.Revision
		f.command(item.c, "direct-lan.upgrade.run", in)
	}
	for _, c := range []*Core{a, b} {
		f.until(func() bool {
			progress := f.command(c, "direct-lan.upgrade.status", UpgradeIntent{}).(UpgradeProgress)
			if progress.State == "failed" || progress.State == "cancelled" {
				f.t.Fatalf("production context controller failed (class=%s)", progress.ErrorCode)
			}
			return progress.State == "network-started"
		}, "production context controller did not finish in original deadline")
		c.op.Lock()
		s := c.directLANStoreCopy()
		s.mu.Lock()
		valid := s.contextPublicationCurrentLocked(c.lanStartNonce) && s.state.Metadata.Peers[0].ContextConfirmed
		s.mu.Unlock()
		c.op.Unlock()
		if !valid {
			f.t.Fatal("controller progress lacked confirmed sole-writer receipt")
		}
	}
}
func (f *endpointAcceptanceFixture) capture(c *Core, key string) *directlan.AcceptanceGeneration {
	f.t.Helper()
	c.op.Lock()
	b := c.endpointBackendLocked()
	c.op.Unlock()
	if b == nil {
		f.t.Fatal("missing real managed backend")
	}
	got, err := f.observer.Capture(b.Node, key)
	if err != nil {
		f.t.Fatal("missing exact native generation observation")
	}
	return got
}
func (f *endpointAcceptanceFixture) follow(c *Core, key string) endpointmeta.FollowApproval {
	f.t.Helper()
	state := c.directLANStoreCopy().copy()
	// Both synthetic scopes are exactly the same /32, with no family expansion.
	scopeDigest, err := state.Metadata.LocalScope.Digest()
	if err != nil {
		f.t.Fatal("synthetic scope digest failed")
	}
	follow := endpointmeta.FollowApproval{ScopeDigest: scopeDigest, Revision: "1", Granted: time.Now().UTC().Format(time.RFC3339Nano), Lifetime: "finite", Expires: f.deadline, Active: true}
	in := directLANEndpointInput{PeerID: key, Follow: &follow}
	review := f.command(c, "direct-lan.endpoint.follow.preview", in).(directLANEndpointReview)
	in.ExpectedRevision = review.Revision
	f.command(c, "direct-lan.endpoint.follow.apply", in)
	return follow
}

type endpointAcceptanceWrites struct {
	mu              sync.Mutex
	transaction     *EndpointTransaction
	fence, finished bool
	err             bool
}

func (f *endpointAcceptanceFixture) observeWrites(c *Core) *endpointAcceptanceWrites {
	f.t.Helper()
	trace := new(endpointAcceptanceWrites)
	c.op.Lock()
	s := c.directLANStoreCopy()
	s.mu.Lock()
	original := s.write
	if original == nil {
		original = config.AtomicWrite
	}
	s.write = func(path string, raw []byte) error {
		var next directLANState
		parseErr := json.Unmarshal(raw, &next)
		// This wrapper delegates the actual original atomic writer once, unchanged.
		// It does not install receipts, alter bytes or drive transaction transitions.
		err := original(path, raw)
		trace.mu.Lock()
		defer trace.mu.Unlock()
		if err != nil || parseErr != nil || next.Metadata == nil {
			trace.err = true
			return err // observation never changes the original writer outcome
		}
		if s.endpointTransaction != nil {
			trace.transaction = s.endpointTransaction
			if next.Metadata.PendingChange != nil {
				trace.fence = true
			}
			if trace.fence && next.Metadata.PendingChange == nil {
				trace.finished = true
			}
		}
		return err
	}
	s.mu.Unlock()
	c.op.Unlock()
	return trace
}
func (f *endpointAcceptanceFixture) assertTransaction(c *Core, trace *endpointAcceptanceWrites, old, fresh *directlan.AcceptanceGeneration) {
	f.t.Helper()
	f.until(func() bool { c.mu.RLock(); defer c.mu.RUnlock(); return c.endpointJob == nil }, "endpoint job did not finish")
	trace.mu.Lock()
	transaction, fence, finished, writeErr := trace.transaction, trace.fence, trace.finished, trace.err
	trace.mu.Unlock()
	if transaction == nil || !fence || !finished || writeErr {
		f.t.Fatal("missing actual successful fence/final publication trace")
	}
	c.op.Lock()
	s := c.directLANStoreCopy()
	s.mu.Lock()
	owner, real := transaction.old.(*endpointNodeOwner)
	valid := real && owner.retired != nil && transaction.joined && transaction.durable && transaction.published && transaction.receipt == s.contextPublication && s.contextPublicationCurrentLocked(c.lanStartNonce)
	s.mu.Unlock()
	c.op.Unlock()
	if !valid {
		f.t.Fatal("transaction lacks actual native join/current sole-writer receipt")
	}
	if !old.MatchesRetirement(owner.retired) {
		f.t.Fatal("transaction retired a different captured generation")
	}
	if old.WaitPhysicalJoin(f.ctx) != nil || owner.retired.Wait(f.ctx) != nil {
		f.t.Fatal("exact old generation did not physically join")
	}
	if !fresh.FreshFrom(old) {
		f.t.Fatal("replacement reused generation/session or lacks birth observation")
	}
}

func TestIntegratedEndpointMoveDelivery(t *testing.T) {
	runIntegratedEndpointMoveDelivery(t)
}
func runIntegratedEndpointMoveDelivery(t *testing.T) (*endpointAcceptanceFixture, *Core, *Core) {
	f := newEndpointAcceptanceFixture(t)
	ar, br, moved := reserveEndpointAcceptance(t), reserveEndpointAcceptance(t), reserveEndpointAcceptance(t)
	aid, bid := directlan.Identity{Seed: strings.Repeat("01", 32)}, directlan.Identity{Seed: strings.Repeat("02", 32)}
	a, b := f.openSeed(aid, bid, ar.endpoint, br.endpoint), f.openSeed(bid, aid, br.endpoint, ar.endpoint)
	f.upgrade(a, b, ar, br)
	follow := f.follow(b, aid.PublicKey())
	// Application consent is explicit. This fixture retains no pre-move TCP
	// stream; normal production peer refresh may still exchange application data.
	// Old-byte/resumption acceptance remains a separate outstanding case.
	for _, item := range []struct {
		c   *Core
		key string
	}{{a, bid.PublicKey()}, {b, aid.PublicKey()}} {
		f.command(item.c, "peer.trust", map[string]any{"peerId": item.key, "trusted": true})
	}
	oldA, oldB := f.capture(a, bid.PublicKey()), f.capture(b, aid.PublicKey())
	traceA, traceB := f.observeWrites(a), f.observeWrites(b)
	enableEndpointAcceptanceTrace(a, b)
	t.Cleanup(func() { disableEndpointAcceptanceTrace(a, b) })
	f.diagnosticTargets = []endpointAcceptanceDiagnosticTarget{{label: "sender", core: a, expected: br.endpoint, writes: traceA}, {label: "receiver", core: b, expected: moved.endpoint, writes: traceB}}
	move := endpointMoveInput{Endpoint: moved.endpoint.String(), Deliveries: []endpointMoveDelivery{{PeerID: bid.PublicKey(), Lifetime: "finite", Expires: f.deadline}}}
	review := f.command(a, "direct-lan.endpoint.move.preview", move).(endpointMoveReview)
	if review.Destinations[bid.PublicKey()] != br.endpoint.String() {
		t.Fatal("move review changed approved recipient endpoint")
	}
	move.ExpectedRevision = review.Revision
	if moved.close() != nil {
		t.Fatal("new endpoint handoff failed")
	}
	result := f.command(a, "direct-lan.endpoint.move.apply", move).(map[string]any)
	t.Logf("sender move saved=%v active=%v", result["saved"], result["active"])
	if deliveries, ok := result["deliveries"].([]endpointDeliveryResult); ok {
		for i, delivery := range deliveries {
			t.Logf("sender delivery index=%d sequence=%s outcome=%s attempts=%d", i, delivery.Sequence, delivery.Outcome, delivery.Attempts)
		}
	}
	f.dumpEndpointDiagnostics("move-command-returned")
	if result["saved"] != true || result["active"] != true {
		t.Fatal("move did not report saved and active separately")
	}
	f.until(func() bool {
		state := b.directLANStoreCopy().copy()
		return state.Metadata.Peers[0].Peer.Endpoint == moved.endpoint.String() && state.Metadata.PendingChange == nil
	}, "signed production observation was not durably followed")
	for _, c := range []*Core{a, b} {
		f.until(func() bool { c.mu.RLock(); defer c.mu.RUnlock(); return c.endpointJob == nil }, "endpoint job did not finish before fresh capture")
	}
	freshA, freshB := f.capture(a, bid.PublicKey()), f.capture(b, aid.PublicKey())
	f.assertTransaction(a, traceA, oldA, freshA)
	f.assertTransaction(b, traceB, oldB, freshB)
	if got := b.directLANStoreCopy().copy().Metadata.Peers[0].EndpointState.Follow; got == nil || !reflect.DeepEqual(*got, follow) {
		t.Fatal("endpoint following renewed original consent")
	}
	// A new application operation must perform fresh ordinary bound-session
	// authentication through the actual production peer API and WG/netstack.
	message := f.command(b, "message.send", map[string]any{"peerId": aid.PublicKey(), "text": "synthetic post-move message"}).(Message)
	if message.Status != "sent" || !freshA.SessionReady() || !freshB.SessionReady() {
		t.Fatal("fresh production session/application exchange incomplete")
	}
	a.mu.RLock()
	received := len(a.messages) > 0 && a.messages[len(a.messages)-1].Text == message.Text
	a.mu.RUnlock()
	if !received {
		t.Fatal("fresh application message absent at owned receiver")
	}
	// Explicit reviewed retry uses the identical persisted proof and original
	// cutoff. Already-applied must derive from B's durable high-water state.
	before := a.directLANStoreCopy().copy().Metadata.Peers[0].EndpointState
	proof := *before.IssuedProof
	digest, _ := proof.Digest()
	store := a.directLANStoreCopy()
	store.mu.Lock()
	cutoff, cutoffPresent := store.endpointIssuedDeadlines[digest]
	store.mu.Unlock()
	if !cutoffPresent || cutoff.expired {
		t.Fatal("missing original issued-proof cutoff")
	}
	in := directLANEndpointExportInput{PeerID: bid.PublicKey()}
	deliveryReview := f.command(a, "direct-lan.endpoint.delivery.preview", in).(map[string]any)
	in.ExpectedRevision = deliveryReview["revision"].(string)
	retry := f.command(a, "direct-lan.endpoint.delivery.apply", in).(endpointDeliveryResult)
	if retry.Outcome != "already_applied" || retry.Sequence != proof.Update.Sequence {
		t.Fatal("explicit exact-proof retry was not durably recognized")
	}
	store.mu.Lock()
	retainedCutoff := store.endpointIssuedDeadlines[digest]
	store.mu.Unlock()
	if retainedCutoff != cutoff {
		t.Fatal("redelivery renewed original process cutoff")
	}
	after := a.directLANStoreCopy().copy().Metadata.Peers[0].EndpointState
	if after.IssuedProof == nil || *after.IssuedProof != proof || after.IssuedHighwater != before.IssuedHighwater {
		t.Fatal("explicit retry changed signed proof, sequence or lifetime")
	}
	t.Log("verified production observation, durable transaction, physical old join, fresh session birth and new application exchange")
	return f, a, b
}

// This is Core-instance close/reopen in the same Go test OS process, with
// unchanged still-valid saved authority and a fresh Core process nonce.
// It does not claim same-process transient socket-loss or old-byte coverage.
func TestIntegratedEndpointStillValidReopen(t *testing.T) {
	f, a, b := runIntegratedEndpointMoveDelivery(t)
	aKey := a.directLANStoreCopy().copy().Identity.PublicKey()
	bKey := b.directLANStoreCopy().copy().Identity.PublicKey()
	oldGeneration := f.capture(b, aKey)
	b.op.Lock()
	store := b.directLANStoreCopy()
	backend := b.endpointBackendLocked()
	store.mu.Lock()
	saved := cloneDirectLANState(store.state)
	oldReceipt, oldProcess := store.contextPublication, b.lanStartNonce
	oldCutoff := backend.currentCompletion().deadline
	store.mu.Unlock()
	b.op.Unlock()
	if oldCutoff.IsZero() || !time.Now().Before(oldCutoff) {
		t.Fatal("reopen fixture lacks still-valid original authority")
	}
	// Wait for this actual Core instance to close before taking its endpoint.
	closed := make(chan error, 1)
	go func() { closed <- b.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal("offline transition failed; state retained")
		}
	case <-f.ctx.Done():
		t.Fatal("offline transition unjoined; state retained")
	}
	if oldGeneration.WaitPhysicalJoin(f.ctx) != nil {
		t.Fatal("closed Core instance retained native generation")
	}
	own := netip.MustParseAddrPort(saved.Selection.Listen)
	if own.Addr() != netip.MustParseAddr("127.0.0.1") {
		t.Fatal("reopen endpoint left owned loopback")
	}
	tcp, err := net.Listen("tcp4", own.String())
	if err != nil {
		t.Fatal("closed owned endpoint reservation failed")
	}
	offline := &endpointAcceptanceReservation{tcp: tcp, endpoint: own}
	t.Cleanup(func() {
		if offline.close() != nil {
			t.Error("offline reservation cleanup failed")
		}
	})
	udp, err := net.ListenPacket("udp4", own.String())
	if err != nil {
		t.Fatal("closed owned UDP reservation failed")
	}
	offline.udp = udp
	// No grant, proof, expiry, state byte, process receipt or ready bit is edited
	// in this offline interval. Release only for normal production Open startup.
	if !time.Now().Before(oldCutoff) {
		t.Fatal("original authority expired while offline")
	}
	if offline.close() != nil {
		t.Fatal("reopen endpoint handoff failed")
	}
	reopened, err := Open(f.ctx, Options{Directory: b.dir, Version: "synthetic-acceptance", NodeFactory: func(string, string) (NetworkBackend, error) { return nil, errors.New("non-direct backend forbidden") }})
	if err != nil {
		t.Fatal("normal current-state reopen failed")
	}
	f.cores = append(f.cores, reopened)
	reopened.op.Lock()
	current := reopened.directLANStoreCopy()
	nextBackend := reopened.endpointBackendLocked()
	current.mu.Lock()
	currentOwner := nextBackend != nil && nextBackend.currentCompletion() != nil
	valid := currentOwner && reopened.lanStartNonce != oldProcess && current.contextPublication != nil && current.contextPublication != oldReceipt && current.contextPublicationCurrentLocked(reopened.lanStartNonce) && reflect.DeepEqual(saved, current.state)
	originalExpiry, expiryErr := time.Parse(time.RFC3339Nano, f.deadline)
	var reopenedCutoff time.Time
	if currentOwner {
		completion := nextBackend.currentCompletion()
		reopenedCutoff = completion.deadline
		valid = valid && completion.currentEndpoints && !completion.deadline.IsZero() && expiryErr == nil && completion.deadline.UTC().Equal(originalExpiry) && time.Now().Before(completion.deadline)
	}
	current.mu.Unlock()
	reopened.op.Unlock()
	if !valid {
		t.Fatal("normal reopen lacked fresh receipt or changed original saved authority/cutoff")
	}
	fresh := f.capture(reopened, aKey)
	if !fresh.FreshFrom(oldGeneration) {
		t.Fatal("normal reopen reused old transport/session authority")
	}
	proof := *a.directLANStoreCopy().copy().Metadata.Peers[0].EndpointState.IssuedProof
	digest, _ := proof.Digest()
	aStore := a.directLANStoreCopy()
	aStore.mu.Lock()
	original := aStore.endpointIssuedDeadlines[digest]
	aStore.mu.Unlock()
	in := directLANEndpointExportInput{PeerID: bKey}
	review := f.command(a, "direct-lan.endpoint.delivery.preview", in).(map[string]any)
	in.ExpectedRevision = review["revision"].(string)
	result := f.command(a, "direct-lan.endpoint.delivery.apply", in).(endpointDeliveryResult)
	aStore.mu.Lock()
	unchangedCutoff := aStore.endpointIssuedDeadlines[digest] == original
	aStore.mu.Unlock()
	if result.Outcome != "already_applied" || result.Sequence != proof.Update.Sequence || !unchangedCutoff {
		t.Fatal("reopened peer did not recognize exact proof without renewed cutoff")
	}
	if nextBackend.currentCompletion().deadline != reopenedCutoff {
		t.Fatal("reopened first process cutoff changed during redelivery")
	}
	after := reopened.directLANStoreCopy().copy()
	if !reflect.DeepEqual(saved, after) {
		t.Fatal("offline return changed saved proof, follow grant or original expiry")
	}
	message := f.command(reopened, "message.send", map[string]any{"peerId": aKey, "text": "synthetic post-reopen message"}).(Message)
	if message.Status != "sent" || !fresh.SessionReady() {
		t.Fatal("reopened peer lacked fresh authenticated application exchange")
	}
	a.mu.RLock()
	received := len(a.messages) > 0 && a.messages[len(a.messages)-1].Text == message.Text
	a.mu.RUnlock()
	if !received {
		t.Fatal("owned receiver lacks post-reopen message")
	}
	t.Log("verified same-OS-process Core reopen with unchanged authority, fresh receipt/session and exact-proof redelivery")
}
