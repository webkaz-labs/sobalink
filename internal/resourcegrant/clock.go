package resourcegrant

import (
	"encoding/json"
	"github.com/webkaz-labs/sobalink/internal/resource"
	"time"

	"github.com/webkaz-labs/sobalink/internal/capacity"
)

// WallCheckpoint is a bounded durable observation, not a trusted clock. It can
// detect backward movement relative to saved observations, but cannot establish
// elapsed downtime or detect rollback of the complete private state. Seconds
// and nanoseconds remain exactly representable in machine JSON.
type WallCheckpoint struct {
	Seconds     int64 `json:"seconds"`
	Nanoseconds int   `json:"nanoseconds"`
	Uncertain   bool  `json:"uncertain"`
}

func (w WallCheckpoint) Validate() error {
	if w.Seconds <= 0 || w.Seconds > maxTimestamp || w.Nanoseconds < 0 || w.Nanoseconds >= 1e9 {
		return ErrInvalid
	}
	return nil
}

// Observe advances only the checkpoint. Once uncertainty is observed it cannot
// be cleared by a later plausible wall time. Callers must durably publish this
// result before deriving new runtime authority from it.
func (w WallCheckpoint) Observe(now time.Time) (WallCheckpoint, error) {
	if w.Validate() != nil || now.Unix() <= 0 || now.Unix() > maxTimestamp {
		return w, ErrInvalid
	}
	next := WallCheckpoint{Seconds: now.Unix(), Nanoseconds: now.Nanosecond(), Uncertain: w.Uncertain}
	if next.Seconds < w.Seconds || next.Seconds == w.Seconds && next.Nanoseconds < w.Nanoseconds {
		w.Uncertain = true
		return w, nil
	}
	return next, nil
}

func Checkpoint(now time.Time) (WallCheckpoint, error) {
	w := WallCheckpoint{Seconds: now.Unix(), Nanoseconds: now.Nanosecond()}
	return w, w.Validate()
}

// ObserveEnvelope returns a separate value suitable for owned publication.
// Expiry is a terminal authority reduction, preserving the original lifetime.
// This is pure data transformation and never mints a runtime fence.
func ObserveEnvelope(e Envelope, now time.Time) (Envelope, error) {
	if e.Validate() != nil {
		return Envelope{}, ErrInvalid
	}
	clock, err := e.Clock.Observe(now)
	if err != nil {
		return Envelope{}, err
	}
	next := e
	next.Clock = &clock
	next.Records = append([]Record{}, e.Records...)
	next.ManagementRecords = append([]ManagementRecord(nil), e.ManagementRecords...)
	record, _ := next.ActiveRecord()
	if record.ID != "" && now.Unix() >= record.ExpiresAt {
		return ExpireEnvelope(next)
	}
	if next.Validate() != nil {
		return Envelope{}, ErrInvalid
	}
	return next, nil
}

// UnmarshalJSON rejects omitted zero-valued checkpoint decisions. In particular,
// a missing uncertainty flag is not affirmative evidence of clock certainty.
func (w *WallCheckpoint) UnmarshalJSON(data []byte) error {
	type plain WallCheckpoint
	var decoded plain
	if resource.Decode(data, 512, &decoded) != nil {
		return ErrInvalid
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil {
		return ErrInvalid
	}
	for _, field := range []string{"seconds", "nanoseconds", "uncertain"} {
		if _, ok := fields[field]; !ok {
			return ErrInvalid
		}
	}
	next := WallCheckpoint(decoded)
	if next.Validate() != nil {
		return ErrInvalid
	}
	*w = next
	return nil
}

// ExpireEnvelope applies the same terminal reduction when a retained monotonic
// cutoff has elapsed before the current wall clock reaches absolute expiry.
func ExpireEnvelope(e Envelope) (Envelope, error) {
	if e.Validate() != nil {
		return Envelope{}, ErrInvalid
	}
	next := e
	next.Records = append([]Record{}, e.Records...)
	next.ManagementRecords = append([]ManagementRecord(nil), e.ManagementRecords...)
	record, management := next.ActiveRecord()
	if record.ID == "" {
		return next, next.Validate()
	}
	if *e.HighWater >= uint64(capacity.MaxJSONInteger) {
		return Envelope{}, ErrInvalid
	}
	high := *e.HighWater + 1
	next.HighWater = &high
	record.State, record.Revision = Expired, high
	if management {
		for i, m := range next.ManagementRecords {
			if m.Record.ID == record.ID {
				m.Record = record
				next.ManagementRecords = append(append(next.ManagementRecords[:i:i], next.ManagementRecords[i+1:]...), m)
				break
			}
		}
	} else {
		for i, r := range next.Records {
			if r.ID == record.ID {
				next.Records = append(append(next.Records[:i:i], next.Records[i+1:]...), record)
				break
			}
		}
	}
	return next, next.Validate()
}
