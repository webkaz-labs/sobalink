package core

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"reflect"
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/lanlink"
	"github.com/webkaz-labs/sobalink/internal/lanpolicy"
)

// Activation is separate from pairing. It may save an exact reviewed network
// configuration, but cannot issue an invitation before both local approvals.
func (s *setupSession) advanceCommit() {
	s.mu.Lock()
	ready := s.plan != nil && s.peerPlan && s.localApproved && s.remoteApproved && s.ctx.Err() == nil
	if !ready || s.connected || s.state == "recovery_required" || s.state == "failed" {
		s.mu.Unlock()
		return
	}
	prepare := !s.commitStarted
	if prepare {
		s.commitStarted = true
		s.state = "preparing"
		s.next = "wait_for_peer"
	}
	plan := *s.plan
	peer := *s.peer
	s.mu.Unlock()
	if prepare {
		if e := s.prepareBackend(); e != nil {
			s.failCommit(e)
			return
		}
	}
	s.mu.Lock()
	node := s.borrowedNode
	if node == nil {
		node = s.ownedNode
	}
	alreadyPaired := s.paired
	haveInvitation := s.invitation != nil
	attempted := s.pairingAttempted
	s.mu.Unlock()
	if node == nil || s.ctx.Err() != nil {
		return
	}
	s.core.op.Lock()
	checkErr := s.checkAuthority(node)
	s.core.op.Unlock()
	if checkErr != nil {
		s.failCommit(checkErr)
		return
	}
	// Existing pair reuse proves the exact key within this backend only. It
	// creates no invite, changes no anchor and does not grant application trust.
	paired := false
	for _, p := range node.PublicPeers() {
		if p.Key == peer.Key {
			paired = true
			break
		}
	}
	if paired && !alreadyPaired {
		s.markPaired()
	}
	if !paired && plan.HostKey == s.local.Key && !haveInvitation {
		remaining := time.Until(plan.Expires) / time.Second
		if remaining < 1 {
			s.failCommit(lanlink.ErrSetup)
			return
		}
		if remaining > 120 {
			remaining = 120
		}
		s.core.op.Lock()
		if e := s.checkAuthority(node); e != nil {
			s.core.op.Unlock()
			s.failCommit(e)
			return
		}
		ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
		inv, e := node.IssueInvitation(ctx, lanlink.Peer{Key: peer.Key, Name: peer.Alias}, s.local.Alias, remaining*time.Second)
		cancel()
		s.core.op.Unlock()
		if e != nil {
			s.failCommit(e)
			return
		}
		raw, e := json.Marshal(inv)
		if e != nil {
			node.CancelInvitation(inv.Token)
			s.failCommit(e)
			return
		}
		frame, e := lanlink.SealSetupMessage(s.identity, plan, lanlink.SetupActionInvitation, raw, time.Now())
		if e != nil {
			node.CancelInvitation(inv.Token)
			s.failCommit(e)
			return
		}
		s.mu.Lock()
		if s.ctx.Err() != nil {
			s.mu.Unlock()
			node.CancelInvitation(inv.Token)
			return
		}
		s.invitation = &inv
		s.invitationFrame = frame
		s.state = "connecting"
		s.mu.Unlock()
	} else if !paired && plan.HostKey == peer.Key && haveInvitation && !attempted {
		s.mu.Lock()
		inv := *s.invitation
		s.pairingAttempted = true
		s.state = "connecting"
		s.mu.Unlock()
		ctx, cancel := context.WithTimeout(s.ctx, 30*time.Second)
		e := node.PairInvitation(ctx, inv)
		cancel()
		if e != nil {
			s.failCommit(e)
			return
		}
		s.core.op.Lock()
		e = s.checkAuthority(node)
		s.core.op.Unlock()
		if e != nil {
			s.failCommit(e)
			return
		}
		s.markPaired()
	}
	s.checkConnected()
}
func (s *setupSession) prepareBackend() error {
	c := s.core
	c.op.Lock()
	defer c.op.Unlock()
	if s.ctx.Err() != nil {
		return s.ctx.Err()
	}
	if e := s.currentScope(); e != nil {
		return e
	}
	if c.setupReview() != s.review {
		return setupError("setup_state_changed", "saved identity or permission changed before activation; review a fresh setup")
	}
	s.mu.Lock()
	plan := *s.plan
	borrowed := s.borrowedNode
	s.mu.Unlock()
	store := c.lanStoreCopy()
	if store == nil || store.copy().Identity.PublicKey() != s.local.Key || setupLANNeedsRecovery(store) {
		return config.ErrAtomicRecovery
	}
	if borrowed != nil {
		s.mu.Lock()
		unchanged := reflect.DeepEqual(s.authority, store.copy()) && s.authorityProfile == c.setupProfileReview()
		s.mu.Unlock()
		if !unchanged {
			return setupError("setup_authority_changed", "saved LAN authority changed before reuse")
		}
		if c.nodeCopy() != borrowed || borrowed.PublicKey() != s.local.Key {
			return setupError("setup_state_changed", "active LAN owner changed")
		}
		return nil
	}
	if c.nodeCopy() != nil {
		return setupError("setup_state_changed", "a network started during connection setup")
	}
	next := store.copy()
	if len(next.Remotes) != 0 || len(next.RouteCandidates) != 0 {
		return setupError("setup_existing_anchor_conflict", "existing LAN pairs or prepared routes require separate review")
	}
	next.Selection = &LANSelection{Kind: "relay", Address: plan.Relay.Address.String(), CertificateSHA256: plan.Relay.CertificateSHA256}
	next.RelayIdentity = nil
	if plan.HostKey == s.local.Key {
		if s.relayIdentity == nil || s.local.Relay == nil || *s.local.Relay != plan.Relay {
			return lanlink.ErrSetup
		}
		next.Selection.Kind = "host"
		id := *s.relayIdentity
		next.RelayIdentity = &id
	}
	policy := lanpolicy.Config{Mode: lanpolicy.AllowedLANDestinations, Prefixes: []string{s.scope.Prefix}}
	if e := applyLANSetupPolicy(&next, &policy); e != nil {
		return e
	}
	if e := validateLANState(next); e != nil {
		return e
	}
	// The exact staged certificate and identity in the approved plan are saved
	// once. configureLAN is not called: it would create a second certificate.
	if e := store.save(next); e != nil {
		return e
	}
	p := c.profileCopy()
	p.Settings.Network = "lan"
	if e := c.saveProfile(p); e != nil {
		if atomicPublished(e) {
			c.mu.Lock()
			c.profile = p
			c.mu.Unlock()
		}
		return e
	}
	c.mu.Lock()
	c.profile = p
	c.mu.Unlock()
	if e := c.startNetwork(s.ctx); e != nil {
		return e
	}
	node, ok := c.nodeCopy().(lanNetworkBackend)
	if !ok {
		return errors.New("LAN activation has no exact owner")
	}
	s.mu.Lock()
	s.ownedNode = node
	s.authority = store.copy()
	s.authorityProfile = c.setupProfileReview()
	s.review = c.setupReview()
	s.mu.Unlock()
	return nil
}
func (s *setupSession) markPaired() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.plan == nil || s.paired || s.ctx.Err() != nil {
		return
	}
	frame, e := lanlink.SealSetupMessage(s.identity, *s.plan, lanlink.SetupActionPaired, nil, time.Now())
	if e != nil {
		return
	}
	s.paired = true
	s.pairedFrame = frame
	s.invitationFrame = nil
	s.invitation = nil
	s.state = "connecting"
	s.next = "wait_for_peer"
	if trust, ok := s.core.trust(s.peer.Key); ok && !trust.Paused {
		s.trusted = true
	}
}
func (s *setupSession) checkConnected() {
	s.mu.Lock()
	if !s.paired || !s.remotePaired || s.ctx.Err() != nil || s.plan == nil {
		s.mu.Unlock()
		return
	}
	local := s.localConnected
	peer := s.peer.Key
	s.mu.Unlock()
	if !local {
		ctx, cancel := context.WithTimeout(s.ctx, 2*time.Second)
		var hello struct {
			Protocol     int      `json:"protocol"`
			Product      string   `json:"product"`
			Capabilities []string `json:"capabilities"`
		}
		e := s.core.peerJSON(ctx, peer, "GET", "/v1/hello", nil, &hello)
		cancel()
		if e == nil && hello.Protocol == 1 && hello.Product == "sobalink" {
			s.mu.Lock()
			if s.ctx.Err() == nil {
				frame, e := lanlink.SealSetupMessage(s.identity, *s.plan, lanlink.SetupActionConnectedReceipt, nil, time.Now())
				if e == nil {
					s.localConnected = true
					s.connectedFrame = frame
				}
			}
			s.mu.Unlock()
		}
	}
	s.core.op.Lock()
	defer s.core.op.Unlock()
	s.mu.Lock()
	node := s.borrowedNode
	if node == nil {
		node = s.ownedNode
	}
	s.mu.Unlock()
	if node == nil {
		return
	}
	if e := s.checkAuthority(node); e != nil {
		s.failCommit(e)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.localConnected && s.remoteConnected && s.ctx.Err() == nil {
		s.connected = true
		s.promoted = true
		s.state = "connected"
		s.reason = ""
		s.next = "done"
	}
}
func (s *setupSession) failCommit(e error) {
	s.mu.Lock()
	state, reason, next := "failed", networkErrorCode(e), "try_again"
	if reason == "" {
		reason = "setup_connection_failed"
	}
	if s.pairingAttempted || setupRecoverablePairError(e) {
		state, reason, next = "recovery_required", "setup_commit_uncertain", "review_recovery"
	}
	s.terminalState, s.terminalReason, s.terminalNext = state, reason, next
	s.state, s.reason, s.next = "cancelling", reason, "wait_for_cleanup"
	s.cancel()
	s.mu.Unlock()
}

func (s *setupSession) cleanup() {
	s.mu.Lock()
	cleanupErr := s.cleanupFailure
	s.mu.Unlock()
	defer func() {
		if cleanupErr != nil {
			s.core.mu.Lock()
			s.core.networkFatal = "setup_cleanup_unconfirmed"
			s.core.mu.Unlock()
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if cleanupErr != nil {
			s.state, s.reason, s.next = "recovery_required", "setup_cleanup_unconfirmed", "review_recovery"
			return
		}
		if s.terminalState != "" {
			s.state, s.reason, s.next = s.terminalState, s.terminalReason, s.terminalNext
		}
	}()
	s.handlerMu.Lock()
	s.handlerClosed = true
	s.handlerMu.Unlock()
	var discoveryErr error
	if s.discovery != nil {
		discoveryErr = s.discovery.Close()
		cleanupErr = errors.Join(cleanupErr, discoveryErr)
	}
	if discoveryErr == nil {
		s.mu.Lock()
		s.discoveryStopped = true
		s.mu.Unlock()
	}
	if s.server != nil {
		if e := s.server.Close(); e != nil && !errors.Is(e, net.ErrClosed) && !errors.Is(e, http.ErrServerClosed) {
			cleanupErr = errors.Join(cleanupErr, e)
		}
	}
	if s.listener != nil {
		if e := s.listener.Close(); e != nil && !errors.Is(e, net.ErrClosed) {
			cleanupErr = errors.Join(cleanupErr, e)
		}
	}
	joined := make(chan struct{})
	go func() {
		if s.serveDone != nil {
			<-s.serveDone
		}
		s.handlerWG.Wait()
		close(joined)
	}()
	select {
	case <-joined:
	case <-time.After(5 * time.Second):
		cleanupErr = errors.Join(cleanupErr, errors.New("setup owner cleanup not joined"))
		s.core.mu.Lock()
		s.core.networkFatal = "setup_cleanup_unconfirmed"
		s.core.mu.Unlock()
		s.mu.Lock()
		s.state = "recovery_required"
		s.reason = "setup_cleanup_unconfirmed"
		s.next = "review_recovery"
		s.mu.Unlock()
	}
	s.mu.Lock()
	owned, borrowed, promoted := s.ownedNode, s.borrowedNode, s.promoted
	inv := s.invitation
	localHost := s.plan != nil && s.plan.HostKey == s.local.Key
	s.mu.Unlock()
	node := borrowed
	if node == nil {
		node = owned
	}
	if node != nil && inv != nil && localHost {
		node.CancelInvitation(inv.Token)
	}
	if owned == nil || promoted {
		return
	}
	c := s.core
	c.op.Lock()
	defer c.op.Unlock()
	c.mu.Lock()
	if c.node != owned {
		c.mu.Unlock()
		return
	}
	c.networkReady.Store(false)
	ps := c.peerServer
	c.networkState = "stopping"
	c.mu.Unlock()
	var peerCloseErr error
	if ps != nil {
		peerCloseErr = ps.Close()
	}
	if e := errors.Join(peerCloseErr, owned.Close()); e != nil {
		cleanupErr = errors.Join(cleanupErr, e)
		c.mu.Lock()
		c.networkState = "error"
		c.networkFatal = "setup_cleanup_unconfirmed"
		c.mu.Unlock()
		s.mu.Lock()
		s.state = "recovery_required"
		s.reason = "setup_cleanup_unconfirmed"
		s.next = "review_recovery"
		s.mu.Unlock()
		return
	}
	c.mu.Lock()
	if c.node == owned {
		c.node = nil
		if c.peerServer == ps {
			c.peerServer = nil
		}
		c.networkState = "offline"
	}
	c.mu.Unlock()
}
