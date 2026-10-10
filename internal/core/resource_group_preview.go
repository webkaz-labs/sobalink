package core

import (
	"context"

	"github.com/webkaz-labs/sobalink/internal/resourcegrant"
	"github.com/webkaz-labs/sobalink/internal/resourcegroup"
)

func resourceGroupVerifiedUnsupported(err error) bool {
	coded, ok := err.(interface{ ErrorCode() string })
	return ok && coded.ErrorCode() == "resource_management_remote_unsupported"
}

func (c *Core) resourceGroupPreview(ctx context.Context, input resourcegroup.PreviewInput) (any, error) {
	ctx, cancelBudget := resourceGroupBudgetContext(ctx, len(input.Selection.Members))
	defer cancelBudget()
	selection, err := resourcegroup.ResolveSelection(input.Selection)
	if err != nil {
		return nil, resourceGroupError("invalid")
	}
	inputHash, err := resourceGroupPreviewInputHash(input)
	if err != nil {
		return nil, err
	}
	c.op.Lock()
	g, err := c.currentResourceGroupLocked(true)
	if err != nil {
		c.op.Unlock()
		return nil, err
	}
	if g.active != nil {
		c.op.Unlock()
		return nil, resourceGroupError("busy")
	}
	if resourceGroupPreviewRepeatMatches(g.prepared, inputHash) {
		view, repeatErr := c.recoverResourceGroupPreviewLocked(ctx, g.prepared)
		c.op.Unlock()
		return view, repeatErr
	}
	if g.prepared != nil && input.ReplaceReviewID != g.prepared.id || g.prepared == nil && input.ReplaceReviewID != "" {
		c.op.Unlock()
		return nil, resourceGroupError("review_changed")
	}
	blocked, err := resourceGroupUnresolved(g.store.state, selection)
	if err != nil || len(blocked) != 0 {
		c.op.Unlock()
		return nil, resourceGroupError("reconcile_required")
	}
	backend, ok := c.nodeCopy().(*directLANBackend)
	if !ok || backend == nil || backend.Node == nil {
		c.op.Unlock()
		return nil, resourceGroupError("unavailable")
	}
	for _, member := range selection.Members {
		if member.PeerKey == backend.Node.PublicKey() {
			c.op.Unlock()
			return nil, resourceGroupError("invalid")
		}
	}
	id, err := newResourceGroupID(g)
	if err != nil {
		c.op.Unlock()
		return nil, resourceGroupError("unavailable")
	}
	p := &resourceGroupPrepared{id: id, previewInputHash: inputHash, controllerID: c.resourceIdentity, bootNonce: c.resourceNonce, storeRevision: resourceDigest(g.store.state), firstUse: g.store.firstUse, state: resourcegroup.AdmissionUnavailable, origins: make([]resourceGroupCapturedOrigin, len(selection.Members))}
	for i, member := range selection.Members {
		p.origins[i].peerKey = member.PeerKey
		original, captureErr := c.captureResourceManagementOriginLocked(member.PeerKey)
		if captureErr == nil {
			p.origins[i].origin = &original
		}
	}
	run, active, finish, err := c.beginResourceGroupActivityLocked(ctx, id, resourcegroup.ActivityIdle, len(selection.Members))
	if err != nil {
		c.op.Unlock()
		return nil, err
	}
	g.prepared = nil // explicit replacement consumes prior approval before I/O
	c.op.Unlock()
	defer finish()
	rows := make([]resourcegroup.ReviewRow, len(selection.Members))
	peers := make([]string, 0, len(rows))
	for i, member := range selection.Members {
		rows[i] = resourcegroup.ReviewRow{SchemaVersion: resourcegroup.SchemaVersion, PeerKey: member.PeerKey, State: resourcegroup.ReviewUnavailable}
		c.op.Lock()
		stopped := c.resourceGroupStopLocked(run, active) != resourcegroup.StopNone
		c.op.Unlock()
		if stopped {
			rows[i].State = resourcegroup.ReviewCanceled
			continue
		}
		original := p.origins[i].origin
		if original == nil {
			continue
		}
		request := resourcegrant.ManagementRequest{ManagementSelector: member.Selector, Action: resourcegrant.PreviewAction, Preview: &resourcegrant.ManagementPreviewRequest{Settings: member.Requested}}
		reply, callErr := c.resourceRemoteManagementExchange(run, resourcegrant.RemoteManagementInput{PeerKey: member.PeerKey, Request: request}, original)
		if resourceGroupVerifiedUnsupported(callErr) {
			rows[i].State = resourcegroup.ReviewUnsupported
		} else if callErr == nil && resourceManagementCapturedReplyMatches(request, reply) {
			rows[i].State, rows[i].Reply = resourcegroup.ReviewReady, &reply
			peers = append(peers, member.PeerKey)
		} else if callErr == nil {
			rows[i].State = resourcegroup.ReviewInvalidReply
		}
	}
	review, err := resourcegroup.BuildReview(selection, rows, peers)
	if err != nil {
		return nil, resourceGroupError("unavailable")
	}
	c.op.Lock()
	defer c.op.Unlock()
	return c.finishResourceGroupPreviewLocked(run, active, p, review)
}

func (c *Core) finishResourceGroupPreviewLocked(run context.Context, active *resourceGroupActivity, p *resourceGroupPrepared, review resourcegroup.ReviewBody) (any, error) {
	g := c.resourceGroups
	p.review = review
	// Disclosure has its own fresh owned/original-origin gate even when local
	// cancellation means no prepared admission will be installed.
	if err := c.resourceGroupPreparedCurrentLocked(p); err != nil {
		return nil, err
	}
	if c.resourceGroupStopLocked(run, active) == resourcegroup.StopNone {
		if len(review.ExecutionPeers) != 0 {
			p.state = resourcegroup.AdmissionPrepared
		}
		view, viewErr := resourceGroupPreparedView(p)
		if viewErr != nil || !c.resourceGroupResultFits(view, 0) {
			return nil, resourceGroupError("capacity")
		}
		g.prepared = p
	} else {
		p.origins = nil // canceled/changed preview returns data without admission
	}
	return resourceGroupPreparedView(p)
}

// Same-input retrieval is a local view read only. It neither previews a peer,
// replaces an origin, creates an ID nor reinstalls a consumed prepared object.
func (c *Core) recoverResourceGroupPreviewLocked(ctx context.Context, p *resourceGroupPrepared) (any, error) {
	if ctx.Err() != nil || p == nil || c.resourceGroups.prepared != p || p.state == resourcegroup.AdmissionCanceled {
		return nil, resourceGroupError("review_unavailable")
	}
	if err := c.resourceGroupPreparedCurrentLocked(p); err != nil {
		c.resourceGroups.prepared = nil
		return nil, err
	}
	return resourceGroupPreparedView(p)
}
