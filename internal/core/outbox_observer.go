package core

import "sync/atomic"

// This private diagnostic observer is nil in production. The native test
// fixture installs it at construction, before maintenance starts. It retains
// only closed counters/enums, never callbacks, identities or application data.
type outboxObserver struct {
	maintenanceEntered   atomic.Uint64
	maintenanceBusy      atomic.Uint64
	maintenanceCompleted atomic.Uint64
	pumpEntered          atomic.Uint64
	ownerAdmitted        atomic.Uint64
	ownerBusy            atomic.Uint64
	ownerRefused         atomic.Uint64
	gated                atomic.Uint64
	lastGate             atomic.Uint32
	planned              atomic.Uint64
	runAdmissionRefused  atomic.Uint64
	finishEntered        atomic.Uint64
	finishOwnerAdmitted  atomic.Uint64
	finishOwnerBusy      atomic.Uint64
	finishOwnerRefused   atomic.Uint64
	finishSaved          atomic.Uint64
	finishSaveFailed     atomic.Uint64
	finishNoRow          atomic.Uint64
}

type outboxObservedGate uint32

const (
	outboxGateNone outboxObservedGate = iota
	outboxGateProfile
	outboxGateNamespace
	outboxGateClock
	outboxGateContext
	outboxGateRetirement
	outboxGatePersist
)

func (o *outboxObserver) pumpFinished(admitted bool, err error, gate outboxObservedGate, planned int) {
	if o == nil {
		return
	}
	if admitted {
		o.ownerAdmitted.Add(1)
	} else if networkErrorCode(err) == "outbox_storage_busy" {
		o.ownerBusy.Add(1)
	} else if err != nil {
		o.ownerRefused.Add(1)
	}
	if gate != outboxGateNone {
		o.gated.Add(1)
		o.lastGate.Store(uint32(gate))
	}
	o.planned.Add(uint64(planned))
}

func (o *outboxObserver) runFinished(admitted, saveAttempted bool, err error) {
	if o == nil {
		return
	}
	if !admitted {
		if networkErrorCode(err) == "outbox_storage_busy" {
			o.finishOwnerBusy.Add(1)
		} else {
			o.finishOwnerRefused.Add(1)
		}
		return
	}
	o.finishOwnerAdmitted.Add(1)
	if !saveAttempted {
		o.finishNoRow.Add(1)
	} else if err != nil {
		o.finishSaveFailed.Add(1)
	} else {
		o.finishSaved.Add(1)
	}
}
