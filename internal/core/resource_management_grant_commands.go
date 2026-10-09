package core

import (
	"context"
	"encoding/json"
	"time"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/resource"
	"github.com/webkaz-labs/sobalink/internal/resourcegrant"
)

func isResourceGrantPreview(name string) bool {
	return name == "resource.grant.preview" || name == "resource.grant.management.preview"
}

// grantRecordData is lifetime/revocation bookkeeping only. Management records
// keep their full actions, so they remain invalid to the inspection validator.
func grantRecordData(e resourcegrant.Envelope) []resourcegrant.Record {
	records := append([]resourcegrant.Record{}, e.Records...)
	for _, m := range e.ManagementRecords {
		records = append(records, m.Record)
	}
	return records
}

func (c *Core) managementGrantReviewHash(review resourcegrant.ManagementGrantReview) string {
	return resourceDigest(struct {
		Domain, Nonce, Base string
		Initialize, Upgrade bool
		Grant               resourcegrant.ManagementRecord
	}{"resource-management-grant-review-v1", c.resourceNonce, review.BaseRevision, review.InitializesState, review.UpgradesFormat, review.Grant})
}

func (c *Core) previewManagementGrant(raw json.RawMessage, now time.Time) (any, error) {
	var input resourcegrant.ManagementGrantInputs
	g := c.resourceGrants
	if resource.Decode(raw, 4096, &input) != nil || input.Scope != resourcegrant.Management {
		return nil, resourceGrantInvalid()
	}
	in := input.Inputs
	if in.Target != g.state.Target || !resource.ValidDigest(in.PeerKey) || in.ExpiresAt <= now.Unix() {
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
	record := resourcegrant.ManagementRecord{Scope: resourcegrant.Management, Record: resourcegrant.Record{ID: id, Revision: *g.state.HighWater + 1, Target: in.Target, ResourceType: resource.Type, Relationship: relationship, Actions: in.Actions, Fields: in.Fields, IssuedAt: now.Unix(), ExpiresAt: in.ExpiresAt, State: resourcegrant.Active}}
	if record.Validate() != nil {
		return nil, resourceGrantInvalid()
	}
	review := resourcegrant.ManagementGrantReview{Grant: record, BaseRevision: resourceDigest(g.state), InitializesState: g.firstUse, UpgradesFormat: g.state.Version == resourcegrant.Version}
	review.ReviewRevision = c.managementGrantReviewHash(review)
	return review, nil
}

func (c *Core) confirmManagementGrant(ctx context.Context, raw json.RawMessage, b *resourcePathBinding, now time.Time) (any, error) {
	var in resourcegrant.ManagementGrantConfirmation
	g := c.resourceGrants
	if resource.Decode(raw, 8192, &in) != nil || !in.Confirm || in.Review.Grant.Validate() != nil {
		return nil, resourceGrantInvalid()
	}
	review := in.Review
	record := review.Grant.Record
	if !c.canCreateResourceGrant() || review.InitializesState != g.firstUse || review.UpgradesFormat != (g.state.Version == resourcegrant.Version) || record.Target != g.state.Target || record.State != resourcegrant.Active || record.Revision != *g.state.HighWater+1 || record.IssuedAt > now.Unix() || record.ExpiresAt <= now.Unix() || review.BaseRevision != resourceDigest(g.state) || review.ReviewRevision != c.managementGrantReviewHash(review) {
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
	next.Version = resourcegrant.ManagementVersion
	next.ManagementRecords = append(append([]resourcegrant.ManagementRecord{}, g.state.ManagementRecords...), review.Grant)
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
	// This is the only version-2-to-3 publication. Decoding, preview and startup
	// never migrate or expand permission. Publication does not mint authority.
	if err := c.writeResourceGrantsBound(next, b); err != nil {
		return nil, resourceGrantUnavailable()
	}
	g.anchorBootDeadline(now)
	if err := c.observeResourceGrantsBound(b, time.Now()); err != nil {
		return nil, resourceGrantUnavailable()
	}
	g.closeFences()
	g.retireRuntime()
	g.activation = "not_started"
	return c.resourceGrantView(), nil
}

func (c *Core) revokeManagementGrant(ctx context.Context, in resourcegrant.GrantRevoke, b *resourcePathBinding) (any, error) {
	g := c.resourceGrants
	index := -1
	for i, m := range g.state.ManagementRecords {
		if m.Record.ID == in.GrantID {
			index = i
			break
		}
	}
	if index < 0 || g.state.ManagementRecords[index].Record.Revision != in.GrantRevision {
		return nil, resourceGrantStale()
	}
	managed := g.state.ManagementRecords[index]
	record := managed.Record
	if record.State == resourcegrant.Revoked || record.State == resourcegrant.Expired {
		return c.resourceGrantView(), nil
	}
	g.closeFences()
	g.retireRuntime()
	if *g.state.HighWater >= uint64(capacity.MaxJSONInteger) || ctx.Err() != nil || c.ctx.Err() != nil {
		return nil, resourceGrantUnavailable()
	}
	record.State, record.Revision = resourcegrant.Revoked, *g.state.HighWater+1
	managed.Record = record
	next := g.state
	high := record.Revision
	next.HighWater = &high
	next.ManagementRecords = make([]resourcegrant.ManagementRecord, 0, len(g.state.ManagementRecords))
	next.ManagementRecords = append(next.ManagementRecords, g.state.ManagementRecords[:index]...)
	next.ManagementRecords = append(next.ManagementRecords, g.state.ManagementRecords[index+1:]...)
	next.ManagementRecords = append(next.ManagementRecords, managed)
	if err := c.writeResourceGrantsBound(next, b); err != nil {
		return nil, resourceGrantUnavailable()
	}
	return c.resourceGrantView(), nil
}
