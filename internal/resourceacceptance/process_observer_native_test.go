//go:build resource_process_native

package resourceacceptance

import (
	"strings"
	"sync"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/resourceacceptance/processmodel"
)

type processTestAliases struct{ lock, core, ordinary, control *int }

func processTestState(t *testing.T) (*processObservation, processTestAliases) {
	t.Helper()
	s, ok := newProcessObservation(processmodel.Owner)
	if !ok {
		t.Fatal("owner observation construction failed")
	}
	a := processTestAliases{new(int), new(int), new(int), new(int)}
	s.ownerLock(a.lock)
	s.coreConstructed(a.core, a.lock)
	return s, a
}

func processTestPrefix(s *processObservation) processmodel.Prefix {
	count := s.recorder.PrefixThrough(0).Reserved
	if count > processmodel.MaxOwnerEvents {
		count = processmodel.MaxOwnerEvents
	}
	return s.recorder.PrefixThrough(count)
}

func requireProcessFailed(t *testing.T, s *processObservation) {
	t.Helper()
	if p := processTestPrefix(s); p.Complete || p.Failures == 0 {
		t.Fatal("invalid ownership observation became a complete prefix")
	}
}

func processTestReady(s *processObservation, a processTestAliases, control bool) {
	if control {
		s.constructorAttempt(a.core, ProcessControlNode)
		s.controlConstructed(a.core, a.control)
		s.controlNodeJoined(a.core, a.control, true)
	}
	s.constructorAttempt(a.core, ProcessManagedNode)
	s.nodeConstructed(a.core, a.ordinary)
	s.webOpened(a.core, "http://127.0.0.1:34567")
	s.ipcOpened(a.core)
}

func TestProcessObservationActualBindingAndLifecycle(t *testing.T) {
	for _, control := range []bool{false, true} {
		s, a := processTestState(t)
		// Ordinary maintenance is retained from Core construction onward.
		s.maintenancePassed(a.core)
		processTestReady(s, a, control)
		run, operation := strings.Repeat("1", 32), strings.Repeat("2", 64)
		s.record(a.core, GroupAccepted, "", run, "")
		s.record(a.core, GroupIntent, "apply", run, operation)
		s.record(a.ordinary, ManagementFrameWritten, "apply", "", operation)
		s.ownersJoined(a.core, true, true)
		s.closeAdmission()
		p := processTestPrefix(s)
		want := uint16(11)
		if control {
			want += 3
		}
		if !p.Complete || p.Count != want || p.ResourceCount != 3 || p.LocalCount != 0 ||
			p.Active != 0 || !p.AdmissionClosed || p.LifecycleCount != want-3 ||
			s.gate.Load() != processAdmissionClosed || s.webPort.Load() != 34567 {
			t.Fatal("actual binding and lifecycle prefix mismatch")
		}
		if p.Events[0].Observation.Kind != processmodel.OwnerLockBound ||
			p.Events[1].Observation.Kind != processmodel.CoreBound ||
			p.Events[2].Observation.Kind != processmodel.MaintenancePassed ||
			p.Events[p.Count-1].Observation != (processmodel.Observation{Kind: processmodel.OwnersClosed, Outcome: processmodel.Success}) {
			t.Fatal("lifecycle source order was not retained")
		}
		// A copied final-looking prefix is not a lifetime seal.
		s.record(a.core, GroupAccepted, "", run, "")
		if later := processTestPrefix(s); later.Failures&processmodel.Late == 0 || later.Complete || s.gate.Load() != processAdmissionClosed {
			t.Fatal("late caller cleared/escaped admission failure")
		}
	}
}

func TestProcessObservationRejectsUnboundAndRetiredAliases(t *testing.T) {
	var nilPointer *int
	for _, alias := range []any{nil, nilPointer, 5, []byte{1}, map[string]int{"x": 1}} {
		s, _ := newProcessObservation(processmodel.Owner)
		s.ownerLock(alias)
		requireProcessFailed(t, s)
	}
	for _, check := range []func(*processObservation, processTestAliases){
		func(s *processObservation, a processTestAliases) { s.ownerLock(a.lock) },
		func(s *processObservation, a processTestAliases) { s.coreConstructed(a.core, a.lock) },
		func(s *processObservation, a processTestAliases) { s.coreConstructed(new(int), new(int)) },
		func(s *processObservation, a processTestAliases) { s.constructorAttempt(new(int), ProcessManagedNode) },
		func(s *processObservation, a processTestAliases) { s.nodeConstructed(a.core, a.ordinary) },
		func(s *processObservation, a processTestAliases) { s.controlConstructed(a.core, a.control) },
		func(s *processObservation, a processTestAliases) { s.maintenancePassed([]byte{1}) },
		func(s *processObservation, a processTestAliases) {
			s.record(a.lock, GroupAccepted, "", strings.Repeat("1", 32), "")
		},
	} {
		s, a := processTestState(t)
		check(s, a)
		requireProcessFailed(t, s)
	}
	for _, afterJoin := range []bool{false, true} {
		s, a := processTestState(t)
		s.constructorAttempt(a.core, ProcessControlNode)
		s.controlConstructed(a.core, a.control)
		if afterJoin {
			s.controlNodeJoined(a.core, a.control, true)
		}
		s.record(a.control, ManagementFrameAttempted, "preview", "", "")
		requireProcessFailed(t, s)
	}
	// The Core is not inferred from a lock-shaped or role-shaped substitute.
	for _, sameLock := range []bool{false, true} {
		s, _ := newProcessObservation(processmodel.Owner)
		lock := new(int)
		s.ownerLock(lock)
		if sameLock {
			s.coreConstructed(lock, lock)
		} else {
			s.coreConstructed(new(int), new(int))
		}
		requireProcessFailed(t, s)
	}
}

func TestProcessObservationConstructorLedger(t *testing.T) {
	for _, test := range []struct {
		kind  ProcessNodeConstructor
		event processmodel.Kind
	}{
		{ProcessLegacyNode, processmodel.LegacyConstructorAttempted},
		{ProcessManagedNode, processmodel.ManagedConstructorAttempted},
		{ProcessManagedStartupNode, processmodel.ManagedStartupConstructorAttempted},
	} {
		s, a := processTestState(t)
		s.constructorAttempt(a.core, test.kind)
		s.nodeConstructed(a.core, a.ordinary)
		p := processTestPrefix(s)
		if !p.Complete || p.Count != 4 || p.Events[2].Observation.Kind != test.event || p.Events[3].Observation.Kind != processmodel.OrdinaryNodeBound {
			t.Fatal("actual ordinary branch was hidden or miscounted")
		}
		s.constructorAttempt(a.core, test.kind)
		requireProcessFailed(t, s)
	}
	for _, check := range []func(*processObservation, processTestAliases){
		func(s *processObservation, a processTestAliases) { s.constructorAttempt(a.core, 0) },
		func(s *processObservation, a processTestAliases) { s.constructorAttempt(a.core, 255) },
		func(s *processObservation, a processTestAliases) {
			s.constructorAttempt(a.core, ProcessControlNode)
			s.constructorAttempt(a.core, ProcessControlNode)
		},
		func(s *processObservation, a processTestAliases) {
			s.constructorAttempt(a.core, ProcessControlNode)
			s.constructorAttempt(a.core, ProcessManagedNode)
		},
		func(s *processObservation, a processTestAliases) {
			s.constructorAttempt(a.core, ProcessManagedNode)
			s.constructorAttempt(a.core, ProcessControlNode)
		},
		func(s *processObservation, a processTestAliases) {
			s.constructorAttempt(a.core, ProcessManagedNode)
			s.nodeConstructed(a.core, a.lock)
		},
		func(s *processObservation, a processTestAliases) {
			s.constructorAttempt(a.core, ProcessControlNode)
			s.controlConstructed(a.core, a.core)
		},
		func(s *processObservation, a processTestAliases) {
			processTestReady(s, a, true)
			s.nodeConstructed(a.core, new(int))
		},
	} {
		s, a := processTestState(t)
		check(s, a)
		requireProcessFailed(t, s)
	}
}

func TestProcessObservationControlJoinIsIdempotentAndFailureSticky(t *testing.T) {
	s, a := processTestState(t)
	s.constructorAttempt(a.core, ProcessControlNode)
	s.controlConstructed(a.core, a.control)
	s.controlNodeJoined(a.core, a.control, true)
	s.controlNodeJoined(a.core, a.control, true)
	if p := processTestPrefix(s); !p.Complete || p.Count != 5 || p.Events[4].Observation.Kind != processmodel.ControlNodeJoined {
		t.Fatal("repeated real close invented another join")
	}
	s.controlNodeJoined(a.core, a.control, false)
	before := processTestPrefix(s)
	s.controlNodeJoined(a.core, a.control, true)
	after := processTestPrefix(s)
	if before.Failures == 0 || after.Failures != before.Failures || after.Reserved != before.Reserved || after.Complete {
		t.Fatal("later successful close cleared earlier failure")
	}
	s, a = processTestState(t)
	s.constructorAttempt(a.core, ProcessControlNode)
	s.controlConstructed(a.core, a.control)
	s.controlNodeJoined(a.core, a.control, false)
	s.controlNodeJoined(a.core, a.control, true)
	requireProcessFailed(t, s)
}

func TestProcessObservationWebIPCAndClosure(t *testing.T) {
	for _, origin := range []string{"", "http://localhost:1234", "https://127.0.0.1:1234", "http://0.0.0.0:1234", "http://127.0.0.1:0", "http://127.0.0.1:0123", "http://127.0.0.1:65536", "http://127.0.0.1:1234/", "http://127.0.0.1:+1234", "http://127.0.0.1:1234?code=x"} {
		s, a := processTestState(t)
		s.webOpened(a.core, origin)
		requireProcessFailed(t, s)
		if s.webPort.Load() != 0 {
			t.Fatal("invalid origin retained a port")
		}
	}
	for _, origin := range []string{"http://127.0.0.1:1", "http://127.0.0.1:65535"} {
		if _, ok := processWebPort(origin); !ok {
			t.Fatal("canonical numeric loopback origin rejected")
		}
	}
	for _, check := range []func(*processObservation, processTestAliases){
		func(s *processObservation, a processTestAliases) { s.ipcOpened(a.core) },
		func(s *processObservation, a processTestAliases) {
			s.webOpened(a.core, "http://127.0.0.1:1234")
			s.webOpened(a.core, "http://127.0.0.1:2345")
		},
		func(s *processObservation, a processTestAliases) { processTestReady(s, a, false); s.ipcOpened(a.core) },
		func(s *processObservation, a processTestAliases) {
			processTestReady(s, a, false)
			s.ownersJoined(a.core, false, true)
		},
		func(s *processObservation, a processTestAliases) {
			processTestReady(s, a, false)
			s.ownersJoined(a.core, true, false)
		},
		func(s *processObservation, a processTestAliases) { s.ownersJoined(a.core, true, true) },
		func(s *processObservation, a processTestAliases) {
			processTestReady(s, a, false)
			s.ownersJoined(a.core, true, true)
			s.maintenancePassed(a.core)
		},
		func(s *processObservation, a processTestAliases) {
			processTestReady(s, a, false)
			s.ownersJoined(a.core, true, true)
			s.ownersJoined(a.core, true, true)
		},
	} {
		s, a := processTestState(t)
		check(s, a)
		requireProcessFailed(t, s)
	}
}

func TestProcessObservationModeAndResourceShapes(t *testing.T) {
	for _, mode := range []processmodel.Mode{0, 255} {
		if s, ok := newProcessObservation(mode); ok || s != nil {
			t.Fatal("invalid mode created observation")
		}
	}
	cli, ok := newProcessObservation(processmodel.CLI)
	if !ok {
		t.Fatal("CLI value observation rejected")
	}
	cli.ownerLock(new(int))
	requireProcessFailed(t, cli)
	unbound, _ := newProcessObservation(processmodel.Owner)
	unbound.maintenancePassed(new(int))
	requireProcessFailed(t, unbound)
	run, operation := strings.Repeat("1", 32), strings.Repeat("2", 64)
	for _, input := range []struct {
		kind                   Kind
		action, run, operation string
	}{
		{GroupAccepted, "", run, ""}, {GroupIntent, "apply", run, operation},
		{ManagementClientInvoked, "inspect", "", ""}, {ManagementFrameAttempted, "preview", "", ""},
		{ManagementFrameWritten, "operation.status", "", operation}, {ProviderAdmitted, "apply", "", operation},
	} {
		s, a := processTestState(t)
		s.record(a.core, input.kind, input.action, input.run, input.operation)
		if p := processTestPrefix(s); !p.Complete || p.ResourceCount != 1 {
			t.Fatal("canonical resource observation rejected")
		}
	}
	for _, input := range []struct {
		kind                   Kind
		action, run, operation string
	}{
		{0, "", "", ""}, {255, "", "", ""}, {GroupAccepted, "", "", ""},
		{GroupAccepted, "", run, operation}, {GroupAccepted, "apply", run, ""},
		{GroupIntent, "preview", run, operation}, {GroupIntent, "apply", run, strings.Repeat("A", 64)},
		{ManagementClientInvoked, "", "", ""}, {ManagementClientInvoked, "other", "", ""},
		{ManagementFrameAttempted, "preview", run, ""}, {ManagementFrameAttempted, "preview", "", operation},
		{ProviderAdmitted, "inspect", "", operation}, {ProviderAdmitted, "apply", "", ""},
	} {
		s, a := processTestState(t)
		s.record(a.core, input.kind, input.action, input.run, input.operation)
		requireProcessFailed(t, s)
	}
}

func TestProcessObservationConcurrentAdmissionAndImmutableAlias(t *testing.T) {
	s, a := processTestState(t)
	var group sync.WaitGroup
	for i := 0; i < 32; i++ {
		group.Add(1)
		go func() { defer group.Done(); s.maintenancePassed(a.core) }()
	}
	group.Wait()
	if p := processTestPrefix(s); !p.Complete || p.Count != 34 || p.Active != 0 || s.gate.Load() != 0 {
		t.Fatal("concurrent leaf observations lost their complete prefix")
	}
	var alias processAlias
	for i := 0; i < 32; i++ {
		group.Add(1)
		go func() { defer group.Done(); alias.bind(a.core) }()
	}
	group.Wait()
	if !alias.equal(a.core) || alias.bind(a.ordinary) || alias.equal(a.ordinary) {
		t.Fatal("published alias was replaced")
	}
	// Admission closure racing a hook cannot restore a clean receipt.
	group.Add(2)
	go func() { defer group.Done(); s.closeAdmission() }()
	go func() { defer group.Done(); s.maintenancePassed(a.core) }()
	group.Wait()
	s.maintenancePassed(a.core)
	if p := processTestPrefix(s); p.Failures&processmodel.Late == 0 || p.Complete || s.gate.Load() != processAdmissionClosed {
		t.Fatal("late hook was hidden after concurrent admission closure")
	}
}

func TestProcessObservationGlobalHooksRemainUninstalled(t *testing.T) {
	if processObserver.Load() != nil {
		t.Fatal("source stage unexpectedly installed a live observer")
	}
	ProcessOwnerLock(nil)
	CoreConstructed(nil, nil)
	ProcessNodeConstructorAttempt(nil, 0)
	ProcessControlNodeConstructed(nil, nil)
	ProcessControlNodeJoined(nil, nil, false)
	NodeConstructed(nil, nil)
	MaintenancePassed(nil)
	ProcessWebOpened(nil, "")
	ProcessIPCReady(nil)
	ProcessOwnersClosed(nil, false, false)
	Record(nil, 0, "", "", "")
	if processObserver.Load() != nil {
		t.Fatal("leaf hook installed itself")
	}
}
