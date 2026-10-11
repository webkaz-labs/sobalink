package resourcegrant

import (
	"sync/atomic"
	"time"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/resource"
)

type CatalogViewSelector struct {
	ProtocolVersion int             `json:"protocolVersion"`
	Target          resource.Target `json:"target"`
	GrantID         string          `json:"grantId"`
	GrantRevision   uint64          `json:"grantRevision"`
}

func (s CatalogViewSelector) Validate() error {
	if s.ProtocolVersion != CatalogViewVersion || s.Target.Validate() != nil || !resource.ValidID(s.GrantID) || s.GrantRevision == 0 || s.GrantRevision > uint64(capacity.MaxJSONInteger) {
		return ErrInvalid
	}
	return nil
}

func (r CatalogViewRecord) Selector() CatalogViewSelector {
	return CatalogViewSelector{CatalogViewVersion, r.Target, r.ID, r.Revision}
}

func (e CatalogViewEnvelope) ActiveRecord() CatalogViewRecord {
	for _, r := range e.Records {
		if r.State == Active {
			return r
		}
	}
	return CatalogViewRecord{}
}

// Expiry is a terminal reduction in the catalog's independent revision domain.
func ExpireCatalogViewEnvelope(e CatalogViewEnvelope) (CatalogViewEnvelope, error) {
	if e.Validate() != nil {
		return CatalogViewEnvelope{}, ErrInvalid
	}
	active := e.ActiveRecord()
	if active.ID == "" {
		return e, nil
	}
	if *e.HighWater >= uint64(capacity.MaxJSONInteger) {
		return CatalogViewEnvelope{}, ErrInvalid
	}
	next := e
	high := *e.HighWater + 1
	next.HighWater = &high
	next.Records = make([]CatalogViewRecord, 0, len(e.Records))
	for _, r := range e.Records {
		if r.ID != active.ID {
			next.Records = append(next.Records, r)
		}
	}
	active.State, active.Revision = Expired, high
	next.Records = append(next.Records, active)
	return next, next.Validate()
}

func ObserveCatalogViewEnvelope(e CatalogViewEnvelope, now time.Time) (CatalogViewEnvelope, error) {
	if e.Validate() != nil {
		return CatalogViewEnvelope{}, ErrInvalid
	}
	clock, err := e.Clock.Observe(now)
	if err != nil {
		return CatalogViewEnvelope{}, err
	}
	next := e
	next.Clock = &clock
	next.Records = append([]CatalogViewRecord{}, e.Records...)
	active := next.ActiveRecord()
	if active.ID != "" && now.Unix() >= active.ExpiresAt {
		return ExpireCatalogViewEnvelope(next)
	}
	return next, next.Validate()
}

// CatalogViewFence is runtime-only, minted only after owned durable approval.
// Admit orders revocation against one precomputed bounded response, not a later
// read or stream. A previously admitted write may finish after revocation.
// Current managed relationship and original transport remain caller duties.
type CatalogViewFence struct {
	selector            CatalogViewSelector
	relationship        Relationship
	services            []string
	issuedAt, expiresAt int64
	deadline            time.Time
	closed              atomic.Bool
	timing              atomic.Uint32
}

func NewCatalogViewFenceBefore(record CatalogViewRecord, cutoff time.Time) (*CatalogViewFence, error) {
	return newCatalogViewFence(record, cutoff, time.Now())
}

func newCatalogViewFence(record CatalogViewRecord, cutoff, now time.Time) (*CatalogViewFence, error) {
	if record.Validate() != nil || record.State != Active || cutoff.IsZero() || !now.Before(cutoff) || now.Unix() < record.IssuedAt || now.Unix() >= record.ExpiresAt {
		return nil, ErrInvalid
	}
	deadline := now.Add(time.Unix(record.ExpiresAt, 0).Sub(now))
	if cutoff.Before(deadline) {
		deadline = cutoff
	}
	return &CatalogViewFence{selector: record.Selector(), relationship: record.Relationship, services: append([]string{}, record.ServiceIDs...), issuedAt: record.IssuedAt, expiresAt: record.ExpiresAt, deadline: deadline}, nil
}

func (f *CatalogViewFence) Close() {
	if f != nil {
		f.closed.Store(true)
	}
}

func (f *CatalogViewFence) Admit(selector CatalogViewSelector, relationship Relationship, serviceIDs []string) bool {
	return f.admit(selector, relationship, serviceIDs, time.Now)
}

func (f *CatalogViewFence) admit(selector CatalogViewSelector, relationship Relationship, serviceIDs []string, clock func() time.Time) bool {
	if f == nil || selector != f.selector || relationship != f.relationship || len(serviceIDs) > len(f.services) {
		return false
	}
	previous := ""
	for _, id := range serviceIDs {
		found := false
		for _, permitted := range f.services {
			if id == permitted {
				found = true
				break
			}
		}
		if !found || id <= previous {
			return false
		}
		previous = id
	}
	if !f.closed.CompareAndSwap(false, false) {
		return false
	}
	now := clock()
	if now.Unix() < f.issuedAt {
		f.timing.Or(2)
		f.Close()
		return false
	}
	if now.Unix() >= f.expiresAt || !now.Before(f.deadline) {
		f.timing.Or(1)
		f.Close()
		return false
	}
	return true
}

func (f *CatalogViewFence) TimingObservation(selector CatalogViewSelector) (expired, uncertain bool) {
	if f == nil || selector != f.selector {
		return false, false
	}
	reason := f.timing.Load()
	return reason&1 != 0, reason&2 != 0
}
