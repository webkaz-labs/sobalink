//go:build resource_process_native

package resourceacceptance

import (
	"bytes"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/resourceacceptance/processmodel"
)

func TestProcessOutputCopiesAndDrainsValues(t *testing.T) {
	state, _ := newProcessObservation(processmodel.CLI)
	out, ok := newProcessOutput(state)
	if !ok {
		t.Fatal("construct output")
	}
	value := []byte("synthetic output")
	if n, err := out.Write(value); err != nil || n != len(value) {
		t.Fatal("write")
	}
	value[0] = '!'
	block, snap := out.drain()
	if !bytes.Equal(block.bytes[:block.length], []byte("synthetic output")) || snap.accepted != snap.drained || snap.failed {
		t.Fatal("copied output mismatch")
	}
	block.bytes[0] = '!'
	if next, _ := out.drain(); next.length != 0 {
		t.Fatal("duplicate drain")
	}
	if n, err := out.Write(nil); n != 0 || err != nil {
		t.Fatal("empty write")
	}
	if processObserver.Load() != nil {
		t.Fatal("unexpected global observer")
	}
}

func TestProcessOutputModeBoundsValues(t *testing.T) {
	for _, mode := range []processmodel.Mode{processmodel.Owner, processmodel.CLI} {
		state, _ := newProcessObservation(mode)
		out, _ := newProcessOutput(state)
		var block [processmodel.MaxStdoutChunk]byte
		for total := uint32(0); total < out.limit; total += uint32(len(block)) {
			if n, err := out.Write(block[:]); n != len(block) || err != nil {
				t.Fatal("capacity rejected")
			}
		}
		if n, err := out.Write([]byte{1}); n != 0 || err == nil {
			t.Fatal("overflow accepted")
		}
		if !out.snapshot().failed || state.snapshotThrough(0).Failures == 0 {
			t.Fatal("overflow not sticky")
		}
		if n, err := out.Write(nil); n != 0 || err == nil {
			t.Fatal("failure reset")
		}
	}
}

func TestProcessOutputClosedValues(t *testing.T) {
	state, _ := newProcessObservation(processmodel.Owner)
	out, _ := newProcessOutput(state)
	_, _ = out.Write([]byte{1, 2, 3})
	if snap := out.close(); !snap.closed || snap.failed || snap.accepted != 3 {
		t.Fatal("close snapshot")
	}
	block, _ := out.drain()
	if block.length != 3 {
		t.Fatal("close lost output")
	}
	if n, err := out.Write(nil); n != 0 || err == nil {
		t.Fatal("late write accepted")
	}
	if !out.close().failed {
		t.Fatal("late failure reset")
	}
	if _, ok := newProcessOutput(nil); ok {
		t.Fatal("nil observation accepted")
	}
}
