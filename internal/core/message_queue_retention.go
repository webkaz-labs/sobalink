package core

import "sort"

const messageEvidenceRetirePerTick = 4

// The visible conversation is changed only by reviewed history cleanup. This
// bounded maintenance retires hidden, known delivery evidence after the key's
// immutable maximum admission horizon. Unknown outcomes retain their ledger
// slot; physical owners and pending completion/cancel obligations remain fences.
func (c *Core) retireMessageQueueEvidenceLocked() error {
	if err := c.messageQueueGuardLocked(); err != nil {
		return err
	}
	now := c.messageQueueNowLocked()
	var ids []string
	for id, row := range c.messageStore.state.Outgoing {
		horizon, valid := textRequestHorizon(id)
		if !valid || now.Before(horizon) || row.Visible || row.Active || !row.NoMoreSend || row.BodyRetained || row.View.DeliveryOutcome == "unknown" {
			continue
		}
		if q := c.messageQueue; q != nil && (q.runs[id] != nil || q.completions[id] != nil || q.pendingCancel[id]) {
			continue
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil
	}
	sort.Strings(ids)
	ids = ids[:min(len(ids), messageEvidenceRetirePerTick)]
	if err := c.mutateMessageStoreLocked(func(next *messageEnvelope) error {
		for _, id := range ids {
			delete(next.Outgoing, id)
		}
		return nil
	}); err != nil {
		return err
	}
	if c.messageQueue != nil {
		for _, id := range ids {
			delete(c.messageQueue.next, id)
		}
	}
	return nil
}
