//go:build resource_process_native

package processmodel

import (
	"sync"
	"testing"
)

func newRecorder(t *testing.T, mode Mode) *Recorder {
	t.Helper()
	r, ok := NewRecorder(mode)
	if !ok {
		t.Fatal("valid recorder mode rejected")
	}
	return r
}

func TestRecorderPrefixAndCopies(t *testing.T) {
	r := newRecorder(t, Owner)
	resource := Observation{Kind: GroupAccepted, RunID: [16]byte{1}}
	r.Record(resource)
	r.Record(Observation{Kind: LocalCommand, Command: LocalStatus, RequestDigest: [32]byte{2}})
	r.Record(Observation{Kind: Barrier, BarrierOrdinal: 1})
	prefix := r.PrefixThrough(3)
	if !prefix.Complete || prefix.Count != 3 || prefix.Reserved != 3 || prefix.ResourceCount != 1 || prefix.LocalCount != 1 || prefix.LifecycleCount != 1 || prefix.Active != 0 || prefix.AdmissionClosed {
		t.Fatal("complete prefix counts incorrect")
	}
	for i := uint16(0); i < prefix.Count; i++ {
		if prefix.Events[i].Sequence != uint64(i+1) {
			t.Fatal("prefix sequence is not contiguous")
		}
	}
	resource.RunID[0] = 8
	prefix.Events[0].Observation.RunID[0] = 9
	again := r.PrefixThrough(3)
	if again.Events[0].Observation.RunID[0] != 1 {
		t.Fatal("caller/prefix mutation changed immutable record")
	}
	short := r.PrefixThrough(1)
	if !short.Complete || short.Count != 1 || short.ResourceCount != 1 || short.LocalCount != 0 || short.LifecycleCount != 0 || short.Reserved != 3 {
		t.Fatal("prefix counts included later reservations")
	}
	if empty := r.PrefixThrough(0); !empty.Complete || empty.Count != 0 || empty.Reserved != 3 {
		t.Fatal("explicit empty prefix was misrepresented")
	}
	if future := r.PrefixThrough(4); future.Complete || future.Count != 3 || future.Failures != 0 {
		t.Fatal("unready prefix certified or prematurely timed out")
	}
}

func TestRecorderBoundsAndInvalid(t *testing.T) {
	if _, ok := NewRecorder(0); ok {
		t.Error("zero mode accepted")
	}
	if _, ok := NewRecorder(255); ok {
		t.Error("unknown mode accepted")
	}
	for _, mode := range []Mode{Owner, CLI} {
		r := newRecorder(t, mode)
		for i := uint64(0); i < mode.capacity(); i++ {
			if !r.enter() {
				t.Fatal("exact admitted-writer cap rejected")
			}
		}
		r.Record(Observation{Kind: IPCConnectAttempt})
		if prefix := r.PrefixThrough(0); prefix.Active != mode.capacity() || prefix.Reserved != 0 || prefix.Failures != Overflow {
			t.Fatal("over-cap caller reserved a ticket or decremented incorrectly")
		}
		r.CloseAdmission()
		r.Record(Observation{Kind: IPCConnectAttempt})
		if prefix := r.PrefixThrough(0); !prefix.AdmissionClosed || prefix.Active != mode.capacity() || prefix.Reserved != 0 || prefix.Failures != Overflow|Late {
			t.Fatal("closed over-cap gate lost late evidence or close bit")
		}
		for i := uint64(0); i < mode.capacity(); i++ {
			r.leave()
		}
		if prefix := r.PrefixThrough(0); !prefix.AdmissionClosed || prefix.Active != 0 || prefix.Failures != Overflow|Late {
			t.Fatal("admitted writers did not leave closed gate exactly once")
		}
	}
	for _, test := range []struct {
		value Observation
		cap   int
	}{
		{Observation{Kind: GroupAccepted}, MaxResourceEvents},
		{Observation{Kind: IPCConnectAttempt}, MaxLocalEvents},
		{Observation{Kind: MaintenancePassed}, MaxLifecycleEvents},
	} {
		r := newRecorder(t, Owner)
		for i := 0; i < test.cap; i++ {
			r.Record(test.value)
		}
		if prefix := r.PrefixThrough(uint64(test.cap)); !prefix.Complete || int(prefix.Count) != test.cap {
			t.Error("exact category cap rejected")
		}
		r.Record(test.value)
		failed := r.PrefixThrough(uint64(test.cap))
		if failed.Complete || failed.Failures&Overflow == 0 || failed.Reserved != uint64(test.cap+1) {
			t.Error("category overflow not sticky")
		}
		r.Record(Observation{Kind: Barrier, BarrierOrdinal: 1})
		if next := r.PrefixThrough(uint64(test.cap)); next.Reserved != failed.Reserved || next.Failures != failed.Failures {
			t.Error("failure permitted a new ticket or cleared latch")
		}
	}
	full := newRecorder(t, Owner)
	for i := 0; i < MaxResourceEvents; i++ {
		full.Record(Observation{Kind: GroupAccepted})
	}
	for i := 0; i < MaxLocalEvents; i++ {
		full.Record(Observation{Kind: IPCConnectAttempt})
	}
	for i := 0; i < MaxLifecycleEvents; i++ {
		full.Record(Observation{Kind: MaintenancePassed})
	}
	if prefix := full.PrefixThrough(MaxOwnerEvents); !prefix.Complete || prefix.Count != MaxOwnerEvents || prefix.Events[MaxOwnerEvents-1].Sequence != MaxOwnerEvents {
		t.Fatal("exact final owner slot rejected")
	}
	full.Record(Observation{Kind: IPCConnectAttempt})
	if prefix := full.PrefixThrough(MaxOwnerEvents); prefix.Complete || prefix.Failures&Overflow == 0 || prefix.Reserved != MaxOwnerEvents+1 {
		t.Fatal("total overflow not retained")
	}
	cli := newRecorder(t, CLI)
	for i := 0; i < MaxCLIEvents; i++ {
		cli.Record(Observation{Kind: IPCConnectAttempt})
	}
	if prefix := cli.PrefixThrough(MaxCLIEvents); !prefix.Complete || prefix.Count != MaxCLIEvents {
		t.Fatal("exact CLI final slot rejected")
	}
	cli.Record(Observation{Kind: EntryReturned, Outcome: Success})
	if prefix := cli.PrefixThrough(MaxCLIEvents); prefix.Failures&Overflow == 0 || prefix.Complete {
		t.Fatal("CLI overflow accepted")
	}
	for _, value := range []Observation{{Kind: Kind(255)}, {Kind: GroupIntent}, {Kind: LocalCommand, Command: Command(255)}, {Kind: Barrier, BarrierOrdinal: 33}} {
		r := newRecorder(t, Owner)
		r.Record(value)
		if prefix := r.PrefixThrough(1); prefix.Complete || prefix.Count != 0 || prefix.Failures&Invalid == 0 {
			t.Error("invalid observation did not fail closed")
		}
	}
	for _, value := range []Observation{{Kind: GroupAccepted}, {Kind: MaintenancePassed}, {Kind: Barrier, BarrierOrdinal: 5}} {
		r := newRecorder(t, CLI)
		r.Record(value)
		if prefix := r.PrefixThrough(1); prefix.Complete || prefix.Failures&Invalid == 0 {
			t.Error("CLI accepted owner-only event")
		}
	}
	for _, tickets := range []uint64{^uint64(0) - 1, ^uint64(0)} {
		r := newRecorder(t, Owner)
		r.tickets.Store(tickets)
		r.Record(Observation{Kind: IPCConnectAttempt})
		if prefix := r.PrefixThrough(0); prefix.Complete || prefix.Failures&SequenceFailure == 0 {
			t.Error("sequence exhaustion was not latched")
		}
		reserved := r.tickets.Load()
		r.Record(Observation{Kind: IPCConnectAttempt})
		if r.tickets.Load() != reserved {
			t.Error("exhausted sequence was reused")
		}
	}
	badTarget := newRecorder(t, Owner)
	if prefix := badTarget.PrefixThrough(MaxOwnerEvents + 1); prefix.Complete || prefix.Failures&Invalid == 0 {
		t.Error("out-of-bounds target accepted")
	}
}

func TestRecorderGapRemainsFailed(t *testing.T) {
	r := newRecorder(t, Owner)
	if !r.enter() {
		t.Fatal("first writer not admitted")
	}
	first, ok := r.reserve(Observation{Kind: IPCConnectAttempt})
	if !ok {
		t.Fatal("first reservation failed")
	}
	if !r.enter() {
		t.Fatal("second writer not admitted")
	}
	second, ok := r.reserve(Observation{Kind: Barrier, BarrierOrdinal: 1})
	if !ok {
		t.Fatal("second reservation failed")
	}
	r.publish(second)
	r.leave()
	gap := r.PrefixThrough(2)
	if gap.Complete || gap.Count != 0 || gap.Active != 1 || gap.Reserved != 2 || gap.Failures != 0 {
		t.Fatal("later ready slot certified across a gap")
	}
	r.FailGap()
	r.publish(first)
	r.leave()
	filled := r.PrefixThrough(2)
	if filled.Complete || filled.Count != 2 || filled.Failures&PrefixGap == 0 || filled.Active != 0 {
		t.Fatal("late publication recovered failed prefix")
	}
	first.Observation.Kind = IPCConnectCompleted
	r.publish(first)
	duplicate := r.PrefixThrough(2)
	if duplicate.Failures != PrefixGap|Invalid || duplicate.Events[0].Observation.Kind != IPCConnectAttempt {
		t.Fatal("duplicate publication rewrote immutable slot")
	}
	r.CloseAdmission()
	r.Record(Observation{Kind: IPCConnectAttempt})
	if final := r.PrefixThrough(2); final.Failures != PrefixGap|Invalid|Late || final.Complete || final.Reserved != 2 {
		t.Fatal("close/late call cleared failure or reused ticket")
	}
}

func TestRecorderCloseAndLate(t *testing.T) {
	closed := newRecorder(t, Owner)
	closed.CloseAdmission()
	closed.Record(Observation{Kind: IPCConnectAttempt})
	if prefix := closed.PrefixThrough(0); prefix.Complete || !prefix.AdmissionClosed || prefix.Active != 0 || prefix.Reserved != 0 || prefix.Failures != Late {
		t.Fatal("post-close record was ignored")
	}
	r := newRecorder(t, Owner)
	if !r.enter() {
		t.Fatal("writer not admitted")
	}
	value, ok := r.reserve(Observation{Kind: EntryReturned, Outcome: Success})
	if !ok {
		t.Fatal("reservation failed")
	}
	r.CloseAdmission()
	if prefix := r.PrefixThrough(1); prefix.Complete || !prefix.AdmissionClosed || prefix.Active != 1 || prefix.Count != 0 {
		t.Fatal("closure certified outstanding writer")
	}
	r.publish(value)
	r.leave()
	copyBeforeLate := r.PrefixThrough(1)
	if !copyBeforeLate.Complete || !copyBeforeLate.AdmissionClosed || copyBeforeLate.Active != 0 {
		t.Fatal("admitted writer could not complete copied prefix")
	}
	r.Record(Observation{Kind: IPCConnectAttempt})
	r.CloseAdmission()
	afterLate := r.PrefixThrough(1)
	if afterLate.Complete || afterLate.Failures != Late || afterLate.Reserved != 1 || afterLate.Active != 0 {
		t.Fatal("late failure overwritten by closure")
	}
	if !copyBeforeLate.Complete || copyBeforeLate.Failures != 0 {
		t.Fatal("old snapshot mutated; snapshots are not live seals")
	}
	// Deterministic value scheduling also covers late admission before an
	// already-entered writer publishes. Successful publication cannot clear it.
	other := newRecorder(t, Owner)
	if !other.enter() {
		t.Fatal("writer not admitted")
	}
	pending, ok := other.reserve(Observation{Kind: IPCConnectAttempt})
	if !ok {
		t.Fatal("reservation failed")
	}
	other.CloseAdmission()
	other.Record(Observation{Kind: IPCConnectAttempt})
	other.publish(pending)
	other.leave()
	if prefix := other.PrefixThrough(1); prefix.Complete || prefix.Failures != Late || prefix.Count != 1 {
		t.Fatal("late latch lost to concurrent admitted publication")
	}
}

func TestRecorderConcurrentWriters(t *testing.T) {
	r := newRecorder(t, Owner)
	const writers = 32
	var workers sync.WaitGroup
	workers.Add(writers + 1)
	for i := 0; i < writers; i++ {
		go func(value byte) {
			defer workers.Done()
			r.Record(Observation{Kind: LocalCommand, Command: LocalStatus, RequestDigest: [32]byte{value}})
		}(byte(i))
	}
	go func() {
		defer workers.Done()
		for i := 0; i < 64; i++ {
			_ = r.PrefixThrough(writers)
		}
	}()
	workers.Wait()
	prefix := r.PrefixThrough(writers)
	if !prefix.Complete || prefix.Count != writers || prefix.LocalCount != writers || prefix.Active != 0 || prefix.Failures != 0 {
		t.Fatal("concurrent values lost, invalid, or unjoined")
	}
	var seen [writers]bool
	for i := 0; i < writers; i++ {
		event := prefix.Events[i]
		value := event.Observation.RequestDigest[0]
		if event.Sequence != uint64(i+1) || int(value) >= writers || seen[value] {
			t.Fatal("ticket reused or value duplicated")
		}
		seen[value] = true
	}
	r.CloseAdmission()
	if final := r.PrefixThrough(writers); !final.Complete || !final.AdmissionClosed {
		t.Fatal("completed value prefix changed on admission closure")
	}
}
