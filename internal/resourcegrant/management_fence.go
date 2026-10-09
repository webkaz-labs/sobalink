package resourcegrant

import (
	"sync/atomic"
	"time"
)

// ManagementFence is separate from the legacy inspection fence. Only the owned
// grant coordinator may mint it after durable current-state certification. It
// is not decoded authority, a transport capability, or a provider permit.
// The concrete managed transport performs independent provider/reply admission.
type ManagementFence struct {
	selector            ManagementSelector
	relationship        Relationship
	issuedAt, expiresAt int64
	deadline            time.Time
	closed              atomic.Bool
	timing              atomic.Uint32
}

func NewManagementFenceBefore(record ManagementRecord, cutoff time.Time) (*ManagementFence, error) {
	return newManagementFenceBefore(record, cutoff, time.Now())
}

func newManagementFenceBefore(record ManagementRecord, cutoff, now time.Time) (*ManagementFence, error) {
	r := record.Record
	if record.Validate() != nil || r.State != Active || now.Unix() < r.IssuedAt || now.Unix() >= r.ExpiresAt || cutoff.IsZero() || !now.Before(cutoff) {
		return nil, ErrInvalid
	}
	deadline := now.Add(time.Unix(r.ExpiresAt, 0).Sub(now))
	if cutoff.Before(deadline) {
		deadline = cutoff
	}
	return &ManagementFence{
		selector:     ManagementSelector{ProtocolVersion: ManagementProtocolVersion, Target: r.Target, GrantID: r.ID, GrantRevision: r.Revision},
		relationship: r.Relationship, issuedAt: r.IssuedAt, expiresAt: r.ExpiresAt, deadline: deadline,
	}, nil
}

func (f *ManagementFence) Close() {
	if f != nil {
		f.closed.Store(true)
	}
}

// Admit is only a terminal grant predicate. The concrete transport must place
// it inside its final admission gate after checking exact current identity,
// endpoint and the one-shot provider bit (or separate response admission).
// It does not call a provider or allow later disclosure by itself. The complete
// fixed action/field scope was validated at construction; no mutable slices are
// retained. There are no callbacks, recursive transport checks, or I/O here.
func (f *ManagementFence) Admit(selector ManagementSelector, action string, relationship Relationship) bool {
	return f.admit(selector, action, relationship, time.Now)
}

func (f *ManagementFence) admit(selector ManagementSelector, action string, relationship Relationship, clock func() time.Time) bool {
	if f == nil || selector != f.selector || relationship != f.relationship || f.closed.Load() {
		return false
	}
	switch action {
	case Inspect, PreviewAction, ApplyAction, StatusAction:
	default:
		return false
	}
	// Order revocation first, then sample the clock freshly. The clock can
	// reject this admission; later revocation does not retract admitted work.
	if !f.closed.CompareAndSwap(false, false) {
		return false
	}
	now := clock()
	if now.Unix() < f.issuedAt {
		f.timing.Or(2)
		f.closed.Store(true)
		return false
	}
	if now.Unix() >= f.expiresAt || !now.Before(f.deadline) {
		f.timing.Or(1)
		f.closed.Store(true)
		return false
	}
	// Preadmitted work may finish; a response needs independent admission.
	return true
}

func (f *ManagementFence) TimingObservation(selector ManagementSelector) (expired, uncertain bool) {
	if f == nil || selector != f.selector {
		return false, false
	}
	reason := f.timing.Load()
	return reason&1 != 0, reason&2 != 0
}
