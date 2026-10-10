package core

import (
	"context"
	"encoding/json"

	"github.com/webkaz-labs/sobalink/internal/resource"
	"github.com/webkaz-labs/sobalink/internal/resourcegroup"
)

// One immediate predecessor input belongs only to its resulting current
// prepared object. It is never persisted, serialized, or forwarded through a
// later replacement, and grants no admission independently of that object.
type resourceGroupSelectReceipt struct {
	input          resourcegroup.SelectInput
	resultID       string
	resultRevision string
}

func canonicalResourceGroupSelectInput(input resourcegroup.SelectInput) (resourcegroup.SelectInput, error) {
	data, err := json.Marshal(input)
	if err != nil {
		return resourcegroup.SelectInput{}, resourcegroup.ErrInvalid
	}
	return resourcegroup.DecodeSelectInput(data)
}

func newResourceGroupSelectReceipt(input resourcegroup.SelectInput, result *resourceGroupPrepared) (*resourceGroupSelectReceipt, error) {
	canonical, err := canonicalResourceGroupSelectInput(input)
	if err != nil || result == nil || !resource.ValidID(result.id) || result.id == canonical.ReviewID || result.review.Validate() != nil || result.state != resourcegroup.AdmissionPrepared && result.state != resourcegroup.AdmissionUnavailable || !resourceGroupJSONEqual(canonical.ExecutionPeers, result.review.ExecutionPeers) {
		return nil, resourcegroup.ErrInvalid
	}
	return &resourceGroupSelectReceipt{input: canonical, resultID: result.id, resultRevision: result.review.Revision}, nil
}

func resourceGroupSelectRepeatMatches(p *resourceGroupPrepared, input resourcegroup.SelectInput) bool {
	if p == nil || p.selectReceipt == nil || p.state != resourcegroup.AdmissionPrepared && p.state != resourcegroup.AdmissionUnavailable {
		return false
	}
	canonical, err := canonicalResourceGroupSelectInput(input)
	r := p.selectReceipt
	return err == nil && r.input.Validate() == nil && p.review.Validate() == nil && r.resultID == p.id && r.resultRevision == p.review.Revision && r.input.ReviewID != p.id && resourceGroupJSONEqual(r.input.ExecutionPeers, p.review.ExecutionPeers) && resourceGroupJSONEqual(r.input, canonical)
}

// Core.op held. This branch can only disclose the same still-current object;
// it allocates no ID, makes no exchange and never installs a prepared record.
func (c *Core) recoverResourceGroupSelectLocked(ctx context.Context, p *resourceGroupPrepared, input resourcegroup.SelectInput) (any, error) {
	g := c.resourceGroups
	if ctx == nil || ctx.Err() != nil || c.ctx == nil || c.ctx.Err() != nil || g == nil || g.prepared != p || !resourceGroupSelectRepeatMatches(p, input) {
		return nil, resourceGroupError("review_unavailable")
	}
	if g.active != nil {
		return nil, resourceGroupError("busy")
	}
	if err := c.resourceGroupPreparedCurrentLocked(p); err != nil {
		g.prepared = nil
		return nil, err
	}
	view, err := resourceGroupPreparedView(p)
	if err != nil || ctx.Err() != nil || c.ctx.Err() != nil {
		return nil, resourceGroupError("unavailable")
	}
	return view, nil
}

func (c *Core) resourceGroupCurrentReview(ctx context.Context) (any, error) {
	c.op.Lock()
	defer c.op.Unlock()
	return c.resourceGroupCurrentReviewLocked(ctx)
}

// Core.op held. This explicit local read may discard canceled/stale unused
// admission as safe reduction. It never changes retained run uncertainty.
func (c *Core) resourceGroupCurrentReviewLocked(ctx context.Context) (any, error) {
	if ctx == nil || ctx.Err() != nil || c.ctx == nil || c.ctx.Err() != nil {
		return nil, resourceGroupError("unavailable")
	}
	g := c.resourceGroups
	if g == nil {
		return nil, resourceGroupError("storage_unavailable")
	}
	if g.active != nil {
		return nil, resourceGroupError("busy")
	}
	if g.prepared != nil && g.prepared.state == resourcegroup.AdmissionCanceled {
		g.prepared = nil
	}
	if _, err := c.currentResourceGroupLocked(true); err != nil {
		return nil, err
	}
	p := g.prepared
	if p == nil {
		if ctx.Err() != nil || c.ctx.Err() != nil {
			return nil, resourceGroupError("unavailable")
		}
		return resourcegroup.CurrentReviewView{SchemaVersion: resourcegroup.SchemaVersion, State: resourcegroup.CurrentReviewNone}, nil
	}
	if err := c.resourceGroupPreparedCurrentLocked(p); err != nil {
		g.prepared = nil
		return nil, err
	}
	view, err := resourceGroupPreparedView(p)
	if err != nil || view.AdmissionState == resourcegroup.AdmissionCanceled {
		g.prepared = nil
		return nil, resourceGroupError("review_changed")
	}
	if ctx.Err() != nil || c.ctx.Err() != nil {
		return nil, resourceGroupError("unavailable")
	}
	return resourcegroup.CurrentReviewView{SchemaVersion: resourcegroup.SchemaVersion, State: resourcegroup.CurrentReviewCurrent, Prepared: &view}, nil
}
