//go:build resource_group_catalog_native

package resourceacceptance

import (
	"strings"
	"testing"
)

func rolloverTestOwners(t *testing.T) [3]Owner {
	t.Helper()
	Stop()
	t.Cleanup(Stop)
	owners := [3]Owner{}
	for i := range owners {
		owners[i] = Owner{Core: new(uint64), Node: new(uint64)}
	}
	if !Start(owners) {
		t.Fatal("synthetic observation aliases rejected")
	}
	return owners
}

func TestControllerRolloverPreservesContinuousObservation(t *testing.T) {
	owners := rolloverTestOwners(t)
	operation := strings.Repeat("1", 64)
	Record(owners[0].Core, ManagementClientInvoked, "apply", "", operation)
	before := Snapshot()
	lock, core, node := new(uint64), new(uint64), new(uint64)
	if !ArmControllerReopen(owners[0], lock) || Snapshot() != before || RolloverSnapshot().Stage != RolloverCorePending {
		t.Fatal("rollover lost original events or did not arm exactly once")
	}
	Record(owners[1].Core, ProviderAdmitted, "apply", "", operation)
	CoreConstructed(new(uint64), new(uint64)) // Unrelated lifecycle stays inert.
	NodeConstructed(new(uint64), new(uint64))
	MaintenancePassed(owners[2].Core)
	if RolloverSnapshot() != (RolloverView{Stage: RolloverCorePending}) {
		t.Fatal("unmatched construction/pass changed lifecycle evidence")
	}
	CoreConstructed(core, lock)
	MaintenancePassed(core) // A legitimate early Open iteration is not failure.
	if RolloverSnapshot() != (RolloverView{Stage: RolloverNodePending}) {
		t.Fatal("pre-Node maintenance counted or poisoned evidence")
	}
	Record(core, ManagementClientInvoked, "apply", "", operation)
	Record(owners[2].Core, ProviderAdmitted, "apply", "", operation)
	NodeConstructed(core, node)
	Record(node, ManagementFrameAttempted, "apply", "", operation)
	Record(node, ManagementFrameWritten, "apply", "", operation)
	MaintenancePassed(core)
	view := Snapshot()
	if view.InvalidOrOverflow || view.Count != 6 || RolloverSnapshot() != (RolloverView{Stage: RolloverObserving, MaintenancePasses: 1}) {
		t.Fatal("continuous owner observation failed")
	}
	roles := [6]Role{Controller, TargetA, Controller, TargetB, Controller, Controller}
	for i, event := range view.Events[:view.Count] {
		if event.Sequence != uint16(i+1) || event.Role != roles[i] || event.OperationID != operation {
			t.Fatal("replacement reset sequence or relabeled target events")
		}
	}
}

func TestControllerRolloverRejectsInvalidRegistration(t *testing.T) {
	for _, kind := range []string{"nil-lock", "typed-nil-lock", "nonpointer-lock", "aliased-lock", "wrong-core", "wrong-node", "duplicate-arm"} {
		t.Run(kind, func(t *testing.T) {
			owners := rolloverTestOwners(t)
			old, lock := owners[0], any(new(uint64))
			switch kind {
			case "nil-lock":
				lock = nil
			case "typed-nil-lock":
				lock = (*uint64)(nil)
			case "nonpointer-lock":
				lock = []byte{1}
			case "aliased-lock":
				lock = owners[1].Core
			case "wrong-core":
				old.Core = owners[1].Core
			case "wrong-node":
				old.Node = owners[1].Node
			case "duplicate-arm":
				if !ArmControllerReopen(old, lock) {
					t.Fatal("first exact arm failed")
				}
			}
			if ArmControllerReopen(old, lock) || !RolloverSnapshot().Invalid || !Snapshot().InvalidOrOverflow || Snapshot().Count != 0 {
				t.Fatal("invalid or repeated registration did not fail evidence")
			}
		})
	}
}

func TestControllerRolloverRejectsAliasedOrDuplicateBinding(t *testing.T) {
	for _, kind := range []string{"core-nil", "core-alias", "core-lock", "core-duplicate", "node-nil", "node-alias", "node-lock", "node-duplicate"} {
		t.Run(kind, func(t *testing.T) {
			owners := rolloverTestOwners(t)
			lock, core, node := new(uint64), new(uint64), new(uint64)
			if !ArmControllerReopen(owners[0], lock) {
				t.Fatal("exact arm failed")
			}
			switch kind {
			case "core-nil":
				CoreConstructed((*uint64)(nil), lock)
			case "core-alias":
				CoreConstructed(owners[1].Core, lock)
			case "core-lock":
				CoreConstructed(lock, lock)
			default:
				CoreConstructed(core, lock)
				switch kind {
				case "core-duplicate":
					CoreConstructed(core, lock)
				case "node-nil":
					NodeConstructed(core, (*uint64)(nil))
				case "node-alias":
					NodeConstructed(core, owners[2].Node)
				case "node-lock":
					NodeConstructed(core, lock)
				case "node-duplicate":
					NodeConstructed(core, node)
					NodeConstructed(core, node)
				}
			}
			if !RolloverSnapshot().Invalid || !Snapshot().InvalidOrOverflow || Snapshot().Count != 0 {
				t.Fatal("aliased or duplicate binding did not fail evidence")
			}
		})
	}
}

func TestControllerRolloverRejectsSealedOwnerEvents(t *testing.T) {
	for _, kind := range []string{"core-record", "node-record", "node-bind", "maintenance"} {
		t.Run(kind, func(t *testing.T) {
			owners := rolloverTestOwners(t)
			if !ArmControllerReopen(owners[0], new(uint64)) {
				t.Fatal("exact arm failed")
			}
			switch kind {
			case "core-record":
				Record(owners[0].Core, ManagementClientInvoked, "inspect", "", "")
			case "node-record":
				Record(owners[0].Node, ManagementFrameAttempted, "inspect", "", "")
			case "node-bind":
				NodeConstructed(owners[0].Core, new(uint64))
			case "maintenance":
				MaintenancePassed(owners[0].Core)
			}
			if !RolloverSnapshot().Invalid || !Snapshot().InvalidOrOverflow || Snapshot().Count != 0 {
				t.Fatal("sealed owner action disappeared without invalidating evidence")
			}
		})
	}
}

func TestControllerRolloverBoundsAndStop(t *testing.T) {
	owners := rolloverTestOwners(t)
	lock, core, node := new(uint64), new(uint64), new(uint64)
	if !ArmControllerReopen(owners[0], lock) {
		t.Fatal("exact arm failed")
	}
	CoreConstructed(core, lock)
	NodeConstructed(core, node)
	for i := 0; i < MaxMaintenancePasses; i++ {
		MaintenancePassed(core)
	}
	if RolloverSnapshot() != (RolloverView{Stage: RolloverObserving, MaintenancePasses: MaxMaintenancePasses}) {
		t.Fatal("bounded maintenance count changed")
	}
	MaintenancePassed(core)
	if !RolloverSnapshot().Invalid || !Snapshot().InvalidOrOverflow || RolloverSnapshot().MaintenancePasses != MaxMaintenancePasses {
		t.Fatal("maintenance overflow was not bounded and terminal evidence")
	}
	Stop()
	CoreConstructed(core, lock)
	NodeConstructed(core, node)
	MaintenancePassed(core)
	Record(owners[1].Core, ProviderAdmitted, "apply", "", strings.Repeat("2", 64))
	if RolloverSnapshot() != (RolloverView{}) || Snapshot() != (View{}) || ArmControllerReopen(owners[0], lock) {
		t.Fatal("Stop retained aliases, counters or registration")
	}
	if !Start(owners) || RolloverSnapshot() != (RolloverView{}) {
		t.Fatal("fresh recorder inherited rollover state")
	}
}

func TestControllerRolloverEventBoundsAndClosedKinds(t *testing.T) {
	for _, kind := range []string{"overflow", "unknown-kind", "unknown-action"} {
		t.Run(kind, func(t *testing.T) {
			owners := rolloverTestOwners(t)
			switch kind {
			case "overflow":
				for i := 0; i <= MaxEvents; i++ {
					Record(owners[1].Core, ProviderAdmitted, "apply", "", strings.Repeat("3", 64))
				}
				if Snapshot().Count != MaxEvents {
					t.Fatal("event overflow changed the fixed bound")
				}
			case "unknown-kind":
				Record(owners[0].Core, Kind(255), "inspect", "", "")
			case "unknown-action":
				Record(owners[0].Core, ManagementClientInvoked, "retry", "", "")
			}
			if !Snapshot().InvalidOrOverflow {
				t.Fatal("invalid event escaped the closed bounded oracle")
			}
		})
	}
}
