package operationjournal

import (
	"encoding/json"
	"math"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/resource"
	"github.com/webkaz-labs/sobalink/internal/resourcegrant"
)

func cloneChoice(c capacity.Choice) capacity.Choice {
	if c.Value != nil {
		n := *c.Value
		c.Value = &n
	}
	return c
}
func cloneSettings(s resource.Settings) resource.Settings {
	s.TransferConcurrentFiles = cloneChoice(s.TransferConcurrentFiles)
	s.TransferConcurrentPerPeer = cloneChoice(s.TransferConcurrentPerPeer)
	return s
}
func cloneApply(r resourcegrant.ManagementApplyRequest) resourcegrant.ManagementApplyRequest {
	r.Settings = cloneSettings(r.Settings)
	return r
}
func cloneLocal(r resource.Record) resource.Record {
	r.Request.Settings = cloneSettings(r.Request.Settings)
	return r
}
func cloneTagged(r TaggedRecord) TaggedRecord {
	if r.Local != nil {
		local := cloneLocal(*r.Local)
		r.Local = &local
	}
	if r.Remote != nil {
		remote := *r.Remote
		remote.Request = cloneApply(remote.Request)
		r.Remote = &remote
	}
	return r
}
func (e EnvelopeV2) clone() EnvelopeV2 {
	high := *e.HighWater
	e.HighWater = &high
	records := make([]TaggedRecord, len(e.Records))
	for i, r := range e.Records {
		records[i] = cloneTagged(r)
	}
	e.Records = records
	return e
}
func nextSequence(e EnvelopeV2) (uint64, error) {
	if e.Validate() != nil {
		return 0, ErrInvalid
	}
	if *e.HighWater == math.MaxUint64 {
		return 0, ErrExhausted
	}
	return *e.HighWater + 1, nil
}

// CurrentRemoteID derives exactly one next-slot identifier from current server
// inputs. Callers must never substitute a retained record's historical nonce.
// This opaque ID is not a bearer credential or evidence of current permission.
func CurrentRemoteID(e EnvelopeV2, current CurrentBinding) (string, error) {
	sequence, err := nextSequence(e)
	if err != nil {
		return "", err
	}
	if current.Scope.Target.ResourceID != e.ResourceID {
		return "", ErrInvalid
	}
	return operationID(current, sequence)
}

// FindRemote only selects retained scoped evidence, never arbitrary history.
// Future callers MUST authorize the current relationship/grant/selector before
// invoking it. This pure helper cannot enforce or claim that prerequisite.
// Missing, evicted, local-owned and other-scope IDs have the same result.
func FindRemote(e EnvelopeV2, scope RemoteOperationScope, id string) (RemoteOperationRecord, bool) {
	if e.Validate() != nil || scope.Validate() != nil || !resource.ValidDigest(id) {
		return RemoteOperationRecord{}, false
	}
	var found RemoteOperationRecord
	matched := false
	for _, r := range e.Records {
		if r.Kind == RemoteKind && r.Remote.Scope == scope && r.Remote.Request.OperationID == id {
			found = *r.Remote
			found.Request = cloneApply(found.Request)
			matched = true
		}
	}
	return found, matched
}

// MatchApply classifies a bounded retained replay or one fresh current token.
// It performs no authorization, provider admission, history enumeration or hash
// reversal. A retained request always hashes its ORIGINAL issuance binding,
// regardless of a later selector's current authorizing revision/generation.
// Both false includes conflicting replay, foreign/local/evicted/old-boot IDs.
func MatchApply(e EnvelopeV2, current CurrentBinding, request resourcegrant.ManagementApplyRequest) (retained, fresh bool) {
	if current.Validate() != nil || !validApply(request) {
		return false, false
	}
	if record, ok := FindRemote(e, current.Scope, request.OperationID); ok {
		candidate := record
		candidate.Request = request
		return remoteRequestHash(candidate) == record.RequestHash, false
	}
	token, err := CurrentRemoteID(e, current)
	return false, err == nil && request.OperationID == token
}

// WithIntent reserves a complete maximum-sized terminal tagged record before
// a future provider call. It only returns a candidate; nothing is published.
func WithIntent(e EnvelopeV2, record TaggedRecord) (EnvelopeV2, error) {
	sequence, err := nextSequence(e)
	if err != nil {
		return EnvelopeV2{}, err
	}
	if record.Validate(e.ResourceID, sequence) != nil || record.sequence() != sequence || record.Local != nil && record.Local.Phase != "intent" || record.Remote != nil && record.Remote.Phase != "intent" {
		return EnvelopeV2{}, ErrInvalid
	}
	next := e.clone()
	*next.HighWater = sequence
	record = cloneTagged(record)
	recordData, _ := json.Marshal(record)
	for {
		trial := next.clone()
		trial.Records = append(trial.Records, cloneTagged(record))
		encoded, err := json.Marshal(trial)
		if err == nil && len(trial.Records) <= MaxRecords && len(encoded)+MaxRecordBytes-len(recordData) <= MaxBytes {
			if trial.Validate() != nil {
				return EnvelopeV2{}, ErrInvalid
			}
			return trial, nil
		}
		evict := -1
		for i, previous := range next.Records {
			if !previous.Pinned() {
				evict = i
				break
			}
		}
		if evict < 0 {
			return EnvelopeV2{}, ErrFull
		}
		next.Records = append(next.Records[:evict], next.Records[evict+1:]...)
	}
}

// Complete replaces only the exact most recently accepted intent. No caller
// can replace its immutable request, scope, issuance inputs, or sequence.
func Complete(e EnvelopeV2, accepted TaggedRecord, outcome resource.Outcome) (EnvelopeV2, error) {
	if e.Validate() != nil || outcome.Validate() != nil || len(e.Records) == 0 || accepted.Validate(e.ResourceID, *e.HighWater) != nil {
		return EnvelopeV2{}, ErrInvalid
	}
	last := e.Records[len(e.Records)-1]
	before, _ := json.Marshal(last)
	expected, _ := json.Marshal(accepted)
	if string(before) != string(expected) || last.Local != nil && last.Local.Phase != "intent" || last.Remote != nil && last.Remote.Phase != "intent" {
		return EnvelopeV2{}, ErrInvalid
	}
	next := e.clone()
	completed := &next.Records[len(next.Records)-1]
	if completed.Local != nil {
		completed.Local.Phase = "result"
		completed.Local.Outcome = outcome
	} else {
		completed.Remote.Phase = "result"
		completed.Remote.Outcome = outcome
	}
	if next.Validate() != nil {
		return EnvelopeV2{}, ErrInvalid
	}
	return next, nil
}

// NormalizeIntents conservatively marks interrupted intents UNKNOWN in memory.
// Only a future owned publication/reopen can establish durability of this state.
func NormalizeIntents(e EnvelopeV2) (EnvelopeV2, error) {
	if e.Validate() != nil {
		return EnvelopeV2{}, ErrInvalid
	}
	next := e.clone()
	for i := range next.Records {
		r := &next.Records[i]
		if r.Local != nil && r.Local.Phase == "intent" {
			r.Local.Phase = "result"
			r.Local.Outcome = resource.UnknownOutcome()
		}
		if r.Remote != nil && r.Remote.Phase == "intent" {
			r.Remote.Phase = "result"
			r.Remote.Outcome = resource.UnknownOutcome()
		}
	}
	if next.Validate() != nil {
		return EnvelopeV2{}, ErrInvalid
	}
	return next, nil
}
