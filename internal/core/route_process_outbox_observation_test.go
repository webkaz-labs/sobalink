//go:build lanlink_integration

package core

import (
	"github.com/webkaz-labs/sobalink/internal/config"
	"os"
	"runtime"
	"testing"
)

// Test-child evidence only. Raw stacks, file names, profile paths, request IDs
// and function names never enter the subprocess reply or CI output. The sample
// is taken after a busy reply: a vanished holder is explicitly not observed.
type routeOutboxBusyObservation struct {
	CommandMS         int64
	SnapshotAvailable bool
	LifecycleObserved bool
	Receiver          bool
	Outbox            bool
	Message           bool
	Resource          bool
	Other             bool
}

func (o *routeOutboxBusyObservation) addFrames(names []string) {
	const prefix = "github.com/webkaz-labs/sobalink/internal/"
	held := false
	for _, name := range names {
		if name == prefix+"config.(*Lock).withOwnershipInfoLocked" {
			held = true
		}
	}
	if !held {
		return
	}
	o.LifecycleObserved = true
	classified := false
	for _, name := range names {
		switch name {
		case prefix + "transfer.(*durableReceiver).withOwner", prefix + "transfer.(*durableReceiver).verify":
			o.Receiver, classified = true, true
		case prefix + "core.(*outboxCoordinator).withOwnedLocked":
			o.Outbox, classified = true, true
		case prefix + "core.(*Core).mutateMessageStoreLocked", prefix + "core.(*Core).recertifyMessageCandidateLocked":
			o.Message, classified = true, true
		case prefix + "core.(*Core).withResourceInspectionState", prefix + "core.(*Core).withResourceCatalogViewState":
			o.Resource, classified = true, true
		}
	}
	if !classified {
		o.Other = true
	}
}

func routeObserveOutboxBusy(commandMS int64) routeOutboxBusyObservation {
	o := routeOutboxBusyObservation{CommandMS: commandMS}
	// One bounded snapshot, without waiting for or acquiring any product lock.
	// A concurrently growing profile is unavailable, not an empty observation.
	records := make([]runtime.StackRecord, 1024)
	n, ok := runtime.GoroutineProfile(records)
	if !ok {
		return o
	}
	o.SnapshotAvailable = true
	for _, record := range records[:n] {
		frames := runtime.CallersFrames(record.Stack())
		var names []string
		for {
			frame, more := frames.Next()
			names = append(names, frame.Function)
			if !more {
				break
			}
		}
		o.addFrames(names)
	}
	return o
}

func TestRouteOutboxObservationClosedFrames(t *testing.T) {
	const prefix = "github.com/webkaz-labs/sobalink/internal/"
	held := prefix + "config.(*Lock).withOwnershipInfoLocked"
	for _, tc := range []struct {
		names []string
		want  routeOutboxBusyObservation
	}{
		{[]string{"synthetic/private-value", prefix + "config.(*Lock).WithOwnershipInfo", prefix + "core.(*outboxCoordinator).withOwnedLocked"}, routeOutboxBusyObservation{}},
		{[]string{held, prefix + "transfer.(*durableReceiver).withOwner"}, routeOutboxBusyObservation{LifecycleObserved: true, Receiver: true}},
		{[]string{held, prefix + "transfer.(*durableReceiver).verify"}, routeOutboxBusyObservation{LifecycleObserved: true, Receiver: true}},
		{[]string{held, prefix + "core.(*outboxCoordinator).withOwnedLocked"}, routeOutboxBusyObservation{LifecycleObserved: true, Outbox: true}},
		{[]string{held, prefix + "core.(*Core).mutateMessageStoreLocked"}, routeOutboxBusyObservation{LifecycleObserved: true, Message: true}},
		{[]string{held, prefix + "core.(*Core).withResourceInspectionState"}, routeOutboxBusyObservation{LifecycleObserved: true, Resource: true}},
		{[]string{held, "synthetic/private-value"}, routeOutboxBusyObservation{LifecycleObserved: true, Other: true}},
		{[]string{held + "extra", prefix + "core.(*outboxCoordinator).withOwnedLocked"}, routeOutboxBusyObservation{}},
	} {
		var got routeOutboxBusyObservation
		got.addFrames(tc.names)
		if got != tc.want {
			t.Fatal("closed lifecycle observation changed")
		}
	}
	o := routeObserveOutboxBusy(3)
	if o.CommandMS != 3 || !o.SnapshotAvailable {
		t.Fatal("bounded runtime sample unavailable")
	}
}

func TestRouteOutboxObservationHeldLifecycle(t *testing.T) {
	dir := t.TempDir()
	owner, err := config.AcquireLock(dir)
	if err != nil {
		t.Fatal("synthetic lifecycle unavailable")
	}
	defer owner.Close()
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- owner.WithOwnershipInfo(dir, func(os.FileInfo, os.FileInfo) error { close(entered); <-release; return nil })
	}()
	<-entered
	o := routeObserveOutboxBusy(7)
	close(release)
	if err := <-done; err != nil {
		t.Fatal("synthetic lifecycle did not release")
	}
	if o.CommandMS != 7 || !o.SnapshotAvailable || !o.LifecycleObserved || !o.Other || o.Receiver || o.Outbox || o.Message || o.Resource {
		t.Fatal("active lifecycle callback was not classified conservatively")
	}
}
