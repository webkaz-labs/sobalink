package resourcegrant

import (
	"sync/atomic"
	"time"
)

// DisclosureFence is runtime-only authority minted by the owned, confirmed
// local grant coordinator after durable publication. It is never decoded from
// wire or disk data. Do not copy a fence, reopen it, or reuse it for a revision.
// A constructor call is not startup policy: only the coordinator may activate
// eligible saved state. The caller must retain its private ownership checks.
type DisclosureFence struct {
	request             InspectRequest
	relationship        Relationship
	issuedAt, expiresAt int64
	deadline            time.Time
	closed              atomic.Bool
	timing              atomic.Uint32 // terminal observations: expiry=1, uncertainty=2
}

func NewDisclosureFence(record Record) (*DisclosureFence, error) {
	return newDisclosureFence(record, time.Now())
}

// NewDisclosureFenceBefore bounds activation by a coordinator-retained boot
// cutoff. Delayed backend readiness and reconnects cannot renew its lifetime.
func NewDisclosureFenceBefore(record Record, cutoff time.Time) (*DisclosureFence, error) {
	now := time.Now()
	if cutoff.IsZero() || !now.Before(cutoff) {
		return nil, ErrInvalid
	}
	fence, err := newDisclosureFence(record, now)
	if err != nil {
		return nil, err
	}
	if cutoff.Before(fence.deadline) {
		fence.deadline = cutoff
	}
	return fence, nil
}

func newDisclosureFence(record Record, now time.Time) (*DisclosureFence, error) {
	if record.Validate() != nil || record.State != Active || now.Unix() < record.IssuedAt || now.Unix() >= record.ExpiresAt {
		return nil, ErrInvalid
	}
	// Add the remaining absolute lifetime to this process's monotonic clock.
	// Computing a Unix-second delta would extend expiry by a fractional second.
	remaining := time.Unix(record.ExpiresAt, 0).Sub(now)
	if remaining <= 0 {
		return nil, ErrInvalid
	}
	return &DisclosureFence{
		request:      InspectRequest{ProtocolVersion: ProtocolVersion, Target: record.Target, GrantID: record.ID, GrantRevision: record.Revision},
		relationship: record.Relationship, issuedAt: record.IssuedAt, expiresAt: record.ExpiresAt,
		deadline: now.Add(remaining),
	}, nil
}

// Close orders revocation before persistence. Previously admitted writes may
// finish; Close neither waits for them nor promises that bytes are retracted.
func (f *DisclosureFence) Close() {
	if f != nil {
		f.closed.Store(true)
	}
}

// Admit is called only by the concrete transport response capability at its
// final, bounded write admission. The request and relationship narrow existing
// authority; they cannot create a fence. No callbacks, I/O or provider work run
// here. Current relationship validity remains the transport's responsibility.
func (f *DisclosureFence) Admit(request InspectRequest, relationship Relationship) bool {
	return f.admit(request, relationship, time.Now)
}

func (f *DisclosureFence) admit(request InspectRequest, relationship Relationship, clock func() time.Time) bool {
	if f == nil || request != f.request || relationship != f.relationship || !f.closed.CompareAndSwap(false, false) {
		return false
	}
	// The successful CAS is the revocation ordering point. A fresh clock check
	// can reject that admission; a later revoke does not undo an admitted write.
	now := clock()
	if now.Unix() < f.issuedAt {
		f.timing.Or(2)
		f.closed.Store(true)
		return false
	}
	if now.Unix() >= f.expiresAt || !now.Before(f.deadline) {
		// Publish the terminal reason before closed. A completed observation cannot
		// be missed by the owned coordinator even if wall time later moves back.
		f.timing.Or(1)
		f.closed.Store(true)
		return false
	}
	return true
}

// TimingObservation exposes only terminal denial evidence for this exact
// immutable grant selector. It cannot clear a fence or grant authority. An
// in-memory observation survives restart only after owned durable publication.
func (f *DisclosureFence) TimingObservation(request InspectRequest) (expired, uncertain bool) {
	if f == nil || request != f.request {
		return false, false
	}
	reason := f.timing.Load()
	return reason&1 != 0, reason&2 != 0
}
