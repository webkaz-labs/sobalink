//go:build resource_group_catalog_native

package resourceacceptance

import "reflect"

type controllerRollover struct {
	view      RolloverView
	retired   [2]any
	lifecycle any
}

// recorder.Lock held. The recorder never dereferences an owner or lock.
func rolloverAliasAvailable(alias any) bool {
	value := reflect.ValueOf(alias)
	if !value.IsValid() || value.Kind() != reflect.Ptr || value.IsNil() {
		return false
	}
	for _, current := range recorder.aliases {
		if alias == current {
			return false
		}
	}
	for _, retired := range recorder.rollover.retired {
		if alias == retired {
			return false
		}
	}
	return alias != recorder.rollover.lifecycle
}

func invalidateRollover() {
	recorder.rollover.view.Invalid = true
	recorder.view.InvalidOrOverflow = true
}

// ArmControllerReopen seals only the exact joined controller's aliases and
// expects one actual new lifecycle lock. A/B and all events stay uninterrupted.
// This test-only registration cannot acquire the lock or change any owner.
func ArmControllerReopen(old Owner, nextLifecycle any) bool {
	recorder.Lock()
	defer recorder.Unlock()
	if !recorder.active {
		return false
	}
	if recorder.rollover.view.Stage != RolloverIdle || old.Core != recorder.aliases[0] || old.Node != recorder.aliases[1] || !rolloverAliasAvailable(nextLifecycle) {
		invalidateRollover()
		return false
	}
	recorder.rollover = controllerRollover{view: RolloverView{Stage: RolloverCorePending}, retired: [2]any{old.Core, old.Node}, lifecycle: nextLifecycle}
	recorder.aliases[0], recorder.aliases[1] = nil, nil
	return true
}

// CoreConstructed runs immediately after allocation, before Open can start
// maintenance or network work. Unmatched construction remains unobserved.
func CoreConstructed(core, lifecycle any) {
	recorder.Lock()
	defer recorder.Unlock()
	if !recorder.active || recorder.rollover.view.Stage == RolloverIdle || lifecycle != recorder.rollover.lifecycle {
		return
	}
	if recorder.rollover.view.Stage != RolloverCorePending || !rolloverAliasAvailable(core) {
		invalidateRollover()
		return
	}
	recorder.aliases[0] = core
	recorder.rollover.view.Stage = RolloverNodePending
}

// NodeConstructed runs after the real constructor returns, before publication
// or Start. It is a leaf observation, outside transport locks and borrows.
func NodeConstructed(core, node any) {
	recorder.Lock()
	defer recorder.Unlock()
	if !recorder.active || recorder.rollover.view.Stage == RolloverIdle {
		return
	}
	if core == recorder.rollover.retired[0] {
		invalidateRollover()
		return
	}
	if recorder.aliases[0] == nil || core != recorder.aliases[0] {
		return
	}
	if recorder.rollover.view.Stage != RolloverNodePending || !rolloverAliasAvailable(node) {
		invalidateRollover()
		return
	}
	recorder.aliases[1] = node
	recorder.rollover.view.Stage = RolloverObserving
}

// MaintenancePassed observes the unlocked tail of a whole ordinary iteration.
// Open may complete such a pass before Node construction; it does not count.
func MaintenancePassed(core any) {
	recorder.Lock()
	defer recorder.Unlock()
	if !recorder.active || recorder.rollover.view.Stage == RolloverIdle {
		return
	}
	if core == recorder.rollover.retired[0] {
		invalidateRollover()
		return
	}
	if recorder.rollover.view.Stage != RolloverObserving || core != recorder.aliases[0] {
		return
	}
	if recorder.rollover.view.MaintenancePasses == MaxMaintenancePasses {
		invalidateRollover()
		return
	}
	recorder.rollover.view.MaintenancePasses++
}

func RolloverSnapshot() RolloverView {
	recorder.Lock()
	defer recorder.Unlock()
	return recorder.rollover.view
}
