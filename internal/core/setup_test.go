package core

import (
	"bytes"
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

	"github.com/webkaz-labs/sobalink/internal/lanlink"
)

func setupTestSessions(t *testing.T) (*setupSession, *setupSession, time.Time) {
	t.Helper()
	now := time.Now().UTC()
	makeSession := func(label, address string) *setupSession {
		id := lanlink.GenerateIdentity()
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		ip := netip.MustParseAddr(address)
		p := lanlink.SetupParticipant{SessionID: strings.Repeat(label, 64), Key: id.PublicKey(), Alias: "peer-" + label, Bootstrap: netip.AddrPortFrom(ip, 54440), Scope: lanlink.SetupScope{LocalAddress: ip, Prefix: netip.MustParsePrefix("192.168.50.0/24"), ConsentRevision: strings.Repeat(label, 64)}, AllowHost: true, Relay: &lanlink.TrustedRelay{Address: netip.AddrPortFrom(ip, 54441), CertificateSHA256: strings.Repeat(label, 64)}, Expires: now.Add(90 * time.Second)}
		return &setupSession{ctx: ctx, cancel: cancel, identity: id, local: p, state: "authenticating", scope: SetupScope{LocalAddress: address, Prefix: p.Scope.Prefix.String()}}
	}
	a, b := makeSession("a", "192.168.50.10"), makeSession("b", "192.168.50.20")
	contact := func(s *setupSession) *SetupContact {
		return &SetupContact{Kind: "sobalink-setup-contact", Version: 1, SessionID: s.local.SessionID, Alias: s.local.Alias, Bootstrap: s.local.Bootstrap.String(), ExpiresAt: s.local.Expires}
	}
	a.selected, b.selected = contact(b), contact(a)
	a.selectedManual, b.selectedManual = true, true
	return a, b, now
}
func setupTestExchange(t *testing.T, from, to *setupSession, inbound bool, now time.Time) {
	t.Helper()
	w, e := from.wireLocked(now)
	if e != nil {
		t.Fatal(e)
	}
	if e = to.acceptWireLocked(w, inbound, now); e != nil {
		t.Fatal(e)
	}
}
func setupTestPlan(t *testing.T, a, b *setupSession, now time.Time) {
	t.Helper()
	setupTestExchange(t, a, b, true, now)
	setupTestExchange(t, b, a, false, now)
	setupTestExchange(t, a, b, true, now)
	setupTestExchange(t, b, a, true, now)
	if e := a.electLocked(now.Add(4 * time.Second)); e != nil {
		t.Fatal(e)
	}
	setupTestExchange(t, a, b, false, now.Add(4*time.Second))
	setupTestExchange(t, b, a, true, now.Add(4*time.Second))
	if a.plan == nil || b.plan == nil || !a.peerPlan || !b.peerPlan {
		t.Fatal("plan proof missing")
	}
}

func TestSetupMalformedFirstParticipantDoesNotPublishPartialState(t *testing.T) {
	a, b, now := setupTestSessions(t)
	w, _ := b.wireLocked(now)
	w.Participant.Scope.Prefix = netip.MustParsePrefix("192.168.50.20/32")
	if e := a.acceptWireLocked(w, false, now); e == nil {
		t.Fatal("accepted non-mutual scope")
	}
	if a.peer != nil || a.hello != nil || len(a.helloProof) > 0 {
		t.Fatal("published partial hello state")
	}
	w.HelloProof = []byte("invalid")
	if e := a.acceptWireLocked(w, false, now); e == nil {
		t.Fatal("accepted malformed proof")
	}
	setupTestPlan(t, a, b, now)
}
func TestSetupBilateralApprovalAndImmutablePlan(t *testing.T) {
	a, b, now := setupTestSessions(t)
	setupTestPlan(t, a, b, now)
	if a.localApproved || a.remoteApproved || b.localApproved || b.remoteApproved {
		t.Fatal("key proof approved local pairing")
	}
	a.localApproved = true
	a.approvalFrame, _ = lanlink.SealSetupMessage(a.identity, *a.plan, lanlink.SetupActionApprove, nil, now.Add(4*time.Second))
	setupTestExchange(t, a, b, true, now.Add(4*time.Second))
	if !b.remoteApproved || b.localApproved || a.remoteApproved {
		t.Fatal("remote approval changed local approval")
	}
	old := *a.plan
	changed := old
	changed.Revision++
	w, _ := b.wireLocked(now)
	w.Plan = &changed
	if a.acceptWireLocked(w, true, now.Add(4*time.Second)) == nil {
		t.Fatal("accepted changed plan under old proof")
	}
	if a.plan.Revision != old.Revision {
		t.Fatal("changed locked plan")
	}
	for i := 0; i < 3; i++ {
		setupTestExchange(t, a, b, true, now.Add(4*time.Second))
	}
	if b.localApproved {
		t.Fatal("duplicate upgraded local permission")
	}
}
func TestSetupFullFingerprintAndStatusContainNoCapability(t *testing.T) {
	a, b, now := setupTestSessions(t)
	setupTestPlan(t, a, b, now)
	a.invitation = &lanlink.Invitation{Token: "synthetic-secret-token"}
	a.invitationFrame = []byte("synthetic-secret-frame")
	view := a.snapshot()
	raw, e := json.Marshal(view)
	if e != nil {
		t.Fatal(e)
	}
	if bytes.Contains(raw, []byte("synthetic-secret")) || bytes.Contains(raw, []byte("private")) {
		t.Fatal("status exposed capability")
	}
	if view.Plan == nil || len(view.Plan.Fingerprint) != 64 || view.Plan.VerificationText != setupVerificationPrefix+view.Plan.Fingerprint || len(view.Plan.VerificationQR) == 0 {
		t.Fatal("missing full verification exchange")
	}
	if view.Trusted || view.Paired || view.Connected {
		t.Fatal("plan display claimed trust/connection")
	}
}
func TestSetupElectionUsesOnlyObservedConsentedDirection(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		in, out, allowA, allowB bool
		want                    string
	}{{"both", true, true, true, true, "a"}, {"only-a", true, false, true, true, "a"}, {"only-b", false, true, true, true, "b"}, {"no-host-consent", true, true, false, false, ""}, {"unknown", false, false, true, true, ""}} {
		t.Run(tc.name, func(t *testing.T) {
			a, b, now := setupTestSessions(t)
			a.local.AllowHost = tc.allowA
			b.local.AllowHost = tc.allowB
			setupTestExchange(t, a, b, true, now)
			setupTestExchange(t, b, a, false, now)
			a.inbound, a.outbound = tc.in, tc.out
			if e := a.electLocked(now.Add(4 * time.Second)); e != nil {
				t.Fatal(e)
			}
			if tc.want == "" {
				if a.plan != nil || a.state != "cancelling" || a.terminalState != "failed" {
					t.Fatal("invented reachable permitted host")
				}
				return
			}
			want := a.local.Key
			if tc.want == "b" {
				want = b.local.Key
			}
			if a.plan == nil || a.plan.HostKey != want {
				t.Fatal("wrong host")
			}
		})
	}
}
func TestSetupReflectionAndSessionMismatchDoNotApprove(t *testing.T) {
	a, b, now := setupTestSessions(t)
	setupTestPlan(t, a, b, now)
	frame, _ := lanlink.SealSetupMessage(a.identity, *a.plan, lanlink.SetupActionApprove, nil, now.Add(4*time.Second))
	w, _ := b.wireLocked(now)
	w.Approval = frame
	if a.acceptWireLocked(w, true, now.Add(4*time.Second)) == nil || a.remoteApproved {
		t.Fatal("reflected own approval")
	}
	w.Approval = nil
	w.TargetSession = strings.Repeat("c", 64)
	if a.acceptWireLocked(w, true, now.Add(4*time.Second)) == nil {
		t.Fatal("accepted wrong target session")
	}
}
func TestSetupContactCanonicalBoundedExpiredAndUntrusted(t *testing.T) {
	a, _, now := setupTestSessions(t)
	contact := SetupContact{Kind: "sobalink-setup-contact", Version: 1, SessionID: a.local.SessionID, Alias: "Synthetic peer", Bootstrap: a.local.Bootstrap.String(), ExpiresAt: a.local.Expires}
	text, e := EncodeSetupContact(contact)
	if e != nil {
		t.Fatal(e)
	}
	got, e := InspectSetupContact(text, now)
	if e != nil || got != contact {
		t.Fatal("roundtrip", e)
	}
	if _, e = InspectSetupContact(text, contact.ExpiresAt); e == nil {
		t.Fatal("expired contact accepted")
	}
	for _, bad := range []string{strings.Repeat("x", maxSetupContact+1), setupContactPrefix + "=", text + "x"} {
		if _, e = InspectSetupContact(bad, now); e == nil {
			t.Fatal("malformed contact accepted")
		}
	}
	contact.Bootstrap = "127.0.0.1:54544"
	if _, e = EncodeSetupContact(contact); e == nil {
		t.Fatal("reserved endpoint accepted")
	}
}
func TestSetupScopeEnumerationIsBoundedPrivateAndReadOnly(t *testing.T) {
	interfaces := func() ([]net.Interface, error) {
		return []net.Interface{{Index: 7, Name: "fixture", HardwareAddr: net.HardwareAddr{2, 0, 0, 0, 0, 1}, Flags: net.FlagUp}}, nil
	}
	addresses := func(net.Interface) ([]net.Addr, error) {
		return []net.Addr{localTestAddress("192.168.50.10/24"), localTestAddress("192.168.50.10/0"), localTestAddress("192.0.2.10/24"), localTestAddress("127.0.0.1/8")}, nil
	}
	scopes, e := readSetupScopes(interfaces, addresses, false)
	if e != nil || len(scopes) != 1 {
		t.Fatal(scopes, e)
	}
	s := scopes[0]
	changed := s
	changed.linkBinding = "different"
	if setupScopeID(s) == setupScopeID(changed) {
		t.Fatal("interface instance not bound")
	}
	c := openLANTestCore(t)
	c.setupScopeRead = func() ([]SetupScope, error) { return scopes, nil }
	before, _ := os.ReadFile(filepath.Join(c.dir, "sobalink.json"))
	value, e := command(c, randomID(), "setup.scopes", map[string]any{})
	if e != nil || len(value.(SetupScopesView).Scopes) != 1 {
		t.Fatal(e)
	}
	after, _ := os.ReadFile(filepath.Join(c.dir, "sobalink.json"))
	if !bytes.Equal(before, after) || c.lanStoreCopy() != nil || c.nodeCopy() != nil {
		t.Fatal("scope read changed authority")
	}
}
func TestSetupRestartAndMissingConsentOpenNoListener(t *testing.T) {
	c := openLANTestCore(t)
	c.attemptedNetwork = "tailnet"
	v, e := command(c, randomID(), "setup.start", SetupStartInput{})
	if e != nil || v.(SetupSnapshot).State != "stop_required" || c.setupCopy() != nil || c.lanStoreCopy() != nil {
		t.Fatal("restart boundary bypassed", v, e)
	}
	c.attemptedNetwork = ""
	if _, e = command(c, randomID(), "setup.start", SetupStartInput{}); networkErrorCode(e) != "setup_consent_required" || c.setupCopy() != nil {
		t.Fatal("missing consent opened setup", e)
	}
}

type setupFakeDiscovery struct {
	candidate SetupCandidate
	invalid   bool
	closeErr  error
}

func (d *setupFakeDiscovery) Candidates() []SetupCandidate { return []SetupCandidate{d.candidate} }
func (d *setupFakeDiscovery) Lookup(id string) (SetupCandidate, error) {
	if d.invalid || id != d.candidate.CandidateID {
		return SetupCandidate{}, errors.New("changed")
	}
	return d.candidate, nil
}
func (d *setupFakeDiscovery) Close() error { return d.closeErr }
func (*setupFakeDiscovery) Err() error     { return nil }
func TestSetupCandidateLookupNeverRevivesChangedSelection(t *testing.T) {
	a, _, now := setupTestSessions(t)
	d := &setupFakeDiscovery{candidate: SetupCandidate{CandidateID: strings.Repeat("c", 64), Contact: *a.selected}}
	a.discovery = d
	a.selectedManual = false
	a.candidateID = d.candidate.CandidateID
	if !a.selectionCurrentLocked(now) {
		t.Fatal("fresh candidate missing")
	}
	d.invalid = true
	if a.selectionCurrentLocked(now) {
		t.Fatal("stale candidate survived")
	}
	d.invalid = false
	d.candidate.CandidateID = strings.Repeat("d", 64)
	if a.selectionCurrentLocked(now) {
		t.Fatal("changed candidate revived old ID")
	}
}
func TestSetupDiscoveryFailedOpenRetainsOwnerAndRecoveryFence(t *testing.T) {
	c := openLANTestCore(t)
	s, _, _ := setupTestSessions(t)
	s.core = c
	d := &setupFakeDiscovery{closeErr: errors.New("synthetic close uncertainty")}
	s.adoptDiscovery(d, d.closeErr)
	if s.discovery != d || s.ctx.Err() == nil || s.cleanupFailure == nil || c.setupProcess().State != "needs_restart" {
		t.Fatal("failed discovery open was downgraded to ordinary unavailable")
	}
	s.cleanup()
	if s.state != "recovery_required" || s.discoveryStopped || s.discovery != d {
		t.Fatal("unconfirmed discovery close lost owner or claimed stopped")
	}
}
func TestSetupDiscoveryUnsupportedLeavesManualPathAvailable(t *testing.T) {
	s, _, _ := setupTestSessions(t)
	s.adoptDiscovery(nil, errors.New("synthetic unsupported adapter"))
	if s.ctx.Err() != nil || s.cleanupFailure != nil || s.discoveryReason != "setup_discovery_unavailable" {
		t.Fatal("ordinary unavailable discovery cancelled manual setup")
	}
}
func TestSetupCancelAfterPotentialCommitReportsRecovery(t *testing.T) {
	a, _, _ := setupTestSessions(t)
	a.pairingAttempted = true
	a.stop("cancelled", "", "try_again")
	if a.state != "cancelling" || a.terminalState != "recovery_required" {
		t.Fatal("cancel claimed rollback")
	}
	b, _, _ := setupTestSessions(t)
	b.commitStarted = true
	b.stop("cancelled", "", "try_again")
	if b.state != "cancelling" || b.terminalState != "cancelled" {
		t.Fatal("activation alone reported remote commit")
	}
}

func TestSetupOwnPairAdditionAllowanceRejectsUnrelatedAuthority(t *testing.T) {
	a, b, _ := setupTestSessions(t)
	peer := lanlink.Peer{Key: b.local.Key, Name: b.local.Alias}
	before := lanState{Version: 1, Identity: a.identity, Trust: lanlink.Snapshot{Version: 1, Peers: []lanlink.Peer{}}, Remotes: []lanlink.RemotePeer{}}
	after := cloneLANState(before)
	after.Trust.Peers = []lanlink.Peer{peer}
	after.Remotes = []lanlink.RemotePeer{{Peer: peer}}
	if !setupOnlyPairAdded(before, after, peer.Key) {
		t.Fatal("own exact pair addition rejected")
	}
	if setupOnlyPairAdded(after, before, peer.Key) || setupOnlyPairAdded(after, after, peer.Key) {
		t.Fatal("revocation/re-add normalized")
	}
	other := cloneLANState(after)
	other.Version = 2
	if setupOnlyPairAdded(before, other, peer.Key) {
		t.Fatal("unrelated state change normalized")
	}
	other = cloneLANState(after)
	other.Trust.Peers[0].Key = a.local.Key
	if setupOnlyPairAdded(before, other, peer.Key) {
		t.Fatal("another peer substituted")
	}
}
func TestSetupAuthorityMutationCancelsWithoutBlockingRevocation(t *testing.T) {
	c := openLANTestCore(t)
	s, _, _ := setupTestSessions(t)
	s.core = c
	c.mu.Lock()
	c.setup = s
	c.mu.Unlock()
	c.invalidateSetupAuthority("lan.revoke")
	if s.ctx.Err() == nil || s.state != "cancelling" || s.terminalState != "cancelled" {
		t.Fatal("revocation retained old approval")
	}
	c.mu.Lock()
	c.setup = nil
	c.mu.Unlock()
}

type setupCloseFailureBackend struct{ *lanBackend }

func (*setupCloseFailureBackend) Close() error { return errors.New("synthetic close uncertainty") }
func TestSetupCleanupPreservesBorrowedBackendAndRetainsFailedOwner(t *testing.T) {
	t.Run("borrowed", func(t *testing.T) {
		c := openLANTestCore(t)
		s, _, _ := setupTestSessions(t)
		node, fake := testLANBackend()
		s.core = c
		s.borrowedNode = node
		s.terminalState = "cancelled"
		c.mu.Lock()
		c.node = node
		c.mu.Unlock()
		s.cleanup()
		if fake.closed || c.nodeCopy() != node || s.state != "cancelled" {
			t.Fatal("setup closed borrowed backend")
		}
	})
	t.Run("failed-owned-close", func(t *testing.T) {
		c := openLANTestCore(t)
		s, _, _ := setupTestSessions(t)
		node, _ := testLANBackend()
		failing := &setupCloseFailureBackend{node}
		s.core = c
		s.ownedNode = failing
		s.terminalState = "cancelled"
		c.mu.Lock()
		c.node = failing
		c.mu.Unlock()
		s.cleanup()
		if c.nodeCopy() != failing || c.networkFatal == "" || s.state != "recovery_required" || c.setupProcess().State != "needs_restart" {
			t.Fatal("lost uncertain owner fence")
		}
	})
}
func TestSetupCancellationStaysPendingUntilHandlerJoined(t *testing.T) {
	c := openLANTestCore(t)
	s, _, _ := setupTestSessions(t)
	s.core = c
	s.done = make(chan struct{})
	s.handlerWG.Add(1)
	s.stop("cancelled", "", "try_again")
	if s.state != "cancelling" {
		t.Fatal("cancellation skipped cleanup state")
	}
	finished := make(chan struct{})
	go func() { s.cleanup(); close(finished) }()
	select {
	case <-finished:
		t.Fatal("cleanup claimed success while handler active")
	case <-time.After(20 * time.Millisecond):
	}
	s.handlerWG.Done()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("cleanup did not join handler")
	}
	if s.state != "cancelled" {
		t.Fatal("joined cleanup not terminal")
	}
}

type setupAcceptFixture struct {
	conn   net.Conn
	closed bool
}

func (l *setupAcceptFixture) Accept() (net.Conn, error) {
	if l.conn == nil {
		return nil, net.ErrClosed
	}
	c := l.conn
	l.conn = nil
	return c, nil
}
func (l *setupAcceptFixture) Close() error { l.closed = true; return nil }
func (*setupAcceptFixture) Addr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 54440}
}
func TestSetupNativeAdmissionBudgetCountsBeforeHTTPParsing(t *testing.T) {
	a, _, _ := setupTestSessions(t)
	left, right := net.Pipe()
	defer right.Close()
	l := &setupAcceptFixture{conn: left}
	a.acceptedConnections = maxSetupRequests
	admission := &setupAdmissionListener{Listener: l, session: a}
	if _, e := admission.Accept(); !errors.Is(e, net.ErrClosed) {
		t.Fatal("exhausted native admission was accepted")
	}
	if !l.closed || a.ctx.Err() == nil || a.state != "cancelling" || a.terminalReason != "setup_receive_budget" {
		t.Fatal("native budget exhaustion did not close session")
	}
}
func TestSetupPinnedPlanNoLongerUsesAdvertisementAsAuthority(t *testing.T) {
	a, b, now := setupTestSessions(t)
	setupTestPlan(t, a, b, now)
	a.selectedManual = false
	a.discovery = &setupFakeDiscovery{invalid: true}
	a.localApproved = true
	if a.selectionCurrentLocked(now.Add(4 * time.Second)) {
		t.Fatal("one-sided approval bypassed candidate freshness")
	}
	a.remoteApproved = true
	if !a.selectionCurrentLocked(now.Add(4 * time.Second)) {
		t.Fatal("mutually verified plan was not pinned")
	}
	if a.selectionCurrentLocked(a.plan.Expires) {
		t.Fatal("pinned selection extended original deadline")
	}
}
func TestSetupStatusRejectsLostProcessSession(t *testing.T) {
	c := openLANTestCore(t)
	if _, e := command(c, randomID(), "setup.status", SetupSessionInput{SessionID: strings.Repeat("a", 64)}); networkErrorCode(e) != "setup_session_changed" {
		t.Fatal("lost session substituted idle result", e)
	}
}
func TestSetupBorrowedAnchorNeverBroadensHostPinOrScope(t *testing.T) {
	a, b, now := setupTestSessions(t)
	setupTestPlan(t, a, b, now)
	node, _ := testLANBackend()
	a.borrowedNode = node
	a.borrowedAnchor = a.plan.Relay
	if !a.validHost(*a.plan) {
		t.Fatal("exact existing anchor rejected")
	}
	changed := *a.plan
	changed.Relay.CertificateSHA256 = strings.Repeat("c", 64)
	if a.validHost(changed) {
		t.Fatal("changed existing pin accepted")
	}
	changed = *a.plan
	changed.Relay.Address = netip.MustParseAddrPort("192.168.51.10:54441")
	if a.validHost(changed) {
		t.Fatal("changed anchor scope accepted")
	}
}

func TestSetupCompletedPairDoesNotEraseCleanupFailure(t *testing.T) {
	c := openLANTestCore(t)
	s, _, _ := setupTestSessions(t)
	s.core = c
	s.connected = true
	s.promoted = true
	s.stop("recovery_required", "setup_cleanup_unconfirmed", "review_recovery")
	if s.terminalState != "recovery_required" || s.cleanupFailure == nil {
		t.Fatal("completed pair erased first cleanup failure")
	}
	s.cleanup()
	if s.state != "recovery_required" || s.reason != "setup_cleanup_unconfirmed" || c.networkFatal == "" {
		t.Fatal("retry erased cleanup fence")
	}
}

func TestSetupNewSessionActionRequiresJoinedTemporaryOwners(t *testing.T) {
	c := openLANTestCore(t)
	s, _, _ := setupTestSessions(t)
	s.core, s.done = c, make(chan struct{})
	s.state, s.next, s.connected = "connected", "done", true
	if s.snapshot().NextAction != "done" {
		t.Fatal("new-session action appeared while receipt listener was owned")
	}
	close(s.done)
	if s.snapshot().NextAction != "start_new_setup" || !s.snapshot().Connected {
		t.Fatal("joined result did not permit deliberate fresh setup")
	}
	c.networkFatal = "setup_cleanup_unconfirmed"
	if s.snapshot().NextAction == "start_new_setup" {
		t.Fatal("cleanup fence permitted new-session action")
	}
}

func TestSetupUncertainAuthorityBlocksNewSession(t *testing.T) {
	c := openLANTestCore(t)
	c.lanStartUncertain.Store(true)
	if c.setupProcess().State != "needs_restart" {
		t.Fatal("uncertain profile authority permitted setup")
	}
	c.lanStartUncertain.Store(false)
	store, e := c.ensureLANIdentity()
	if e != nil {
		t.Fatal(e)
	}
	store.mu.Lock()
	store.startUncertain = true
	store.mu.Unlock()
	if c.setupProcess().State != "needs_restart" {
		t.Fatal("uncertain identity authority permitted setup")
	}
}

func TestSetupNoHostRetryRequiresCleanJoinedSession(t *testing.T) {
	newFailure := func() *setupSession {
		s, _, _ := setupTestSessions(t)
		s.done = make(chan struct{})
		s.state, s.reason, s.next = "failed", "setup_no_permitted_host", "review_existing_network"
		return s
	}
	s := newFailure()
	if v := s.snapshot(); v.State != "cancelling" || v.NextAction != "wait_for_cleanup" {
		t.Fatal("exposed terminal no-host before join", v.State, v.NextAction)
	}
	close(s.done)
	if v := s.snapshot(); v.State != "failed" || v.ReasonCode != "setup_no_permitted_host" || v.NextAction != "try_again" {
		t.Fatal("clean joined result cannot reopen review", v.State, v.NextAction)
	}
	for name, block := range map[string]func(*setupSession){
		"other-reason":    func(s *setupSession) { s.reason = "setup_existing_anchor_conflict" },
		"plan":            func(s *setupSession) { s.plan = &lanlink.SetupPlan{} },
		"local-approval":  func(s *setupSession) { s.localApproved = true },
		"remote-approval": func(s *setupSession) { s.remoteApproved = true },
		"pairing-attempt": func(s *setupSession) { s.pairingAttempted = true },
		"paired":          func(s *setupSession) { s.paired = true },
		"connected":       func(s *setupSession) { s.connected = true },
		"cleanup-error":   func(s *setupSession) { s.cleanupFailure = errors.New("synthetic cleanup failure") },
		"recovery":        func(s *setupSession) { s.state = "recovery_required"; s.next = "review_recovery" },
		"process-fence":   func(s *setupSession) { s.core = openLANTestCore(t); s.core.networkFatal = "setup_cleanup_unconfirmed" },
	} {
		t.Run(name, func(t *testing.T) {
			s := newFailure()
			close(s.done)
			block(s)
			if v := s.snapshot(); v.NextAction == "try_again" {
				t.Fatal("unsafe retry", name)
			}
		})
	}
}

func TestSetupBothDeclineNoHostDoesNotGrantRole(t *testing.T) {
	a, b, now := setupTestSessions(t)
	for _, s := range []*setupSession{a, b} {
		s.local.AllowHost = false
		s.local.Relay = nil
		s.done = make(chan struct{})
	}
	setupTestExchange(t, a, b, true, now)
	setupTestExchange(t, b, a, false, now)
	setupTestExchange(t, a, b, true, now)
	setupTestExchange(t, b, a, true, now)
	if err := a.electLocked(now.Add(4 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if a.state != "cancelling" || a.terminalState != "failed" || a.terminalReason != "setup_no_permitted_host" {
		t.Fatal("missing bounded no-host failure", a.state, a.terminalReason)
	}
	for _, s := range []*setupSession{a, b} {
		if s.local.AllowHost || s.local.Relay != nil || s.plan != nil || s.localApproved || s.remoteApproved || s.pairingAttempted {
			t.Fatal("declined role acquired authority")
		}
	}
	// The owner publishes its terminal result only after cleanup; the public
	// projection still waits for the final join before offering fresh review.
	a.state, a.reason, a.next = a.terminalState, a.terminalReason, a.terminalNext
	if v := a.snapshot(); v.State != "cancelling" || v.NextAction != "wait_for_cleanup" {
		t.Fatal(v.State, v.NextAction)
	}
	close(a.done)
	if v := a.snapshot(); v.NextAction != "try_again" || v.Paired || v.Connected {
		t.Fatal(v.State, v.NextAction)
	}
}
