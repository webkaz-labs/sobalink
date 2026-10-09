package core

import (
	"encoding/json"
	"errors"
	"os"

	"github.com/webkaz-labs/sobalink/internal/operationjournal"
	"github.com/webkaz-labs/sobalink/internal/resource"
)

// The private v2 arm is authoritative. Header mirrors support the unchanged
// local slot protocol; local Records must remain nil for this arm.
func scopedResourceEnvelope(e operationjournal.EnvelopeV2) resourceEnvelope {
	high := *e.HighWater
	e.HighWater = &high
	records := make([]operationjournal.TaggedRecord, len(e.Records))
	for i, r := range e.Records {
		if r.Local != nil {
			v := cloneResourceRecord(*r.Local)
			r.Local = &v
		}
		if r.Remote != nil {
			v := *r.Remote
			v.Request.Settings = cloneResourceSettings(v.Request.Settings)
			r.Remote = &v
		}
		records[i] = r
	}
	e.Records = records
	mirror := high
	return resourceEnvelope{SchemaVersion: e.SchemaVersion, ResourceID: e.ResourceID, HighWater: &mirror, scoped: &e}
}
func cloneResourceSettings(s resource.Settings) resource.Settings {
	if s.TransferConcurrentFiles.Value != nil {
		v := *s.TransferConcurrentFiles.Value
		s.TransferConcurrentFiles.Value = &v
	}
	if s.TransferConcurrentPerPeer.Value != nil {
		v := *s.TransferConcurrentPerPeer.Value
		s.TransferConcurrentPerPeer.Value = &v
	}
	return s
}
func cloneResourceRecord(r resource.Record) resource.Record {
	r.Request.Settings = cloneResourceSettings(r.Request.Settings)
	return r
}
func (s resourceEnvelope) MarshalJSON() ([]byte, error) {
	if s.scoped != nil {
		if err := s.validate(); err != nil {
			return nil, err
		}
		return operationjournal.Encode(*s.scoped)
	}
	// Preserve the v1 wire shape exactly, including invalid fixtures used to
	// prove the strict reader fails closed. No implicit version conversion.
	return json.Marshal(operationjournal.LegacyEnvelope{SchemaVersion: s.SchemaVersion, ResourceID: s.ResourceID, HighWater: s.HighWater, Records: s.Records})
}
func (s resourceEnvelope) normalizeIntents() (resourceEnvelope, error) {
	if err := s.validate(); err != nil {
		return resourceEnvelope{}, err
	}
	if s.scoped != nil {
		next, err := operationjournal.NormalizeIntents(*s.scoped)
		if err != nil {
			return resourceEnvelope{}, err
		}
		return scopedResourceEnvelope(next), nil
	}
	next := s.clone()
	for i := range next.Records {
		if next.Records[i].Phase == "intent" {
			next.Records[i].Phase = "result"
			next.Records[i].Outcome = resource.UnknownOutcome()
		}
	}
	return next, next.validate()
}
func (s resourceEnvelope) withLocalResult(accepted resource.Record, outcome resource.Outcome) (resourceEnvelope, error) {
	if s.scoped != nil {
		next, err := operationjournal.Complete(*s.scoped, operationjournal.TaggedRecord{Kind: operationjournal.LocalKind, Local: &accepted}, outcome)
		if err != nil {
			return resourceEnvelope{}, err
		}
		return scopedResourceEnvelope(next), nil
	}
	next := s.clone()
	accepted.Phase, accepted.Outcome = "result", outcome
	next.Records[len(next.Records)-1] = accepted
	return next, next.validate()
}
func resourceJournalError(err error) error {
	if errors.Is(err, operationjournal.ErrFull) {
		return &localCommandError{"resource_journal_full", "local resource evidence is full; unresolved operations cannot be evicted"}
	}
	if errors.Is(err, operationjournal.ErrExhausted) {
		return resourceSequenceExhausted()
	}
	return err
}

// migrateResourceJournalBound is called only by the authorized remote apply
// coordinator while it holds c.op and actual owned
// lifecycle admission, has rechecked current remote authority, and supplies the exact
// next remote intent. Neither this helper nor a decoded candidate grants it.
// Conversion and intent publication are distinct writes. This method publishes
// only conversion; the preflight reserves nothing and invokes no provider.
func (c *Core) migrateResourceJournalBound(intent operationjournal.TaggedRecord, binding *resourcePathBinding) error {
	if c.resourceFrozen {
		return &localCommandError{"resource_journal_uncertain", "resource evidence could not be certified; restart the owning agent before new applies"}
	}
	if binding == nil || c.resourceIdentity == "" || c.resourceLock == nil || binding.dir != c.dir || c.resourceDirectoryIdentity == nil || !os.SameFile(c.resourceDirectoryIdentity, binding.journalInfo) || binding.check() != nil {
		return errResourceBinding
	}
	if c.resourceState.ResourceID != c.resourceIdentity {
		return operationjournal.ErrInvalid
	}
	if err := c.resourceState.validate(); err != nil {
		return err
	}
	if intent.Kind != operationjournal.RemoteKind || intent.Remote == nil || intent.Local != nil {
		return operationjournal.ErrInvalid
	}
	if c.resourceState.scoped != nil {
		_, err := operationjournal.WithIntent(*c.resourceState.scoped, intent)
		return resourceJournalError(err)
	}
	old := c.resourceState
	candidate, err := operationjournal.ConvertV1(operationjournal.LegacyEnvelope{SchemaVersion: old.SchemaVersion, ResourceID: old.ResourceID, HighWater: old.HighWater, Records: old.Records})
	if err != nil {
		return resourceJournalError(err)
	}
	if _, err = operationjournal.WithIntent(candidate, intent); err != nil {
		return resourceJournalError(err)
	}
	converted := scopedResourceEnvelope(candidate)
	if err := c.writeResourceEnvelopeBound(converted, binding); err != nil {
		if atomicPublished(err) {
			c.resourceState, c.resourceFrozen = converted, true
		}
		return err
	}
	c.resourceState = converted
	return nil
}
