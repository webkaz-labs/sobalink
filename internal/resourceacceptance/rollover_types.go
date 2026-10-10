package resourceacceptance

// RolloverStage describes one passive, in-process controller replacement.
// Only the tagged recorder advances it; it is never product authority.
type RolloverStage uint8

const (
	RolloverIdle RolloverStage = iota
	RolloverCorePending
	RolloverNodePending
	RolloverObserving
)

const MaxMaintenancePasses = 256

// RolloverView has no owner pointers, paths, callbacks or network state.
// A pass certifies a completed iteration, not successful network recovery.
type RolloverView struct {
	Stage             RolloverStage
	MaintenancePasses uint16
	Invalid           bool
}
