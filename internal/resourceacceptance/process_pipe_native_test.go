//go:build resource_process_native

package resourceacceptance

import (
	"os"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/resourceacceptance/processmodel"
)

// These helpers are native anonymous-pipe effects, not value-test fixtures.
// Every transferred endpoint retains its sole closer and the original cutoff.
func processTestPipePair(t *testing.T, mode processmodel.Mode, cutoff time.Time) (*processPipe, *processPipe) {
	t.Helper()
	rawRead, rawWrite, err := os.Pipe()
	if err != nil {
		t.Fatal("anonymous pipe creation failed")
	}
	read, readOK := newProcessPipe(rawRead, processPipeRead, mode, cutoff)
	write, writeOK := newProcessPipe(rawWrite, processPipeWrite, mode, cutoff)
	// Even an unexpected kind rejection must retain concrete cleanup owners.
	if !readOK {
		read = &processPipe{file: rawRead, direction: processPipeRead, mode: mode, cutoff: cutoff}
	}
	if !writeOK {
		write = &processPipe{file: rawWrite, direction: processPipeWrite, mode: mode, cutoff: cutoff}
	}
	t.Cleanup(func() {
		deadline := processBefore(cutoff, 2*time.Second)
		a, b := read.closeAndJoin(deadline), write.closeAndJoin(deadline)
		if !a.ioJoined || !a.closeJoined || !b.ioJoined || !b.closeJoined {
			t.Error("native pipe cleanup unjoined")
		}
		if a.closeJoined && read.closer.outcome != processPipeOK || b.closeJoined && write.closer.outcome != processPipeOK {
			t.Error("native endpoint Close failed")
		}
	})
	if !readOK || !writeOK {
		t.Fatal("anonymous endpoint kind rejected")
	}
	return read, write
}

func TestProcessPipeGuardsValues(t *testing.T) {
	if _, ok := newProcessPipe(nil, processPipeRead, processmodel.Owner, time.Now().Add(time.Second)); ok {
		t.Fatal("nil endpoint accepted")
	}
	p := &processPipe{direction: processPipeRead, cutoff: time.Now().Add(time.Second)}
	if _, outcome := p.readExact(0, p.cutoff); outcome != processPipeInvalid || p.tasks != 0 || p.active != nil || p.closer != nil {
		t.Fatal("invalid length started work")
	}
	p = &processPipe{direction: processPipeWrite, cutoff: time.Now().Add(time.Second)}
	if _, outcome := p.readExact(1, p.cutoff); outcome != processPipeInvalid || p.tasks != 0 {
		t.Fatal("wrong direction started work")
	}
	p = &processPipe{direction: processPipeRead, cutoff: time.Now().Add(time.Second)}
	if _, outcome := p.readExact(1, p.cutoff.Add(time.Second)); outcome != processPipeInvalid || p.tasks != 0 {
		t.Fatal("extended deadline accepted")
	}
	done := make(chan struct{})
	close(done)
	if processWait(done, time.Now().Add(-time.Second)) {
		t.Fatal("expired join accepted")
	}
}

func TestProcessPipeNativeExactAndCloseOnce(t *testing.T) {
	cutoff := time.Now().Add(10 * time.Second)
	read, write := processTestPipePair(t, processmodel.CLI, cutoff)
	block := processPipeBlock{length: 3}
	copy(block.bytes[:], "abc")
	done := make(chan struct{})
	var writeOutcome processPipeOutcome
	go func() {
		defer close(done)
		writeOutcome = write.writeExact(block, processBefore(cutoff, 2*time.Second))
	}()
	t.Cleanup(func() {
		deadline := processBefore(cutoff, 2*time.Second)
		_ = read.closeAndJoin(deadline)
		_ = write.closeAndJoin(deadline)
		if !processWait(done, deadline) {
			t.Error("exact writer caller unjoined")
		}
	})
	got, outcome := read.readExact(3, processBefore(cutoff, 2*time.Second))
	if !processWait(done, cutoff) {
		t.Fatal("writer unjoined")
	}
	if outcome != processPipeOK || writeOutcome != processPipeOK || string(got.bytes[:got.length]) != "abc" {
		t.Fatal("exact transfer failed")
	}
	join := write.closeAndJoin(cutoff)
	closer := write.closer
	second := write.closeAndJoin(cutoff)
	if join.outcome != processPipeOK || second.outcome != processPipeOK || write.closer != closer || !second.closeJoined {
		t.Fatal("close was not retained")
	}
	_, outcome = read.readExact(1, processBefore(cutoff, 2*time.Second))
	if outcome != processPipeEOF {
		t.Fatal("zero-byte EOF missing")
	}
}

func TestProcessPipeNativePartialEOFIsFailure(t *testing.T) {
	cutoff := time.Now().Add(10 * time.Second)
	read, write := processTestPipePair(t, processmodel.Owner, cutoff)
	block := processPipeBlock{length: 1}
	block.bytes[0] = 1
	done := make(chan struct{})
	var outcome processPipeOutcome
	var joined processPipeJoin
	go func() {
		defer close(done)
		outcome = write.writeExact(block, processBefore(cutoff, 2*time.Second))
		joined = write.closeAndJoin(processBefore(cutoff, 2*time.Second))
	}()
	t.Cleanup(func() {
		deadline := processBefore(cutoff, 2*time.Second)
		_ = read.closeAndJoin(deadline)
		_ = write.closeAndJoin(deadline)
		if !processWait(done, deadline) {
			t.Error("partial writer caller unjoined")
		}
	})
	got, result := read.readExact(2, processBefore(cutoff, 2*time.Second))
	if !processWait(done, cutoff) {
		t.Fatal("partial writer unjoined")
	}
	if outcome != processPipeOK || joined.outcome != processPipeOK || result != processPipeIOFailure || got != (processPipeBlock{}) {
		t.Fatal("partial EOF accepted")
	}
}

func TestProcessPipeNativeBlockedReadJoins(t *testing.T) {
	cutoff := time.Now().Add(5 * time.Second)
	read, _ := processTestPipePair(t, processmodel.Owner, cutoff)
	block, outcome := read.readExact(1, processBefore(cutoff, 50*time.Millisecond))
	join := read.closeAndJoin(cutoff)
	if outcome != processPipeTimeout || block != (processPipeBlock{}) || !join.ioJoined || !join.closeJoined {
		t.Fatal("blocked read cancellation unproven")
	}
	if _, outcome := read.readExact(1, cutoff); outcome != processPipeInvalid {
		t.Fatal("failed pipe reused")
	}
}

func TestProcessPipeNativeBlockedWriteJoins(t *testing.T) {
	cutoff := time.Now().Add(5 * time.Second)
	_, write := processTestPipePair(t, processmodel.Owner, cutoff)
	block := processPipeBlock{length: processmodel.MaxFrameSize}
	deadline := processBefore(cutoff, 100*time.Millisecond)
	blocked := false
	for attempt := 0; attempt < processPipeTaskLimit; attempt++ {
		outcome := write.writeExact(block, deadline)
		if outcome == processPipeTimeout {
			blocked = true
			break
		}
		if outcome != processPipeOK {
			t.Fatal("blocked write cancellation unproven")
		}
	}
	join := write.closeAndJoin(cutoff)
	if !blocked || !join.ioJoined || !join.closeJoined {
		t.Fatal("blocked write not observed and joined")
	}
}

func TestProcessPipeNativeConcurrentCloseOnce(t *testing.T) {
	cutoff := time.Now().Add(5 * time.Second)
	read, _ := processTestPipePair(t, processmodel.Owner, cutoff)
	firstDone, secondDone := make(chan struct{}), make(chan struct{})
	var first, second processPipeJoin
	go func() { defer close(firstDone); first = read.closeAndJoin(cutoff) }()
	go func() { defer close(secondDone); second = read.closeAndJoin(cutoff) }()
	t.Cleanup(func() {
		deadline := processBefore(cutoff, 2*time.Second)
		_ = read.closeAndJoin(deadline)
		firstJoined := processWait(firstDone, deadline)
		secondJoined := processWait(secondDone, deadline)
		if !firstJoined || !secondJoined {
			t.Error("close callers unjoined during cleanup")
		}
	})
	firstJoined, secondJoined := processWait(firstDone, cutoff), processWait(secondDone, cutoff)
	if !firstJoined || !secondJoined {
		t.Fatal("concurrent close callers unjoined")
	}
	if first.outcome != processPipeOK || second.outcome != processPipeOK || read.closer == nil {
		t.Fatal("concurrent close not retained")
	}
	closer := read.closer
	if final := read.closeAndJoin(cutoff); final.outcome != processPipeOK || read.closer != closer {
		t.Fatal("closer replaced")
	}
}
