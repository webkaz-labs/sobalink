//go:build resource_group_catalog_native

package resourceacceptance

import (
	"reflect"
	"sync"
)

var recorder struct {
	sync.Mutex
	active  bool
	aliases [6]any
	view    View
}

// Start accepts exactly three roles with two distinct, nonnil pointer aliases
// each. It cannot inspect or alter any owner's authority or runtime state.
func Start(owners [3]Owner) bool {
	var aliases [6]any
	for i, owner := range owners {
		aliases[2*i], aliases[2*i+1] = owner.Core, owner.Node
	}
	for i, alias := range aliases {
		value := reflect.ValueOf(alias)
		if !value.IsValid() || value.Kind() != reflect.Ptr || value.IsNil() {
			return false
		}
		// All already-checked aliases are pointers, hence comparable.
		for j := 0; j < i; j++ {
			if alias == aliases[j] {
				return false
			}
		}
	}
	recorder.Lock()
	defer recorder.Unlock()
	if recorder.active {
		return false
	}
	recorder.aliases, recorder.view, recorder.active = aliases, View{}, true
	return true
}

func Snapshot() View {
	recorder.Lock()
	defer recorder.Unlock()
	return recorder.view
}

// Stop releases every owner alias and correlation retained by this fixture.
func Stop() {
	recorder.Lock()
	defer recorder.Unlock()
	recorder.aliases, recorder.view, recorder.active = [6]any{}, View{}, false
}

func hexID(value string, size int) bool {
	if len(value) != size {
		return false
	}
	for i := 0; i < size; i++ {
		if value[i] < '0' || value[i] > '9' && value[i] < 'a' || value[i] > 'f' {
			return false
		}
	}
	return true
}

func eventAction(kind Kind, action, runID, operationID string) (Action, bool) {
	var typed Action
	switch action {
	case "":
		typed = None
	case "inspect":
		typed = Inspect
	case "preview":
		typed = Preview
	case "apply":
		typed = Apply
	case "operation.status":
		typed = Status
	default:
		return None, false
	}
	switch kind {
	case GroupAccepted:
		return typed, typed == None && hexID(runID, 32) && operationID == ""
	case GroupIntent:
		return typed, typed == Apply && hexID(runID, 32) && hexID(operationID, 64)
	case ManagementClientInvoked, ManagementFrameAttempted, ManagementFrameWritten:
		if runID != "" || typed == None {
			return typed, false
		}
		if typed == Apply || typed == Status {
			return typed, hexID(operationID, 64)
		}
		return typed, operationID == ""
	case ProviderAdmitted:
		return typed, typed == Apply && runID == "" && hexID(operationID, 64)
	default:
		return None, false
	}
}

// Record is a leaf observation with no result, callbacks or other locks.
// Callers must be outside every transport lock and endpoint borrow. Overflow
// fails only the fixture oracle; it cannot alter product admission or results.
func Record(owner any, kind Kind, action, runID, operationID string) {
	recorder.Lock()
	defer recorder.Unlock()
	if !recorder.active {
		return
	}
	role := Role(0)
	for i, alias := range recorder.aliases {
		// Interface equality is safe even for an unexpected noncomparable owner:
		// each registered dynamic type is a pointer and different dynamic types
		// compare false without comparing their underlying values.
		if owner == alias {
			role = Role(i/2 + 1)
			break
		}
	}
	if role == 0 {
		return
	}
	typed, valid := eventAction(kind, action, runID, operationID)
	if !valid || recorder.view.Count == MaxEvents {
		recorder.view.InvalidOrOverflow = true
		return
	}
	index := recorder.view.Count
	recorder.view.Events[index] = Event{Sequence: index + 1, Role: role, Kind: kind, Action: typed, RunID: runID, OperationID: operationID}
	recorder.view.Count++
}
