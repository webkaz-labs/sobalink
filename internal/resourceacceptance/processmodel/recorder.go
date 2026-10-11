//go:build resource_process_native

package processmodel

import "sync/atomic"

const (
	admissionClosed = uint64(1) << 63
	activeMask      = admissionClosed - 1
	slotEmpty       = uint32(0)
	slotWriting     = uint32(1)
	slotReady       = uint32(2)
)

type slot struct {
	state atomic.Uint32
	event Event
}

// Recorder must not be copied after construction. Mode is immutable and Record
// is a bounded, nonblocking leaf operation. No actual owner is bound here.
type Recorder struct {
	mode     Mode
	gate     atomic.Uint64
	tickets  atomic.Uint64
	counts   [3]atomic.Uint64
	failures atomic.Uint32
	slots    [MaxOwnerEvents]slot
}

func NewRecorder(mode Mode) (*Recorder, bool) {
	if mode.capacity() == 0 {
		return nil, false
	}
	return &Recorder{mode: mode}, true
}

// Record never returns a product result or waits for a reader. Failure does not
// cancel or change an operation; it only prevents valid observation evidence.
func (r *Recorder) Record(value Observation) {
	if !r.enter() {
		return
	}
	if event, ok := r.reserve(value); ok {
		r.publish(event)
	}
	r.leave()
}

func (r *Recorder) enter() bool {
	gate := r.gate.Add(1)
	if gate&admissionClosed != 0 {
		r.fail(Late)
		r.leave()
		return false
	}
	if r.mode.capacity() == 0 {
		r.fail(Invalid)
		r.leave()
		return false
	}
	// The sampled gate includes rejected callers until they leave. This bounds
	// admitted writers, not unlimited concurrent entry or a live producer set.
	if gate&activeMask > r.mode.capacity() {
		r.fail(Overflow)
		r.leave()
		return false
	}
	return true
}

func (r *Recorder) leave() { r.gate.Add(^uint64(0)) }

func (r *Recorder) fail(flags FailureFlags) { r.failures.Or(uint32(flags)) }

func (r *Recorder) reserve(value Observation) (Event, bool) {
	if r.failures.Load() != 0 {
		return Event{}, false
	}
	sequence := r.tickets.Add(1)
	if sequence == 0 || sequence == ^uint64(0) {
		r.fail(SequenceFailure)
		return Event{}, false
	}
	if r.mode.capacity() == 0 {
		r.fail(Invalid)
		return Event{}, false
	}
	if sequence > r.mode.capacity() {
		r.fail(Overflow)
		return Event{}, false
	}
	if !validObservation(r.mode, value) {
		r.fail(Invalid)
		return Event{}, false
	}
	category := eventCategory(value.Kind)
	limit := uint64(MaxLifecycleEvents)
	if category == resourceCategory {
		limit = MaxResourceEvents
	} else if category == localCategory {
		limit = MaxLocalEvents
	}
	if r.counts[category].Add(1) > limit {
		r.fail(Overflow)
		return Event{}, false
	}
	return Event{Sequence: sequence, Observation: value}, true
}

func (r *Recorder) publish(event Event) {
	if !validEvent(r.mode, event) || event.Sequence > r.tickets.Load() {
		r.fail(Invalid)
		return
	}
	slot := &r.slots[event.Sequence-1]
	if !slot.state.CompareAndSwap(slotEmpty, slotWriting) {
		r.fail(Invalid)
		return
	}
	slot.event = event
	slot.state.Store(slotReady)
}

// CloseAdmission does not join callers, certify a prefix, or seal a live owner.
// Already-admitted writers may finish. Every later call latches Late.
func (r *Recorder) CloseAdmission() { r.gate.Or(admissionClosed) }

// FailGap is a reporter-side evidence latch. This package owns no clock or
// deadline and cannot decide that a live reporter's timeout has elapsed.
func (r *Recorder) FailGap() { r.fail(PrefixGap) }

type Prefix struct {
	Events          [MaxOwnerEvents]Event
	Count           uint16
	ResourceCount   uint16
	LocalCount      uint16
	LifecycleCount  uint16
	Target          uint64
	Reserved        uint64
	Active          uint64
	AdmissionClosed bool
	Failures        FailureFlags
	// Complete describes only this copied range at observation time. It is
	// never a seal: an active or future late caller can invalidate evidence.
	Complete bool
}

// PrefixThrough reads only immutable payloads after an atomic Ready load. Counts
// describe the copied prefix, not a mixture of concurrent category reservations.
func (r *Recorder) PrefixThrough(target uint64) Prefix {
	prefix := Prefix{Target: target, Reserved: r.tickets.Load()}
	if r.mode.capacity() == 0 || target > r.mode.capacity() {
		r.fail(Invalid)
	} else {
		for sequence := uint64(1); sequence <= target; sequence++ {
			slot := &r.slots[sequence-1]
			if slot.state.Load() != slotReady {
				break
			}
			event := slot.event
			prefix.Events[prefix.Count] = event
			prefix.Count++
			switch eventCategory(event.Observation.Kind) {
			case resourceCategory:
				prefix.ResourceCount++
			case localCategory:
				prefix.LocalCount++
			case lifecycleCategory:
				prefix.LifecycleCount++
			}
		}
	}
	gate := r.gate.Load()
	prefix.Active, prefix.AdmissionClosed = gate&activeMask, gate&admissionClosed != 0
	prefix.Failures = FailureFlags(r.failures.Load())
	prefix.Complete = uint64(prefix.Count) == target && target <= prefix.Reserved && prefix.Failures == 0
	return prefix
}
