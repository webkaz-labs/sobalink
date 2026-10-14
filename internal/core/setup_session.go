package core

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"reflect"
	"sync"
	"time"

	qrcode "github.com/skip2/go-qrcode"
	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/lanlink"
)

// The owner is process-local: neither a restart nor a reused display name can
// restore consent. c.op serializes local management; mu serializes wire state.
type setupSession struct {
	core                                        *Core
	ctx                                         context.Context
	cancel                                      context.CancelFunc
	done                                        chan struct{}
	mu                                          sync.Mutex
	scope                                       SetupScope
	local                                       lanlink.SetupParticipant
	identity                                    lanlink.Identity
	relayIdentity                               *lanlink.RelayIdentity
	contact                                     SetupContact
	contactText                                 string
	contactQR                                   [][]bool
	selected                                    *SetupContact
	selectedManual                              bool
	candidateID                                 string
	peer                                        *lanlink.SetupParticipant
	hello                                       *lanlink.SetupHello
	helloProof                                  []byte
	peerHello                                   bool
	inbound, outbound                           bool
	authenticatedAt                             time.Time
	plan                                        *lanlink.SetupPlan
	planProof                                   []byte
	peerPlan                                    bool
	localApproved, remoteApproved               bool
	approvalFrame                               []byte
	invitation                                  *lanlink.Invitation
	invitationFrame                             []byte
	pairedFrame, connectedFrame                 []byte
	remotePaired, remoteConnected               bool
	paired, connected, localConnected           bool
	trusted                                     bool
	state, reason, next                         string
	discovery                                   setupDiscovery
	discoveryReason                             string
	listener                                    net.Listener
	server                                      *http.Server
	requests                                    int
	ownedNode                                   lanNetworkBackend
	borrowedNode                                lanNetworkBackend
	promoted                                    bool
	commitStarted                               bool
	pairingAttempted                            bool
	review                                      string
	authority                                   lanState
	authorityProfile                            string
	serveDone                                   chan struct{}
	handlerMu                                   sync.Mutex
	handlerWG                                   sync.WaitGroup
	handlerClosed                               bool
	borrowedAnchor                              lanlink.TrustedRelay
	terminalState, terminalReason, terminalNext string
	acceptedConnections                         int
	discoveryStopped                            bool
	cleanupFailure                              error
}

type setupDiscovery interface {
	Candidates() []SetupCandidate
	Lookup(string) (SetupCandidate, error)
	Close() error
	Err() error
}

func (c *Core) setupCopy() *setupSession { c.mu.RLock(); defer c.mu.RUnlock(); return c.setup }
func (c *Core) setupReview() string {
	var state any
	if store := c.lanStoreCopy(); store != nil {
		state = store.copy()
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return privateRevision(struct {
		LAN                 any
		Profile             Profile
		Attempted, Hostname string
	}{state, c.profile, c.attemptedNetwork, c.attemptedHostname})
}
func setupIdle() SetupSnapshot {
	return SetupSnapshot{Kind: "sobalink-setup", Version: 1, State: "idle", NextAction: "choose_scope", Candidates: []SetupCandidate{}, Process: SetupProcessView{State: "ready", ProfilePreserved: true}, Discovery: SetupDiscoveryView{State: "stopped"}}
}
func (c *Core) setupProcess() SetupProcessView {
	c.mu.RLock()
	defer c.mu.RUnlock()
	state := "ready"
	if c.closing || c.ctx.Err() != nil || c.networkFatal != "" || c.networkState == "error" || c.lanStartUncertain.Load() || c.lan != nil && setupLANNeedsRecovery(c.lan) {
		state = "needs_restart"
	} else if c.attemptedNetwork != "" && c.attemptedNetwork != "lan" || c.node != nil && c.profile.Settings.Network != "lan" {
		state = "stop_required"
	}
	return SetupProcessView{State: state, ProfilePreserved: true}
}
func (c *Core) setupCommand(ctx context.Context, name string, raw json.RawMessage) (any, error) {
	switch name {
	case "setup.scopes":
		var in struct{}
		if e := decodePayload(raw, &in); e != nil {
			return nil, e
		}
		scopes, e := c.setupScopes()
		if e != nil {
			return nil, e
		}
		return SetupScopesView{Kind: "sobalink-setup-scopes", Version: 1, Scopes: scopes}, nil
	case "setup.start":
		var in SetupStartInput
		if e := decodePayload(raw, &in); e != nil {
			return nil, e
		}
		return c.startSetup(in)
	case "setup.status":
		var in SetupSessionInput
		if e := decodePayload(raw, &in); e != nil {
			return nil, e
		}
		s := c.setupCopy()
		if s == nil {
			if in.SessionID != "" {
				return nil, setupError("setup_session_changed", "setup session no longer exists in this process; start a fresh session")
			}
			v := setupIdle()
			v.Process = c.setupProcess()
			if v.Process.State != "ready" {
				v.State = v.Process.State
				v.NextAction = "restart_offline"
			}
			return v, nil
		}
		if in.SessionID != "" && in.SessionID != s.local.SessionID {
			return nil, setupError("setup_session_changed", "setup session changed; refresh the current session")
		}
		return s.snapshot(), nil
	case "setup.select":
		var in SetupSelectInput
		if e := decodePayload(raw, &in); e != nil {
			return nil, e
		}
		s, e := c.requireSetup(in.SessionID)
		if e != nil {
			return nil, e
		}
		s.mu.Lock()
		d := s.discovery
		s.mu.Unlock()
		if d == nil {
			return nil, setupError("setup_discovery_unavailable", "nearby discovery is unavailable; exchange current setup contact text or QR")
		}
		candidate, e := d.Lookup(in.CandidateID)
		if e != nil {
			return nil, setupError("setup_candidate_expired", "nearby candidate changed or expired; select it again")
		}
		return s.selectPeer(candidate.Contact, in.CandidateID, false)
	case "setup.manual":
		var in SetupManualInput
		if e := decodePayload(raw, &in); e != nil {
			return nil, e
		}
		s, e := c.requireSetup(in.SessionID)
		if e != nil {
			return nil, e
		}
		contact, e := InspectSetupContact(in.ContactText, time.Now())
		if e != nil {
			return nil, e
		}
		return s.selectPeer(contact, "", true)
	case "setup.approve":
		var in SetupApproveInput
		if e := decodePayload(raw, &in); e != nil {
			return nil, e
		}
		s, e := c.requireSetup(in.SessionID)
		if e != nil {
			return nil, e
		}
		if _, e = c.setupScope(s.scope.ScopeID); e != nil {
			s.stop("cancelled", "setup_scope_changed", "choose_scope")
			return nil, e
		}
		if c.setupReview() != s.review {
			return nil, setupError("setup_state_changed", "saved identity or permissions changed; cancel and review a fresh setup session")
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if !s.selectionCurrentLocked(time.Now()) {
			return nil, setupError("setup_candidate_expired", "selected contact changed or expired; cancel and select it again")
		}
		if s.plan == nil || !s.peerPlan || !in.Verified || s.ctx.Err() != nil {
			return nil, setupError("setup_verification_required", "compare the complete setup fingerprint using the other device's QR or copied text before approving")
		}
		digest, _ := s.plan.Fingerprint()
		if in.Revision != s.plan.Revision || in.Fingerprint != digest {
			return nil, setupError("setup_plan_changed", "connection plan changed; review and compare the current full fingerprint")
		}
		if s.localApproved {
			return s.snapshotLocked(), nil
		}
		frame, e := lanlink.SealSetupMessage(s.identity, *s.plan, lanlink.SetupActionApprove, nil, time.Now())
		if e != nil {
			return nil, setupError("setup_plan_expired", "connection plan expired; start a fresh session")
		}
		s.localApproved = true
		s.approvalFrame = frame
		s.state = "awaiting_approval"
		s.next = "wait_for_peer"
		return s.snapshotLocked(), nil
	case "setup.cancel":
		var in SetupSessionInput
		if e := decodePayload(raw, &in); e != nil {
			return nil, e
		}
		s, e := c.requireSetup(in.SessionID)
		if e != nil {
			return nil, e
		}
		s.stop("cancelled", "", "try_again")
		return s.snapshot(), nil
	}
	return nil, setupError("setup_command_invalid", "unknown setup command")
}
func (c *Core) requireSetup(id string) (*setupSession, error) {
	s := c.setupCopy()
	if s == nil || id == "" || s.local.SessionID != id {
		return nil, setupError("setup_session_changed", "setup session changed; refresh the current session")
	}
	return s, nil
}

// startSetup is called under c.op. It checks process and saved-authority
// boundaries before allocating any native network resource.
func (c *Core) startSetup(in SetupStartInput) (SetupSnapshot, error) {
	empty := setupIdle()
	empty.Process = c.setupProcess()
	if empty.Process.State != "ready" {
		empty.State = empty.Process.State
		empty.ReasonCode = "setup_restart_required"
		empty.NextAction = "restart_offline"
		return empty, nil
	}
	if !in.Consent || !setupAlias(in.Alias) || !setupHex(in.ScopeID) {
		return empty, setupError("setup_consent_required", "choose a network and explicit display name, then approve short-lived discovery and bootstrap listening")
	}
	scope, e := c.setupScope(in.ScopeID)
	if e != nil {
		return empty, e
	}
	ip := netip.MustParseAddr(scope.LocalAddress)
	endpoint := netip.AddrPortFrom(ip, in.BootstrapPort)
	if _, e = setupEndpoint(endpoint.String()); e != nil {
		return empty, setupError("setup_port_invalid", "choose an unreserved high bootstrap port")
	}
	if in.AllowHost {
		relay := netip.AddrPortFrom(ip, in.RelayPort)
		if _, e = setupEndpoint(relay.String()); e != nil || relay == endpoint {
			return empty, setupError("setup_port_invalid", "choose a distinct unreserved high relay port")
		}
	} else if in.RelayPort != 0 {
		return empty, setupError("setup_port_invalid", "a relay port requires explicit host-role consent")
	}
	c.mu.RLock()
	conflict := c.web != nil && (c.web.Port() == in.BootstrapPort || in.AllowHost && c.web.Port() == in.RelayPort)
	c.mu.RUnlock()
	if conflict {
		return empty, setupError("setup_port_invalid", "setup port conflicts with local management; choose another high port")
	}
	if old := c.setupCopy(); old != nil {
		old.mu.Lock()
		live := old.ctx.Err() == nil
		old.mu.Unlock()
		if live {
			return old.snapshot(), setupError("setup_busy", "a setup session is already open; finish or cancel it first")
		}
		select {
		case <-old.done:
		default:
			return old.snapshot(), setupError("setup_busy", "previous setup is still closing; wait for cleanup before starting again")
		}
	}
	store := c.lanStoreCopy()
	node := c.nodeCopy()
	var borrowed lanNetworkBackend
	if node != nil {
		var ok bool
		borrowed, ok = node.(lanNetworkBackend)
		if !ok {
			return empty, setupError("setup_restart_required", "stop and reopen the same profile offline before choosing this network")
		}
	}
	if borrowed == nil && (len(c.profileCopy().Peers) > 0 || len(c.startupPending) > 0 || len(c.savedProxyPending) > 0) {
		return empty, setupError("setup_saved_authority_review_required", "saved peers or permissions need separate review before starting their network; use existing network setup")
	}
	if store != nil {
		st := store.copy()
		if setupLANNeedsRecovery(store) {
			return empty, config.ErrAtomicRecovery
		}
		if borrowed == nil && (len(st.Remotes) > 0 || len(st.RouteCandidates) > 0 || len(c.profileCopy().Peers) > 0 || len(c.startupPending) > 0 || len(c.savedProxyPending) > 0) {
			return empty, setupError("setup_saved_authority_review_required", "saved peers or permissions need separate review before starting their network; use existing network setup")
		}
		if borrowed != nil {
			policy, _ := st.DestinationPolicy.Canonical()
			compatible := policy.Strict() && len(policy.Prefixes) == 1 && policy.Prefixes[0] == scope.Prefix && st.Selection != nil
			if compatible && st.Selection.Kind == "host" {
				compatible = in.AllowHost && st.Selection.Address == netip.AddrPortFrom(ip, in.RelayPort).String()
			} else if compatible {
				compatible = st.Selection.Kind == "relay" && !in.AllowHost && in.RelayPort == 0
			}
			if !compatible {
				return empty, setupError("setup_existing_anchor_conflict", "the active LAN anchor or destination permission differs from this setup; keep existing connections and review advanced network setup")
			}
			checkctx, cancel := context.WithTimeout(c.ctx, 3*time.Second)
			state, checkErr := borrowed.State(checkctx)
			cancel()
			if checkErr != nil || !state.Snapshot.Running {
				return empty, setupError("setup_network_unavailable", "existing LAN is not ready; check its current state before connection setup")
			}
		}
	}
	store, e = c.ensureLANIdentity()
	if e != nil {
		return empty, e
	}
	id, e := lanlink.NewSetupSessionID()
	if e != nil {
		return empty, setupError("setup_unavailable", "cannot create a setup session")
	}
	// Only this fresh opaque revision leaves the device. The immutable local
	// scope (including interface-instance binding) remains independently checked.
	consentRevision, e := lanlink.NewSetupSessionID()
	if e != nil {
		return empty, setupError("setup_unavailable", "cannot create a setup consent revision")
	}
	now := time.Now().UTC()
	expires := now.Add(setupLifetime)
	ctx, cancel := context.WithDeadline(c.ctx, expires)
	s := &setupSession{core: c, ctx: ctx, cancel: cancel, done: make(chan struct{}), scope: scope, identity: store.copy().Identity, state: "discovering", next: "select_peer", borrowedNode: borrowed}
	s.local = lanlink.SetupParticipant{SessionID: id, Key: s.identity.PublicKey(), Alias: in.Alias, Bootstrap: endpoint, Scope: lanlink.SetupScope{LocalAddress: ip, Prefix: netip.MustParsePrefix(scope.Prefix), ConsentRevision: consentRevision}, AllowHost: in.AllowHost, ExistingHost: borrowed != nil && in.AllowHost, Expires: expires}
	if borrowed != nil {
		s.borrowedAnchor, e = savedLANRelay(store.copy())
		if e != nil {
			cancel()
			return empty, e
		}
	}
	if in.AllowHost {
		var relayID lanlink.RelayIdentity
		if borrowed != nil {
			relayID = *store.copy().RelayIdentity
		} else {
			relayID, e = lanlink.GenerateRelayIdentity(ip)
			if e != nil {
				cancel()
				return empty, setupError("setup_unavailable", "cannot prepare the selected relay identity")
			}
		}
		relay, e := relayID.Endpoint(netip.AddrPortFrom(ip, in.RelayPort))
		if e != nil {
			cancel()
			return empty, e
		}
		s.relayIdentity = &relayID
		s.local.Relay = &relay
	}
	s.contact = SetupContact{Kind: "sobalink-setup-contact", Version: 1, SessionID: id, Alias: in.Alias, Bootstrap: endpoint.String(), ExpiresAt: expires}
	s.contactText, _ = EncodeSetupContact(s.contact)
	if qr, e := qrcode.New(s.contactText, qrcode.Medium); e == nil {
		s.contactQR = qr.Bitmap()
	}
	if e := s.local.Validate(now); e != nil {
		cancel()
		return empty, setupError("setup_scope_invalid", "selected network scope or endpoint is invalid")
	}
	s.review = c.setupReview()
	s.authority = store.copy()
	s.authorityProfile = c.setupProfileReview()
	if e = s.startListener(); e != nil {
		cancel()
		return empty, e
	}
	d, e := s.startDiscovery()
	s.adoptDiscovery(d, e)
	c.mu.Lock()
	c.setup = s
	c.mu.Unlock()
	c.wg.Add(1)
	go s.run()
	return s.snapshot(), nil
}
func (s *setupSession) selectPeer(contact SetupContact, candidateID string, manual bool) (SetupSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if s.ctx.Err() != nil {
		return s.snapshotLocked(), setupError("setup_session_expired", "setup session ended; start a fresh session")
	}
	if s.selected != nil {
		if s.selected.SessionID == contact.SessionID && s.selected.Bootstrap == contact.Bootstrap && s.selected.Alias == contact.Alias {
			return s.snapshotLocked(), nil
		}
		return s.snapshotLocked(), setupError("setup_selection_locked", "cancel this session before choosing a different device")
	}
	if e := validateSetupContact(contact, now); e != nil {
		return s.snapshotLocked(), e
	}
	ap, _ := setupEndpoint(contact.Bootstrap)
	if contact.SessionID == s.local.SessionID || ap == s.local.Bootstrap || !s.local.Scope.Prefix.Contains(ap.Addr()) {
		return s.snapshotLocked(), setupError("setup_scope_mismatch", "selected device is outside the approved scope or is this same session")
	}
	s.selected = &contact
	s.selectedManual = manual
	s.candidateID = candidateID
	s.state = "authenticating"
	s.next = "select_on_both_devices"
	return s.snapshotLocked(), nil
}
func (s *setupSession) stop(state, reason, next string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-s.done:
		return
	default:
	}
	if s.pairingAttempted && !s.connected && state != "recovery_required" {
		state, reason, next = "recovery_required", "setup_commit_uncertain", "review_recovery"
	}
	if s.connected && state != "recovery_required" {
		state, reason, next = "connected", "", "done"
	}
	if reason == "setup_cleanup_unconfirmed" {
		s.cleanupFailure = errors.New("setup cleanup was not confirmed")
	}
	s.terminalState, s.terminalReason, s.terminalNext = state, reason, next
	s.state, s.reason, s.next = "cancelling", reason, "wait_for_cleanup"
	s.cancel()
}

func (s *setupSession) snapshot() SetupSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshotLocked()
}
func (s *setupSession) snapshotLocked() SetupSnapshot {
	v := setupIdle()
	if s.core != nil {
		v.Process = s.core.setupProcess()
	}
	v.SessionID = s.local.SessionID
	v.ExpiresAt = s.local.Expires
	v.State, v.ReasonCode, v.NextAction = s.state, s.reason, s.next
	// Only a clean, uncommitted no-host result can reopen role review. The
	// terminal state is assigned during cleanup, just before run closes done;
	// keep clients polling until every temporary owner has physically joined.
	if s.state == "failed" && s.reason == "setup_no_permitted_host" && s.plan == nil && !s.localApproved && !s.remoteApproved && !s.pairingAttempted && !s.paired && !s.connected && s.cleanupFailure == nil && v.Process.State == "ready" {
		select {
		case <-s.done:
			v.NextAction = "try_again"
		default:
			v.State, v.NextAction = "cancelling", "wait_for_cleanup"
		}
	}
	if s.state == "connected" && v.Process.State == "ready" {
		select {
		case <-s.done:
			v.NextAction = "start_new_setup"
		default:
		}
	}
	scope := s.scope
	v.Scope = &scope
	contact := s.contact
	v.Contact = &contact
	v.ContactText = s.contactText
	v.ContactQR = s.contactQR
	v.LocalApproved = s.localApproved
	v.RemoteApproved = s.remoteApproved
	v.Paired = s.paired
	v.Connected = s.connected && s.state == "connected"
	v.Trusted = s.trusted
	if s.core != nil && s.peer != nil {
		trust, ok := s.core.trust(s.peer.Key)
		v.Trusted = ok && !trust.Paused
	}
	if s.selected != nil {
		selected := *s.selected
		v.Selected = &selected
	}
	if s.discovery != nil {
		v.Candidates = s.discovery.Candidates()
		v.Discovery.State = "active"
		if s.discovery.Err() != nil {
			v.Discovery.State = "unavailable"
			v.Discovery.ReasonCode = "setup_discovery_unavailable"
		}
	} else {
		v.Discovery.State = "unavailable"
		v.Discovery.ReasonCode = s.discoveryReason
	}
	if s.discoveryStopped {
		v.Discovery.State = "stopped"
	}
	if s.plan != nil && s.peerPlan {
		fingerprint, _ := s.plan.Fingerprint()
		text := setupVerificationPrefix + fingerprint
		p := &SetupPlanView{Revision: s.plan.Revision, Fingerprint: fingerprint, Verification: "full-sha256", VerificationText: text, Backend: "lan", Participants: s.plan.Participants, HostKey: s.plan.HostKey, Relay: LANSelection{Kind: "relay", Address: s.plan.Relay.Address.String(), CertificateSHA256: s.plan.Relay.CertificateSHA256}, ExpiresAt: s.plan.Expires}
		if qr, e := qrcode.New(text, qrcode.Medium); e == nil {
			p.VerificationQR = qr.Bitmap()
		}
		v.Plan = p
	}
	return v
}
func (s *setupSession) currentScope() error                     { _, e := s.core.setupScope(s.scope.ScopeID); return e }
func setupParticipantsEqual(a, b lanlink.SetupParticipant) bool { return reflect.DeepEqual(a, b) }
func setupRecoverablePairError(e error) bool {
	return errors.Is(e, lanlink.ErrPairReplyUncertain) || errors.Is(e, lanlink.ErrRemotePairedLocalSave) || errors.Is(e, config.ErrAtomicCommitted) || errors.Is(e, config.ErrAtomicRecovery)
}
