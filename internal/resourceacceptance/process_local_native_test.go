//go:build resource_process_native

package resourceacceptance

import (
	"crypto/sha256"
	"strings"
	"sync"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/resourceacceptance/processmodel"
)

const processTestDirectory = "/synthetic/state/a"

func processScopedTestState(t *testing.T) (*processObservation, processTestAliases) {
	t.Helper()
	s, ok := newScopedProcessObservation(processmodel.Owner, processTestDirectory)
	if !ok {
		t.Fatal("scoped owner value rejected")
	}
	a := processTestAliases{new(int), new(int), new(int), new(int)}
	s.ownerLock(a.lock)
	s.coreConstructed(a.core, a.lock)
	return s, a
}

func TestProcessLocalCommandMappingAndDigest(t *testing.T) {
	names := []string{"direct-lan.upgrade.review", "direct-lan.upgrade.run", "direct-lan.upgrade.status",
		"resource.list", "resource.inspect", "resource.preview", "resource.apply", "resource.operation.status",
		"resource.grant.management.preview", "resource.grant.management.confirm", "resource.grant.inspect",
		"resource.group.preview", "resource.group.review.current", "resource.group.apply", "resource.group.status"}
	for i, name := range names {
		s, a := processScopedTestState(t)
		requestID := strings.Repeat("r", 128)
		s.localCommand(a.core, name, requestID)
		p := processTestPrefix(s)
		want := processmodel.Observation{Kind: processmodel.LocalCommand, Command: processmodel.Command(int(processmodel.UpgradeReview) + i), RequestDigest: sha256.Sum256([]byte(requestID))}
		if !p.Complete || p.Count != 3 || p.LocalCount != 1 || p.Events[2].Observation != want {
			t.Fatal("actual command name or request digest changed")
		}
		if s.directory != processTestDirectory {
			t.Fatal("recording changed immutable scope")
		}
	}
	for value := ProcessControlLimits; value <= ProcessGroupStatus; value++ {
		if command, ok := processModelCommand(value); !ok || command != processmodel.Command(value) {
			t.Fatal("closed command values diverged")
		}
	}
	for _, value := range []ProcessCommandKind{0, 20, 255} {
		if _, ok := processModelCommand(value); ok {
			t.Fatal("foreign command enum accepted")
		}
	}
}

func TestProcessLocalLiteralAndBadEntry(t *testing.T) {
	for _, literal := range []string{"status", "stop"} {
		s, a := processScopedTestState(t)
		s.localLiteral(a.core, literal)
		p := processTestPrefix(s)
		want := processmodel.LocalStatus
		if literal == "stop" {
			want = processmodel.LocalStop
		}
		if !p.Complete || p.LocalCount != 1 || p.Events[2].Observation != (processmodel.Observation{Kind: processmodel.LocalCommand, Command: want}) {
			t.Fatal("literal dispatch became another command or acquired a request ID")
		}
	}
	for _, check := range []func(*processObservation, processTestAliases){
		func(s *processObservation, a processTestAliases) { s.localLiteral(a.core, "ui") },
		func(s *processObservation, a processTestAliases) { s.localLiteral(a.core, "resource.list") },
		func(s *processObservation, a processTestAliases) { s.localLiteral(new(int), "status") },
		func(s *processObservation, a processTestAliases) { s.localCommand(a.core, "status", "r") },
		func(s *processObservation, a processTestAliases) { s.localCommand(a.core, "control.limits", "r") },
		func(s *processObservation, a processTestAliases) {
			s.localCommand(a.core, "lifecycle.upgrade.identity", "r")
		},
		func(s *processObservation, a processTestAliases) {
			s.localCommand(a.core, "resource.grant.management.inspect", "r")
		},
		func(s *processObservation, a processTestAliases) {
			s.localCommand(a.core, "resource.group.status.refresh", "r")
		},
		func(s *processObservation, a processTestAliases) { s.localCommand(a.core, "resource.list", "") },
		func(s *processObservation, a processTestAliases) {
			s.localCommand(a.core, "resource.list", strings.Repeat("r", 129))
		},
		func(s *processObservation, a processTestAliases) { s.localCommand(a.lock, "resource.list", "r") },
	} {
		s, a := processScopedTestState(t)
		check(s, a)
		requireProcessFailed(t, s)
	}
}

func TestProcessIPCScopeBoundAndNoInstall(t *testing.T) {
	if s, ok := newScopedProcessObservation(processmodel.Owner, strings.Repeat("x", processDirectoryLimit+1)); ok || s != nil {
		t.Fatal("oversized directory retained")
	}
	for _, mode := range []processmodel.Mode{processmodel.Owner, processmodel.CLI} {
		dir := strings.Repeat("x", processDirectoryLimit)
		s, ok := newScopedProcessObservation(mode, dir)
		if !ok || !s.ipcDirectoryMatches(dir) || s.directory != dir {
			t.Fatal("exact directory value bound rejected")
		}
		if p := processTestPrefix(s); !p.Complete || p.Count != 0 {
			t.Fatal("scope comparison manufactured an event or ownership")
		}
		if s.ipcDirectoryMatches(dir + "/") {
			t.Fatal("alternate directory matched")
		}
		requireProcessFailed(t, s)
	}
	empty, _ := newProcessObservation(processmodel.Owner)
	if empty.ipcDirectoryMatches("") {
		t.Fatal("unbound empty directory matched")
	}
	requireProcessFailed(t, empty)
	if processObserver.Load() != nil {
		t.Fatal("unexpected live installation")
	}
	if ProcessIPCDirectoryMatches(processTestDirectory) {
		t.Fatal("scope query installed an observer")
	}
	ProcessLocalCommand(nil, "resource.list", "r")
	ProcessLocalLiteral(nil, "status")
	ProcessIPCConnectAttempt(processTestDirectory)
	ProcessIPCConnectCompleted(processTestDirectory)
	ProcessIPCRequestDispatched(processTestDirectory, ProcessControlLimits)
	if processObserver.Load() != nil {
		t.Fatal("local hook installed itself")
	}
}

func TestProcessIPCDialAndDispatchShapes(t *testing.T) {
	cli, ok := newScopedProcessObservation(processmodel.CLI, processTestDirectory)
	if !ok {
		t.Fatal("CLI scope rejected")
	}
	// The real CLI does limits lookup plus its command, with two actual dials.
	for i := 0; i < 2; i++ {
		cli.ipcConnect(processTestDirectory, false)
		cli.ipcConnect(processTestDirectory, true)
	}
	p := processTestPrefix(cli)
	if !p.Complete || p.Count != 4 || p.LocalCount != 4 {
		t.Fatal("CLI limits/action dials not counted")
	}
	for i := uint16(0); i < p.Count; i++ {
		kind := processmodel.IPCConnectAttempt
		if i%2 != 0 {
			kind = processmodel.IPCConnectCompleted
		}
		if p.Events[i].Observation != (processmodel.Observation{Kind: kind}) {
			t.Fatal("dial retained foreign data")
		}
	}
	s, a := processScopedTestState(t)
	s.ipcDispatched(processTestDirectory, ProcessControlLimits)
	s.ipcDispatched(processTestDirectory, ProcessResourceList)
	s.localCommand(a.core, "resource.list", "fixed-request")
	p = processTestPrefix(s)
	if !p.Complete || p.Count != 5 || p.LocalCount != 3 ||
		p.Events[2].Observation != (processmodel.Observation{Kind: processmodel.IPCRequestDispatched, Command: processmodel.ControlLimits}) ||
		p.Events[3].Observation != (processmodel.Observation{Kind: processmodel.IPCRequestDispatched, Command: processmodel.ResourceList}) ||
		p.Events[4].Observation.Kind != processmodel.LocalCommand {
		t.Fatal("transport dispatch and Core entry were conflated")
	}
}

func TestProcessIPCRejectsWrongScopeAndCommands(t *testing.T) {
	for _, check := range []func(*processObservation){
		func(s *processObservation) { s.ipcConnect("/synthetic/state/b", false) },
		func(s *processObservation) { s.ipcConnect("/synthetic/state/b", true) },
		func(s *processObservation) { s.ipcDispatched("/synthetic/state/b", ProcessResourceList) },
		func(s *processObservation) { s.ipcDispatched(processTestDirectory, ProcessCommandUnknown) },
		func(s *processObservation) { s.ipcDispatched(processTestDirectory, 255) },
		func(s *processObservation) { s.ipcDirectoryMatches("/synthetic/state/x/../a") },
	} {
		s, _ := processScopedTestState(t)
		check(s)
		requireProcessFailed(t, s)
	}
	for _, mode := range []processmodel.Mode{processmodel.Owner, processmodel.CLI} {
		s, _ := newScopedProcessObservation(mode, processTestDirectory)
		// Owner mode alone is not actual Core binding; a CLI is not a server.
		s.ipcDispatched(processTestDirectory, ProcessControlLimits)
		requireProcessFailed(t, s)
	}
}

func TestProcessLocalLateAndConcurrentCalls(t *testing.T) {
	s, a := processScopedTestState(t)
	var joined sync.WaitGroup
	for i := 0; i < 16; i++ {
		joined.Add(1)
		go func() { defer joined.Done(); s.localCommand(a.core, "resource.list", "fixed") }()
	}
	joined.Wait()
	if p := processTestPrefix(s); !p.Complete || p.LocalCount != 16 || p.Active != 0 || s.gate.Load() != 0 {
		t.Fatal("concurrent local observations did not join")
	}
	s.closeAdmission()
	if s.ipcDirectoryMatches(processTestDirectory) {
		t.Fatal("closed scope matched")
	}
	s.localLiteral(a.core, "status")
	s.ipcConnect(processTestDirectory, false)
	s.ipcDispatched(processTestDirectory, ProcessControlLimits)
	if p := processTestPrefix(s); p.Complete || p.Failures&processmodel.Late == 0 || s.gate.Load() != processAdmissionClosed {
		t.Fatal("late local observation escaped closure")
	}
}
