package core

import (
	"errors"
	"time"

	"github.com/webkaz-labs/sobalink/internal/nearby"
)

type nearbySetupDiscovery struct {
	session       *nearby.Session
	version       int
	receiversOnly bool
}

func (s *setupSession) startDiscovery() (setupDiscovery, error) {
	// The native adapter is selected only after local scope/disclosure consent.
	// Loopback tests and unsupported adapters use the same manual contact path.
	scope := nearby.Scope{InterfaceIndex: s.scope.InterfaceIndex, LocalAddress: s.local.Scope.LocalAddress, Prefix: s.local.Scope.Prefix}
	protocol, capability := nearby.Protocol, nearby.Capability
	if s.local.Protocol == 2 {
		protocol, capability = nearby.RequestProtocol, nearby.RequestCapability
		if s.local.Mode == "receive" {
			capability = nearby.ReceiveCapability
		}
	}
	d, e := nearby.StartNative(s.ctx, nearby.Config{Scope: scope, Advertisement: nearby.Advertisement{SessionID: s.local.SessionID, Alias: s.local.Alias, Protocol: protocol, Capability: capability, Bootstrap: s.local.Bootstrap}})
	if d == nil {
		return nil, e
	}
	// A failed constructor can still own a socket whose closure is uncertain.
	// Retain that admission-disabled owner so cancellation cannot lose it.
	return &nearbySetupDiscovery{session: d, version: s.wireVersion(), receiversOnly: s.local.Protocol == 2 && s.local.Mode != "receive"}, e
}
func (s *setupSession) adoptDiscovery(d setupDiscovery, err error) {
	s.discovery = d
	if err == nil {
		return
	}
	s.discoveryReason = "setup_discovery_unavailable"
	if d != nil || errors.Is(err, nearby.ErrCloseUncertain) || errors.Is(err, nearby.ErrCloseUnresolved) {
		s.stop("recovery_required", "setup_cleanup_unconfirmed", "review_recovery")
		s.core.mu.Lock()
		s.core.networkFatal = "setup_cleanup_unconfirmed"
		s.core.mu.Unlock()
	}
}
func nearbySetupCandidate(c nearby.Candidate) SetupCandidate {
	version := 1
	if c.Advertisement.Protocol == nearby.RequestProtocol {
		version = 2
	}
	return SetupCandidate{CandidateID: c.ID, Contact: SetupContact{Kind: "sobalink-setup-contact", Version: version, SessionID: c.Advertisement.SessionID, Alias: c.Advertisement.Alias, Bootstrap: c.Advertisement.Bootstrap.String(), ExpiresAt: c.ExpiresAt.UTC()}, ObservedAt: c.ObservedAt.UTC(), ExpiresAt: c.ExpiresAt.UTC(), Verification: "unverified"}
}
func (d *nearbySetupDiscovery) Candidates() []SetupCandidate {
	out := []SetupCandidate{}
	for _, c := range d.session.Candidates() {
		if setupAlias(c.Advertisement.Alias) && setupHex(c.Advertisement.SessionID) && d.compatible(c.Advertisement) {
			out = append(out, nearbySetupCandidate(c))
		}
	}
	return out
}
func (d *nearbySetupDiscovery) Lookup(id string) (SetupCandidate, error) {
	c, e := d.session.Lookup(id)
	if e != nil {
		return SetupCandidate{}, e
	}
	if !setupAlias(c.Advertisement.Alias) || !setupHex(c.Advertisement.SessionID) || !d.compatible(c.Advertisement) {
		return SetupCandidate{}, nearby.ErrInvalidAdvertisement
	}
	return nearbySetupCandidate(c), nil
}
func (d *nearbySetupDiscovery) Close() error { return d.session.Close() }
func (d *nearbySetupDiscovery) Err() error   { return d.session.Err() }
func (s *setupSession) selectionCurrentLocked(now time.Time) bool {
	if s.local.Protocol == 2 {
		return s.selected != nil && s.peer != nil && now.Before(s.local.Expires) && now.Before(s.peer.Expires)
	}
	if s.selected == nil {
		return false
	}
	// Bilateral approval pins the already verified exact transcript. Discovery
	// freshness no longer authorizes or changes its selected endpoint.
	if s.localApproved && s.remoteApproved && s.peerPlan && s.plan != nil && s.plan.Validate(now) == nil {
		return true
	}
	if s.selectedManual {
		return now.Before(s.selected.ExpiresAt)
	}
	if s.discovery == nil {
		return false
	}
	candidate, e := s.discovery.Lookup(s.candidateID)
	if e != nil {
		return false
	}
	return candidate.Contact.SessionID == s.selected.SessionID && candidate.Contact.Alias == s.selected.Alias && candidate.Contact.Bootstrap == s.selected.Bootstrap
}

func (d *nearbySetupDiscovery) compatible(a nearby.Advertisement) bool {
	if d.version == 2 {
		return a.Protocol == nearby.RequestProtocol && (!d.receiversOnly || a.Capability == nearby.ReceiveCapability)
	}
	return a.Protocol == nearby.Protocol
}

func setupDiscoveryFailureReason(err error) string {
	if errors.Is(err, nearby.ErrExpired) {
		return "setup_discovery_expired"
	}
	return "setup_discovery_unavailable"
}
