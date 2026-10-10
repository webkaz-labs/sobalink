package resourceacceptance

// ProcessNodeConstructor identifies an observed constructor call, never a
// requested constructor or an authority to allocate a transport owner.
type ProcessNodeConstructor uint8

const (
	ProcessControlNode ProcessNodeConstructor = iota + 1
	ProcessLegacyNode
	ProcessManagedNode
	ProcessManagedStartupNode
)

// ProcessCommandKind is a closed observation value, not a product command API.
// Transport dispatch and local validated entry do not establish command success.
type ProcessCommandKind uint8

const (
	ProcessCommandUnknown ProcessCommandKind = iota
	ProcessControlLimits
	ProcessLocalStatus
	ProcessLocalStop
	ProcessUpgradeIdentity
	ProcessUpgradeReview
	ProcessUpgradeRun
	ProcessUpgradeStatus
	ProcessResourceList
	ProcessResourceInspect
	ProcessResourcePreview
	ProcessResourceApply
	ProcessResourceOperationStatus
	ProcessManagementGrantPreview
	ProcessManagementGrantConfirm
	// The product route is resource.grant.inspect. The observed value does not
	// prove that the inspected payload contains a management-scoped grant.
	ProcessManagementGrantInspect
	ProcessGroupPreview
	ProcessGroupCurrent
	ProcessGroupApply
	ProcessGroupStatus
)
