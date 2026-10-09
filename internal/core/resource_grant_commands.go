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

type resourceGrantInputs = resourcegrant.GrantInputs
type resourceGrantReview = resourcegrant.GrantReview
type resourceGrantConfirmation = resourcegrant.GrantConfirmation
type resourceGrantRevoke = resourcegrant.GrantRevoke
type resourceGrantLocalView = resourcegrant.LocalGrantView

func resourceGrantUnavailable() error {
	return &localCommandError{"resource_grant_unavailable", "resource grants are unavailable; check the owning agent and private state"}
}
func resourceGrantInvalid() error {
	return &localCommandError{"resource_grant_invalid", "provide the exact one-peer grant scope, finite expiry and explicit reviewed confirmation"}
}
func resourceGrantStale() error {
	return &localCommandError{"resource_grant_stale", "the grant review is no longer current; inspect the grant and review again"}
}

// These commands are only in the existing authenticated LOCAL dispatcher.
// The remote wire protocol has no grant command or local-dispatch bridge.
func (c *Core) resourceGrantCommand(ctx context.Context, name string, raw json.RawMessage) (any, error) {
	g := c.resourceGrants
	if g == nil || g.frozen || c.resourceIdentity == "" || c.resourceLock == nil || ctx.Err() != nil {
		return nil, resourceGrantUnavailable()
	}
	var result any
	var commandErr error
	err := c.resourceLock.WithOwnershipInfo(c.dir, func(directory, lock os.FileInfo) error {
		b, err := openResourcePathBinding(c.dir, directory, lock)
		if err != nil {
			return err
		}
		defer b.close()
		if c.resourceDirectoryIdentity == nil || !os.SameFile(c.resourceDirectoryIdentity, b.journalInfo) {
			return resourcegrant.ErrInvalid
		}
		if err := c.currentResourceGrantsBound(b); err != nil {
			return err
		}
		if isResourceGrantPreview(name) {
			_ = g.observe(time.Now()) // safety denial only; preview never writes saved state
		} else if err := c.observeResourceGrantsBound(b, time.Now()); err != nil {
			return err
		}
		if name == "resource.grant.revoke" {
			result, commandErr = c.revokeResourceGrant(ctx, raw, b)
		} else {
			now := time.Now()
			if name != "resource.grant.inspect" {
				if err := g.observe(now); err != nil {
					commandErr = resourceGrantUnavailable()
					return b.check()
				}
			}
			switch name {
			case "resource.grant.inspect":
				var in struct {
					Target resource.Target `json:"target"`
				}
				if resource.Decode(raw, 4096, &in) != nil || in.Target != g.state.Target {
					commandErr = resourceGrantInvalid()
					break
				}
				result = c.resourceGrantView()
			case "resource.grant.management.preview":
				result, commandErr = c.previewManagementGrant(raw, now)
			case "resource.grant.management.confirm":
				result, commandErr = c.confirmManagementGrant(ctx, raw, b, now)
			case "resource.grant.preview":
				result, commandErr = c.previewResourceGrant(raw, now)
			case "resource.grant.confirm":
				result, commandErr = c.confirmResourceGrant(ctx, raw, b, now)
			default:
				commandErr = resourceGrantInvalid()
			}
		}
		return b.check()
	})
	if err != nil {
		g.freeze()
		return nil, resourceGrantUnavailable()
	}
	if commandErr == nil && !isResourceGrantPreview(name) {
		c.reconcileResourceInspection()
		result = c.resourceGrantView()
	}
	return result, commandErr
}

func (c *Core) resourceGrantView() resourceGrantLocalView {
	g := c.resourceGrants
	records := make([]resourcegrant.Record, len(g.state.Records))
	for i, r := range g.state.Records {
		r.Actions = append([]string(nil), r.Actions...)
		r.Fields = append([]string(nil), r.Fields...)
		records[i] = r
	}
	// Readiness describes exclusive listener ownership, never peer reachability.
	activation := g.activation
	if activation == "" {
		activation = "not_started"
	}
	ready := g.runtime != nil && !inspectionRuntimeDone(g.runtime) && g.runtime.ctx.Err() == nil || g.managementRuntime != nil && !managementRuntimeDone(g.managementRuntime) && g.managementRuntime.ctx.Err() == nil
	management := make([]resourcegrant.ManagementRecord, len(g.state.ManagementRecords))
	for i, m := range g.state.ManagementRecords {
		m.Record.Actions = append([]string(nil), m.Record.Actions...)
		m.Record.Fields = append([]string(nil), m.Record.Fields...)
		management[i] = m
	}
	return resourceGrantLocalView{Target: g.state.Target, Records: records, ManagementRecords: management, InitializesState: g.firstUse, ListenerReady: ready, Activation: activation, TimeUncertain: g.timeUncertain}
}

// The protected saved projection is eligibility only. Remote authentication
// still requires an accepted concrete managed application capability.
func (c *Core) resourceGrantRelationship(peer string, now time.Time) (resourcegrant.Relationship, error) {
	c.mu.RLock()
	s := c.directLAN
	mode := c.profile.Settings.Network
	closing := c.closing
	c.mu.RUnlock()
	if s == nil || mode != "direct-lan" || closing || c.ctx.Err() != nil {
		return resourcegrant.Relationship{}, resourceGrantUnavailable()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg, err := s.managedCurrentEndpointProjectionLocked(now)
	if err != nil {
		return resourcegrant.Relationship{}, resourceGrantUnavailable()
	}
	pair, ok := cfg.PairContexts[peer]
	if !ok {
		return resourcegrant.Relationship{}, resourceGrantUnavailable()
	}
	binding, err := pair.Binding()
	if err != nil {
		return resourcegrant.Relationship{}, resourceGrantUnavailable()
	}
	relationship := resourcegrant.Relationship{Backend: resourcegrant.Backend, TargetKey: cfg.Identity.PublicKey(), PeerKey: peer, PairBinding: binding}
	if relationship.Validate() != nil {
		return resourcegrant.Relationship{}, resourceGrantUnavailable()
	}
	return relationship, nil
}

func (c *Core) resourceGrantReviewHash(review resourceGrantReview) string {
	return resourceDigest(struct {
		Domain, Nonce, Base string
		Initialize          bool
		Grant               resourcegrant.Record
	}{"resource-inspection-grant-review-v1", c.resourceNonce, review.BaseRevision, review.InitializesState, review.Grant})
}
func (c *Core) canCreateResourceGrant() bool {
	g := c.resourceGrants
	if g.state.HighWater == nil || *g.state.HighWater >= uint64(capacity.MaxJSONInteger)-1 || len(g.state.Records)+len(g.state.ManagementRecords) >= resourcegrant.MaxRecords {
		return false
	}
	if record, _ := g.state.ActiveRecord(); record.ID != "" {
		return false
	}
	return true
}
func (c *Core) previewResourceGrant(raw json.RawMessage, now time.Time) (any, error) {
	var in resourceGrantInputs
	g := c.resourceGrants
	if resource.Decode(raw, 4096, &in) != nil || in.Target != g.state.Target || !resource.ValidDigest(in.PeerKey) || in.ExpiresAt <= now.Unix() {
		return nil, resourceGrantInvalid()
	}
	if !c.canCreateResourceGrant() {
		return nil, &localCommandError{"resource_grant_conflict", "revoke the existing grant first; retained grant capacity or revision exhaustion requires explicit recovery"}
	}
	relationship, err := c.resourceGrantRelationship(in.PeerKey, now)
	if err != nil {
		return nil, err
	}
	id, err := newResourceID()
	if err != nil {
		return nil, resourceGrantUnavailable()
	}
	record := resourcegrant.Record{ID: id, Revision: *g.state.HighWater + 1, Target: in.Target, ResourceType: resource.Type, Relationship: relationship, Actions: in.Actions, Fields: in.Fields, IssuedAt: now.Unix(), ExpiresAt: in.ExpiresAt, State: resourcegrant.Active}
	if record.Validate() != nil {
		return nil, resourceGrantInvalid()
	}
	review := resourceGrantReview{Grant: record, BaseRevision: resourceDigest(g.state), InitializesState: g.firstUse}
	review.ReviewRevision = c.resourceGrantReviewHash(review)
	return review, nil
}
func (c *Core) confirmResourceGrant(ctx context.Context, raw json.RawMessage, b *resourcePathBinding, now time.Time) (any, error) {
	var in resourceGrantConfirmation
	g := c.resourceGrants
	if resource.Decode(raw, 8192, &in) != nil || !in.Confirm || in.Review.Grant.Validate() != nil {
		return nil, resourceGrantInvalid()
	}
	review := in.Review
	record := review.Grant
	if !c.canCreateResourceGrant() || review.InitializesState != g.firstUse || record.Target != g.state.Target || record.State != resourcegrant.Active || record.Revision != *g.state.HighWater+1 || record.IssuedAt > now.Unix() || record.ExpiresAt <= now.Unix() || review.BaseRevision != resourceDigest(g.state) || review.ReviewRevision != c.resourceGrantReviewHash(review) {
		return nil, resourceGrantStale()
	}
	for _, retained := range grantRecordData(g.state) {
		if retained.ID == record.ID {
			return nil, resourceGrantStale()
		}
	}
	relationship, err := c.resourceGrantRelationship(record.Relationship.PeerKey, now)
	if err != nil {
		return nil, err
	}
	if relationship != record.Relationship || ctx.Err() != nil || c.ctx.Err() != nil {
		return nil, resourceGrantStale()
	}
	next := g.state
	high := record.Revision
	next.HighWater = &high
	next.Records = append(append([]resourcegrant.Record{}, g.state.Records...), record)
	if next.Validate() != nil {
		return nil, resourceGrantInvalid()
	}
	if g.firstUse {
		if err := c.initializeResourceGrantDirectoryBound(b); err != nil {
			return nil, resourceGrantUnavailable()
		}
	}
	if ctx.Err() != nil || c.ctx.Err() != nil {
		g.freeze()
		return nil, resourceGrantUnavailable()
	}
	if err := c.writeResourceGrantsBound(next, b); err != nil {
		return nil, resourceGrantUnavailable()
	}
	// Anchor the original lifetime to the pre-publication monotonic observation.
	// A slow publication cannot add lifetime; recheck time before runtime admission.
	g.anchorBootDeadline(now)
	if err := c.observeResourceGrantsBound(b, time.Now()); err != nil {
		return nil, resourceGrantUnavailable()
	}
	return c.resourceGrantView(), nil
}
func (c *Core) revokeResourceGrant(ctx context.Context, raw json.RawMessage, b *resourcePathBinding) (any, error) {
	var in resourceGrantRevoke
	g := c.resourceGrants
	if resource.Decode(raw, 4096, &in) != nil || !in.Confirm || in.Target != g.state.Target || !resource.ValidID(in.GrantID) || in.GrantRevision == 0 {
		return nil, resourceGrantInvalid()
	}
	index := -1
	for i, record := range g.state.Records {
		if record.ID == in.GrantID {
			index = i
			break
		}
	}
	if index < 0 {
		return c.revokeManagementGrant(ctx, in, b)
	}
	if g.state.Records[index].Revision != in.GrantRevision {
		return nil, resourceGrantStale()
	}
	record := g.state.Records[index]
	if record.State == resourcegrant.Revoked || record.State == resourcegrant.Expired {
		return c.resourceGrantView(), nil
	}
	// Reduction never depends on peer connectivity, eligibility or grant time.
	// The runtime denial happens before every possible persistence failure.
	g.closeFences()
	g.retireRuntime()
	if *g.state.HighWater >= uint64(capacity.MaxJSONInteger) || ctx.Err() != nil || c.ctx.Err() != nil {
		return nil, resourceGrantUnavailable()
	}
	record.State = resourcegrant.Revoked
	record.Revision = *g.state.HighWater + 1
	next := g.state
	high := record.Revision
	next.HighWater = &high
	next.Records = make([]resourcegrant.Record, 0, len(g.state.Records))
	next.Records = append(next.Records, g.state.Records[:index]...)
	next.Records = append(next.Records, g.state.Records[index+1:]...)
	next.Records = append(next.Records, record)
	if err := c.writeResourceGrantsBound(next, b); err != nil {
		return nil, resourceGrantUnavailable()
	}
	return c.resourceGrantView(), nil
}
