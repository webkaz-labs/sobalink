package core

import (
	"context"
	"encoding/json"
	"math"

	"github.com/webkaz-labs/sobalink/internal/resource"
)

func resourceSequenceExhausted() error {
	return &localCommandError{"resource_sequence_exhausted", "local resource operation sequence is exhausted; no new operation can be admitted"}
}
func resourceConflict() error {
	return &localCommandError{"resource_revision_conflict", "resource review or operation slot changed; preview the current settings again"}
}
func (c *Core) resourceOperationView(record resource.Record, durable bool) resource.Operation {
	return resource.Operation{Target: record.Request.Target, OperationID: record.Request.OperationID, Outcome: record.Outcome, EvidenceDurable: durable, Current: c.resourceDescriptor(c.capacityPolicy(), c.profileCopy()), Journal: c.resourceJournalUsage()}
}
func (c *Core) resourceMissingOperation(id string) error {
	_, _, seq, _ := resource.ParseOperationID(id)
	if seq <= *c.resourceState.HighWater {
		return &localCommandError{"resource_operation_not_retained", "operation evidence is not retained; this operation will not be executed again"}
	}
	return &localCommandError{"resource_operation_not_found", "operation evidence was not found; no execution outcome is known"}
}
func (c *Core) resourceStatus(raw json.RawMessage) (any, error) {
	var in resource.StatusRequest
	if resource.Decode(raw, 4096, &in) != nil || in.Target.Validate() != nil {
		return nil, resourcePayloadError()
	}
	id, _, _, err := resource.ParseOperationID(in.OperationID)
	if err != nil || id != in.ResourceID {
		return nil, resourcePayloadError()
	}
	if in.ResourceID != c.resourceIdentity {
		return nil, &localCommandError{"resource_not_found", "local resource was not found; list the current resources"}
	}
	if record, ok := c.resourceState.find(in.OperationID); ok {
		return c.resourceOperationView(record, record.Phase == "result"), nil
	}
	return nil, c.resourceMissingOperation(in.OperationID)
}
func (c *Core) resourceApply(ctx context.Context, raw json.RawMessage, binding *resourcePathBinding) (any, error) {
	var in resource.ApplyRequest
	if resource.Decode(raw, 4096, &in) != nil || in.Validate() != nil {
		return nil, resourcePayloadError()
	}
	if in.ResourceID != c.resourceIdentity {
		return nil, &localCommandError{"resource_not_found", "local resource was not found; list the current resources"}
	}
	digest := resource.RequestHash(in)
	// Historical evidence precedes current boot, sequence and policy checks.
	// The actor is a server-owned constant, never a client-controlled identity.
	if previous, ok := c.resourceState.find(in.OperationID); ok {
		if previous.Actor != resource.LocalActor || previous.RequestHash != digest {
			return nil, &localCommandError{"resource_operation_mismatch", "operation ID was already bound to different content; use its original request"}
		}
		return c.resourceOperationView(previous, previous.Phase == "result"), nil
	}
	if c.resourceFrozen {
		return nil, &localCommandError{"resource_journal_uncertain", "resource evidence could not be certified; restart the owning agent before new applies"}
	}
	_, nonce, seq, _ := resource.ParseOperationID(in.OperationID)
	if seq <= *c.resourceState.HighWater {
		return nil, c.resourceMissingOperation(in.OperationID)
	}
	if *c.resourceState.HighWater == math.MaxUint64 {
		return nil, resourceSequenceExhausted()
	}
	if nonce != c.resourceNonce || seq != *c.resourceState.HighWater+1 {
		return nil, resourceConflict()
	}
	current, profile := c.capacityPolicy(), c.profileCopy()
	proposed := resourceProjection(current, in.Settings)
	base := c.resourceRevision(current, profile)
	if in.BaseRevision != base || in.Revision != resourceReviewRevision(in.OperationID, base, capacityRevision(proposed, profile), in.Settings) {
		return nil, resourceConflict()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	intent := resource.Record{Request: in, Actor: resource.LocalActor, RequestHash: digest, Phase: "intent", Outcome: resource.UnknownOutcome()}
	next, err := c.resourceState.withIntent(intent)
	if err != nil {
		return nil, err
	}
	if err := c.writeResourceEnvelopeBound(next, binding); err != nil {
		if atomicPublished(err) {
			// Publication may have consumed the slot. Keep an unknown pinned copy and
			// freeze admission, never turn uncertain durability into a retryable write.
			c.resourceState, c.resourceFrozen = next, true
			return c.resourceOperationView(intent, false), nil
		}
		return nil, &localCommandError{"resource_journal_write_failed", "resource intent was not saved; no settings change was attempted"}
	}
	c.resourceState = next
	if binding.check() != nil {
		c.resourceFrozen = true
		return c.resourceOperationView(intent, false), nil
	}
	result := intent
	result.Phase = "result"
	if ctx.Err() != nil || c.ctx != nil && c.ctx.Err() != nil {
		result.Outcome = resource.Outcome{Status: "canceled", Configuration: "not_attempted", Accounting: "not_attempted", Transfer: "not_attempted"}
	} else {
		result.Outcome = resourceProviderOutcome(c.applyCapacityPolicyBound(proposed, binding))
	}
	// Synchronous, bounded completion is attempted even after caller/lifetime
	// cancellation. Close waits on c.op; there is no detached evidence goroutine.
	completed := c.resourceState.clone()
	completed.Records[len(completed.Records)-1] = result
	if err := c.writeResourceEnvelopeBound(completed, binding); err != nil {
		c.resourceFrozen = true
		// Keep only intent in memory even when result publication was observed.
		// A later owned startup must validate and durably republish disk evidence.
		return c.resourceOperationView(intent, false), nil
	}
	c.resourceState = completed
	return c.resourceOperationView(result, true), nil
}
func resourceProviderOutcome(out capacityApplyOutcome) resource.Outcome {
	result := resource.Outcome{Status: "failed", Configuration: "not_attempted", Accounting: "not_attempted", Transfer: "not_attempted"}
	if !out.SaveAttempted {
		return result
	}
	if !out.Published {
		result.Configuration = "not_published"
		return result
	}
	result.Status, result.Configuration = "applied", "durable"
	stage := func(attempted bool, err error) string {
		if !attempted {
			return "not_required"
		}
		if err != nil {
			return "failed"
		}
		return "succeeded"
	}
	result.Accounting = stage(out.AccountingAttempted, out.AccountingErr)
	result.Transfer = stage(out.TransferAttempted, out.TransferErr)
	if out.AccountingErr != nil {
		result.Transfer = "not_attempted"
	}
	if out.AccountingErr != nil || out.TransferErr != nil {
		result.Status = "saved_not_applied"
	}
	if out.SaveErr != nil {
		result.Status, result.Configuration = "unknown", "uncertain"
	}
	return result
}
