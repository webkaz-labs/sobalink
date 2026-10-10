package core

import (
	"context"
	"encoding/json"

	"github.com/webkaz-labs/sobalink/internal/resourcegroup"
)

// These exact authenticated local commands never enter the generic mutation
// history cache, dispatch arbitrary names, or accept a caller-imported review.
func (c *Core) resourceGroupCommand(ctx context.Context, name string, raw json.RawMessage) (result any, resultErr error) {
	defer func() {
		if resultErr == nil && !c.resourceGroupResultFits(result, 0) {
			resultErr = resourceGroupError("capacity")
		}
		if resultErr != nil {
			result = nil
		}
	}()
	if ctx == nil || len(raw) > resourcegroup.MaxLocalInputBytes {
		return nil, resourceGroupError("invalid")
	}
	limits, limitsErr := localLimitsFor(c.capacityPolicy())
	if limitsErr != nil || !resourceGroupRequestFits(name, raw, limits) {
		return nil, resourceGroupError("capacity")
	}
	done, err := c.beginWork()
	if err != nil {
		return nil, resourceGroupError("unavailable")
	}
	defer done()
	switch name {
	case resourcegroup.LocalCurrentReviewCommand:
		if _, err := resourcegroup.DecodeCurrentReviewInput(raw); err != nil {
			return nil, resourceGroupError("invalid")
		}
		return c.resourceGroupCurrentReview(ctx)
	case resourcegroup.LocalPreviewCommand:
		input, err := resourcegroup.DecodePreviewInput(raw)
		if err != nil {
			return nil, resourceGroupError("invalid")
		}
		return c.resourceGroupPreview(ctx, input)
	case resourcegroup.LocalSelectCommand:
		input, err := resourcegroup.DecodeSelectInput(raw)
		if err != nil {
			return nil, resourceGroupError("invalid")
		}
		return c.resourceGroupSelect(ctx, input)
	case resourcegroup.LocalApplyCommand:
		input, err := resourcegroup.DecodeApplyInput(raw)
		if err != nil {
			return nil, resourceGroupError("invalid")
		}
		return c.resourceGroupApply(ctx, input)
	case resourcegroup.LocalStatusCommand:
		input, err := resourcegroup.DecodeStatusInput(raw)
		if err != nil {
			return nil, resourceGroupError("invalid")
		}
		c.op.Lock()
		defer c.op.Unlock()
		return c.resourceGroupStatusLocked(input.RunID)
	case resourcegroup.LocalRefreshCommand:
		input, err := resourcegroup.DecodeRefreshInput(raw)
		if err != nil {
			return nil, resourceGroupError("invalid")
		}
		return c.resourceGroupRefresh(ctx, input)
	case resourcegroup.LocalCancelCommand:
		input, err := resourcegroup.DecodeCancelInput(raw)
		if err != nil {
			return nil, resourceGroupError("invalid")
		}
		return c.resourceGroupCancel(input)
	default:
		return nil, resourceGroupError("invalid")
	}
}

func (c *Core) resourceGroupStatusLocked(id string) (resourcegroup.RunView, error) {
	g, err := c.currentResourceGroupLocked(false)
	if err != nil {
		return resourcegroup.RunView{}, err
	}
	run, found := g.retained(id)
	if !found {
		return resourcegroup.RunView{}, resourceGroupError("review_unavailable")
	}
	return resourceGroupRunView(run, g.activity(id))
}

// Exact in-memory cancellation is safe reduction even if storage disclosure
// subsequently fails. No unverified history is returned by this first step.
func (c *Core) reduceResourceGroupCancelLocked(id string) (*resourceGroupPrepared, context.CancelFunc) {
	g := c.resourceGroups
	if g == nil {
		return nil, nil
	}
	var cancel context.CancelFunc
	if g.active != nil && g.active.id == id {
		g.active.stop = resourcegroup.StopUserCanceled
		cancel = g.active.cancel
	}
	if g.prepared != nil && g.prepared.id == id {
		g.prepared.state = resourcegroup.AdmissionCanceled
		g.prepared.origins = nil
		g.prepared.selectReceipt = nil
		return g.prepared, cancel
	}
	return nil, cancel
}

func (c *Core) resourceGroupCancel(input resourcegroup.CancelInput) (any, error) {
	c.op.Lock()
	prepared, cancel := c.reduceResourceGroupCancelLocked(input.ReviewID)
	c.op.Unlock()
	if cancel != nil {
		cancel()
	}
	c.op.Lock()
	defer c.op.Unlock()
	if _, err := c.currentResourceGroupLocked(false); err != nil {
		return nil, err
	}
	if prepared != nil {
		view, err := resourceGroupPreparedView(prepared)
		if err != nil {
			return nil, err
		}
		return resourcegroup.CancelView{SchemaVersion: resourcegroup.SchemaVersion, Prepared: &view}, nil
	}
	view, err := c.resourceGroupStatusLocked(input.ReviewID)
	if err != nil {
		return nil, err
	}
	return resourcegroup.CancelView{SchemaVersion: resourcegroup.SchemaVersion, Run: &view}, nil
}

func (c *Core) resourceGroupSelect(ctx context.Context, input resourcegroup.SelectInput) (any, error) {
	c.op.Lock()
	defer c.op.Unlock()
	if ctx == nil || ctx.Err() != nil || c.ctx == nil || c.ctx.Err() != nil {
		return nil, resourceGroupError("unavailable")
	}
	g, err := c.currentResourceGroupLocked(true)
	if err != nil {
		return nil, err
	}
	if g.active != nil {
		return nil, resourceGroupError("busy")
	}
	p := g.prepared
	if resourceGroupSelectRepeatMatches(p, input) {
		return c.recoverResourceGroupSelectLocked(ctx, p, input)
	}
	if p == nil || p.state == resourcegroup.AdmissionCanceled || input.ReviewID != p.id || input.ReviewRevision != p.review.Revision || ctx.Err() != nil {
		return nil, resourceGroupError("review_unavailable")
	}
	if err := c.resourceGroupPreparedCurrentLocked(p); err != nil {
		g.prepared = nil
		return nil, err
	}
	if ctx.Err() != nil || c.ctx.Err() != nil {
		return nil, resourceGroupError("unavailable")
	}
	if resourceGroupJSONEqual(input.ExecutionPeers, p.review.ExecutionPeers) {
		// Identical canonical subset is a no-op: no new identity or randomized
		// digest, after all exact owned/current checks above.
		return resourceGroupPreparedView(p)
	}
	review, err := resourcegroup.BuildReview(p.review.Selection, p.review.Rows, input.ExecutionPeers)
	if err != nil {
		return nil, resourceGroupError("invalid")
	}
	id, err := newResourceGroupID(g)
	if err != nil || id == p.id {
		return nil, resourceGroupError("unavailable")
	}
	next := *p
	next.id, next.review, next.state = id, review, resourcegroup.AdmissionPrepared
	next.previewInputHash = "" // subset replacement consumes the original preview reply
	if len(review.ExecutionPeers) == 0 {
		next.state = resourcegroup.AdmissionUnavailable
	}
	view, err := resourceGroupPreparedView(&next)
	if err != nil || !c.resourceGroupResultFits(view, 0) {
		return nil, resourceGroupError("capacity")
	}
	receipt, err := newResourceGroupSelectReceipt(input, &next)
	if err != nil {
		return nil, resourceGroupError("invalid")
	}
	if ctx.Err() != nil || c.ctx.Err() != nil {
		return nil, resourceGroupError("unavailable")
	}
	next.selectReceipt = receipt // replace, never chain the predecessor receipt
	g.prepared = &next           // old ID is consumed, never retained as admission
	return view, nil
}
