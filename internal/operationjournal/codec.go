package operationjournal

import (
	"encoding/json"

	"github.com/webkaz-labs/sobalink/internal/resource"
)

// Decoded is exactly one version-specific envelope. Decoding does not convert
// or publish state. A v1 validation error is never retried as another version.
type Decoded struct {
	Legacy  *LegacyEnvelope
	Journal *EnvelopeV2
}

func Decode(data []byte) (Decoded, error) {
	// The first pass rejects duplicate/null/trailing/excess-depth data and
	// enforces exact envelope fields. The selected second pass also enforces
	// exact record shapes, including nested aliases and unknown fields.
	var header struct {
		SchemaVersion int               `json:"schemaVersion"`
		ResourceID    string            `json:"resourceId"`
		HighWater     *uint64           `json:"highWater"`
		Records       []json.RawMessage `json:"records"`
	}
	if resource.Decode(data, MaxBytes, &header) != nil {
		return Decoded{}, ErrInvalid
	}
	switch header.SchemaVersion {
	case resource.SchemaVersion:
		var e LegacyEnvelope
		if resource.Decode(data, MaxBytes, &e) != nil || e.Validate() != nil {
			return Decoded{}, ErrInvalid
		}
		return Decoded{Legacy: &e}, nil
	case FormatVersion:
		var e EnvelopeV2
		if resource.Decode(data, MaxBytes, &e) != nil || e.Validate() != nil {
			return Decoded{}, ErrInvalid
		}
		return Decoded{Journal: &e}, nil
	default:
		return Decoded{}, ErrInvalid
	}
}
func Encode(e EnvelopeV2) ([]byte, error) {
	if e.Validate() != nil {
		return nil, ErrInvalid
	}
	return json.Marshal(e)
}

// ConvertV1 preserves every local record and high-water value. Conversion has
// no eviction, normalization, intent admission, I/O or durability assertion.
func ConvertV1(old LegacyEnvelope) (EnvelopeV2, error) { return convertV1(old, MaxBytes) }
func convertV1(old LegacyEnvelope, byteLimit int) (EnvelopeV2, error) {
	if old.Validate() != nil {
		return EnvelopeV2{}, ErrInvalid
	}
	high := *old.HighWater
	next := EnvelopeV2{FormatVersion, old.ResourceID, &high, make([]TaggedRecord, 0, len(old.Records))}
	for _, r := range old.Records {
		local := cloneLocal(r)
		next.Records = append(next.Records, TaggedRecord{Kind: LocalKind, Local: &local})
	}
	if next.Validate() != nil {
		return EnvelopeV2{}, ErrInvalid
	}
	data, err := json.Marshal(next)
	if err != nil || len(data) > byteLimit {
		return EnvelopeV2{}, ErrFull
	}
	return next, nil
}
