package core

import (
	"context"
	"errors"
	"time"

	"github.com/webkaz-labs/sobalink/internal/resourcegroup"
)

const resourceGroupPeerBudget = 15 * time.Second

func resourceGroupBudgetContext(ctx context.Context, count int) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, time.Duration(count)*resourceGroupPeerBudget)
}

// Caller holds Core.op. The only shared shutdown publication follows op -> mu.
func (c *Core) beginResourceGroupActivityLocked(ctx context.Context, id string, kind resourcegroup.Activity, count int) (context.Context, *resourceGroupActivity, func(), error) {
	g := c.resourceGroups
	if ctx == nil || count < 1 || count > resourcegroup.MaxMembers || g == nil || g.active != nil {
		return nil, nil, nil, resourceGroupError("busy")
	}
	run, cancel := resourceGroupBudgetContext(ctx, count)
	stopCore := context.AfterFunc(c.ctx, cancel)
	a := &resourceGroupActivity{id: id, kind: kind, cancel: cancel, done: make(chan struct{}), stop: resourcegroup.StopNone}
	c.mu.Lock()
	if c.closing || c.ctx.Err() != nil || run.Err() != nil {
		c.mu.Unlock()
		stopCore()
		cancel()
		return nil, nil, nil, resourceGroupError("unavailable")
	}
	g.active = a
	c.mu.Unlock()
	finish := func() {
		stopCore()
		cancel()
		c.op.Lock()
		c.mu.Lock()
		if g.active == a {
			g.active = nil
		}
		c.mu.Unlock()
		c.op.Unlock()
		close(a.done)
	}
	return run, a, finish, nil
}

// Called after Core cancellation and before taking Core.op during Close.
// Joining includes bounded local historical completion, never another exchange.
func (c *Core) stopResourceGroups() error {
	c.mu.Lock()
	var active *resourceGroupActivity
	if c.resourceGroups != nil {
		active = c.resourceGroups.active
	}
	c.mu.Unlock()
	if active != nil {
		active.cancel()
		<-active.done
	}
	return nil
}

func (c *Core) resourceGroupStopLocked(run context.Context, active *resourceGroupActivity) resourcegroup.StopReason {
	if active.stop != resourcegroup.StopNone {
		return active.stop
	}
	if c.ctx.Err() != nil {
		return resourcegroup.StopContextChanged
	}
	if errors.Is(run.Err(), context.DeadlineExceeded) {
		return resourcegroup.StopBudgetExhausted
	}
	if run.Err() != nil {
		return resourcegroup.StopUserCanceled
	}
	return resourcegroup.StopNone
}

// Disclosure remains possible for an exact certified frozen candidate. Every
// admission additionally rejects frozen, so this never promotes uncertain data.
func (c *Core) currentResourceGroupLocked(mutable bool) (*resourceGroupCoordinator, error) {
	g := c.resourceGroups
	if g == nil || g.store == nil || g.store.current(c) != nil {
		if g != nil {
			g.prepared = nil
		}
		return nil, resourceGroupError("storage_unavailable")
	}
	if mutable && g.store.frozen {
		g.prepared = nil
		return nil, resourceGroupError("storage_unavailable")
	}
	return g, nil
}

func (c *Core) resourceGroupPreparedCurrentLocked(p *resourceGroupPrepared) error {
	g, err := c.currentResourceGroupLocked(true)
	if err != nil {
		return err
	}
	if p == nil || p.controllerID != c.resourceIdentity || p.bootNonce != c.resourceNonce || p.storeRevision != resourceDigest(g.store.state) || p.firstUse != g.store.firstUse {
		return resourceGroupError("review_changed")
	}
	for _, selected := range p.origins {
		if selected.origin != nil && c.resourceManagementOriginCurrentLocked(*selected.origin) != nil {
			return resourceGroupError("review_changed")
		}
	}
	return nil
}

// All changes here are local historical reduction after an actual publication
// failure. No prepared approval is retained and no dispatch may follow.
func (g *resourceGroupCoordinator) retainFailedResourceGroupPublication(run resourceGroupRecord) {
	copy, err := cloneResourceGroupRecord(run)
	if err != nil {
		g.prepared = nil
		g.store.freeze()
		return
	}
	run = copy
	g.prepared = nil
	run.Admission = resourceGroupAdmissionFinished
	for i := range run.Evidence.Members {
		row := &run.Evidence.Members[i]
		if row.Dispatch == resourcegroup.Dispatching {
			row.Dispatch = resourcegroup.DispatchUnknown
			// A proven pre-publication intent failure preceded every call. Retain
			// that known local non-dispatch; an ambiguous rename stays unknown.
			if !g.store.uncertain {
				for _, prior := range g.store.state.Runs {
					if prior.RunID == run.RunID && prior.Evidence.Members[i].Dispatch == resourcegroup.DispatchNotAttempted {
						row.Dispatch = resourcegroup.DispatchNotAttempted
					}
				}
			}
		}
		row.LocalDurability = resourcegroup.LocalNotSaved
		if g.store.uncertain {
			row.LocalDurability = resourcegroup.LocalUncertain
		}
		if row.Execution == resourcegroup.ExecutionSelected && row.Dispatch == resourcegroup.DispatchNotAttempted && row.AdmissionStop == resourcegroup.StopNone {
			row.AdmissionStop = resourcegroup.StopPersistenceUncertain
		}
	}
	g.overlay = &run
}

func (c *Core) publishResourceGroupUpdateLocked(run resourceGroupRecord) error {
	g := c.resourceGroups
	next, err := withResourceGroupUpdate(g.store.state, run)
	if err != nil {
		return resourceGroupModelError(err)
	}
	if err := g.store.publish(c, next); err != nil {
		g.retainFailedResourceGroupPublication(run)
		return resourceGroupError("storage_unavailable")
	}
	return nil
}

func resourceGroupStoppedRecord(run resourceGroupRecord, reason resourcegroup.StopReason) (resourceGroupRecord, error) {
	next, err := cloneResourceGroupRecord(run)
	if err != nil {
		return resourceGroupRecord{}, err
	}
	next.UpdateSequence++
	next.Admission = resourceGroupAdmissionFinished
	for i := range next.Evidence.Members {
		row := &next.Evidence.Members[i]
		if row.Dispatch == resourcegroup.Dispatching {
			row.Dispatch = resourcegroup.DispatchUnknown
		}
		if row.Execution == resourcegroup.ExecutionSelected && row.Dispatch == resourcegroup.DispatchNotAttempted && row.AdmissionStop == resourcegroup.StopNone {
			row.AdmissionStop = reason
		}
	}
	if next.validate() != nil {
		return resourceGroupRecord{}, errResourceGroupState
	}
	return next, nil
}
