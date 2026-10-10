package core

import (
	"context"
	"time"

	"github.com/webkaz-labs/sobalink/internal/identity"
	"github.com/webkaz-labs/sobalink/internal/resourcecatalog"
	"github.com/webkaz-labs/sobalink/internal/transportorigin"
)

type resourceCatalogDiscoveryOrigin struct {
	node                      NetworkBackend
	network, peer, revocation string
	trust, writeRevision      uint64
	confirmed                 time.Time
	observation               DiscoveryObservation
	services                  []RemoteService
	transport                 transportorigin.Origin
}

// Only these concrete production State methods have been inspected: Tailnet
// reads its embedded node's local Status API; LAN and DirectLAN read local
// readiness/peer snapshots. None starts a peer probe, dial, refresh or listener.
// A mixed route needs its own exact original-route contract before disclosure.
func resourceCatalogDiscoveryBackend(node NetworkBackend) bool {
	switch value := node.(type) {
	case *identity.Node:
		return value != nil
	case *lanBackend:
		return value != nil && value.lanEngine != nil
	case *directLANBackend:
		return value != nil && value.Node != nil
	}
	return false
}

func resourceCatalogDiscoveryPeerCurrent(ctx context.Context, node NetworkBackend, id string) bool {
	if ctx.Err() != nil || !resourceCatalogDiscoveryBackend(node) {
		return false
	}
	check, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	state, err := node.State(check)
	if err != nil || check.Err() != nil || !state.Snapshot.Running {
		return false
	}
	for _, peer := range state.Snapshot.Peers {
		if peer.ID == id && !peer.Expired {
			return true
		}
	}
	return false
}

func (c *Core) resourceCatalogDiscoveryScalarsCurrent(o *resourceCatalogDiscoveryOrigin) bool {
	if o == nil || c.ctx.Err() != nil {
		return false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.closing || c.node != o.node || c.profile.Settings.Network != o.network || c.trustGeneration != o.trust || c.lanStartWriteRevision.Load() != o.writeRevision || c.startup.Revocations[o.peer] != o.revocation || c.managedDenied[o.peer] || c.managedDenied[mixedID("direct-lan", o.peer)] || c.confirmed[o.peer] != o.confirmed || c.discoveryObservations[o.peer] != o.observation {
		return false
	}
	services := c.discovered[o.peer]
	if len(services) != len(o.services) {
		return false
	}
	for i := range services {
		if services[i] != o.services[i] {
			return false
		}
	}
	return o.transport == nil || !channelClosed(o.transport.StopRequested())
}

func (c *Core) resourceCatalogDiscoveryCurrent(ctx context.Context, o *resourceCatalogDiscoveryOrigin) bool {
	return o != nil && resourceCatalogDiscoveryPeerCurrent(ctx, o.node, o.peer) && c.resourceCatalogDiscoveryScalarsCurrent(o)
}

func (c *Core) resourceCatalogDiscoveredServices(ctx context.Context, s resourcecatalog.Selection, l resourcecatalog.Limits, budget *resourceCatalogBudget) (resourceCatalogCapture, *resourceCatalogDiscoveryOrigin) {
	c.mu.RLock()
	o := &resourceCatalogDiscoveryOrigin{node: c.node, network: c.profile.Settings.Network, peer: s.PeerKey, revocation: c.startup.Revocations[s.PeerKey], trust: c.trustGeneration, writeRevision: c.lanStartWriteRevision.Load()}
	denied := c.closing || c.managedDenied[s.PeerKey] || c.managedDenied[mixedID("direct-lan", s.PeerKey)]
	c.mu.RUnlock()
	if denied || !resourceCatalogDiscoveryPeerCurrent(ctx, o.node, s.PeerKey) {
		return resourceCatalogFailure(s, "unavailable"), nil
	}
	if direct, ok := o.node.(*directLANBackend); ok {
		// CapturePeer is local-only and does not dial. Keep this original
		// generation's stop fence; never replace it after another source read.
		peer, err := direct.Node.CapturePeer(s.PeerKey)
		if err != nil || peer.Origin() == nil || peer.Origin().Identity() == nil {
			return resourceCatalogFailure(s, "unavailable"), nil
		}
		o.transport = peer.Origin()
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.node != o.node || c.profile.Settings.Network != o.network || c.closing || c.trustGeneration != o.trust || c.lanStartWriteRevision.Load() != o.writeRevision || c.startup.Revocations[s.PeerKey] != o.revocation || c.managedDenied[s.PeerKey] || c.managedDenied[mixedID("direct-lan", s.PeerKey)] {
		return resourceCatalogFailure(s, "unavailable"), nil
	}
	o.observation = c.discoveryObservations[s.PeerKey]
	o.confirmed = c.confirmed[s.PeerKey]
	observation := o.observation
	if observation.State == "" || observation.State == "pending" || observation.State == "unconfirmed" {
		return resourceCatalogCapture{Selection: s, State: "unconfirmed", Rows: []resourcecatalog.Row{}}, nil
	}
	if observation.State == "unsupported" || observation.State == "limited" {
		if observation.CheckedAt.IsZero() || !o.confirmed.IsZero() || len(c.discovered[s.PeerKey]) != 0 {
			return resourceCatalogFailure(s, "unavailable"), nil
		}
		capture := resourceCatalogFailure(s, observation.State)
		capture.CheckedAt = observation.CheckedAt.UnixMilli()
		// These are authenticated cached outcomes too. Retain their original
		// empty-cache scalar fence through final disclosure, even without rows.
		o.services = []RemoteService{}
		return capture, o
	}
	if observation.State != "confirmed" || o.confirmed.IsZero() || observation.CheckedAt.Before(o.confirmed) || observation.Services != len(c.discovered[s.PeerKey]) {
		return resourceCatalogFailure(s, "unavailable"), nil
	}
	services := c.discovered[s.PeerKey]
	if len(services) > budget.rowCapacity() {
		return resourceCatalogFailure(s, "limited"), nil
	}
	capture := resourceCatalogProjectDiscovered(ctx, s, services, o.confirmed, l, budget)
	if capture.State != "stale" {
		return capture, nil
	}
	o.services = append([]RemoteService{}, services...)
	return capture, o
}

func resourceCatalogProjectDiscovered(ctx context.Context, s resourcecatalog.Selection, services []RemoteService, checked time.Time, l resourcecatalog.Limits, budget *resourceCatalogBudget) resourceCatalogCapture {
	if len(services) > budget.rowCapacity() {
		return resourceCatalogFailure(s, "limited")
	}
	rows := make([]resourcecatalog.Row, 0, len(services))
	for _, service := range services {
		if ctx.Err() != nil {
			return resourceCatalogFailure(s, "unavailable")
		}
		// Pre-bound the base64 review's JSON expansion before encoding it.
		extra := 1024
		for _, value := range []string{s.PeerKey, service.ID, service.Purpose, service.Network, service.Ports, service.Lifetime, service.Application} {
			if extra > budget.bytes || len(value) > (budget.bytes-extra)/8 {
				return resourceCatalogFailure(s, "limited")
			}
			extra += 8 * len(value)
		}
		if !budget.reserveWithExtra(extra, service.ID, service.Purpose, service.Network, service.Ports, service.Lifetime, service.Application) {
			return resourceCatalogFailure(s, "limited")
		}
		var expires int64
		if !service.ExpiresAt.IsZero() {
			expires = service.ExpiresAt.UnixMilli()
		}
		row, err := resourcecatalog.ProjectSharedService(s, service.ID, resourcecatalog.SharedService{Purpose: service.Purpose, Network: service.Network, Ports: service.Ports, Lifetime: service.Lifetime, ExpiresAt: expires, Application: service.Application, ReviewRevision: encodeDiscoveryReview(s.PeerKey, service, checked)}, l)
		if err != nil {
			return resourceCatalogFailure(s, "invalid")
		}
		rows = append(rows, row)
	}
	capture := resourceCatalogComplete(s, checked.UnixMilli(), rows)
	// The existing cache publishes under the original transport permit but does
	// not retain its identity. A newly captured origin cannot certify the old
	// publication. Preserve authorized historical observation only, even when
	// its timestamp is within 15 seconds or its result was empty. Current/actionable
	// discovery remains gated on a separately reviewed provenance association.
	capture.State = "stale"
	return capture
}
