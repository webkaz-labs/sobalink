//go:build lanlink_integration

package core

// This projection exists only in the inherited-pipe native fixture. Try-locks
// observe possible contention without waiting for it or scheduling new work.
// Request/delivery identities select a row but are never returned.
type outboxProcessPhase struct {
	ContextDone     bool                 `json:"contextDone"`
	NextKnown       bool                 `json:"nextKnown"`
	History         outboxProcessHistory `json:"history"`
	OpBusy          bool                 `json:"opBusy"`
	RefreshBusy     bool                 `json:"refreshBusy"`
	Coordinator     bool                 `json:"coordinator"`
	CoordinatorBusy bool                 `json:"coordinatorBusy"`
	Selected        bool                 `json:"selected"`
	RunPresent      bool                 `json:"runPresent"`
	RunBusy         bool                 `json:"runBusy"`
	RunState        string               `json:"runState"`
	Running         bool                 `json:"running"`
	NextPending     bool                 `json:"nextPending"`
	OwnerCheck      string               `json:"ownerCheck"`
	OwnerAdmitted   bool                 `json:"ownerAdmitted"`
	NamespaceCheck  string               `json:"namespaceCheck"`
	CoreBusy        bool                 `json:"coreBusy"`
	Active          int                  `json:"active"`
	Queue           int                  `json:"queue"`
	SelectedQueued  bool                 `json:"selectedQueued"`
}

func inspectOutboxProcessPhase(c *Core, requestID, deliveryID string) outboxProcessPhase {
	v := outboxProcessPhase{ContextDone: c.ctx.Err() != nil, History: inspectOutboxProcessHistory(c.outboxObserver), OwnerCheck: "not_checked", NamespaceCheck: "not_checked"}
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
				next, known := o.next[deliveryID]
				v.NextKnown = known
				v.NextPending = o.now().Before(next)
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

// Individual loads are sequential observations, not an atomic global snapshot.
type outboxProcessHistory struct {
	Observed             bool   `json:"observed"`
	MaintenanceEntered   uint64 `json:"maintenanceEntered"`
	MaintenanceBusy      uint64 `json:"maintenanceBusy"`
	MaintenanceCompleted uint64 `json:"maintenanceCompleted"`
	PumpEntered          uint64 `json:"pumpEntered"`
	OwnerAdmitted        uint64 `json:"ownerAdmitted"`
	OwnerBusy            uint64 `json:"ownerBusy"`
	OwnerRefused         uint64 `json:"ownerRefused"`
	Gated                uint64 `json:"gated"`
	LastGate             string `json:"lastGate"`
	Planned              uint64 `json:"planned"`
	RunAdmissionRefused  uint64 `json:"runAdmissionRefused"`
	FinishEntered        uint64 `json:"finishEntered"`
	FinishOwnerAdmitted  uint64 `json:"finishOwnerAdmitted"`
	FinishOwnerBusy      uint64 `json:"finishOwnerBusy"`
	FinishOwnerRefused   uint64 `json:"finishOwnerRefused"`
	FinishSaved          uint64 `json:"finishSaved"`
	FinishSaveFailed     uint64 `json:"finishSaveFailed"`
	FinishNoRow          uint64 `json:"finishNoRow"`
}

func inspectOutboxProcessHistory(o *outboxObserver) outboxProcessHistory {
	if o == nil {
		return outboxProcessHistory{}
	}
	v := outboxProcessHistory{
		Observed:           true,
		MaintenanceEntered: o.maintenanceEntered.Load(), MaintenanceBusy: o.maintenanceBusy.Load(), MaintenanceCompleted: o.maintenanceCompleted.Load(),
		PumpEntered: o.pumpEntered.Load(), OwnerAdmitted: o.ownerAdmitted.Load(), OwnerBusy: o.ownerBusy.Load(), OwnerRefused: o.ownerRefused.Load(),
		Gated: o.gated.Load(), Planned: o.planned.Load(), RunAdmissionRefused: o.runAdmissionRefused.Load(),
		FinishEntered: o.finishEntered.Load(), FinishOwnerAdmitted: o.finishOwnerAdmitted.Load(), FinishOwnerBusy: o.finishOwnerBusy.Load(), FinishOwnerRefused: o.finishOwnerRefused.Load(),
		FinishSaved: o.finishSaved.Load(), FinishSaveFailed: o.finishSaveFailed.Load(), FinishNoRow: o.finishNoRow.Load(),
	}
	switch outboxObservedGate(o.lastGate.Load()) {
	case outboxGateNone:
		v.LastGate = "none"
	case outboxGateProfile:
		v.LastGate = "profile_guard"
	case outboxGateNamespace:
		v.LastGate = "namespace_check"
	case outboxGateClock:
		v.LastGate = "clock_guard"
	case outboxGateContext:
		v.LastGate = "context_done"
	case outboxGateRetirement:
		v.LastGate = "retirement"
	case outboxGatePersist:
		v.LastGate = "persist_failed"
	default:
		v.LastGate = "other"
	}
	return v
}
