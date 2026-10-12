//go:build lanlink_integration

package core

// This projection exists only in the inherited-pipe native fixture. Try-locks
// observe possible contention without waiting for it or scheduling new work.
// Request/delivery identities select a row but are never returned.
type outboxProcessPhase struct {
	OpBusy          bool   `json:"opBusy"`
	RefreshBusy     bool   `json:"refreshBusy"`
	Coordinator     bool   `json:"coordinator"`
	CoordinatorBusy bool   `json:"coordinatorBusy"`
	Selected        bool   `json:"selected"`
	RunPresent      bool   `json:"runPresent"`
	RunBusy         bool   `json:"runBusy"`
	RunState        string `json:"runState"`
	Running         bool   `json:"running"`
	NextPending     bool   `json:"nextPending"`
	OwnerCheck      string `json:"ownerCheck"`
	OwnerAdmitted   bool   `json:"ownerAdmitted"`
	NamespaceCheck  string `json:"namespaceCheck"`
	CoreBusy        bool   `json:"coreBusy"`
	Active          int    `json:"active"`
	Queue           int    `json:"queue"`
	SelectedQueued  bool   `json:"selectedQueued"`
}

func inspectOutboxProcessPhase(c *Core, requestID, deliveryID string) outboxProcessPhase {
	v := outboxProcessPhase{OwnerCheck: "not_checked", NamespaceCheck: "not_checked"}
	v.OpBusy = !c.op.TryLock()
	if !v.OpBusy {
		c.op.Unlock()
	}
	v.RefreshBusy = !c.refreshMu.TryLock()
	if !v.RefreshBusy {
		c.refreshMu.Unlock()
	}
	v.CoreBusy = !c.mu.TryRLock()
	if v.CoreBusy {
		return v
	}
	o := c.outbox
	v.Active, v.Queue = c.outgoingActive, len(c.outgoingQueue)
	for _, run := range c.outgoingQueue {
		if run.batch.outboxID == requestID && run.batch.ID == deliveryID {
			v.SelectedQueued = true
		}
	}
	c.mu.RUnlock()
	v.Coordinator = o != nil
	if o != nil {
		v.CoordinatorBusy = !o.mu.TryLock()
		if !v.CoordinatorBusy {
			if row := o.state.Intents[requestID]; row != nil {
				for _, child := range row.View.Children {
					v.Selected = v.Selected || child.DeliveryID == deliveryID
				}
			}
			if v.Selected {
				v.NextPending = o.now().Before(o.next[deliveryID])
				if run := o.runs[deliveryID]; run != nil {
					v.RunPresent = true
					v.RunBusy = !run.mu.TryLock()
					if !v.RunBusy {
						v.RunState, v.Running = run.State, run.running
						run.mu.Unlock()
					}
				}
			}
			// This scope performs only the exact existing owner/namespace check.
			// It neither persists a snapshot nor changes admission/frozen state.
			err := o.withOwnedLocked(func() error {
				v.OwnerAdmitted = true
				v.NamespaceCheck = outboxPhaseCheckCode(o.checkLocked())
				return nil
			})
			v.OwnerCheck = outboxPhaseCheckCode(err)
			o.mu.Unlock()
		}
	}
	return v
}

func outboxPhaseCheckCode(err error) string {
	if err == nil {
		return "ok"
	}
	switch code := networkErrorCode(err); code {
	case "outbox_storage_busy", "outbox_recovery_required", "outbox_storage_uncertain", "outbox_payload_recovery_required", "outbox_clock_rollback", "outbox_capacity":
		return code
	}
	return "refused"
}
