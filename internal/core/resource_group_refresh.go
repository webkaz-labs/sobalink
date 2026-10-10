package core

import (
	"context"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/resourcegrant"
	"github.com/webkaz-labs/sobalink/internal/resourcegroup"
)

func resourceGroupRefreshIndices(record resourceGroupRecord, peers []string) ([]int, error) {
	if len(peers) == 0 || len(peers) > resourcegroup.MaxMembers {
		return nil, resourceGroupError("invalid")
	}
	indices := make([]int, 0, len(peers))
	for _, peer := range peers {
		found := false
		for i, row := range record.Evidence.Members {
			if row.PeerKey == peer && resourceGroupStatusCandidate(row) {
				for _, prior := range indices {
					if prior == i {
						return nil, resourceGroupError("invalid")
					}
				}
				indices = append(indices, i)
				found = true
				break
			}
		}
		if !found {
			return nil, resourceGroupError("invalid")
		}
	}
	return indices, nil
}

func resourceGroupOriginalStatusRequest(row resourcegroup.MemberEvidence) (resourcegrant.ManagementRequest, error) {
	if !resourceGroupStatusCandidate(row) || row.Validate() != nil {
		return resourcegrant.ManagementRequest{}, resourceGroupError("invalid")
	}
	request := resourcegrant.ManagementRequest{ManagementSelector: row.Request.ManagementSelector, Action: resourcegrant.StatusAction, Status: &resourcegrant.ManagementStatusRequest{OperationID: row.Request.Apply.OperationID}}
	if request.Validate() != nil {
		return resourcegrant.ManagementRequest{}, resourceGroupError("invalid")
	}
	return request, nil
}

func (c *Core) resourceGroupRefresh(ctx context.Context, input resourcegroup.RefreshInput) (any, error) {
	ctx, cancelBudget := resourceGroupBudgetContext(ctx, len(input.Peers))
	defer cancelBudget()
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
	record, found := g.retained(input.RunID)
	if !found || record.Admission != resourceGroupAdmissionFinished {
		c.op.Unlock()
		return nil, resourceGroupError("review_unavailable")
	}
	indices, err := resourceGroupRefreshIndices(record, input.Peers)
	if err != nil {
		c.op.Unlock()
		return nil, err
	}
	// Reject the whole refresh before its first query if the requested bounded
	// history updates cannot all be represented. No counter reset or rollover.
	if record.UpdateSequence > uint64(capacity.MaxJSONInteger)-uint64(len(indices)) {
		c.op.Unlock()
		return nil, resourceGroupError("capacity")
	}
	for _, i := range indices {
		if record.Evidence.Members[i].Status.Sequence >= uint64(capacity.MaxJSONInteger) {
			c.op.Unlock()
			return nil, resourceGroupError("capacity")
		}
	}
	run, active, finish, err := c.beginResourceGroupActivityLocked(ctx, input.RunID, resourcegroup.ActivityRefreshing, len(indices))
	if err != nil {
		c.op.Unlock()
		return nil, err
	}
	c.op.Unlock()
	defer finish()
	for _, i := range indices {
		c.op.Lock()
		if c.resourceGroupStopLocked(run, active) != resourcegroup.StopNone {
			c.op.Unlock()
			break
		}
		if _, err := c.currentResourceGroupLocked(true); err != nil {
			c.op.Unlock()
			break
		}
		current, found := g.retained(input.RunID)
		if !found {
			c.op.Unlock()
			return nil, resourceGroupError("storage_unavailable")
		}
		next, err := cloneResourceGroupRecord(current)
		if err != nil {
			c.op.Unlock()
			return nil, resourceGroupError("storage_unavailable")
		}
		row := next.Evidence.Members[i]
		request, err := resourceGroupOriginalStatusRequest(row)
		observedAt, timeErr := resourceGroupObservedTime()
		if err != nil || timeErr != nil {
			c.op.Unlock()
			return nil, resourceGroupError("unavailable")
		}
		original, captureErr := c.captureResourceManagementOriginLocked(row.PeerKey)
		saved := next.Origins[i]
		if saved.State != resourceGroupOriginCaptured || saved.Relationship == nil || original.relationship != *saved.Relationship {
			captureErr = resourceGroupError("unavailable")
		}
		c.op.Unlock()
		var reply resourcegrant.ManagementReply
		callErr := captureErr
		if callErr == nil {
			// Only operation.status, with the original immutable selector/token.
			// This fresh current transport capture cannot revive prepared approval.
			reply, callErr = c.resourceRemoteManagementExchange(run, resourcegrant.RemoteManagementInput{PeerKey: row.PeerKey, Request: request}, &original)
		}
		c.op.Lock()
		state := resourcegroup.StatusUnavailable
		if resourceGroupVerifiedUnsupported(callErr) {
			state = resourcegroup.StatusUnsupported
		}
		updated, observeErr := resourcegroup.ObserveStatusFailure(row, state, row.Status.Sequence+1, observedAt, resourcegroup.LocalDurable)
		if callErr == nil {
			updated, observeErr = resourcegroup.ObserveStatus(row, reply, row.Status.Sequence+1, observedAt, resourcegroup.LocalDurable)
		}
		if observeErr != nil {
			updated, observeErr = resourcegroup.ObserveStatusFailure(row, resourcegroup.StatusQueryFailed, row.Status.Sequence+1, observedAt, resourcegroup.LocalDurable)
		}
		if observeErr != nil {
			c.op.Unlock()
			return nil, resourceGroupError("unavailable")
		}
		next.Evidence.Members[i] = updated
		next.UpdateSequence++
		if err := c.publishResourceGroupUpdateLocked(next); err != nil {
			c.op.Unlock()
			return nil, err
		}
		c.op.Unlock()
	}
	c.op.Lock()
	defer c.op.Unlock()
	if _, err := c.currentResourceGroupLocked(false); err != nil {
		return nil, err
	}
	result, found := g.retained(input.RunID)
	if !found {
		return nil, resourceGroupError("storage_unavailable")
	}
	return resourceGroupRunView(result, resourcegroup.ActivityIdle)
}
