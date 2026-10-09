package core

import (
	"encoding/json"
	"time"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/directlan"
	"github.com/webkaz-labs/sobalink/internal/operationjournal"
	"github.com/webkaz-labs/sobalink/internal/resource"
	"github.com/webkaz-labs/sobalink/internal/resourcegrant"
)

// This is the only producer of current remote operation scope. The caller
// holds Core.op and actual lifecycle ownership, not a decoded authority token.
// It deliberately performs no operation-history lookup.
func (c *Core) authorizeManagementBound(runtime *resourceManagementRuntime, capability *directlan.ManagedManagement, request resourcegrant.ManagementRequest, b *resourcePathBinding) (operationjournal.CurrentBinding, error) {
	var empty operationjournal.CurrentBinding
	g := c.resourceGrants
	if g == nil || g.frozen || g.timeUncertain || g.relationshipDenied || g.managementFence == nil || runtime == nil || g.managementRuntime != runtime || runtime.ctx.Err() != nil || c.ctx.Err() != nil || capability == nil || request.Validate() != nil || !capability.Current(request) {
		return empty, resourcegrant.ErrInvalid
	}
	relationship, epoch, ok := capability.Snapshot()
	if !ok || relationship != runtime.relationship {
		return empty, resourcegrant.ErrInvalid
	}
	if err := c.currentResourceGrantsBound(b); err != nil {
		return empty, err
	}
	if err := c.observeResourceGrantsBound(b, time.Now()); err != nil {
		return empty, err
	}
	managed, ok := activeManagementGrant(g)
	record := managed.Record
	if !ok || managed.Validate() != nil || g.timeUncertain || managementSelector(record) != request.ManagementSelector || record.Relationship != relationship || g.bootGrantID != record.ID || g.bootRevision != record.Revision || g.bootDeadline.IsZero() {
		return empty, resourcegrant.ErrInvalid
	}
	current, err := c.resourceGrantRelationship(record.Relationship.PeerKey, time.Now())
	if err != nil {
		return empty, resourcegrant.ErrInvalid
	}
	if current != relationship {
		g.relationshipDenied = true
		g.closeFences()
		g.retireRuntime()
		return empty, resourcegrant.ErrInvalid
	}
	binding := operationjournal.CurrentBinding{
		Scope:               operationjournal.RemoteOperationScope{Target: record.Target, Relationship: relationship, GrantID: record.ID, ScopeVersion: operationjournal.ScopeVersion},
		AuthorizingRevision: record.Revision, IssuanceNonce: c.resourceNonce, ManagedGeneration: epoch,
	}
	if binding.Validate() != nil {
		return empty, resourcegrant.ErrInvalid
	}
	// Fresh owned recertification follows the relationship projection. Fence
	// admission samples wall/monotonic time again under the transport final gate.
	if err := c.currentResourceGrantsBound(b); err != nil {
		return empty, err
	}
	if err := c.observeResourceGrantsBound(b, time.Now()); err != nil {
		return empty, err
	}
	if g.frozen || g.timeUncertain || runtime.ctx.Err() != nil {
		return empty, resourcegrant.ErrInvalid
	}
	managed, ok = activeManagementGrant(g)
	if !ok || managementSelector(managed.Record) != request.ManagementSelector || managed.Record.Relationship != relationship {
		return empty, resourcegrant.ErrInvalid
	}
	return binding, nil
}

// A pure view never migrates or allocates a slot. Frozen evidence is retained
// conservatively in memory; terminal durability is carried by its saved phase.
func managementJournalView(state resourceEnvelope) (operationjournal.EnvelopeV2, error) {
	if state.validate() != nil {
		return operationjournal.EnvelopeV2{}, operationjournal.ErrInvalid
	}
	if state.scoped != nil {
		return *scopedResourceEnvelope(*state.scoped).scoped, nil
	}
	return operationjournal.ConvertV1(operationjournal.LegacyEnvelope{SchemaVersion: state.SchemaVersion, ResourceID: state.ResourceID, HighWater: state.HighWater, Records: state.Records})
}

// Called only after current authorization. A changed owned journal cannot be
// used to acknowledge historical evidence or to overwrite an unknown update.
func (c *Core) currentManagementJournalBound(b *resourcePathBinding) error {
	deny := func() error {
		c.resourceFrozen, c.resourceRemoteEvidenceDenied = true, true
		return resourcegrant.ErrInvalid
	}
	if c.resourceRemoteEvidenceDenied {
		return resourcegrant.ErrInvalid
	}
	if b == nil || b.check() != nil || c.resourceState.validate() != nil {
		return deny()
	}
	// Frozen self-publication still requires a fresh owned disk read on EVERY
	// evidence request. A later deletion, corruption, or unknown replacement
	// cannot be hidden by the in-memory UNKNOWN retained after the write error.
	current, err := readResourceEnvelope(resourceStatePath(c.dir))
	if err != nil || b.check() != nil {
		return deny()
	}
	digest := resourceDigest(current)
	if digest != resourceDigest(c.resourceState) {
		possible := c.resourceUncertainWrite
		if !c.resourceFrozen || possible == nil || digest != possible.before && digest != possible.attempted {
			return deny()
		}
	}
	// A matching possible completion proves only that this owned read matches
	// an exact attempted self-write. Keep the original in-memory intent and
	// false evidenceDurable; only owned reopen may recertify terminal evidence.
	return nil
}

func managementOperationReply(request resourcegrant.ManagementRequest, record operationjournal.RemoteOperationRecord) resourcegrant.ManagementReply {
	durable := record.Phase == "result"
	return resourcegrant.ManagementReply{ManagementSelector: request.ManagementSelector, Action: request.Action,
		Operation: &resourcegrant.ManagementOperation{OperationID: record.Request.OperationID, Outcome: record.Outcome, EvidenceDurable: &durable}}
}
func managementReviewRevision(operationID, base, proposed string, settings resource.Settings) string {
	return resourceDigest(struct {
		Domain, OperationID, Base, Proposed string
		Settings                            resource.Settings
	}{"remote-management-review-v1", operationID, base, proposed, settings})
}

func (c *Core) managementOperationBound(runtime *resourceManagementRuntime, capability *directlan.ManagedManagement, request resourcegrant.ManagementRequest, binding operationjournal.CurrentBinding, b *resourcePathBinding) (resourcegrant.ManagementReply, error) {
	reply := resourcegrant.ManagementReply{ManagementSelector: request.ManagementSelector, Action: request.Action}
	if request.Action == resourcegrant.Inspect {
		current := c.capacityPolicy()
		reply.Inspection = &resourcegrant.ManagementInspection{Requested: cloneResourceSettings(resourceSettings(current)), Effective: resourceEffective(current)}
		return reply, reply.Validate()
	}
	if err := c.currentManagementJournalBound(b); err != nil {
		return resourcegrant.ManagementReply{}, err
	}
	journal, err := managementJournalView(c.resourceState)
	if err != nil {
		return resourcegrant.ManagementReply{}, err
	}
	switch request.Action {
	case resourcegrant.StatusAction:
		// FindRemote scans the entire bounded scoped envelope. No raw-ID lookup,
		// global count, local actor, sequence or provider name reaches this DTO.
		if record, ok := operationjournal.FindRemote(journal, binding.Scope, request.Status.OperationID); ok {
			return managementOperationReply(request, record), nil
		}
		reply.Unavailable = &resourcegrant.ManagementStatusRequest{OperationID: request.Status.OperationID}
		return reply, nil
	case resourcegrant.PreviewAction:
		if c.resourceFrozen {
			return resourcegrant.ManagementReply{}, resourcegrant.ErrInvalid
		}
		current, profile := c.capacityPolicy(), c.profileCopy()
		settings := cloneResourceSettings(request.Preview.Settings)
		proposed := resourceProjection(current, settings)
		// Use the canonical provider's validation, but never expose its private
		// view or call its apply path during a preview.
		policyRequest, _ := json.Marshal(struct {
			Policy capacity.Policy `json:"policy"`
		}{proposed})
		if _, err := c.capacityCommand("policy.preview", policyRequest); err != nil {
			return resourcegrant.ManagementReply{}, resourcegrant.ErrInvalid
		}
		id, err := operationjournal.CurrentRemoteID(journal, binding)
		if err != nil {
			return resourcegrant.ManagementReply{}, err
		}
		base := c.resourceRevision(current, profile)
		reply.Preview = &resourcegrant.ManagementPreview{OperationID: id, BaseRevision: base, ReviewRevision: managementReviewRevision(id, base, capacityRevision(proposed, profile), settings), Requested: settings, Effective: resourceEffective(proposed)}
		return reply, reply.Validate()
	case resourcegrant.ApplyAction:
		return c.applyManagementBound(runtime, capability, request, binding, journal, b)
	default:
		return resourcegrant.ManagementReply{}, resourcegrant.ErrInvalid
	}
}

func (c *Core) applyManagementBound(runtime *resourceManagementRuntime, capability *directlan.ManagedManagement, request resourcegrant.ManagementRequest, binding operationjournal.CurrentBinding, journal operationjournal.EnvelopeV2, b *resourcePathBinding) (resourcegrant.ManagementReply, error) {
	in := *request.Apply
	// This current authorization was completed before reaching any history.
	// Retained matching uses the original nonce, epoch and authorizing revision,
	// and therefore precedes fresh current-token and base/review comparisons.
	retained, fresh := operationjournal.MatchApply(journal, binding, in)
	if retained {
		record, ok := operationjournal.FindRemote(journal, binding.Scope, in.OperationID)
		if !ok {
			return resourcegrant.ManagementReply{}, resourcegrant.ErrInvalid
		}
		return managementOperationReply(request, record), nil
	}
	if !fresh || c.resourceFrozen {
		return resourcegrant.ManagementReply{}, resourcegrant.ErrInvalid
	}
	current, profile := c.capacityPolicy(), c.profileCopy()
	proposed := resourceProjection(current, in.Settings)
	base := c.resourceRevision(current, profile)
	if in.BaseRevision != base || in.ReviewRevision != managementReviewRevision(in.OperationID, base, capacityRevision(proposed, profile), in.Settings) {
		return resourcegrant.ManagementReply{}, resourcegrant.ErrInvalid
	}
	intent, err := operationjournal.NewRemoteIntent(binding, *journal.HighWater+1, in)
	if err != nil {
		return resourcegrant.ManagementReply{}, err
	}
	if _, err = operationjournal.WithIntent(journal, intent); err != nil {
		return resourcegrant.ManagementReply{}, err
	}
	// Recertify before the first write. Preserve-all conversion is a distinct
	// publication; an uncertain conversion never reaches intent or provider.
	if _, err = c.authorizeManagementBound(runtime, capability, request, b); err != nil {
		return resourcegrant.ManagementReply{}, err
	}
	if err = c.migrateResourceJournalBound(intent, b); err != nil {
		if atomicPublished(err) {
			c.resourceFrozen = true
		}
		return resourcegrant.ManagementReply{}, err
	}
	// Migration itself is not execution evidence and may outlive permission.
	if _, err = c.authorizeManagementBound(runtime, capability, request, b); err != nil {
		return resourcegrant.ManagementReply{}, err
	}
	next, err := operationjournal.WithIntent(*c.resourceState.scoped, intent)
	if err != nil {
		return resourcegrant.ManagementReply{}, err
	}
	pending := scopedResourceEnvelope(next)
	if err = c.writeResourceEnvelopeBound(pending, b); err != nil {
		if atomicPublished(err) {
			c.resourceState, c.resourceFrozen = pending, true
			return managementOperationReply(request, *intent.Remote), nil
		}
		return resourcegrant.ManagementReply{}, err
	}
	c.resourceState = pending
	return c.finishManagementIntentBound(runtime, capability, request, intent, proposed, b)
}

// Only the admitted-intent path calls this helper. Even a missing capability
// produces durable not-attempted evidence, never a provider fallback.
func (c *Core) finishManagementIntentBound(runtime *resourceManagementRuntime, capability *directlan.ManagedManagement, request resourcegrant.ManagementRequest, intent operationjournal.TaggedRecord, proposed capacity.Policy, b *resourcePathBinding) (resourcegrant.ManagementReply, error) {
	outcome := resource.Outcome{Status: "canceled", Configuration: "not_attempted", Accounting: "not_attempted", Transfer: "not_attempted"}
	// No write lease or transport borrow remains here. The concrete transport
	// consumes its exact request's provider bit. It never invokes a callback.
	if _, err := c.authorizeManagementBound(runtime, capability, request, b); err == nil && capability.AdmitProvider(c.resourceGrants.managementFence) {
		outcome = resourceProviderOutcome(c.applyCapacityPolicyBound(proposed, b))
	}
	// Revocation/disconnect after intent cannot skip synchronous completion.
	// A failed terminal publication retains UNKNOWN intent and freezes new work.
	return c.completeManagementBound(request, intent, outcome, b)
}

func (c *Core) completeManagementBound(request resourcegrant.ManagementRequest, intent operationjournal.TaggedRecord, outcome resource.Outcome, b *resourcePathBinding) (resourcegrant.ManagementReply, error) {
	completed, err := operationjournal.Complete(*c.resourceState.scoped, intent, outcome)
	if err != nil {
		c.resourceFrozen = true
		return managementOperationReply(request, *intent.Remote), nil
	}
	result := scopedResourceEnvelope(completed)
	if err = c.writeResourceEnvelopeBound(result, b); err != nil {
		c.resourceFrozen = true
		return managementOperationReply(request, *intent.Remote), nil
	}
	c.resourceState = result
	record := *completed.Records[len(completed.Records)-1].Remote
	return managementOperationReply(request, record), nil
}
