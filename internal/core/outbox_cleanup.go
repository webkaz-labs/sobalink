package core

import "sort"

const outboxCleanupPerTick = 4

// Completion and cancellation persist CleanupPending before unlinking bytes.
// Once delivery is confirmed its sender no longer runs, so payload cleanup must
// have its own bounded maintenance path. The retained marker/identities and
// terminal-owner checks remain the only authority for deletion.
//
// A transient filesystem failure retains pending evidence and its byte charge;
// it does not block every other cleanup or unrelated sender. Uncertain state
// persistence freezes the coordinator as usual. Fairness is process-local and
// cannot extend delivery permission or make an old request admissible again.
func (o *outboxCoordinator) retryPendingCleanupLocked() error {
	ids := make([]string, 0, len(o.state.Payloads))
	for id, p := range o.state.Payloads {
		if p.CleanupPending && !p.Released {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	sort.Strings(ids)
	start := sort.Search(len(ids), func(i int) bool { return ids[i] > o.cleanupCursor })
	for n := 0; n < min(len(ids), outboxCleanupPerTick); n++ {
		id := ids[(start+n)%len(ids)]
		o.cleanupCursor = id
		if err := o.cleanupPayloadOwnedLocked(o.state.Payloads[id]); err != nil && o.frozen != "" {
			return err
		}
	}
	return nil
}
