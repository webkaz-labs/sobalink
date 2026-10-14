package core

import (
	"bytes"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/lanlink"
)

func setupRequestTestSessions(t *testing.T, verification string) (*setupSession, *setupSession, time.Time) {
	t.Helper()
	a, b, now := setupTestSessions(t)
	now = now.Add(-5 * time.Second)
	for _, s := range []*setupSession{a, b} {
		s.local.Protocol = 2
		s.local.Verification = verification
		s.local.Expires = now.Add(20 * time.Minute)
		s.selected = nil
		s.rejected = map[string]setupWire{}
		s.sourceAdmissions = map[netip.Addr]int{}
	}
	a.local.Mode = "outbound"
	a.local.AllowHost = false
	a.local.Relay = nil
	a.local.Bootstrap = netip.AddrPortFrom(a.local.Scope.LocalAddress, 0)
	b.local.Mode = "receive"
	target := b.local
	a.target = &target
	a.targetRevision = 1
	return a, b, now
}
func setupRequestTestPlan(t *testing.T, a, b *setupSession, now time.Time) {
	t.Helper()
	_, e := a.requestPeer(SetupRequestInput{TargetRevision: 1, SessionID: a.local.SessionID, TargetSessionID: b.local.SessionID, ExpectedPeerKey: b.local.Key, TOFUAcknowledged: true})
	if e != nil {
		t.Fatal(e)
	}
	w, e := a.wireLocked(now)
	if e != nil {
		t.Fatal(e)
	}
	if e = b.admitRequestLocked(w, a.local.Scope.LocalAddress, now); e != nil {
		t.Fatal(e)
	}
	setupTestExchange(t, a, b, true, now)
	setupTestExchange(t, b, a, false, now)
	if e = b.electLocked(now.Add(4 * time.Second)); e != nil {
		t.Fatal(e)
	}
	setupTestExchange(t, b, a, false, now.Add(4*time.Second))
	setupTestExchange(t, a, b, true, now.Add(4*time.Second))
	if a.plan == nil || b.plan == nil || !a.peerPlan || !b.peerPlan {
		t.Fatal("v2 plan did not converge")
	}
}
func TestSetupRequestPinsTargetBeforeSenderConsent(t *testing.T) {
	a, b, _ := setupRequestTestSessions(t, "tofu")
	for _, in := range []SetupRequestInput{
		{TargetRevision: 1, SessionID: a.local.SessionID, TargetSessionID: b.local.SessionID, ExpectedPeerKey: strings.Repeat("c", 64), TOFUAcknowledged: true},
		{TargetRevision: 1, SessionID: a.local.SessionID, TargetSessionID: strings.Repeat("c", 64), ExpectedPeerKey: b.local.Key, TOFUAcknowledged: true},
		{TargetRevision: 1, SessionID: a.local.SessionID, TargetSessionID: b.local.SessionID, ExpectedPeerKey: b.local.Key},
	} {
		if _, e := a.requestPeer(in); e == nil {
			t.Fatal("consent accepted wrong pin/session or missing TOFU acknowledgement")
		}
		if a.requestConsent || a.localApproved || a.selected != nil {
			t.Fatal("rejected request retained consent")
		}
	}
}
func TestSetupRequestTOFURequiresBilateralApprovalAndExactPlan(t *testing.T) {
	a, b, now := setupRequestTestSessions(t, "tofu")
	setupRequestTestPlan(t, a, b, now)
	if !a.localApproved || a.remoteApproved || b.localApproved || !b.remoteApproved || b.commitStarted || a.commitStarted {
		t.Fatal("request upgraded receiver approval or activation")
	}
	if a.snapshot().Plan.Verification != "tofu" || b.snapshot().Request.RequestID != a.local.SessionID || a.snapshot().Contact != nil {
		t.Fatal("status misrepresented TOFU, request or outbound listener")
	}
	old := a.peer.Key
	w, _ := b.wireLocked(now.Add(4 * time.Second))
	w.Participant.Key = lanlink.GenerateIdentity().PublicKey()
	if e := a.acceptWireLocked(w, false, now.Add(4*time.Second)); e == nil || a.peer.Key != old {
		t.Fatal("response replaced preflight key")
	}
	w, _ = b.wireLocked(now.Add(4 * time.Second))
	changed := *w.Plan
	changed.Revision++
	w.Plan = &changed
	if e := a.acceptWireLocked(w, false, now.Add(4*time.Second)); e == nil {
		t.Fatal("request silently accepted changed plan")
	}
	a.cancel()
	w, _ = b.wireLocked(now.Add(4 * time.Second))
	if e := a.acceptWireLocked(w, false, now.Add(4*time.Second)); e == nil {
		t.Fatal("cancelled request accepted late response")
	}
}
func TestSetupRequestFullVerificationNeverAutoApproves(t *testing.T) {
	a, b, now := setupRequestTestSessions(t, "full-sha256")
	setupRequestTestPlan(t, a, b, now)
	if a.localApproved || a.remoteApproved || b.localApproved || b.remoteApproved {
		t.Fatal("request substituted for full verification")
	}
	if a.snapshot().Plan.Verification != "full-sha256" {
		t.Fatal("full verification downgraded")
	}
}
func TestSetupRequestAdmissionRequiresProofAndPreservesLockedPeer(t *testing.T) {
	a, b, now := setupRequestTestSessions(t, "tofu")
	_, e := a.requestPeer(SetupRequestInput{TargetRevision: 1, TargetSessionID: b.local.SessionID, ExpectedPeerKey: b.local.Key, TOFUAcknowledged: true})
	if e != nil {
		t.Fatal(e)
	}
	w, _ := a.wireLocked(now)
	bad := w
	bad.HelloProof = []byte("invalid")
	if b.admitRequestLocked(bad, a.local.Scope.LocalAddress, now) == nil || b.peer != nil || b.selected != nil {
		t.Fatal("invalid proof occupied request slot")
	}
	if b.admitRequestLocked(w, netip.MustParseAddr("192.168.50.99"), now) == nil {
		t.Fatal("unbound source accepted")
	}
	if e = b.admitRequestLocked(w, a.local.Scope.LocalAddress, now); e != nil {
		t.Fatal(e)
	}
	other, _, _ := setupRequestTestSessions(t, "tofu")
	other.local.SessionID = strings.Repeat("c", 64)
	other.target = &b.local
	other.targetRevision = 1
	_, e = other.requestPeer(SetupRequestInput{TargetRevision: 1, TargetSessionID: b.local.SessionID, ExpectedPeerKey: b.local.Key, TOFUAcknowledged: true})
	if e != nil {
		t.Fatal(e)
	}
	foreign, _ := other.wireLocked(now)
	if b.acceptWireLocked(foreign, true, now) == nil || b.peer.Key != a.local.Key {
		t.Fatal("new request replaced locked approval target")
	}
}
func TestSetupRequestRejectClearsApprovalAndRetainsBoundTombstone(t *testing.T) {
	a, b, now := setupRequestTestSessions(t, "tofu")
	setupRequestTestPlan(t, a, b, now)
	requestID := b.requestID
	if _, e := b.rejectRequest(SetupRejectInput{RequestID: strings.Repeat("c", 64)}); e == nil {
		t.Fatal("wrong request rejected")
	}
	if _, e := b.rejectRequest(SetupRejectInput{RequestID: requestID}); e != nil {
		t.Fatal(e)
	}
	if b.state != "receiving" || b.peer != nil || b.localApproved || b.remoteApproved || b.plan != nil || b.requestID != "" {
		t.Fatal("rejected consent survived")
	}
	rejected := b.rejected[requestID]
	if e := a.acceptWireLocked(rejected, false, now.Add(4*time.Second)); e != nil || a.ctx.Err() == nil || a.terminalState != "rejected" {
		t.Fatal("authenticated rejection missing", e)
	}
	if len(b.rejected) != 1 {
		t.Fatal("request tombstone missing")
	}
}
func TestSetupRequestAdmissionHasIndependentFiniteBounds(t *testing.T) {
	_, b, now := setupRequestTestSessions(t, "tofu")
	source := netip.MustParseAddr("192.168.50.10")
	for i := 0; i < 4; i++ {
		if !b.admitRequestSourceLocked(source, now) {
			t.Fatal("small burst rejected")
		}
	}
	if b.admitRequestSourceLocked(source, now) {
		t.Fatal("per-second rate unbounded")
	}
	for i := 4; i < 64; i++ {
		if !b.admitRequestSourceLocked(source, now.Add(time.Duration(i)*time.Second)) {
			t.Fatal("source budget rejected too early")
		}
	}
	if b.admitRequestSourceLocked(source, now.Add(time.Minute*2)) {
		t.Fatal("unadmitted source budget unbounded")
	}
	b.sourceAdmissions = map[netip.Addr]int{}
	for i := 0; i < 32; i++ {
		ip := netip.AddrFrom4([4]byte{192, 168, 50, byte(i + 1)})
		if !b.admitRequestSourceLocked(ip, now.Add(time.Duration(i+200)*time.Second)) {
			t.Fatal("source table unexpectedly full")
		}
	}
	if b.admitRequestSourceLocked(netip.MustParseAddr("192.168.50.99"), now.Add(300*time.Second)) {
		t.Fatal("source rotation allocation unbounded")
	}
	if b.requestLimit() < 1200 || b.requestLimit() > 4096 {
		t.Fatal("extended review polling budget is invalid")
	}
}
func TestSetupRequestModeValidationAndNoBindOutbound(t *testing.T) {
	c := openLANTestCore(t)
	scope := SetupScope{InterfaceIndex: 1, Interface: "synthetic", LocalAddress: "127.0.0.1", Prefix: "127.0.0.0/8", linkBinding: "fixture"}
	scope.ScopeID = setupScopeID(scope)
	c.setupScopeRead = func() ([]SetupScope, error) { return []SetupScope{scope}, nil }
	in := SetupStartInput{Alias: "Synthetic requester", ScopeID: scope.ScopeID, Consent: true, Mode: "outbound", Verification: "tofu", ExpiresInSeconds: 1200}
	v, e := command(c, randomID(), "setup.start", in)
	if e != nil {
		t.Fatal(e)
	}
	s := c.setupCopy()
	view := v.(SetupSnapshot)
	if s.listener != nil || s.server != nil || s.discovery != nil || view.Contact != nil || view.ContactText != "" || view.Mode != "outbound" || view.Version != 2 || time.Until(view.ExpiresAt) < 19*time.Minute {
		t.Fatal("outbound allocated listeners/discovery or lost explicit lifetime")
	}
	for _, mutate := range []func(*SetupStartInput){func(i *SetupStartInput) { i.BootstrapPort = 54440 }, func(i *SetupStartInput) { i.AllowHost = true; i.RelayPort = 54441 }, func(i *SetupStartInput) { i.ExpiresInSeconds = 1201 }, func(i *SetupStartInput) { i.ExpiresInSeconds = 119 }, func(i *SetupStartInput) { i.Verification = "unknown" }} {
		bad := in
		mutate(&bad)
		if validateSetupMode(bad) == nil {
			t.Fatal("unsafe mode accepted")
		}
	}
}
func TestSetupRequestPublicEndpointRejectsBrowserAndWrongScope(t *testing.T) {
	a, b, now := setupRequestTestSessions(t, "tofu")
	c := openLANTestCore(t)
	b.core = c
	scope := b.scope
	scope.ScopeID = setupScopeID(scope)
	b.scope = scope
	c.setupScopeRead = func() ([]SetupScope, error) { return []SetupScope{scope}, nil }
	for _, origin := range []string{"https://example.test", ""} {
		req := httptest.NewRequest(http.MethodGet, "http://"+b.local.Bootstrap.String()+"/sobalink/setup/v2/contact", nil)
		req.RemoteAddr = a.local.Scope.LocalAddress.String() + ":40000"
		req.Header.Set("Content-Type", "application/octet-stream")
		req.Header.Set("Origin", origin)
		recorder := httptest.NewRecorder()
		b.serveRequest(recorder, req)
		if origin != "" {
			if recorder.Code != http.StatusForbidden {
				t.Fatal("browser origin admitted")
			}
		} else {
			if recorder.Code != http.StatusOK {
				t.Fatal("public preflight unavailable")
			}
			var p lanlink.SetupParticipant
			if json.Unmarshal(recorder.Body.Bytes(), &p) != nil || p.Key != b.local.Key || p.Validate(now) != nil {
				t.Fatal("bad public participant")
			}
			if bytes.Contains(recorder.Body.Bytes(), []byte("private")) {
				t.Fatal("private state exposed")
			}
		}
	}
}

func TestSetupRequestEarlyPeerExpiryClearsSlotWithoutReusingConsent(t *testing.T) {
	a, b, now := setupRequestTestSessions(t, "tofu")
	a.local.Expires = now.Add(120 * time.Second)
	setupRequestTestPlan(t, a, b, now)
	id := b.requestID
	b.expireRequestLocked(a.local.Expires)
	if b.requestID != "" || b.plan != nil || b.localApproved || b.remoteApproved || b.peer != nil || b.state != "receiving" {
		t.Fatal("expired request retained approval slot")
	}
	if _, ok := b.rejected[id]; !ok {
		t.Fatal("expired request can be replayed")
	}
}
func TestSetupTrustBasisIsDurableAndRevocationFailsClosed(t *testing.T) {
	c := openLANTestCore(t)
	store, e := c.ensureLANIdentity()
	if e != nil {
		t.Fatal(e)
	}
	key := lanlink.GenerateIdentity().PublicKey()
	state := store.copy()
	if e = rememberSetupTrust(&state, key, "tofu"); e != nil {
		t.Fatal(e)
	}
	if e = store.save(state); e != nil {
		t.Fatal(e)
	}
	loaded, e := readLANStore(store.path)
	if e != nil || loaded.copy().SetupTrust[0].Verification != "tofu" {
		t.Fatal("TOFU basis lost on reload", e)
	}
	state = store.copy()
	if e = rememberSetupTrust(&state, key, "full-sha256"); e != nil {
		t.Fatal(e)
	}
	if e = rememberSetupTrust(&state, key, "tofu"); e == nil {
		t.Fatal("weaker verification mode accepted for a fully verified key")
	}
	if state.SetupTrust[0].Verification != "full-sha256" {
		t.Fatal("TOFU downgraded verified key")
	}
	if e = store.save(state); e != nil {
		t.Fatal(e)
	}
	if e = c.recordSetupRevocation(key); e != nil {
		t.Fatal(e)
	}
	loaded, e = readLANStore(store.path)
	if e != nil {
		t.Fatal(e)
	}
	state = loaded.copy()
	if !state.SetupTrust[0].Revoked || rememberSetupTrust(&state, key, "tofu") == nil || rememberSetupTrust(&state, key, "full-sha256") == nil {
		t.Fatal("revocation revived after restart")
	}
	if len(c.profileCopy().Peers) != 0 {
		t.Fatal("transport basis granted application trust")
	}
	clone := cloneLANState(state)
	clone.SetupTrust[0].Revoked = false
	if !state.SetupTrust[0].Revoked {
		t.Fatal("snapshot alias mutated durable trust")
	}
}

func TestSetupRequestFloodCannotEvictPinnedApprovalTarget(t *testing.T) {
	a, b, now := setupRequestTestSessions(t, "tofu")
	setupRequestTestPlan(t, a, b, now)
	for i := 0; i < maxSetupRequests+100; i++ {
		b.admitRequestSourceLocked(netip.MustParseAddr("192.168.50.99"), now.Add(time.Duration(i)*time.Second))
	}
	if !b.admitRequestSourceLocked(a.local.Scope.LocalAddress, now.Add(500*time.Second)) || b.peer == nil || b.peer.Key != a.local.Key || b.requestID != a.local.SessionID || b.ctx.Err() != nil {
		t.Fatal("unrelated admission exhaustion displaced the locked request")
	}
}

func TestSetupRequestCancellationIsExactAndClearsPendingSlot(t *testing.T) {
	a, b, now := setupRequestTestSessions(t, "tofu")
	setupRequestTestPlan(t, a, b, now)
	packet, _ := a.wireLocked(now.Add(4 * time.Second))
	packet.Cancellation = []byte("invalid")
	if b.acceptWireLocked(packet, true, now.Add(4*time.Second)) == nil || b.requestID == "" {
		t.Fatal("unauthenticated cancellation removed request")
	}
	frame, e := lanlink.SealSetupCancellation(a.identity, *a.hello, now.Add(4*time.Second))
	if e != nil {
		t.Fatal(e)
	}
	packet.Cancellation = frame
	id := b.requestID
	if e = b.acceptWireLocked(packet, true, now.Add(4*time.Second)); e != nil {
		t.Fatal(e)
	}
	if b.peer != nil || b.plan != nil || b.localApproved || b.remoteApproved || b.state != "receiving" {
		t.Fatal("cancel left replayable approval")
	}
	if _, ok := b.rejected[id]; !ok {
		t.Fatal("cancelled request was not tombstoned")
	}
}
func TestSetupRevocationFencesDelayedPersistenceAndRestart(t *testing.T) {
	c := openLANTestCore(t)
	if e := c.configureLAN(testLANSelection()); e != nil {
		t.Fatal(e)
	}
	store := c.lanStoreCopy()
	key := lanlink.GenerateIdentity().PublicKey()
	if e := c.recordSetupRevocation(key); e != nil {
		t.Fatal(e)
	}
	stale := lanlink.Snapshot{Version: 1, Peers: []lanlink.Peer{{Key: key, Name: "Synthetic delayed peer"}}}
	if e := store.persistAt(store.copy().SetupPersistenceEpoch)(stale, nil); networkErrorCode(e) != "setup_peer_revoked" {
		t.Fatal("delayed persistence revived revoked key", e)
	}
	if len(store.copy().Trust.Peers) != 0 {
		t.Fatal("failed revoked save published authority")
	}
	// A pre-existing pair with a saved tombstone is loadable for local repair,
	// but must never start an engine after process restart.
	store.mu.Lock()
	store.state.Trust = stale
	store.mu.Unlock()
	if _, e := c.newLANBackend(store); networkErrorCode(e) != "setup_peer_revoked" {
		t.Fatal("restart ignored tombstone", e)
	}
}

func TestSetupRequestBootstrapOverLoopback(t *testing.T) {
	a, b := openLANTestCore(t), openLANTestCore(t)
	scope := SetupScope{InterfaceIndex: 1, Interface: "synthetic-loopback", LocalAddress: "127.0.0.1", Prefix: "127.0.0.0/8", linkBinding: "fixture"}
	scope.ScopeID = setupScopeID(scope)
	for _, c := range []*Core{a, b} {
		c.setupScopeRead = func() ([]SetupScope, error) { return []SetupScope{scope}, nil }
	}
	port := func() uint16 {
		for {
			listener, e := net.Listen("tcp", "127.0.0.1:0")
			if e != nil {
				if errors.Is(e, os.ErrPermission) {
					t.Skip("loopback TCP binding is denied in this environment")
				}
				t.Fatal(e)
			}
			p := uint16(listener.Addr().(*net.TCPAddr).Port)
			_ = listener.Close()
			if _, e = setupEndpoint(netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), p).String()); e == nil {
				return p
			}
		}
	}
	bootstrap, relay := port(), port()
	for relay == bootstrap {
		relay = port()
	}
	av := mustCommand(t, a, "setup.start", SetupStartInput{Alias: "Synthetic sender", ScopeID: scope.ScopeID, Consent: true, Mode: "outbound", Verification: "tofu", ExpiresInSeconds: 1200}).(SetupSnapshot)
	bv := mustCommand(t, b, "setup.start", SetupStartInput{Alias: "Synthetic receiver", ScopeID: scope.ScopeID, Consent: true, Mode: "receive", Verification: "tofu", ExpiresInSeconds: 1200, BootstrapPort: bootstrap, AllowHost: true, RelayPort: relay}).(SetupSnapshot)
	av = mustCommand(t, a, "setup.inspect-target", SetupInspectTargetInput{SessionID: av.SessionID, Address: bv.Contact.Bootstrap}).(SetupSnapshot)
	if av.Target == nil || av.Target.RelayAddress == "" || av.Target.ScopePrefix != scope.Prefix || av.Target.RelayCertificateSHA256 == "" {
		t.Fatal("consent view omitted exact relay/scope")
	}
	av = mustCommand(t, a, "setup.request", SetupRequestInput{TargetRevision: 1, SessionID: av.SessionID, TargetSessionID: av.Target.SessionID, ExpectedPeerKey: av.Target.PeerKey, TOFUAcknowledged: true}).(SetupSnapshot)
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		av = a.setupCopy().snapshot()
		bv = b.setupCopy().snapshot()
		if av.Plan != nil && bv.Plan != nil && av.LocalApproved && bv.RemoteApproved {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if av.Plan == nil || bv.Plan == nil || !av.LocalApproved || !bv.RemoteApproved || bv.LocalApproved || av.RemoteApproved {
		t.Fatal("real HTTP request/approval boundary did not converge", av.State, av.ReasonCode, bv.State, bv.ReasonCode)
	}
	sender := a.setupCopy()
	if sender.listener != nil || sender.discovery != nil || sender.server != nil {
		t.Fatal("outbound HTTP opened a listening owner")
	}
	if a.nodeCopy() != nil || b.nodeCopy() != nil || len(a.lanStoreCopy().copy().Trust.Peers) != 0 || len(b.lanStoreCopy().copy().Trust.Peers) != 0 {
		t.Fatal("one-sided approval activated pairing")
	}
	mustCommand(t, a, "setup.cancel", SetupSessionInput{SessionID: av.SessionID})
	bv = b.setupCopy().snapshot()
	if bv.Request != nil || bv.State != "receiving" || bv.RemoteApproved {
		t.Fatal("authenticated cancellation did not clear real receiver slot", bv.State, bv.ReasonCode)
	}
}

func TestSetupRequestCompletedSnapshotDoesNotResurrectRevokedKey(t *testing.T) {
	a, b, now := setupRequestTestSessions(t, "tofu")
	setupRequestTestPlan(t, a, b, now)
	c := openLANTestCore(t)
	if _, e := c.ensureLANIdentity(); e != nil {
		t.Fatal(e)
	}
	a.core = c
	a.paired, a.connected, a.state = true, true, "connected"
	if e := c.recordSetupRevocation(b.local.Key); e != nil {
		t.Fatal(e)
	}
	view := a.snapshot()
	if view.Paired || view.Connected || view.Trusted || view.State == "connected" || view.ReasonCode != "setup_peer_revoked" {
		t.Fatal("historical setup receipt overrode current revocation")
	}
}

func TestSetupRequestStaleTargetRevisionCannotApproveChangedMetadata(t *testing.T) {
	a, b, _ := setupRequestTestSessions(t, "tofu")
	old := a.targetViewLocked(*a.target)
	changed := *a.target
	relay := *changed.Relay
	relay.Address = netip.AddrPortFrom(relay.Address.Addr(), 54442)
	changed.Relay = &relay
	a.target = &changed
	a.targetRevision++
	_, e := a.requestPeer(SetupRequestInput{TargetRevision: old.Revision, TargetSessionID: b.local.SessionID, ExpectedPeerKey: b.local.Key, TOFUAcknowledged: true})
	if networkErrorCode(e) != "setup_target_changed" || a.requestConsent || a.localApproved || a.peer != nil {
		t.Fatal("stale view approved replaced same-key/session metadata", e)
	}
}

func TestSetupRequestExpiredApprovedSlotCannotDonateRemoteReceipts(t *testing.T) {
	a, b, now := setupRequestTestSessions(t, "tofu")
	a.local.Expires = now.Add(120 * time.Second)
	setupRequestTestPlan(t, a, b, now)
	b.localApproved = true
	b.approvalFrame, _ = lanlink.SealSetupMessage(b.identity, *b.plan, lanlink.SetupActionApprove, nil, now.Add(4*time.Second))
	setupTestExchange(t, b, a, false, now.Add(4*time.Second))
	a.pairedFrame, _ = lanlink.SealSetupMessage(a.identity, *a.plan, lanlink.SetupActionPaired, nil, now.Add(4*time.Second))
	a.connectedFrame, _ = lanlink.SealSetupMessage(a.identity, *a.plan, lanlink.SetupActionConnectedReceipt, nil, now.Add(4*time.Second))
	setupTestExchange(t, a, b, true, now.Add(4*time.Second))
	if !b.localApproved || !b.remoteApproved || !b.remotePaired || !b.remoteConnected || b.commitStarted {
		t.Fatal("test did not reach approved pre-commit receipt boundary")
	}
	oldID := b.requestID
	b.expireRequestLocked(a.local.Expires)
	if b.peer != nil || b.selected != nil || b.selectedManual || b.candidateID != "" || b.target != nil || b.hello != nil || b.helloProof != nil || b.peerHello || b.plan != nil || b.planProof != nil || b.peerPlan || b.localApproved || b.remoteApproved || b.approvalFrame != nil || b.requestConsent || b.requestID != "" || b.invitation != nil || b.invitationFrame != nil || b.invitationBinding != ([32]byte{}) || b.pairedFrame != nil || b.connectedFrame != nil || b.remotePaired || b.remoteConnected || b.paired || b.connected || b.localConnected || b.trusted || b.commitStarted || b.pairingAttempted || b.promoted {
		t.Fatal("expired request retained consent, proof or receipt state")
	}
	if _, ok := b.rejected[oldID]; !ok {
		t.Fatal("expired request was not tombstoned")
	}
	fresh, _, _ := setupRequestTestSessions(t, "tofu")
	fresh.local.SessionID = strings.Repeat("c", 64)
	fresh.local.Expires = b.local.Expires
	fresh.target = &b.local
	setupRequestTestPlan(t, fresh, b, now)
	if b.remotePaired || b.remoteConnected || b.localApproved || b.requestID == oldID {
		t.Fatal("fresh request inherited old approval or success")
	}
	b.paired, b.localConnected, b.localApproved, b.remoteApproved = true, true, true, true
	b.checkConnected()
	if b.connected {
		t.Fatal("new request completed without its own remote receipts")
	}
}

func TestSetupRequestRequiresPositiveExactSafeTargetRevision(t *testing.T) {
	a, b, _ := setupRequestTestSessions(t, "tofu")
	for _, revision := range []uint64{0, 2, setupMaxGeneration + 1} {
		if _, err := a.requestPeer(SetupRequestInput{TargetRevision: revision, TargetSessionID: b.local.SessionID, ExpectedPeerKey: b.local.Key, TOFUAcknowledged: true}); networkErrorCode(err) != "setup_target_changed" {
			t.Fatal("invalid preflight revision accepted", err)
		}
	}
}

func TestSetupRequestCanRejectExactAuthenticatedSlotBeforePlanProof(t *testing.T) {
	a, b, now := setupRequestTestSessions(t, "tofu")
	if _, err := a.requestPeer(SetupRequestInput{TargetRevision: 1, TargetSessionID: b.local.SessionID, ExpectedPeerKey: b.local.Key, TOFUAcknowledged: true}); err != nil {
		t.Fatal(err)
	}
	w, err := a.wireLocked(now)
	if err != nil {
		t.Fatal(err)
	}
	if err = b.admitRequestLocked(w, a.local.Scope.LocalAddress, now); err != nil {
		t.Fatal(err)
	}
	if b.plan != nil || b.peerPlan || b.localApproved {
		t.Fatal("fixture already has plan consent")
	}
	if _, err = b.rejectRequest(SetupRejectInput{RequestID: strings.Repeat("f", 64)}); err == nil {
		t.Fatal("wrong request rejected")
	}
	if _, err = b.rejectRequest(SetupRejectInput{RequestID: a.local.SessionID}); err != nil {
		t.Fatal("pre-plan request could not be declined", err)
	}
	if b.peer != nil || b.requestID != "" || b.state != "receiving" {
		t.Fatal("pre-plan decline kept slot")
	}
	if err = b.admitRequestLocked(w, a.local.Scope.LocalAddress, now); err == nil {
		t.Fatal("declined pre-plan request replayed")
	}
	if rejected := b.rejected[a.local.SessionID]; len(rejected.Rejection) != 0 {
		t.Fatal("claimed a signed rejection without a shared plan")
	}
}
