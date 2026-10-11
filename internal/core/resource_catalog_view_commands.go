package core

import (
	"context"
	"encoding/json"
	"os"
	"time"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/resource"
	"github.com/webkaz-labs/sobalink/internal/resourcegrant"
)

func catalogViewInvalid() error {
	return &localCommandError{"catalog_view_invalid", "select the exact peer, active shared service IDs, finite expiry and explicit reviewed confirmation"}
}
func catalogViewUnavailable() error {
	return &localCommandError{"catalog_view_unavailable", "catalog view permission is unavailable; check the owning agent and private state"}
}
func catalogViewStale() error {
	return &localCommandError{"catalog_view_stale", "catalog view review changed; refresh and review the current peer and shared services"}
}

// Core.op is held. This binding is independent of the inspection/management
// grant store: enabling or reducing catalog access never opens that store.
func (c *Core) withCatalogViewState(preview bool, fn func(*resourcePathBinding) error) error {
	g := c.catalogViews
	if g == nil || g.frozen || c.resourceLock == nil || c.ctx == nil || c.ctx.Err() != nil {
		return catalogViewUnavailable()
	}
	var commandErr error
	err := c.resourceLock.WithOwnershipInfo(c.dir, func(directory, lock os.FileInfo) error {
		b, err := openResourcePathBinding(c.dir, directory, lock)
		if err != nil {
			return err
		}
		defer b.close()
		if c.resourceDirectoryIdentity == nil || !os.SameFile(c.resourceDirectoryIdentity, b.journalInfo) {
			return errResourceBinding
		}
		if err := c.currentCatalogViewsBound(b); err != nil {
			return err
		}
		if preview {
			_ = g.observe(time.Now())
		} else if err := c.observeCatalogViewsBound(b, time.Now()); err != nil {
			return err
		}
		commandErr = fn(b)
		return b.check()
	})
	if err != nil {
		g.freeze()
		return catalogViewUnavailable()
	}
	return commandErr
}

func (c *Core) catalogViewLocalView() resourcegrant.CatalogViewLocalView {
	g := c.catalogViews
	records := append([]resourcegrant.CatalogViewRecord{}, g.state.Records...)
	for i := range records {
		records[i].ServiceIDs = append([]string{}, records[i].ServiceIDs...)
	}
	return resourcegrant.CatalogViewLocalView{Target: g.state.Target, Records: records, InitializesState: g.firstUse, TimeUncertain: g.timeUncertain}
}

func (c *Core) catalogViewCommand(ctx context.Context, name string, raw json.RawMessage) (any, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	var result any
	err := c.withCatalogViewState(name == "resource.catalog.view.preview", func(b *resourcePathBinding) error {
		var err error
		switch name {
		case "resource.catalog.view.inspect":
			var in struct {
				Target resource.Target `json:"target"`
			}
			if resource.Decode(raw, 4096, &in) != nil || in.Target != c.catalogViews.state.Target {
				return catalogViewInvalid()
			}
			result = c.catalogViewLocalView()
		case "resource.catalog.view.preview":
			result, err = c.previewCatalogView(raw, time.Now())
		case "resource.catalog.view.confirm":
			result, err = c.confirmCatalogView(ctx, raw, b, time.Now())
		case "resource.catalog.view.revoke":
			result, err = c.revokeCatalogView(ctx, raw, b)
		default:
			err = catalogViewInvalid()
		}
		return err
	})
	return result, err
}

func (c *Core) canCreateCatalogView() bool {
	g := c.catalogViews
	return !g.timeUncertain && g.state.HighWater != nil && *g.state.HighWater < uint64(capacity.MaxJSONInteger)-1 && len(g.state.Records) < resourcegrant.MaxRecords && g.state.ActiveRecord().ID == ""
}

func (c *Core) catalogViewReviewHash(r resourcegrant.CatalogViewReview) string {
	return resourceDigest(struct {
		Domain, Nonce string
		Review        resourcegrant.CatalogViewReview
	}{"catalog-view-review-v1", c.resourceNonce, resourcegrant.CatalogViewReview{Grant: r.Grant, BaseRevision: r.BaseRevision, InitializesState: r.InitializesState}})
}

func (c *Core) previewCatalogView(raw json.RawMessage, now time.Time) (any, error) {
	var in resourcegrant.CatalogViewInputs
	g := c.catalogViews
	if resource.Decode(raw, resourcegrant.MaxBytes, &in) != nil || in.Target != g.state.Target || in.ExpiresAt <= now.Unix() {
		return nil, catalogViewInvalid()
	}
	if !c.canCreateCatalogView() {
		return nil, &localCommandError{"catalog_view_conflict", "revoke the existing catalog view grant before approving another"}
	}
	relationship, err := c.resourceGrantRelationship(in.PeerKey)
	if err != nil {
		return nil, catalogViewUnavailable()
	}
	id, err := newResourceID()
	if err != nil {
		return nil, catalogViewUnavailable()
	}
	record := resourcegrant.CatalogViewRecord{ID: id, Revision: *g.state.HighWater + 1, Target: in.Target, Relationship: relationship, ServiceIDs: append([]string{}, in.ServiceIDs...), IssuedAt: now.Unix(), ExpiresAt: in.ExpiresAt, State: resourcegrant.Active}
	if record.Validate() != nil {
		return nil, catalogViewInvalid()
	}
	if !c.catalogViewSelectionCurrent(record) {
		return nil, catalogViewStale()
	}
	review := resourcegrant.CatalogViewReview{Grant: record, BaseRevision: resourceDigest(g.state), InitializesState: g.firstUse}
	review.ReviewRevision = c.catalogViewReviewHash(review)
	return review, nil
}

func (c *Core) confirmCatalogView(ctx context.Context, raw json.RawMessage, b *resourcePathBinding, now time.Time) (any, error) {
	var in resourcegrant.CatalogViewConfirmation
	g := c.catalogViews
	if resource.Decode(raw, resourcegrant.MaxBytes, &in) != nil || !in.Confirm {
		return nil, catalogViewInvalid()
	}
	r, record := in.Review, in.Review.Grant
	if !c.canCreateCatalogView() || r.InitializesState != g.firstUse || record.Target != g.state.Target || record.State != resourcegrant.Active || record.Revision != *g.state.HighWater+1 || record.IssuedAt > now.Unix() || record.ExpiresAt <= now.Unix() || r.BaseRevision != resourceDigest(g.state) || r.ReviewRevision != c.catalogViewReviewHash(r) {
		return nil, catalogViewStale()
	}
	for _, previous := range g.state.Records {
		if previous.ID == record.ID {
			return nil, catalogViewStale()
		}
	}
	relationship, err := c.resourceGrantRelationship(record.Relationship.PeerKey)
	if err != nil || relationship != record.Relationship || !c.catalogViewSelectionCurrent(record) || ctx.Err() != nil {
		return nil, catalogViewStale()
	}
	next := g.state
	high := record.Revision
	next.HighWater = &high
	next.Records = append(append([]resourcegrant.CatalogViewRecord{}, g.state.Records...), record)
	if next.Validate() != nil {
		return nil, catalogViewInvalid()
	}
	if g.firstUse {
		if err := c.initializeCatalogViewDirectoryBound(b); err != nil {
			return nil, catalogViewUnavailable()
		}
	}
	if ctx.Err() != nil || c.ctx.Err() != nil {
		g.freeze()
		return nil, catalogViewUnavailable()
	}
	if err := c.writeCatalogViewsBound(next, b); err != nil {
		return nil, catalogViewUnavailable()
	}
	g.anchorBootDeadline(now)
	if err := c.observeCatalogViewsBound(b, time.Now()); err != nil {
		return nil, catalogViewUnavailable()
	}
	// Only durable state may become eligible for final disclosure. The gate is
	// lazy and still requires current managed transport and service generations.
	return c.catalogViewLocalView(), nil
}

func (c *Core) revokeCatalogView(ctx context.Context, raw json.RawMessage, b *resourcePathBinding) (any, error) {
	var in resourcegrant.GrantRevoke
	g := c.catalogViews
	if resource.Decode(raw, 4096, &in) != nil || !in.Confirm || in.Target != g.state.Target {
		return nil, catalogViewInvalid()
	}
	index := -1
	for i, record := range g.state.Records {
		if record.ID == in.GrantID {
			index = i
			break
		}
	}
	if index < 0 || g.state.Records[index].Revision != in.GrantRevision {
		return nil, catalogViewStale()
	}
	record := g.state.Records[index]
	if record.State == resourcegrant.Revoked || record.State == resourcegrant.Expired {
		return c.catalogViewLocalView(), nil
	}
	g.closeFences()
	if *g.state.HighWater >= uint64(capacity.MaxJSONInteger) || ctx.Err() != nil {
		return nil, catalogViewUnavailable()
	}
	record.State, record.Revision = resourcegrant.Revoked, *g.state.HighWater+1
	next := g.state
	high := record.Revision
	next.HighWater = &high
	next.Records = make([]resourcegrant.CatalogViewRecord, 0, len(g.state.Records))
	for i, retained := range g.state.Records {
		if i != index {
			next.Records = append(next.Records, retained)
		}
	}
	next.Records = append(next.Records, record)
	if err := c.writeCatalogViewsBound(next, b); err != nil {
		return nil, catalogViewUnavailable()
	}
	return c.catalogViewLocalView(), nil
}

// Core.op is held; share identity is its existing activation ID. Viewer
// membership in service PeerIDs is deliberately not a condition for viewing.
func (c *Core) catalogViewSelectionCurrent(record resourcegrant.CatalogViewRecord) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, id := range record.ServiceIDs {
		found := false
		for _, a := range c.active {
			if a.discoveryID == id && a.spec.Direction == "share" && a.spec.Discoverable && a.guard() == nil {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// Whole-peer removal reduces this independent grant as well. Close before
// ownership or persistence can fail, and never create pristine sidecar state.
func (c *Core) revokeCatalogViewPeerPermission(ctx context.Context, peer string) error {
	g := c.catalogViews
	if g == nil {
		return nil
	}
	record := g.state.ActiveRecord()
	if record.ID == "" || record.Relationship.PeerKey != peer {
		return nil
	}
	g.closeFences()
	return c.withCatalogViewState(false, func(b *resourcePathBinding) error {
		record := g.state.ActiveRecord()
		if record.ID == "" || record.Relationship.PeerKey != peer {
			return nil
		}
		raw, err := json.Marshal(resourcegrant.GrantRevoke{Target: record.Target, GrantID: record.ID, GrantRevision: record.Revision, Confirm: true})
		if err != nil {
			return err
		}
		_, err = c.revokeCatalogView(ctx, raw, b)
		return err
	})
}
