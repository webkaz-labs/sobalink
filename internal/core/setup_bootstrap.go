package core

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"time"

	"github.com/webkaz-labs/sobalink/internal/lanlink"
	"golang.org/x/net/netutil"
)

const maxSetupWire = 128 << 10
const maxSetupRequests = 256
const setupElectionDelay = 3 * time.Second

// Each protected field is independently domain/action/session/plan bound. The
// outer participant is untrusted until hello proof and out-of-band comparison.
type setupWire struct {
	Version       int                      `json:"version"`
	TargetSession string                   `json:"target_session"`
	Participant   lanlink.SetupParticipant `json:"participant"`
	HelloProof    []byte                   `json:"hello_proof,omitempty"`
	Plan          *lanlink.SetupPlan       `json:"plan,omitempty"`
	PlanProof     []byte                   `json:"plan_proof,omitempty"`
	Approval      []byte                   `json:"approval,omitempty"`
	Invitation    []byte                   `json:"invitation,omitempty"`
	Paired        []byte                   `json:"paired,omitempty"`
	Connected     []byte                   `json:"connected,omitempty"`
	Rejection     []byte                   `json:"rejection,omitempty"`
	Cancellation  []byte                   `json:"cancellation,omitempty"`
}

func (s *setupSession) startListener() error {
	if e := s.currentScope(); e != nil {
		return e
	}
	var lc net.ListenConfig
	listener, e := lc.Listen(s.ctx, "tcp", s.local.Bootstrap.String())
	if e != nil {
		return setupError("setup_port_unavailable", "selected bootstrap port is unavailable; choose another high port without broadening the address")
	}
	if listener.Addr().String() != s.local.Bootstrap.String() {
		_ = listener.Close()
		return setupError("setup_listener_changed", "bootstrap listener did not bind the approved exact endpoint")
	}
	s.listener = netutil.LimitListener(&setupAdmissionListener{Listener: listener, session: s}, 4)
	s.server = &http.Server{ErrorLog: log.New(io.Discard, "", 0), Handler: http.HandlerFunc(s.serve), ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 3 * time.Second, WriteTimeout: 3 * time.Second, IdleTimeout: time.Second, MaxHeaderBytes: 4096, BaseContext: func(net.Listener) context.Context { return s.ctx }}
	s.server.SetKeepAlivesEnabled(false)
	s.serveDone = make(chan struct{})
	go func() {
		defer close(s.serveDone)
		err := s.server.Serve(s.listener)
		if err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) && s.ctx.Err() == nil {
			s.stop("failed", "setup_listener_failed", "try_again")
		}
	}()
	return nil
}
func (s *setupSession) serve(w http.ResponseWriter, r *http.Request) {
	if s.local.Protocol == lanlink.SetupRequestVersion {
		s.serveRequest(w, r)
		return
	}
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
	if r.Method != http.MethodPost || r.URL.Path != "/sobalink/setup/v1" || r.URL.RawQuery != "" || r.Host != s.local.Bootstrap.String() || r.Header.Get("Origin") != "" || r.Header.Get("Sec-Fetch-Site") != "" || r.Header.Get("Content-Type") != "application/octet-stream" {
		http.Error(w, "setup unavailable", http.StatusForbidden)
		return
	}
	source, e := netip.ParseAddrPort(r.RemoteAddr)
	if e != nil {
		http.Error(w, "setup unavailable", http.StatusForbidden)
		return
	}
	// Selection/source/session gates precede body decoding and cryptography.
	s.mu.Lock()
	selected := s.selected
	allowed := selected != nil && s.selectionCurrentLocked(time.Now()) && s.ctx.Err() == nil && s.requests < maxSetupRequests
	if allowed {
		remote, _ := netip.ParseAddrPort(selected.Bootstrap)
		allowed = source.Addr().Unmap() == remote.Addr()
	}
	if allowed {
		s.requests++
	}
	s.mu.Unlock()
	if !allowed || s.currentScope() != nil {
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
	e = s.acceptWireLocked(packet, true, time.Now())
	var response setupWire
	if e == nil {
		response, e = s.wireLocked(time.Now())
	}
	s.mu.Unlock()
	if e != nil {
		http.Error(w, "setup unavailable", http.StatusForbidden)
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
func (s *setupSession) wireLocked(now time.Time) (setupWire, error) {
	if s.selected == nil || s.ctx.Err() != nil {
		return setupWire{}, lanlink.ErrSetup
	}
	if s.peer != nil {
		if e := s.verifySavedBasis(s.peer.Key); e != nil {
			return setupWire{}, e
		}
	}
	out := setupWire{Version: s.wireVersion(), TargetSession: s.selected.SessionID, Participant: s.local, HelloProof: s.helloProof}
	if s.plan != nil {
		out.Plan = s.plan
		out.PlanProof = s.planProof
		out.Approval = s.approvalFrame
		out.Invitation = s.invitationFrame
		if len(out.Invitation) > 0 {
			s.pairingAttempted = true
		}
		out.Paired = s.pairedFrame
		out.Connected = s.connectedFrame
	}
	return out, nil
}
func (s *setupSession) acceptWireLocked(w setupWire, inbound bool, now time.Time) error {
	if s.selected == nil || s.ctx.Err() != nil || w.Version != s.wireVersion() || w.TargetSession != s.local.SessionID || w.Participant.SessionID != s.selected.SessionID || w.Participant.Alias != s.selected.Alias || w.Participant.Bootstrap.String() != s.selected.Bootstrap || w.Participant.Key == s.local.Key || w.Participant.Validate(now) != nil || w.Participant.Protocol != s.local.Protocol {
		return lanlink.ErrSetup
	}
	if s.selectedManual && !w.Participant.Expires.Equal(s.selected.ExpiresAt) {
		return lanlink.ErrSetup
	}
	if s.peer != nil && !setupParticipantsEqual(*s.peer, w.Participant) {
		return lanlink.ErrSetup
	}
	if e := s.verifySavedBasis(w.Participant.Key); e != nil {
		return e
	}
	if s.peer == nil {
		peer := w.Participant
		hello, e := lanlink.NewSetupHello(s.local, peer)
		if e != nil {
			return e
		}
		proof, e := lanlink.SealSetupHello(s.identity, hello, now)
		if e != nil {
			return e
		}
		if len(w.HelloProof) > 0 {
			if e := lanlink.OpenSetupHello(s.identity, hello, peer.Key, w.HelloProof, now); e != nil {
				return e
			}
		}
		s.peer = &peer
		s.hello = &hello
		s.helloProof = proof
	}
	if len(w.HelloProof) > 0 {
		if e := lanlink.OpenSetupHello(s.identity, *s.hello, s.peer.Key, w.HelloProof, now); e != nil {
			return e
		}
		s.peerHello = true
		if s.authenticatedAt.IsZero() {
			s.authenticatedAt = now
		}
		if inbound {
			s.inbound = true
		} else {
			s.outbound = true
		}
	}
	if len(w.Cancellation) > 0 {
		if s.local.Protocol != 2 || s.local.Mode != "receive" || s.hello == nil {
			return lanlink.ErrSetup
		}
		if e := lanlink.OpenSetupCancellation(s.identity, *s.hello, s.peer.Key, w.Cancellation, now); e != nil {
			return e
		}
		if !s.commitStarted && !s.localApproved && len(s.rejected) < maxSetupRejected {
			s.rejected[s.requestID] = setupWire{}
			s.clearPendingRequestLocked("setup_request_cancelled")
		} else {
			s.terminalState, s.terminalReason, s.terminalNext = "cancelled", "setup_request_cancelled", "try_again"
			if s.pairingAttempted {
				s.terminalState, s.terminalReason, s.terminalNext = "recovery_required", "setup_commit_uncertain", "review_recovery"
			}
			s.state, s.reason, s.next = "cancelling", s.terminalReason, "wait_for_cleanup"
			s.cancel()
		}
		return nil
	}
	if w.Plan != nil {
		if !s.peerHello || w.Plan.Validate(now) != nil {
			return lanlink.ErrSetup
		}
		if s.plan == nil {
			if !s.peerCoordinates() || w.Plan.Revision != 1 || !s.samePlanParticipants(*w.Plan) || !s.validHost(*w.Plan) {
				return lanlink.ErrSetup
			}
			plan := *w.Plan
			s.plan = &plan
			proof, e := lanlink.SealSetupMessage(s.identity, plan, lanlink.SetupActionHelloProof, nil, now)
			if e != nil {
				return e
			}
			s.planProof = proof
		}
		own, _ := s.plan.Fingerprint()
		incoming, _ := w.Plan.Fingerprint()
		if own != incoming {
			return lanlink.ErrSetup
		}
	}
	if len(w.PlanProof) > 0 {
		if s.plan == nil {
			return lanlink.ErrSetup
		}
		if _, e := lanlink.OpenSetupMessage(s.identity, *s.plan, s.peer.Key, lanlink.SetupActionHelloProof, w.PlanProof, now); e != nil {
			return e
		}
		s.peerPlan = true
		if !s.localApproved {
			s.state = "awaiting_approval"
			s.next = "compare_full_fingerprint"
			if s.local.Protocol == 2 && s.local.Verification == "tofu" {
				s.next = "review_request"
				if s.local.Mode == "receive" {
					s.state = "request_received"
				}
			}
		}
	}
	if len(w.Rejection) > 0 {
		if s.local.Protocol != 2 || s.local.Mode == "receive" || s.plan == nil || !s.peerPlan || s.commitStarted {
			return lanlink.ErrSetup
		}
		if _, e := lanlink.OpenSetupMessage(s.identity, *s.plan, s.peer.Key, lanlink.SetupActionReject, w.Rejection, now); e != nil {
			return e
		}
		s.terminalState, s.terminalReason, s.terminalNext = "rejected", "setup_request_rejected", "try_again"
		s.state, s.reason, s.next = "cancelling", s.terminalReason, "wait_for_cleanup"
		s.cancel()
		return nil
	}
	if e := s.approveRequestedPlanLocked(now); e != nil {
		return e
	}
	if len(w.Approval) > 0 {
		if s.plan == nil || !s.peerPlan {
			return lanlink.ErrSetup
		}
		if _, e := lanlink.OpenSetupMessage(s.identity, *s.plan, s.peer.Key, lanlink.SetupActionApprove, w.Approval, now); e != nil {
			return e
		}
		s.remoteApproved = true
	}
	if len(w.Invitation) > 0 {
		if s.plan == nil || !s.localApproved || !s.remoteApproved || s.plan.HostKey != s.peer.Key {
			return lanlink.ErrSetup
		}
		plain, e := lanlink.OpenSetupMessage(s.identity, *s.plan, s.peer.Key, lanlink.SetupActionInvitation, w.Invitation, now)
		if e != nil {
			return e
		}
		var inv lanlink.Invitation
		if strictLANJSON(plain, &inv) != nil || inv.ValidateFor(s.local.Key, now) != nil || inv.Host.Peer.Key != s.peer.Key || inv.Host.Peer.Name != s.peer.Alias || inv.Relay != s.plan.Relay || inv.Expires.After(s.plan.Expires) {
			return lanlink.ErrSetup
		}
		binding := sha256.Sum256(plain)
		if s.invitationBinding != ([32]byte{}) && binding != s.invitationBinding {
			return lanlink.ErrSetup
		}
		if s.paired {
			if binding != s.invitationBinding {
				return lanlink.ErrSetup
			}
		} else if s.invitation != nil {
			old, _ := json.Marshal(s.invitation)
			if !bytes.Equal(old, plain) {
				return lanlink.ErrSetup
			}
		} else {
			s.invitation = &inv
			s.invitationBinding = binding
		}
	}
	if len(w.Paired) > 0 {
		if s.plan == nil || !s.localApproved || !s.remoteApproved {
			return lanlink.ErrSetup
		}
		if _, e := lanlink.OpenSetupMessage(s.identity, *s.plan, s.peer.Key, lanlink.SetupActionPaired, w.Paired, now); e != nil {
			return e
		}
		s.remotePaired = true
	}
	if len(w.Connected) > 0 {
		if s.plan == nil || !s.localApproved || !s.remoteApproved || !s.remotePaired {
			return lanlink.ErrSetup
		}
		if _, e := lanlink.OpenSetupMessage(s.identity, *s.plan, s.peer.Key, lanlink.SetupActionConnectedReceipt, w.Connected, now); e != nil {
			return e
		}
		s.remoteConnected = true
	}
	return nil
}
func (s *setupSession) peerCoordinates() bool {
	if s.local.Protocol == 2 {
		return s.peer != nil && s.local.Mode != "receive"
	}
	return s.peer != nil && s.peer.SessionID < s.local.SessionID
}
func (s *setupSession) samePlanParticipants(p lanlink.SetupPlan) bool {
	if s.peer == nil {
		return false
	}
	a, ok := p.Participant(s.local.Key)
	if !ok || !setupParticipantsEqual(a, s.local) {
		return false
	}
	b, ok := p.Participant(s.peer.Key)
	return ok && setupParticipantsEqual(b, *s.peer)
}
func (s *setupSession) validHost(p lanlink.SetupPlan) bool {
	host, ok := p.Participant(p.HostKey)
	if !ok || !host.AllowHost || host.Relay == nil || *host.Relay != p.Relay {
		return false
	}
	// A borrowed live anchor cannot be changed or replaced by autochoice.
	return s.borrowedNode == nil || p.Relay == s.borrowedAnchor
}
func (s *setupSession) electLocked(now time.Time) error {
	if s.plan != nil || s.peer == nil || !s.peerHello || s.peerCoordinates() || now.Sub(s.authenticatedAt) < setupElectionDelay {
		return nil
	}
	if e := s.verifySavedBasis(s.peer.Key); e != nil {
		return e
	}
	var candidates []lanlink.SetupParticipant
	if s.inbound && s.local.AllowHost && s.local.Relay != nil {
		candidates = append(candidates, s.local)
	}
	if s.outbound && s.peer.AllowHost && s.peer.Relay != nil {
		candidates = append(candidates, *s.peer)
	}
	if len(candidates) == 0 {
		s.terminalState, s.terminalReason, s.terminalNext = "failed", "setup_no_permitted_host", "review_existing_network"
		s.state, s.reason, s.next = "cancelling", s.terminalReason, "wait_for_cleanup"
		s.cancel()
		return nil
	}
	host := candidates[0]
	if len(candidates) == 2 {
		other := candidates[1]
		if other.ExistingHost && !host.ExistingHost || other.ExistingHost == host.ExistingHost && other.SessionID < host.SessionID {
			host = other
		}
	}
	expires := s.local.Expires
	if s.peer.Expires.Before(expires) {
		expires = s.peer.Expires
	}
	plan, e := lanlink.NewSetupPlan(s.local, *s.peer, 1, host.Key, *host.Relay, now.UTC(), expires)
	if e != nil {
		return e
	}
	if !s.validHost(plan) {
		s.terminalState, s.terminalReason, s.terminalNext = "failed", "setup_existing_anchor_conflict", "review_existing_network"
		s.state, s.reason, s.next = "cancelling", s.terminalReason, "wait_for_cleanup"
		s.cancel()
		return nil
	}
	proof, e := lanlink.SealSetupMessage(s.identity, plan, lanlink.SetupActionHelloProof, nil, now)
	if e != nil {
		return e
	}
	s.plan = &plan
	s.planProof = proof
	return nil
}
func (s *setupSession) exchange() {
	if s.currentScope() != nil {
		s.stop("cancelled", "setup_scope_changed", "choose_scope")
		return
	}
	s.mu.Lock()
	if s.selected == nil || s.local.Protocol == 2 && s.local.Mode == "receive" || s.ctx.Err() != nil || s.requests >= s.requestLimit() {
		s.mu.Unlock()
		return
	}
	if !s.selectionCurrentLocked(time.Now()) {
		s.mu.Unlock()
		s.stop("cancelled", "setup_candidate_expired", "select_peer")
		return
	}
	packet, e := s.wireLocked(time.Now())
	selected := *s.selected
	s.requests++
	s.mu.Unlock()
	if e != nil {
		return
	}
	raw, e := json.Marshal(packet)
	if e != nil || len(raw) > maxSetupWire {
		return
	}
	if s.local.Protocol == 2 {
		s.exchangeRequest(raw, selected)
		return
	}
	destination, _ := netip.ParseAddrPort(selected.Bootstrap)
	ctx, cancel := context.WithTimeout(s.ctx, 1500*time.Millisecond)
	defer cancel()
	dialer := &net.Dialer{Timeout: time.Second, LocalAddr: &net.TCPAddr{IP: net.IP(s.local.Scope.LocalAddress.AsSlice())}}
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, ResponseHeaderTimeout: time.Second, MaxResponseHeaderBytes: 4096, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		if network != "tcp" || address != destination.String() || !s.local.Scope.Prefix.Contains(destination.Addr()) || s.currentScope() != nil {
			return nil, errors.New("setup destination changed")
		}
		return dialer.DialContext(ctx, "tcp", address)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("setup redirects forbidden") }}
	request, e := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+destination.String()+s.wirePath(), bytes.NewReader(raw))
	if e != nil {
		return
	}
	request.Header.Set("Content-Type", "application/octet-stream")
	response, e := client.Do(request)
	if e != nil {
		return
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "application/octet-stream" {
		return
	}
	body, e := io.ReadAll(io.LimitReader(response.Body, maxSetupWire+1))
	if e != nil || len(body) > maxSetupWire {
		return
	}
	var reply setupWire
	if strictLANJSON(body, &reply) != nil {
		return
	}
	s.mu.Lock()
	_ = s.acceptWireLocked(reply, false, time.Now())
	s.mu.Unlock()
}
func (s *setupSession) run() {
	defer s.core.wg.Done()
	defer close(s.done)
	defer s.cleanup()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-s.ctx.Done():
			s.mu.Lock()
			if s.terminalState == "" {
				s.terminalState, s.terminalReason, s.terminalNext = "expired", "setup_session_expired", "try_again"
				if s.pairingAttempted {
					s.terminalState, s.terminalReason, s.terminalNext = "recovery_required", "setup_commit_uncertain", "review_recovery"
				}
				if s.connected {
					s.terminalState, s.terminalReason, s.terminalNext = "connected", "", "done"
				}
			}
			s.state, s.reason, s.next = "cancelling", s.terminalReason, "wait_for_cleanup"
			s.mu.Unlock()
			return
		case <-tick.C:
		}
		s.mu.Lock()
		s.expireRequestLocked(time.Now())
		s.mu.Unlock()
		s.exchange()
		s.mu.Lock()
		_ = s.electLocked(time.Now())
		s.mu.Unlock()
		s.advanceCommit()
		s.mu.Lock()
		stopDiscovery := s.connected && !s.discoveryStopped
		d := s.discovery
		s.mu.Unlock()
		if stopDiscovery {
			var closeErr error
			if d != nil {
				closeErr = d.Close()
			}
			if closeErr != nil {
				s.stop("recovery_required", "setup_cleanup_unconfirmed", "review_recovery")
			} else {
				s.mu.Lock()
				s.discoveryStopped = true
				s.mu.Unlock()
			}
		}

	}
}

// Count every accepted native connection, including invalid HTTP or an
// unselected source, before the HTTP parser can process a single header.
type setupAdmissionListener struct {
	net.Listener
	session *setupSession
}

func (l *setupAdmissionListener) Accept() (net.Conn, error) {
	for {
		conn, e := l.Listener.Accept()
		if e != nil {
			return nil, e
		}
		s := l.session
		s.mu.Lock()
		s.acceptedConnections++
		exhausted := s.acceptedConnections > s.requestLimit()
		selected := s.selected
		allowed := selected != nil && s.ctx.Err() == nil && s.selectionCurrentLocked(time.Now())
		if allowed {
			src, e := netip.ParseAddrPort(conn.RemoteAddr().String())
			dst, _ := netip.ParseAddrPort(selected.Bootstrap)
			allowed = e == nil && src.Addr().Unmap() == dst.Addr()
		}
		if s.local.Protocol == 2 {
			src, err := netip.ParseAddrPort(conn.RemoteAddr().String())
			exhausted = false // unrelated sources cannot evict an already displayed request
			allowed = err == nil && s.admitRequestSourceLocked(src.Addr().Unmap(), time.Now())
		}
		s.mu.Unlock()
		if exhausted {
			_ = conn.Close()
			s.stop("failed", "setup_receive_budget", "try_again")
			_ = l.Listener.Close()
			return nil, net.ErrClosed
		}
		if !allowed {
			_ = conn.Close()
			continue
		}
		return conn, nil
	}
}
