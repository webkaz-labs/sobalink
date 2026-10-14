package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"time"

	"github.com/webkaz-labs/sobalink/internal/lanlink"
)

const maxSetupV2Requests = 4096
const maxSetupRejected = 16

func validateSetupMode(in SetupStartInput) error {
	if in.Mode == "" {
		if in.Verification != "" || in.ExpiresInSeconds != 0 {
			return setupError("setup_version_mismatch", "legacy setup does not accept request-mode options")
		}
		return nil
	}
	if in.Mode != "request" && in.Mode != "receive" && in.Mode != "outbound" || in.Verification != "tofu" && in.Verification != "full-sha256" || in.ExpiresInSeconds != 0 && (in.ExpiresInSeconds < 120 || in.ExpiresInSeconds > 1200) {
		return setupError("setup_mode_invalid", "choose request, receive or outbound mode, a verification method, and a lifetime from 120 to 1200 seconds")
	}
	if in.Mode == "outbound" && (in.BootstrapPort != 0 || in.AllowHost || in.RelayPort != 0) || in.Mode == "request" && (in.AllowHost || in.RelayPort != 0) {
		return setupError("setup_role_invalid", "a requesting device cannot host; outbound-only setup also cannot open a bootstrap listener")
	}
	if in.Mode == "receive" && !in.AllowHost {
		return setupError("setup_no_permitted_host", "receiving requests requires explicit permission for this device to host the exact relay")
	}
	return nil
}

func (s *setupSession) requestLimit() int {
	if s.local.Protocol == lanlink.SetupRequestVersion {
		return maxSetupV2Requests
	}
	return maxSetupRequests
}
func (s *setupSession) wireVersion() int {
	if s.local.Protocol == lanlink.SetupRequestVersion {
		return lanlink.SetupRequestVersion
	}
	return lanlink.SetupVersion
}
func (s *setupSession) wirePath() string {
	if s.local.Protocol == lanlink.SetupRequestVersion {
		return "/sobalink/setup/v2"
	}
	return "/sobalink/setup/v1"
}
func setupParticipantContact(p lanlink.SetupParticipant) SetupContact {
	return SetupContact{Kind: "sobalink-setup-contact", Version: 2, SessionID: p.SessionID, Alias: p.Alias, Bootstrap: p.Bootstrap.String(), ExpiresAt: p.Expires}
}
func (s *setupSession) targetViewLocked(p lanlink.SetupParticipant) SetupTargetView {
	expiry := p.Expires
	if s.local.Expires.Before(expiry) {
		expiry = s.local.Expires
	}
	relayAddress, relayPin := "", ""
	if p.Relay != nil {
		relayAddress, relayPin = p.Relay.Address.String(), p.Relay.CertificateSHA256
	}
	return SetupTargetView{Revision: s.targetRevision, RelayAddress: relayAddress, RelayCertificateSHA256: relayPin, ScopePrefix: p.Scope.Prefix.String(), SessionID: p.SessionID, PeerKey: p.Key, Alias: p.Alias, Address: p.Bootstrap.String(), ExpiresAt: expiry, Verification: p.Verification, KeyStatus: s.peerKeyStatus(p.Key)}
}
func (s *setupSession) peerKeyStatus(key string) string {
	if s.core == nil {
		return "new"
	}
	store := s.core.lanStoreCopy()
	if store == nil {
		return "new"
	}
	state := store.copy()
	for _, binding := range state.SetupTrust {
		if binding.PeerKey == key && binding.Revoked {
			return "revoked"
		}
	}
	for _, p := range state.Trust.Peers {
		if p.Key == key {
			return "saved"
		}
	}
	return "new"
}

// Scoped HTTP never resolves hostnames, follows redirects, uses a proxy, or
// changes the local binding. An outbound session owns no listening socket.
func (s *setupSession) targetHTTP(method string, destination netip.AddrPort, path string, raw []byte) ([]byte, error) {
	return s.targetHTTPContext(s.ctx, method, destination, path, raw)
}
func (s *setupSession) targetHTTPContext(parent context.Context, method string, destination netip.AddrPort, path string, raw []byte) ([]byte, error) {
	if s.currentScope() != nil || !s.local.Scope.Prefix.Contains(destination.Addr()) {
		return nil, lanlink.ErrSetup
	}
	ctx, cancel := context.WithTimeout(parent, 1500*time.Millisecond)
	defer cancel()
	dialer := &net.Dialer{Timeout: time.Second, LocalAddr: &net.TCPAddr{IP: net.IP(s.local.Scope.LocalAddress.AsSlice())}}
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, ResponseHeaderTimeout: time.Second, MaxResponseHeaderBytes: 4096, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		if network != "tcp" || address != destination.String() || s.currentScope() != nil || !s.local.Scope.Prefix.Contains(destination.Addr()) {
			return nil, errors.New("setup destination changed")
		}
		return dialer.DialContext(ctx, "tcp", address)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("setup redirects forbidden") }}
	req, e := http.NewRequestWithContext(ctx, method, "http://"+destination.String()+path, bytes.NewReader(raw))
	if e != nil {
		return nil, e
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	response, e := client.Do(req)
	if e != nil {
		return nil, e
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "application/octet-stream" {
		return nil, lanlink.ErrSetup
	}
	body, e := io.ReadAll(io.LimitReader(response.Body, maxSetupWire+1))
	if e != nil || len(body) > maxSetupWire {
		return nil, lanlink.ErrSetup
	}
	return body, nil
}

func (s *setupSession) inspectTarget(in SetupInspectTargetInput) (SetupSnapshot, error) {
	s.mu.Lock()
	if s.local.Protocol != lanlink.SetupRequestVersion || s.local.Mode == "receive" || s.selected != nil || s.ctx.Err() != nil {
		s.mu.Unlock()
		return s.snapshot(), setupError("setup_selection_locked", "choose a target in a fresh requesting session")
	}
	discovery := s.discovery
	s.target = nil // a failed new inspection cannot leave a different target actionable
	s.mu.Unlock()
	if (in.Address == "") == (in.CandidateID == "") {
		return s.snapshot(), setupError("setup_target_invalid", "choose exactly one nearby device or numeric address")
	}
	address := in.Address
	var candidate *SetupContact
	if in.CandidateID != "" {
		if discovery == nil {
			return s.snapshot(), setupError("setup_discovery_unavailable", "enter the receiving device's numeric address")
		}
		found, e := discovery.Lookup(in.CandidateID)
		if e != nil {
			return s.snapshot(), setupError("setup_candidate_expired", "select a current candidate")
		}
		if found.Contact.Version != 2 {
			return s.snapshot(), setupError("setup_version_mismatch", "the selected device does not offer request-mode setup")
		}
		address = found.Contact.Bootstrap
		candidate = &found.Contact
	}
	ap, e := setupEndpoint(address)
	if e != nil || !s.local.Scope.Prefix.Contains(ap.Addr()) || ap == s.local.Bootstrap {
		return s.snapshot(), setupError("setup_scope_mismatch", "enter an exact private numeric address and high port within the selected scope")
	}
	body, e := s.targetHTTP(http.MethodGet, ap, "/sobalink/setup/v2/contact", nil)
	if e != nil {
		return s.snapshot(), setupError("setup_target_unreachable", "the receiving device could not be reached; check its current address and acceptance session")
	}
	var peer lanlink.SetupParticipant
	if strictLANJSON(body, &peer) != nil || peer.Validate(time.Now()) != nil || peer.Protocol != 2 || peer.Mode != "receive" || peer.Bootstrap != ap || peer.Key == s.local.Key || !peer.AllowHost || peer.Relay == nil || peer.Verification != s.local.Verification {
		return s.snapshot(), setupError("setup_target_changed", "the device or verification method differs; inspect a compatible receiving session again")
	}
	if candidate != nil && (peer.SessionID != candidate.SessionID || peer.Alias != candidate.Alias) {
		return s.snapshot(), setupError("setup_candidate_expired", "the selected candidate changed; select it again")
	}
	if _, e = lanlink.NewSetupHello(s.local, peer); e != nil {
		return s.snapshot(), setupError("setup_scope_mismatch", "both selected network scopes must include the exact participants and relay")
	}
	if e := s.verifySavedBasis(peer.Key); e != nil {
		return s.snapshot(), e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.selected != nil || s.ctx.Err() != nil {
		return s.snapshotLocked(), setupError("setup_selection_locked", "the session changed during target inspection")
	}
	if s.targetRevision >= setupMaxGeneration {
		return s.snapshotLocked(), setupError("setup_target_changed", "target revision exhausted; start a fresh session")
	}
	s.targetRevision++
	s.target = &peer
	return s.snapshotLocked(), nil
}

func (s *setupSession) requestPeer(in SetupRequestInput) (SetupSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.local.Protocol != 2 || s.local.Mode == "receive" || s.ctx.Err() != nil || s.target == nil || in.TargetRevision == 0 || in.TargetRevision > setupMaxGeneration || in.TargetRevision != s.targetRevision || s.target.SessionID != in.TargetSessionID || s.target.Key != in.ExpectedPeerKey || s.target.Validate(time.Now()) != nil {
		return s.snapshotLocked(), setupError("setup_target_changed", "inspect and explicitly request the current exact device key and session")
	}
	if s.local.Verification == "tofu" && !in.TOFUAcknowledged {
		return s.snapshotLocked(), setupError("setup_tofu_acknowledgement_required", "acknowledge that first-use key memory does not independently identify the physical device")
	}
	if e := s.verifySavedBasis(in.ExpectedPeerKey); e != nil {
		return s.snapshotLocked(), e
	}
	if s.selected != nil {
		if s.requestConsent && s.peer != nil && s.peer.Key == in.ExpectedPeerKey && s.peer.SessionID == in.TargetSessionID {
			return s.snapshotLocked(), nil
		}
		return s.snapshotLocked(), setupError("setup_selection_locked", "cancel before choosing another request")
	}
	hello, e := lanlink.NewSetupHello(s.local, *s.target)
	if e != nil {
		return s.snapshotLocked(), e
	}
	proof, e := lanlink.SealSetupHello(s.identity, hello, time.Now())
	if e != nil {
		return s.snapshotLocked(), e
	}
	peer := *s.target
	contact := setupParticipantContact(peer)
	s.selected, s.peer, s.hello, s.helloProof = &contact, &peer, &hello, proof
	s.selectedManual = true
	s.requestID = s.local.SessionID
	s.requestConsent = true
	s.state, s.next = "requesting", "wait_for_peer"
	return s.snapshotLocked(), nil
}

func (s *setupSession) approveRequestedPlanLocked(now time.Time) error {
	if s.local.Protocol != 2 || s.local.Mode == "receive" || s.local.Verification != "tofu" || !s.requestConsent || s.localApproved || !s.peerPlan || s.plan == nil {
		return nil
	}
	if s.target == nil || s.peer == nil || !setupParticipantsEqual(*s.target, *s.peer) || s.plan.RequestID != s.requestID || s.plan.HostKey != s.peer.Key || s.plan.Verification != "tofu" || !s.samePlanParticipants(*s.plan) {
		return lanlink.ErrSetup
	}
	frame, e := lanlink.SealSetupMessage(s.identity, *s.plan, lanlink.SetupActionApprove, nil, now)
	if e != nil {
		return e
	}
	s.localApproved = true
	s.approvalFrame = frame
	s.state, s.next = "awaiting_approval", "wait_for_peer"
	return nil
}

func (s *setupSession) rejectRequest(in SetupRejectInput) (SetupSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.local.Protocol != 2 || s.local.Mode != "receive" || s.ctx.Err() != nil || in.RequestID == "" || in.RequestID != s.requestID || s.peer == nil || s.commitStarted || s.localApproved {
		return s.snapshotLocked(), setupError("setup_request_changed", "only the exact pending, unapproved request can be rejected")
	}
	if len(s.rejected) >= maxSetupRejected {
		return s.snapshotLocked(), setupError("setup_receive_budget", "this acceptance session has reached its request limit; cancel and start fresh")
	}
	if s.plan == nil || !s.peerPlan {
		// An authenticated requester cannot monopolize the slot by withholding
		// the shared plan. Only local rejection is known at this stage.
		s.rejected[s.requestID] = setupWire{}
		s.clearPendingRequestLocked("")
		return s.snapshotLocked(), nil
	}
	frame, e := lanlink.SealSetupMessage(s.identity, *s.plan, lanlink.SetupActionReject, nil, time.Now())
	if e != nil {
		return s.snapshotLocked(), e
	}
	response, e := s.wireLocked(time.Now())
	if e != nil {
		return s.snapshotLocked(), e
	}
	response.Rejection = frame
	s.rejected[s.requestID] = response
	s.clearPendingRequestLocked("")
	return s.snapshotLocked(), nil
}

func (s *setupSession) admitRequestSourceLocked(source netip.Addr, now time.Time) bool {
	if s.ctx.Err() != nil || !s.local.Scope.Prefix.Contains(source) {
		return false
	}
	if s.admissionWindow.IsZero() || now.Sub(s.admissionWindow) >= time.Second {
		s.admissionWindow = now
		s.admissionWindowCount = 0
		s.selectedAdmissionWindowCount = 0
	}
	// Reserve a separate small budget for the displayed exact peer. Rotating
	// unselected sources cannot consume its parser/crypto budget or replace it.
	if s.selected != nil && s.peer != nil && source == s.peer.Scope.LocalAddress {
		if s.selectedAdmissionWindowCount >= 4 {
			return false
		}
		s.selectedAdmissionWindowCount++
		s.selectedConnections++
		return s.selectedConnections <= maxSetupV2Requests
	}
	if s.admissionWindowCount >= 4 {
		return false
	}
	s.admissionWindowCount++
	s.unselectedConnections++
	if s.unselectedConnections > maxSetupRequests {
		return false
	}
	if s.sourceAdmissions == nil {
		s.sourceAdmissions = map[netip.Addr]int{}
	}
	n, seen := s.sourceAdmissions[source]
	if !seen && len(s.sourceAdmissions) >= 32 {
		return false
	}
	if n >= 64 {
		return false
	}
	s.sourceAdmissions[source] = n + 1
	return s.local.Mode == "receive" || s.selected != nil
}

func (s *setupSession) serveRequest(w http.ResponseWriter, r *http.Request) {
	s.handlerMu.Lock()
	if s.handlerClosed {
		s.handlerMu.Unlock()
		http.Error(w, "setup unavailable", http.StatusForbidden)
		return
	}
	s.handlerWG.Add(1)
	s.handlerMu.Unlock()
	defer s.handlerWG.Done()
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.RawQuery != "" || r.Host != s.local.Bootstrap.String() || r.Header.Get("Origin") != "" || r.Header.Get("Sec-Fetch-Site") != "" || r.Header.Get("Content-Type") != "application/octet-stream" {
		http.Error(w, "setup unavailable", http.StatusForbidden)
		return
	}
	source, e := netip.ParseAddrPort(r.RemoteAddr)
	if e != nil || !s.local.Scope.Prefix.Contains(source.Addr().Unmap()) || s.currentScope() != nil {
		http.Error(w, "setup unavailable", http.StatusForbidden)
		return
	}
	s.mu.Lock()
	live := s.ctx.Err() == nil && s.requests < s.requestLimit()
	if live {
		s.requests++
	}
	s.mu.Unlock()
	if !live {
		http.Error(w, "setup unavailable", http.StatusServiceUnavailable)
		return
	}
	if r.Method == http.MethodGet && r.URL.Path == "/sobalink/setup/v2/contact" && s.local.Mode == "receive" {
		raw, e := json.Marshal(s.local)
		if e != nil {
			http.Error(w, "setup unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(raw)
		return
	}
	if r.Method != http.MethodPost || r.URL.Path != "/sobalink/setup/v2" {
		http.Error(w, "setup unavailable", http.StatusForbidden)
		return
	}
	raw, e := io.ReadAll(io.LimitReader(r.Body, maxSetupWire+1))
	if e != nil || len(raw) > maxSetupWire {
		http.Error(w, "setup unavailable", http.StatusBadRequest)
		return
	}
	var packet setupWire
	if strictLANJSON(raw, &packet) != nil {
		http.Error(w, "setup unavailable", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	var response setupWire
	if rejected, ok := s.rejected[packet.Participant.SessionID]; ok {
		// Rejection delivery still proves the exact previous request key/session.
		if packet.Version != 2 || packet.TargetSession != s.local.SessionID || packet.Participant.Key == "" || rejected.Plan == nil {
			e = lanlink.ErrSetup
		} else {
			previous, ok := rejected.Plan.Participant(packet.Participant.Key)
			if !ok || !setupParticipantsEqual(previous, packet.Participant) {
				e = lanlink.ErrSetup
			} else {
				response = rejected
			}
		}
	} else {
		if s.selected != nil && (s.peer == nil || s.peer.Scope.LocalAddress != source.Addr().Unmap()) {
			e = lanlink.ErrSetup
		}
		if s.selected == nil {
			e = s.admitRequestLocked(packet, source.Addr().Unmap(), time.Now())
		}
		if e == nil {
			e = s.acceptWireLocked(packet, true, time.Now())
		}
		if e == nil {
			response, e = s.wireLocked(time.Now())
		}
	}
	s.mu.Unlock()
	if e != nil {
		http.Error(w, "setup unavailable", http.StatusConflict)
		return
	}
	data, e := json.Marshal(response)
	if e != nil || len(data) > maxSetupWire {
		http.Error(w, "setup unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	_, _ = w.Write(data)
}

func (s *setupSession) admitRequestLocked(w setupWire, source netip.Addr, now time.Time) error {
	if _, rejected := s.rejected[w.Participant.SessionID]; rejected {
		return lanlink.ErrSetup
	}
	if s.selected != nil || s.local.Mode != "receive" || s.ctx.Err() != nil || len(s.rejected) >= maxSetupRejected || w.Version != 2 || w.TargetSession != s.local.SessionID || w.Participant.Protocol != 2 || w.Participant.Mode == "receive" || w.Participant.Scope.LocalAddress != source || w.Participant.Validate(now) != nil || w.Participant.Key == s.local.Key || len(w.HelloProof) == 0 || w.Plan != nil || len(w.Approval) > 0 || len(w.Invitation) > 0 || len(w.Paired) > 0 || len(w.Connected) > 0 || len(w.Rejection) > 0 {
		return lanlink.ErrSetup
	}
	if e := s.verifySavedBasis(w.Participant.Key); e != nil {
		return e
	}
	hello, e := lanlink.NewSetupHello(s.local, w.Participant)
	if e != nil {
		return e
	}
	if e = lanlink.OpenSetupHello(s.identity, hello, w.Participant.Key, w.HelloProof, now); e != nil {
		return e
	}
	proof, e := lanlink.SealSetupHello(s.identity, hello, now)
	if e != nil {
		return e
	}
	peer := w.Participant
	contact := setupParticipantContact(peer)
	s.selected, s.peer, s.hello, s.helloProof = &contact, &peer, &hello, proof
	s.selectedManual = true
	s.requestID = peer.SessionID
	s.state, s.next = "authenticating", "wait_for_peer"
	return nil
}

// Clearing a pending slot also clears every proof and approval. Tombstones stay
// for the bounded acceptance session; a fresh requester session is mandatory.
func (s *setupSession) clearPendingRequestLocked(reason string) {
	s.target = nil
	s.selected = nil
	s.selectedManual = false
	s.candidateID = ""
	s.peer = nil
	s.hello = nil
	s.helloProof = nil
	s.peerHello = false
	s.inbound = false
	s.outbound = false
	s.authenticatedAt = time.Time{}
	s.plan = nil
	s.planProof = nil
	s.peerPlan = false
	s.localApproved = false
	s.remoteApproved = false
	s.approvalFrame = nil
	s.requestID = ""
	s.requestConsent = false
	s.invitation = nil
	s.invitationFrame = nil
	s.invitationBinding = [32]byte{}
	s.pairedFrame = nil
	s.connectedFrame = nil
	s.remotePaired, s.remoteConnected = false, false
	s.paired, s.connected, s.localConnected, s.trusted = false, false, false, false
	s.commitStarted, s.pairingAttempted, s.promoted = false, false, false
	s.state, s.reason, s.next = "receiving", reason, "wait_for_request"
}
func (s *setupSession) expireRequestLocked(now time.Time) {
	if s.local.Protocol != 2 || s.peer == nil || now.Before(s.peer.Expires) {
		return
	}
	if s.local.Mode == "receive" && !s.commitStarted && len(s.rejected) < maxSetupRejected {
		s.rejected[s.requestID] = setupWire{}
		s.clearPendingRequestLocked("setup_request_expired")
		return
	}
	s.terminalState, s.terminalReason, s.terminalNext = "expired", "setup_request_expired", "try_again"
	if s.pairingAttempted {
		s.terminalState, s.terminalReason, s.terminalNext = "recovery_required", "setup_commit_uncertain", "review_recovery"
	}
	s.state, s.reason, s.next = "cancelling", s.terminalReason, "wait_for_cleanup"
	s.cancel()
}

func (s *setupSession) sendRequestCancellation() {
	s.mu.Lock()
	if s.local.Protocol != 2 || s.local.Mode == "receive" || s.ctx.Err() != nil || s.hello == nil || s.selected == nil || s.pairingAttempted || s.connected {
		s.mu.Unlock()
		s.stop("cancelled", "", "try_again")
		return
	}
	packet, e := s.wireLocked(time.Now())
	if e == nil {
		packet.Cancellation, e = lanlink.SealSetupCancellation(s.identity, *s.hello, time.Now())
	}
	destination, _ := netip.ParseAddrPort(s.selected.Bootstrap)
	s.mu.Unlock()
	// End local consent before any best-effort network operation can block.
	s.stop("cancelled", "", "try_again")
	if e != nil {
		return
	}
	raw, e := json.Marshal(packet)
	if e != nil || len(raw) > maxSetupWire {
		return
	}
	// This is a best-effort notification only. Local cancellation always wins;
	// a lost response cannot authorize another request or claim remote rollback.
	_, _ = s.targetHTTPContext(s.core.ctx, http.MethodPost, destination, s.wirePath(), raw)
}

func (s *setupSession) exchangeRequest(raw []byte, selected SetupContact) {
	destination, e := setupEndpoint(selected.Bootstrap)
	if e != nil {
		s.stop("failed", "setup_target_changed", "try_again")
		return
	}
	body, e := s.targetHTTP(http.MethodPost, destination, s.wirePath(), raw)
	if e != nil {
		s.mu.Lock()
		if s.ctx.Err() == nil {
			s.reason = "setup_request_unavailable"
		}
		s.mu.Unlock()
		return
	}
	var reply setupWire
	if strictLANJSON(body, &reply) != nil {
		s.stop("failed", "setup_target_changed", "try_again")
		return
	}
	s.mu.Lock()
	e = s.acceptWireLocked(reply, false, time.Now())
	if e == nil && s.ctx.Err() == nil {
		s.reason = ""
	}
	s.mu.Unlock()
	if e != nil {
		s.stop("failed", "setup_target_changed", "try_again")
	}
}
