package directlan

import (
	"strings"
	"testing"
)

func managementEpochIdentity() managedAuthentication {
	return managedAuthentication{peer: &peerState{}, generation: &runtimeGeneration{}, policy: &bindPolicy{}, registration: 1, binding: strings.Repeat("a", 64)}
}

func TestManagementEpochStableAcrossCaptures(t *testing.T) {
	identity := managementEpochIdentity()
	slot, ok := selectManagementEpoch(managementEpochSlot{}, identity, strings.Repeat("b", 64))
	if !ok {
		t.Fatal("initial epoch rejected")
	}
	for _, candidate := range []string{strings.Repeat("c", 64), ""} {
		again, ok := selectManagementEpoch(slot, identity, candidate)
		if !ok || again != slot {
			t.Fatal("same tuple changed epoch")
		}
	}
	// The value snapshot does not change when the caller later changes its copy.
	identity.registration++
	if slot.identity.registration != 1 {
		t.Fatal("identity not snapshotted")
	}
}

func TestManagementEpochReplacesEveryIdentityComponent(t *testing.T) {
	identity := managementEpochIdentity()
	slot, ok := selectManagementEpoch(managementEpochSlot{}, identity, strings.Repeat("b", 64))
	if !ok {
		t.Fatal("initial epoch")
	}
	for _, mutate := range []func(*managedAuthentication){
		func(a *managedAuthentication) { a.peer = &peerState{} },
		func(a *managedAuthentication) { a.generation = &runtimeGeneration{} },
		func(a *managedAuthentication) { a.policy = &bindPolicy{} },
		func(a *managedAuthentication) { a.registration++ },
		func(a *managedAuthentication) { a.binding = strings.Repeat("d", 64) },
	} {
		changed := identity
		mutate(&changed)
		next, ok := selectManagementEpoch(slot, changed, strings.Repeat("c", 64))
		if !ok || next.digest == slot.digest || next.identity != changed {
			t.Fatal("changed tuple retained epoch")
		}
		if _, ok := selectManagementEpoch(slot, changed, slot.digest); ok {
			t.Fatal("replacement reused digest")
		}
		if _, ok := selectManagementEpoch(slot, changed, ""); ok {
			t.Fatal("replacement accepted random failure")
		}
	}
}

func TestManagementEpochInvalidInputsFailClosed(t *testing.T) {
	identity := managementEpochIdentity()
	for _, mutate := range []func(*managedAuthentication){
		func(a *managedAuthentication) { a.peer = nil },
		func(a *managedAuthentication) { a.generation = nil },
		func(a *managedAuthentication) { a.policy = nil },
		func(a *managedAuthentication) { a.registration = 0 },
		func(a *managedAuthentication) { a.binding = "" },
	} {
		invalid := identity
		mutate(&invalid)
		if _, ok := selectManagementEpoch(managementEpochSlot{}, invalid, strings.Repeat("b", 64)); ok {
			t.Fatal("invalid tuple accepted")
		}
	}
	for _, candidate := range []string{"", strings.Repeat("b", 63), strings.Repeat("z", 64)} {
		if _, ok := selectManagementEpoch(managementEpochSlot{}, identity, candidate); ok {
			t.Fatal("invalid digest accepted")
		}
	}
}
