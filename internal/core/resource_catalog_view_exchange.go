package core

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"time"

	"github.com/webkaz-labs/sobalink/internal/resource"
	"github.com/webkaz-labs/sobalink/internal/resourcegrant"
	"github.com/webkaz-labs/sobalink/internal/transportorigin"
)

const catalogViewPath = "/v1/catalog/services"

type catalogViewService struct {
	Service        RemoteService `json:"service"`
	ConnectAllowed bool          `json:"connectAllowed"`
}

type catalogViewReply struct {
	Selector resourcegrant.CatalogViewSelector `json:"selector"`
	Services []catalogViewService              `json:"services"`
}

// The public revision is an owning-process review reference, not authority.
// Original transport attribution stays private and is never reconstructed
// from that reference, the peer's key or a newly captured transport generation.
type catalogViewObservation struct {
	catalogViewReply
	PeerKey        string    `json:"peerKey"`
	CheckedAt      time.Time `json:"checkedAt"`
	ReviewRevision string    `json:"reviewRevision"`
}

type catalogViewReceipt struct {
	observation  catalogViewObservation
	original     transportorigin.Origin
	relationship resourcegrant.Relationship
	process      string
	consumed     bool
}

// Prepared under Core.op; no source reads or encoding remain at admission.
type catalogViewDisclosure struct {
	payload      []byte
	fence        *resourcegrant.CatalogViewFence
	selector     resourcegrant.CatalogViewSelector
	relationship resourcegrant.Relationship
	serviceIDs   []string
}

// Core.op is held. This builds only the granted, still-active generations.
// Connection membership is shown separately and never created by viewing.
func (c *Core) catalogViewServices(record resourcegrant.CatalogViewRecord) ([]catalogViewService, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]catalogViewService, 0, len(record.ServiceIDs))
	for _, id := range record.ServiceIDs {
		var selected *activeService
		for _, a := range c.active {
			if a.discoveryID == id {
				selected = a
				break
			}
		}
		if selected == nil || selected.spec.Direction != "share" || !selected.spec.Discoverable || selected.guard() != nil {
			return nil, catalogViewStale()
		}
		a := selected
		expires, lifetime := a.expires, a.spec.Lifetime
		if lease := a.leaseExpires.Load(); lease != nil && (expires.IsZero() || lease.Before(expires)) {
			expires, lifetime = *lease, "finite"
		}
		purpose := a.spec.Purpose
		if purpose == "" || !validServicePurpose(purpose) {
			purpose = "generic"
		}
		service := RemoteService{ID: a.discoveryID, Purpose: purpose, Network: a.spec.Network, Ports: a.effective.String(), ExpiresAt: expires, Lifetime: lifetime, Application: "unverified"}
		if validateRemote(service) != nil {
			return nil, catalogViewStale()
		}
		out = append(out, catalogViewService{Service: service, ConnectAllowed: slices.Contains(a.spec.PeerIDs, record.Relationship.PeerKey)})
	}
	return out, nil
}

// Incoming identity comes from the existing owned HTTP request, not JSON.
// Comparison with today's selection verifies the original owner; it never
// substitutes the current origin for that original authenticated connection.
func (c *Core) catalogViewTransportCurrent(ctx context.Context, peer string, original transportorigin.Origin) bool {
	backend, ok := c.nodeCopy().(*directLANBackend)
	if !ok || backend == nil || backend.Node == nil || original == nil || original.Identity() == nil || channelClosed(original.StopRequested()) || ctx.Err() != nil {
		return false
	}
	completion := backend.currentCompletion()
	if completion == nil || !completion.activationCurrent() || !completion.coreCurrent(peer) {
		return false
	}
	selected, err := backend.Node.CapturePeer(peer)
	return err == nil && selected.Origin() != nil && selected.Origin().Identity() == original.Identity()
}

// Called under Core.op after current owned saved-state recertification.
func (c *Core) prepareCatalogView(selector resourcegrant.CatalogViewSelector, peer string) (catalogViewReply, *resourcegrant.CatalogViewFence, error) {
	g := c.catalogViews
	var empty catalogViewReply
	if selector.Validate() != nil || selector.Target != g.state.Target || g.timeUncertain {
		return empty, nil, catalogViewUnavailable()
	}
	var record resourcegrant.CatalogViewRecord
	for _, r := range g.state.Records {
		if r.ID == selector.GrantID {
			record = r
			break
		}
	}
	if record.ID == "" || record.Revision != selector.GrantRevision {
		return empty, nil, catalogViewStale()
	}
	if record.Relationship.PeerKey != peer {
		return empty, nil, catalogViewUnavailable()
	}
	if record.State == resourcegrant.Revoked {
		return empty, nil, &localCommandError{"catalog_view_revoked", "catalog view permission was revoked; request explicit approval again"}
	}
	if record.State == resourcegrant.Expired {
		return empty, nil, &localCommandError{"catalog_view_expired", "catalog view permission expired; request explicit approval again"}
	}
	relationship, err := c.resourceGrantRelationship(peer)
	if err != nil {
		return empty, nil, catalogViewUnavailable()
	}
	if relationship != record.Relationship {
		g.closeFences()
		return empty, nil, catalogViewStale()
	}
	services, err := c.catalogViewServices(record)
	if err != nil {
		return empty, nil, err
	}
	if g.fence == nil {
		g.fence, err = resourcegrant.NewCatalogViewFenceBefore(record, g.bootDeadline)
		if err != nil {
			return empty, nil, catalogViewUnavailable()
		}
	}
	return catalogViewReply{Selector: selector, Services: services}, g.fence, nil
}

func (c *Core) serveCatalogView(w http.ResponseWriter, r *http.Request, peer string) {
	// Only the concrete owned peer response can admit a bounded disclosure.
	response, ok := w.(*peerIncomingResponse)
	h, owned := r.Context().Value(peerIncomingRequestKey{}).(*peerIncomingRequest)
	if !ok || !owned {
		peerFailure(w, http.StatusForbidden)
		return
	}
	h.mu.Lock()
	original := h.origin
	h.mu.Unlock()
	var request json.RawMessage
	var selector resourcegrant.CatalogViewSelector
	if readJSON(r, 4096, &request) != nil || resource.Decode(request, 4096, &selector) != nil || selector.Validate() != nil {
		peerFailure(w, http.StatusBadRequest)
		return
	}
	// Decode exact keys, duplicate/null keys included, before any source read.
	// readJSON's bounded shape supplies only the selector; no peer self-claim.
	c.op.Lock()
	disclosure := catalogViewDisclosure{selector: selector}
	err := c.withCatalogViewState(false, func(*resourcePathBinding) error {
		if !c.catalogViewTransportCurrent(r.Context(), peer, original) {
			return catalogViewUnavailable()
		}
		prepared, f, err := c.prepareCatalogView(selector, peer)
		if err != nil {
			return err
		}
		disclosure.payload, err = json.Marshal(prepared)
		if err != nil || int64(len(disclosure.payload)) > min(c.limit("resources", "discoveryBytes"), c.limit("resources", "pageBytes")) {
			return &localCommandError{"catalog_view_limited", "catalog view exceeds the configured response budget"}
		}
		disclosure.fence = f
		disclosure.relationship = c.catalogViews.state.ActiveRecord().Relationship
		for _, row := range prepared.Services {
			disclosure.serviceIDs = append(disclosure.serviceIDs, row.Service.ID)
		}
		return nil
	})
	if err != nil {
		c.op.Unlock()
		peerFailure(w, http.StatusForbidden)
		return
	}
	current := r.Context().Err() == nil && c.catalogViewTransportCurrent(r.Context(), peer, original) && c.catalogViewSelectionCurrent(c.catalogViews.state.ActiveRecord())
	c.finishCatalogViewDisclosure(response, r.Context(), original, disclosure, current)
}

// The caller holds Core.op and has just checked current transport and selection.
// Release both the original publication permit and Core.op before network I/O.
func (c *Core) finishCatalogViewDisclosure(response *peerIncomingResponse, ctx context.Context, original transportorigin.Origin, disclosure catalogViewDisclosure, current bool) {
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("X-Content-Type-Options", "nosniff")
	raw, borrowErr := response.borrow(true)
	// All source reads and encoding precede this final admission. Revoke closes
	// the same fence under op before any fallible save. The single bounded write
	// admitted here may finish. An ordinary request lease or a sampled stop
	// channel cannot order this admission against transport retirement.
	admitted := false
	if borrowErr == nil && current && ctx.Err() == nil && original != nil {
		permit, err := original.AcquirePublication(ctx)
		if err == nil {
			admitted = ctx.Err() == nil && disclosure.fence.Admit(disclosure.selector, disclosure.relationship, disclosure.serviceIDs)
			permit.Release()
		}
	}
	c.op.Unlock()
	if borrowErr != nil {
		return
	}
	if !admitted {
		response.returned()
		peerFailure(response, http.StatusForbidden)
		return
	}
	raw.WriteHeader(http.StatusOK)
	_, _ = raw.Write(disclosure.payload)
	response.returned()
}

func (c *Core) remoteCatalogViewCommand(ctx context.Context, raw json.RawMessage) (any, error) {
	var input resourcegrant.RemoteCatalogViewInput
	if resource.Decode(raw, 4096, &input) != nil || input.Validate() != nil {
		return nil, catalogViewInvalid()
	}
	// Capture the chosen relationship and concrete backend before network work.
	c.op.Lock()
	c.catalogViewReceived = nil
	backend, ok := c.nodeCopy().(*directLANBackend)
	relationship, err := c.resourceGrantRelationship(input.PeerKey)
	c.op.Unlock()
	if !ok || backend == nil || backend.Node == nil || err != nil {
		return nil, catalogViewUnavailable()
	}
	peer, err := backend.Node.CapturePeer(input.PeerKey)
	if err != nil || peer.Origin() == nil {
		return nil, catalogViewUnavailable()
	}
	original := peer.Origin()
	call, application, err := c.peerHTTPApplication(ctx)
	if err != nil {
		return nil, catalogViewUnavailable()
	}
	defer application.finish()
	call, cancel := context.WithTimeout(call, 8*time.Second)
	defer cancel()
	var result catalogViewReply
	if err := c.peerJSON(call, input.PeerKey, "POST", catalogViewPath, input.Request, &result); err != nil {
		return nil, err
	}
	c.op.Lock()
	defer c.op.Unlock()
	current, err := c.resourceGrantRelationship(input.PeerKey)
	actual := application.discoveryOrigin()
	if err != nil || current != relationship || c.nodeCopy() != backend || actual == nil || actual.Identity() != original.Identity() || !c.catalogViewTransportCurrent(ctx, input.PeerKey, original) || result.Selector != input.Request || result.Services == nil || len(result.Services) > resourcegrant.MaxRecords {
		return nil, catalogViewStale()
	}
	previous := ""
	for _, row := range result.Services {
		if !resource.ValidID(row.Service.ID) || row.Service.ID <= previous || validateRemote(row.Service) != nil {
			return nil, catalogViewStale()
		}
		previous = row.Service.ID
	}
	revision, err := newResourceID()
	if err != nil || !resource.ValidID(c.resourceNonce) {
		return nil, catalogViewUnavailable()
	}
	observation := catalogViewObservation{catalogViewReply: result, PeerKey: input.PeerKey, CheckedAt: time.Now(), ReviewRevision: revision}
	retained := observation
	retained.Services = append([]catalogViewService{}, result.Services...)
	receipt := &catalogViewReceipt{observation: retained, original: original, relationship: relationship, process: c.resourceNonce}
	if err := c.publishCatalogViewReceipt(call, receipt); err != nil {
		return nil, err
	}
	// This result and its original attribution are separate from historical
	// discovery cache. They do not relabel legacy catalog rows current or grant
	// service.connect permission. A connection must re-fetch this exact grant.
	return observation, nil
}

// Core.op is held after the original transport/current-selection checks. The
// HTTP result's earlier private copy does not admit this separate publication.
func (c *Core) publishCatalogViewReceipt(ctx context.Context, receipt *catalogViewReceipt) error {
	permit, err := receipt.original.AcquirePublication(ctx)
	if err != nil {
		return catalogViewStale()
	}
	defer permit.Release()
	if ctx.Err() != nil {
		return catalogViewStale()
	}
	c.catalogViewReceived = receipt
	return nil
}
