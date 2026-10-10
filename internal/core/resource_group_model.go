package core

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/webkaz-labs/sobalink/internal/resourcegroup"
)

// Core.op owns all data below. Only the active pointer is also published under
// Core.mu so Close can cancel/join without holding Core.op. Nothing restores a
// prepared review or starts a saved run during Open.
type resourceGroupCoordinator struct {
	store    *resourceGroupStore
	prepared *resourceGroupPrepared
	active   *resourceGroupActivity
	overlay  *resourceGroupRecord // bounded failed-publication observation, never admission
}

type resourceGroupPrepared struct {
	id               string
	review           resourcegroup.ReviewBody
	origins          []resourceGroupCapturedOrigin
	controllerID     string
	bootNonce        string
	storeRevision    string
	previewInputHash string
	selectReceipt    *resourceGroupSelectReceipt
	firstUse         bool
	state            resourcegroup.AdmissionState
}

type resourceGroupCapturedOrigin struct {
	peerKey string
	origin  *resourceManagementOrigin // nil is the closed unavailable arm
}

type resourceGroupActivity struct {
	id     string
	kind   resourcegroup.Activity
	cancel context.CancelFunc
	done   chan struct{}
	stop   resourcegroup.StopReason // Core.op; Close signals context only
}

func resourceGroupError(code string) error {
	switch code {
	case "invalid":
		return &localCommandError{"resource_group_invalid", "invalid fixed group request; supply the exact selection, review and execution subset"}
	case "busy":
		return &localCommandError{"resource_group_busy", "a fixed group operation is still active; inspect its local status or cancel admission"}
	case "review_changed":
		return &localCommandError{"resource_group_review_changed", "the local review context changed; inspect retained status before preparing a new review"}
	case "reconcile_required":
		return &localCommandError{"resource_group_reconcile_required", "a prior operation remains uncertain for a selected peer and resource; explicitly refresh its original status before a new review"}
	case "capacity":
		return &localCommandError{"resource_group_capacity", "bounded group evidence capacity is exhausted; unresolved records cannot be discarded to admit another operation"}
	case "storage_unavailable":
		return &localCommandError{"resource_group_storage_unavailable", "owned group evidence is unavailable or uncertain; restart its owning agent without deleting saved evidence"}
	case "review_unavailable":
		return &localCommandError{"resource_group_review_unavailable", "the exact current unused group review is unavailable; inspect retained status before preparing a new review"}
	default:
		return &localCommandError{"resource_group_unavailable", "the fixed group operation could not be completed; inspect its retained local status"}
	}
}

func resourceGroupModelError(err error) error {
	if errors.Is(err, errResourceGroupCapacity) {
		return resourceGroupError("capacity")
	}
	return resourceGroupError("review_changed")
}

func (c *Core) initializeResourceGroups() {
	c.resourceGroups = &resourceGroupCoordinator{store: c.openResourceGroupStore()}
}

func resourceGroupObservedTime() (int64, error) {
	now := time.Now().Unix()
	if now <= 0 || now > resourcegroup.MaxObservationTime {
		return 0, resourceGroupError("unavailable")
	}
	return now, nil
}

func resourceGroupPreparedView(p *resourceGroupPrepared) (resourcegroup.PreparedView, error) {
	if p == nil {
		return resourcegroup.PreparedView{}, resourceGroupError("review_unavailable")
	}
	firstUse := p.firstUse
	view := resourcegroup.PreparedView{SchemaVersion: resourcegroup.SchemaVersion, ReviewID: p.id, Review: p.review, AdmissionState: p.state, InitializesLocalEvidence: &firstUse}
	data, err := json.Marshal(view)
	if err != nil {
		return resourcegroup.PreparedView{}, resourceGroupError("unavailable")
	}
	copy, err := resourcegroup.DecodePreparedView(data)
	if err != nil {
		return resourcegroup.PreparedView{}, resourceGroupError("unavailable")
	}
	return copy, nil
}

func resourceGroupRunView(run resourceGroupRecord, activity resourcegroup.Activity) (resourcegroup.RunView, error) {
	summary, err := resourcegroup.ReduceReview(run.Review, run.Evidence.Members)
	if err != nil {
		return resourcegroup.RunView{}, resourceGroupError("storage_unavailable")
	}
	durability := resourcegroup.LocalDurable
	for _, row := range run.Evidence.Members {
		if row.LocalDurability == resourcegroup.LocalUncertain {
			durability = resourcegroup.LocalUncertain
		} else if row.LocalDurability != resourcegroup.LocalDurable && durability == resourcegroup.LocalDurable {
			durability = resourcegroup.LocalNotSaved
		}
	}
	view := resourcegroup.RunView{SchemaVersion: resourcegroup.SchemaVersion, RunID: run.RunID, AcceptedAt: run.AcceptedAt, Review: run.Review, Evidence: run.Evidence, Summary: summary, LocalDurability: durability, Activity: activity}
	data, err := json.Marshal(view)
	if err != nil {
		return resourcegroup.RunView{}, resourceGroupError("unavailable")
	}
	copy, err := resourcegroup.DecodeRunView(data)
	if err != nil {
		return resourcegroup.RunView{}, resourceGroupError("storage_unavailable")
	}
	return copy, nil
}

func (g *resourceGroupCoordinator) retained(id string) (resourceGroupRecord, bool) {
	if g.overlay != nil && g.overlay.RunID == id {
		return *g.overlay, true
	}
	for _, run := range g.store.state.Runs {
		if run.RunID == id {
			return run, true
		}
	}
	return resourceGroupRecord{}, false
}

func (g *resourceGroupCoordinator) activity(id string) resourcegroup.Activity {
	if g.active != nil && g.active.id == id {
		return g.active.kind
	}
	return resourcegroup.ActivityIdle
}

func resourceGroupPersistedOrigins(origins []resourceGroupCapturedOrigin) []resourceGroupOrigin {
	result := make([]resourceGroupOrigin, len(origins))
	for i, original := range origins {
		result[i] = resourceGroupOrigin{PeerKey: original.peerKey, State: resourceGroupOriginUnavailable}
		if original.origin != nil {
			relationship := original.origin.relationship
			result[i].State, result[i].Relationship = resourceGroupOriginCaptured, &relationship
		}
	}
	return result
}

func resourceGroupSameSelection(input resourcegroup.ApplyInput, p *resourceGroupPrepared) bool {
	return input.Validate() == nil && p != nil && p.state == resourcegroup.AdmissionPrepared && input.ReviewID == p.id && input.ReviewRevision == p.review.Revision && resourceGroupJSONEqual(input.ExecutionPeers, p.review.ExecutionPeers)
}

func resourceGroupStatusCandidate(row resourcegroup.MemberEvidence) bool {
	return row.Execution == resourcegroup.ExecutionSelected && row.Request != nil && row.Dispatch != resourcegroup.DispatchNotAttempted && row.Dispatch != resourcegroup.Dispatching
}

func newResourceGroupID(g *resourceGroupCoordinator) (string, error) {
	id, err := newResourceID()
	if err != nil || g == nil || g.store == nil || g.prepared != nil && g.prepared.id == id || g.active != nil && g.active.id == id {
		return "", resourceGroupError("unavailable")
	}
	if _, found := g.retained(id); found {
		return "", resourceGroupError("unavailable")
	}
	return id, nil
}

func resourceGroupPreviewInputHash(input resourcegroup.PreviewInput) (string, error) {
	data, err := json.Marshal(input)
	if err != nil {
		return "", resourceGroupError("invalid")
	}
	canonical, err := resourcegroup.DecodePreviewInput(data)
	if err != nil {
		return "", resourceGroupError("invalid")
	}
	return resourceDigest(canonical), nil
}

func resourceGroupPreviewRepeatMatches(p *resourceGroupPrepared, inputHash string) bool {
	return p != nil && p.state != resourcegroup.AdmissionCanceled && p.previewInputHash != "" && inputHash == p.previewInputHash
}
