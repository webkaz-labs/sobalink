package core

import (
	"context"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/resourceacceptance"
	"github.com/webkaz-labs/sobalink/internal/resourcegrant"
	"github.com/webkaz-labs/sobalink/internal/resourcegroup"
)

// ClientInvoked is local call evidence only, never proof of a sent frame or
// target execution. A canceled interval after durable intent remains unknown.
type resourceGroupDispatchAttempt struct {
	reply         resourcegrant.ManagementReply
	err           error
	stop          resourcegroup.StopReason
	clientInvoked bool
}

func (c *Core) resourceGroupDispatchAfterIntent(ctx context.Context, active *resourceGroupActivity, peer string, request resourcegrant.ManagementRequest, original resourceManagementOrigin) resourceGroupDispatchAttempt {
	c.op.Lock()
	reason := c.resourceGroupStopLocked(ctx, active)
	c.op.Unlock()
	if reason != resourcegroup.StopNone {
		return resourceGroupDispatchAttempt{err: resourceGroupError("unavailable"), stop: reason}
	}
	reply, err := c.resourceRemoteManagementExchange(ctx, resourcegrant.RemoteManagementInput{PeerKey: peer, Request: request, Confirm: true}, &original)
	return resourceGroupDispatchAttempt{reply: reply, err: err, stop: resourcegroup.StopNone, clientInvoked: true}
}

func (c *Core) resourceGroupRunContextLocked(p *resourceGroupPrepared) error {
	if p.controllerID != c.resourceIdentity || p.bootNonce != c.resourceNonce {
		return resourceGroupError("review_changed")
	}
	if _, err := c.currentResourceGroupLocked(true); err != nil {
		return err
	}
	for _, original := range p.origins {
		if original.origin != nil && c.resourceManagementOriginCurrentLocked(*original.origin) != nil {
			return resourceGroupError("review_changed")
		}
	}
	return nil
}

func (c *Core) resourceGroupApply(ctx context.Context, input resourcegroup.ApplyInput) (any, error) {
	ctx, cancelBudget := resourceGroupBudgetContext(ctx, len(input.ExecutionPeers))
	defer cancelBudget()
	c.op.Lock()
	g, err := c.currentResourceGroupLocked(false)
	if err != nil {
		c.op.Unlock()
		return nil, err
	}
	if retained, found := g.retained(input.ReviewID); found {
		if retained.InputHash != resourceGroupInputHash(input) {
			c.op.Unlock()
			return nil, resourceGroupError("review_changed")
		}
		view, err := resourceGroupRunView(retained, g.activity(retained.RunID))
		c.op.Unlock()
		return view, err // repeat is historical data only, including while active
	}
	if g.store.frozen {
		c.op.Unlock()
		return nil, resourceGroupError("storage_unavailable")
	}
	if g.active != nil {
		c.op.Unlock()
		return nil, resourceGroupError("busy")
	}
	p := g.prepared
	if !resourceGroupSameSelection(input, p) {
		c.op.Unlock()
		return nil, resourceGroupError("review_unavailable")
	}
	if err := c.resourceGroupPreparedCurrentLocked(p); err != nil {
		g.prepared = nil
		c.op.Unlock()
		return nil, err
	}
	observedAt, err := resourceGroupObservedTime()
	if err != nil || *g.store.state.HighWater >= uint64(capacity.MaxJSONInteger) {
		c.op.Unlock()
		return nil, resourceGroupError("capacity")
	}
	record, err := newResourceGroupRecord(p.id, p.review, resourceGroupPersistedOrigins(p.origins), *g.store.state.HighWater+1, observedAt)
	if err != nil {
		c.op.Unlock()
		return nil, resourceGroupModelError(err)
	}
	if err := c.resourceGroupReserveResponse(record); err != nil {
		c.op.Unlock()
		return nil, err
	}
	next, err := withResourceGroupRun(g.store.state, record)
	if err != nil {
		c.op.Unlock()
		return nil, resourceGroupModelError(err)
	}
	run, active, finish, err := c.beginResourceGroupActivityLocked(ctx, p.id, resourcegroup.ActivityApplying, len(p.review.ExecutionPeers))
	if err != nil {
		c.op.Unlock()
		return nil, err
	}
	// Consume before publication, reentrant calls or cancellation can observe the
	// acceptance attempt. Neither a failed write nor a lost reply restores it.
	g.prepared = nil
	if err := g.store.publish(c, next); err != nil {
		g.retainFailedResourceGroupPublication(record)
		c.op.Unlock()
		finish()
		return nil, resourceGroupError("storage_unavailable")
	}
	resourceacceptance.Record(c, resourceacceptance.GroupAccepted, "", p.id, "")
	c.op.Unlock()
	defer finish()

	stopReason := resourcegroup.StopNone
	for i, member := range record.Evidence.Members {
		if member.Execution != resourcegroup.ExecutionSelected {
			continue
		}
		c.op.Lock()
		stopReason = c.resourceGroupStopLocked(run, active)
		if stopReason == resourcegroup.StopNone && c.resourceGroupRunContextLocked(p) != nil {
			stopReason = resourcegroup.StopContextChanged
		}
		if stopReason != resourcegroup.StopNone {
			c.op.Unlock()
			break
		}
		current, found := g.retained(p.id)
		if !found || current.Admission != resourceGroupAdmissionActive || current.Evidence.Members[i].Dispatch != resourcegroup.DispatchNotAttempted || p.origins[i].origin == nil {
			c.op.Unlock()
			stopReason = resourcegroup.StopContextChanged
			break
		}
		intent, cloneErr := cloneResourceGroupRecord(current)
		if cloneErr != nil || intent.UpdateSequence > uint64(capacity.MaxJSONInteger)-3 {
			c.op.Unlock()
			stopReason = resourcegroup.StopBudgetExhausted
			break
		}
		intent.UpdateSequence++
		intent.Evidence.Members[i].Dispatch = resourcegroup.Dispatching
		if err := c.publishResourceGroupUpdateLocked(intent); err != nil {
			c.op.Unlock()
			stopReason = resourcegroup.StopPersistenceUncertain
			break
		}
		resourceacceptance.Record(c, resourceacceptance.GroupIntent, resourcegrant.ApplyAction, p.id, intent.Evidence.Members[i].Request.Apply.OperationID)
		// Required second cancellation gate after durable intent. Even a stop
		// here is conservative unknown; no later command may resend this row.
		request := *intent.Evidence.Members[i].Request
		original := *p.origins[i].origin
		c.op.Unlock()
		attempt := c.resourceGroupDispatchAfterIntent(run, active, member.PeerKey, request, original)
		stopReason = attempt.stop
		c.op.Lock()
		completed, cloneErr := cloneResourceGroupRecord(intent)
		if cloneErr != nil {
			g.store.freeze()
			c.op.Unlock()
			stopReason = resourcegroup.StopPersistenceUncertain
			break
		}
		completed.UpdateSequence++
		row, observeErr := resourcegroup.ObserveDispatchUnknown(intent.Evidence.Members[i], resourcegroup.LocalDurable)
		if attempt.err == nil {
			row, observeErr = resourcegroup.ObserveApply(intent.Evidence.Members[i], attempt.reply, resourcegroup.LocalDurable)
		}
		if observeErr != nil {
			row, _ = resourcegroup.ObserveDispatchUnknown(intent.Evidence.Members[i], resourcegroup.LocalDurable)
		}
		completed.Evidence.Members[i] = row
		if err := c.publishResourceGroupUpdateLocked(completed); err != nil {
			c.op.Unlock()
			stopReason = resourcegroup.StopPersistenceUncertain
			break
		}
		stopReason = c.resourceGroupStopLocked(run, active)
		if stopReason == resourcegroup.StopNone && c.resourceGroupRunContextLocked(p) != nil {
			stopReason = resourcegroup.StopContextChanged
		}
		c.op.Unlock()
		if stopReason != resourcegroup.StopNone {
			break
		}
	}
	c.op.Lock()
	defer c.op.Unlock()
	if !g.store.frozen {
		current, found := g.retained(p.id)
		if !found {
			return nil, resourceGroupError("storage_unavailable")
		}
		finished, err := resourceGroupStoppedRecord(current, stopReason)
		if err != nil {
			g.store.freeze()
			return nil, resourceGroupError("storage_unavailable")
		}
		if err := c.publishResourceGroupUpdateLocked(finished); err != nil {
			return nil, err
		}
	}
	if _, err := c.currentResourceGroupLocked(false); err != nil {
		return nil, err
	}
	result, found := g.retained(p.id)
	if !found {
		return nil, resourceGroupError("storage_unavailable")
	}
	return resourceGroupRunView(result, resourcegroup.ActivityIdle)
}
