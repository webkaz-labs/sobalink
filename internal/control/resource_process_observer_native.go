//go:build resource_process_native

package control

import (
	"encoding/json"

	"github.com/webkaz-labs/sobalink/internal/resourceacceptance"
)

const resourceProcessCommandLimit = 8192

func observeResourceProcessDialAttempt(directory string) {
	resourceacceptance.ProcessIPCConnectAttempt(directory)
}
func observeResourceProcessDialCompleted(directory string) {
	resourceacceptance.ProcessIPCConnectCompleted(directory)
}
func observeResourceProcessDispatch(directory, raw string) {
	if !resourceacceptance.ProcessIPCDirectoryMatches(directory) {
		return
	}
	resourceacceptance.ProcessIPCRequestDispatched(directory, resourceProcessCommandKind(raw))
}

// Bounded control-side classification is outside the leaf recorder contract.
// It observes a transport-dispatched string, not successful Core validation.
// Unmarshal's duplicate/case/null name behavior matches the product decoder;
// ignored request ID/payload fields are never retained or emitted here.
func resourceProcessCommandKind(raw string) resourceacceptance.ProcessCommandKind {
	if len(raw) == 0 || len(raw) > resourceProcessCommandLimit {
		return resourceacceptance.ProcessCommandUnknown
	}
	switch raw {
	case "control.limits":
		return resourceacceptance.ProcessControlLimits
	case "status":
		return resourceacceptance.ProcessLocalStatus
	case "stop":
		return resourceacceptance.ProcessLocalStop
	case "lifecycle.upgrade.identity":
		return resourceacceptance.ProcessUpgradeIdentity
	}
	var command struct {
		Name string `json:"name"`
	}
	if json.Unmarshal([]byte(raw), &command) != nil {
		return resourceacceptance.ProcessCommandUnknown
	}
	switch command.Name {
	case "direct-lan.upgrade.review":
		return resourceacceptance.ProcessUpgradeReview
	case "direct-lan.upgrade.run":
		return resourceacceptance.ProcessUpgradeRun
	case "direct-lan.upgrade.status":
		return resourceacceptance.ProcessUpgradeStatus
	case "resource.list":
		return resourceacceptance.ProcessResourceList
	case "resource.inspect":
		return resourceacceptance.ProcessResourceInspect
	case "resource.preview":
		return resourceacceptance.ProcessResourcePreview
	case "resource.apply":
		return resourceacceptance.ProcessResourceApply
	case "resource.operation.status":
		return resourceacceptance.ProcessResourceOperationStatus
	case "resource.grant.management.preview":
		return resourceacceptance.ProcessManagementGrantPreview
	case "resource.grant.management.confirm":
		return resourceacceptance.ProcessManagementGrantConfirm
	case "resource.grant.inspect":
		return resourceacceptance.ProcessManagementGrantInspect
	case "resource.group.preview":
		return resourceacceptance.ProcessGroupPreview
	case "resource.group.review.current":
		return resourceacceptance.ProcessGroupCurrent
	case "resource.group.apply":
		return resourceacceptance.ProcessGroupApply
	case "resource.group.status":
		return resourceacceptance.ProcessGroupStatus
	default:
		return resourceacceptance.ProcessCommandUnknown
	}
}
