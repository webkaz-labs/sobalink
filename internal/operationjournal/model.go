// Package operationjournal models bounded private operation evidence. Its pure
// helpers establish neither authentication nor durable storage.
package operationjournal

import (
	"encoding/json"
	"errors"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/resource"
	"github.com/webkaz-labs/sobalink/internal/resourcegrant"
)

const (
	FormatVersion = 2
	ScopeVersion  = 1
	MaxRecords    = 128
	MaxBytes      = 256 << 10
	// MaxRecordBytes includes the complete tagged wrapper, not just its payload.
	MaxRecordBytes = 2048
	LocalKind      = "local"
	RemoteKind     = "remote-management"
)

var (
	ErrInvalid   = errors.New("invalid operation journal")
	ErrFull      = errors.New("operation journal is full")
	ErrExhausted = errors.New("operation sequence exhausted")
)

// RemoteOperationScope is an immutable identity, not a grant. ScopeVersion 1
// denotes exactly the full management action/field set, never a partial scope.
// Field order is part of the canonical identifier and replay-hash contract.
type RemoteOperationScope struct {
	Target       resource.Target            `json:"target"`
	Relationship resourcegrant.Relationship `json:"relationship"`
	GrantID      string                     `json:"grantId"`
	ScopeVersion int                        `json:"scopeVersion"`
}

func (s RemoteOperationScope) Validate() error {
	if s.Target.Validate() != nil || s.Relationship.Validate() != nil || !resource.ValidID(s.GrantID) || s.ScopeVersion != ScopeVersion {
		return ErrInvalid
	}
	return nil
}

// AuthorizingRevision remains the original revision even when a later current
// selector authorizes a status query. IssuanceNonce only validates old evidence;
// it must never be reused as a fresh operation's current-boot issuance input.
type RemoteOperationRecord struct {
	Sequence            uint64                               `json:"sequence"`
	Scope               RemoteOperationScope                 `json:"scope"`
	AuthorizingRevision uint64                               `json:"authorizingRevision"`
	IssuanceNonce       string                               `json:"issuanceNonce"`
	ManagedGeneration   string                               `json:"managedGeneration"`
	Request             resourcegrant.ManagementApplyRequest `json:"request"`
	RequestHash         string                               `json:"requestHash"`
	Phase               string                               `json:"phase"`
	Outcome             resource.Outcome                     `json:"outcome"`
}

type TaggedRecord struct {
	Kind   string                 `json:"kind"`
	Local  *resource.Record       `json:"local,omitempty"`
	Remote *RemoteOperationRecord `json:"remote,omitempty"`
}

type EnvelopeV2 struct {
	SchemaVersion int            `json:"schemaVersion"`
	ResourceID    string         `json:"resourceId"`
	HighWater     *uint64        `json:"highWater"`
	Records       []TaggedRecord `json:"records"`
}

// LegacyEnvelope mirrors the unchanged v1 storage shape for pure conversion.
// Owned Core storage selects the version and publishes conversion separately.
type LegacyEnvelope struct {
	SchemaVersion int               `json:"schemaVersion"`
	ResourceID    string            `json:"resourceId"`
	HighWater     *uint64           `json:"highWater"`
	Records       []resource.Record `json:"records"`
}

func validRevision(v uint64) bool { return v > 0 && v <= uint64(capacity.MaxJSONInteger) }
func validApply(r resourcegrant.ManagementApplyRequest) bool {
	return resource.ValidDigest(r.OperationID) && resource.ValidDigest(r.BaseRevision) && resource.ValidDigest(r.ReviewRevision) && r.Settings.Validate() == nil
}
func validPhase(phase string, outcome resource.Outcome) bool {
	return outcome.Validate() == nil && (phase == "result" || phase == "intent" && outcome == resource.UnknownOutcome())
}
func (r RemoteOperationRecord) Validate(id string, highWater uint64) error {
	b := r.binding()
	if b.Validate() != nil || r.Scope.Target.ResourceID != id || r.Sequence == 0 || r.Sequence > highWater || !validApply(r.Request) || !validPhase(r.Phase, r.Outcome) {
		return ErrInvalid
	}
	token, err := operationID(b, r.Sequence)
	if err != nil || token != r.Request.OperationID || r.RequestHash != remoteRequestHash(r) {
		return ErrInvalid
	}
	return nil
}
func (r TaggedRecord) sequence() uint64 {
	if r.Kind == LocalKind && r.Local != nil {
		_, _, seq, _ := resource.ParseOperationID(r.Local.Request.OperationID)
		return seq
	}
	if r.Kind == RemoteKind && r.Remote != nil {
		return r.Remote.Sequence
	}
	return 0
}
func (r TaggedRecord) Validate(id string, highWater uint64) error {
	switch r.Kind {
	case LocalKind:
		if r.Local == nil || r.Remote != nil || r.Local.Validate(id, highWater) != nil {
			return ErrInvalid
		}
	case RemoteKind:
		if r.Remote == nil || r.Local != nil || r.Remote.Validate(id, highWater) != nil {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	data, err := json.Marshal(r)
	if err != nil || len(data) > MaxRecordBytes {
		return ErrInvalid
	}
	return nil
}
func (r TaggedRecord) Pinned() bool {
	switch r.Kind {
	case LocalKind:
		return r.Local == nil || r.Local.Pinned()
	case RemoteKind:
		return r.Remote == nil || r.Remote.Phase == "intent" || r.Remote.Outcome.Status == "unknown"
	default:
		return true
	}
}
func validEnvelopeHeader(version, expected int, id string, high *uint64, n int, nonNil bool) bool {
	return version == expected && resource.ValidID(id) && high != nil && nonNil && n <= MaxRecords
}
func validHighWater(high uint64, n int, last uint64) bool {
	return (high == 0) == (n == 0) && (n == 0 || last == high)
}
func (e EnvelopeV2) Validate() error {
	if !validEnvelopeHeader(e.SchemaVersion, FormatVersion, e.ResourceID, e.HighWater, len(e.Records), e.Records != nil) {
		return ErrInvalid
	}
	var previous uint64
	seenRemote := make(map[string]bool, len(e.Records))
	for _, r := range e.Records {
		if r.Validate(e.ResourceID, *e.HighWater) != nil || r.sequence() <= previous {
			return ErrInvalid
		}
		if r.Remote != nil {
			if seenRemote[r.Remote.Request.OperationID] {
				return ErrInvalid
			}
			seenRemote[r.Remote.Request.OperationID] = true
		}
		previous = r.sequence()
	}
	if !validHighWater(*e.HighWater, len(e.Records), previous) {
		return ErrInvalid
	}
	data, err := json.Marshal(e)
	if err != nil || len(data) > MaxBytes {
		return ErrInvalid
	}
	return nil
}
func (e LegacyEnvelope) Validate() error {
	if !validEnvelopeHeader(e.SchemaVersion, resource.SchemaVersion, e.ResourceID, e.HighWater, len(e.Records), e.Records != nil) {
		return ErrInvalid
	}
	var previous uint64
	for _, r := range e.Records {
		if r.Validate(e.ResourceID, *e.HighWater) != nil {
			return ErrInvalid
		}
		_, _, seq, _ := resource.ParseOperationID(r.Request.OperationID)
		data, err := json.Marshal(r)
		if seq <= previous || err != nil || len(data) > MaxRecordBytes {
			return ErrInvalid
		}
		previous = seq
	}
	if !validHighWater(*e.HighWater, len(e.Records), previous) {
		return ErrInvalid
	}
	data, err := json.Marshal(e)
	if err != nil || len(data) > MaxBytes {
		return ErrInvalid
	}
	return nil
}
