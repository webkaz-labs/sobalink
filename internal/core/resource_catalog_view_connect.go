package core

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/webkaz-labs/sobalink/internal/resource"
	"github.com/webkaz-labs/sobalink/internal/resourcegrant"
)

// This process-local review reference is not a credential. Its owning receipt
// supplies original transport attribution and is consumed before any start.
type catalogViewConnection struct {
	ReviewRevision string `json:"reviewRevision"`
	ServiceID      string `json:"serviceId"`
}
type catalogViewConnectKey struct{}
type catalogViewConnectProof struct {
	receipt *catalogViewReceipt
	row     catalogViewService
}

func (p *catalogViewConnectProof) current() bool {
	return p != nil && p.receipt != nil && p.receipt.original != nil && p.receipt.original.Identity() != nil && !channelClosed(p.receipt.original.StopRequested()) && p.row.ConnectAllowed && validateRemote(p.row.Service) == nil
}

func catalogViewConnectInput(raw json.RawMessage) (*catalogViewConnection, error) {
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil {
		return nil, catalogViewInvalid()
	}
	value, present := object["catalogView"]
	if !present {
		return nil, nil
	}
	var in catalogViewConnection
	if resource.Decode(value, 4096, &in) != nil || !resource.ValidID(in.ReviewRevision) || !resource.ValidID(in.ServiceID) {
		return nil, catalogViewInvalid()
	}
	return &in, nil
}

// Network retrieval does not hold op and never replaces the original review.
// Its actual application origin must match the retained publication origin.
func (c *Core) fetchCatalogViewForConnect(ctx context.Context, r *catalogViewReceipt) (catalogViewReply, error) {
	var result catalogViewReply
	call, application, err := c.peerHTTPApplication(ctx)
	if err != nil {
		return result, catalogViewUnavailable()
	}
	defer application.finish()
	call, cancel := context.WithTimeout(call, 8*time.Second)
	defer cancel()
	if err := c.peerJSON(call, r.observation.PeerKey, "POST", catalogViewPath, r.observation.Selector, &result); err != nil {
		return result, err
	}
	actual := application.discoveryOrigin()
	if actual == nil || r.original == nil || actual.Identity() != r.original.Identity() || channelClosed(r.original.StopRequested()) {
		return result, catalogViewStale()
	}
	return result, nil
}

func catalogViewSelectedRow(r *catalogViewReceipt, in *catalogViewConnection) (catalogViewService, bool) {
	if r == nil || r.consumed || in == nil || r.observation.ReviewRevision != in.ReviewRevision {
		return catalogViewService{}, false
	}
	for _, row := range r.observation.Services {
		if row.Service.ID == in.ServiceID {
			return row, true
		}
	}
	return catalogViewService{}, false
}
func catalogViewRefetchedRow(r *catalogViewReceipt, selected catalogViewService, result catalogViewReply) (catalogViewService, bool) {
	if result.Selector != r.observation.Selector || result.Services == nil || len(result.Services) > resourcegrant.MaxRecords {
		return catalogViewService{}, false
	}
	previous := ""
	found := false
	for _, row := range result.Services {
		if !resource.ValidID(row.Service.ID) || row.Service.ID <= previous || validateRemote(row.Service) != nil {
			return catalogViewService{}, false
		}
		previous = row.Service.ID
		if row.Service.ID == selected.Service.ID {
			if row != selected || !row.ConnectAllowed {
				return catalogViewService{}, false
			}
			found = true
		}
	}
	return selected, found
}

// executeCommand calls this before taking its normal mutation lock. Normal
// request deduplication still surrounds it, including a failed/unknown result.
func (c *Core) connectCatalogViewCommand(ctx context.Context, raw json.RawMessage, in *catalogViewConnection) (any, error) {
	c.op.Lock()
	receipt := c.catalogViewReceived
	row, ok := catalogViewSelectedRow(receipt, in)
	if !ok || receipt.process != c.resourceNonce || !row.ConnectAllowed || validateRemote(row.Service) != nil || ctx.Err() != nil || c.ctx.Err() != nil {
		c.op.Unlock()
		return nil, catalogViewStale()
	}
	relationship, err := c.resourceGrantRelationship(receipt.observation.PeerKey)
	current := err == nil && relationship == receipt.relationship && c.catalogViewTransportCurrent(ctx, receipt.observation.PeerKey, receipt.original)
	c.op.Unlock()
	if !current {
		return nil, catalogViewStale()
	}
	result, err := c.fetchCatalogViewForConnect(ctx, receipt)
	if err != nil {
		return nil, err
	}
	c.op.Lock()
	defer c.op.Unlock()
	selected, ok := catalogViewSelectedRow(c.catalogViewReceived, in)
	relationship, err = c.resourceGrantRelationship(receipt.observation.PeerKey)
	if c.catalogViewReceived != receipt || !ok || selected != row || receipt.process != c.resourceNonce || err != nil || relationship != receipt.relationship || !c.catalogViewTransportCurrent(ctx, receipt.observation.PeerKey, receipt.original) || ctx.Err() != nil || c.ctx.Err() != nil {
		return nil, catalogViewStale()
	}
	row, ok = catalogViewRefetchedRow(receipt, row, result)
	if !ok {
		return nil, catalogViewStale()
	}
	// Consume even if persistence or listener startup reports an unknown result.
	// A retry must fetch and review a new receipt rather than repeat a save/start.
	receipt.consumed = true
	proof := &catalogViewConnectProof{receipt: receipt, row: row}
	return c.startServiceCommand(context.WithValue(ctx, catalogViewConnectKey{}, proof), "service.connect", raw)
}

func (p *catalogViewConnectProof) matchesInput(peer, network, ports, purpose, serviceID, revision string, remotePort int) bool {
	if !p.current() {
		return false
	}
	if ports == "" && remotePort > 0 {
		ports = strconv.Itoa(remotePort)
	}
	if purpose == "" {
		purpose = "generic"
	}
	return peer == p.receipt.observation.PeerKey && network == p.row.Service.Network && ports == p.row.Service.Ports && purpose == p.row.Service.Purpose && (serviceID == "" || serviceID == p.row.Service.ID) && (revision == "" || revision == encodeDiscoveryReview(peer, p.row.Service, p.receipt.observation.CheckedAt))
}

// One bounded watcher belongs to the existing service lifetime. Stopping the
// original generation or reaching its advertised deadline cancels established
// streams as well as blocking future dials; a guard alone cannot stop keepalive.
func (c *Core) watchCatalogConnection(a *activeService) error {
	done, err := c.beginWork()
	if err != nil {
		return err
	}
	var timer *time.Timer
	var expires <-chan time.Time
	if !a.catalogExpires.IsZero() {
		timer = time.NewTimer(time.Until(a.catalogExpires))
		expires = timer.C
	}
	go func() {
		defer done()
		if timer != nil {
			defer timer.Stop()
		}
		select {
		case <-a.ctx.Done():
			return
		case <-a.catalogOrigin.StopRequested():
		case <-expires:
		}
		a.ready.Store(false)
		a.cancel()
	}()
	return nil
}
