//go:build resource_process_native

package resourceacceptance

import (
	"errors"
	"sync"

	"github.com/webkaz-labs/sobalink/internal/resourceacceptance/processmodel"
)

var errProcessOutput = errors.New("process observation output unavailable")

type processOutputSnapshot struct {
	accepted, drained uint32
	closed, failed    bool
}

// Product writes copy under this instrumentation-only mutex. They perform no
// native I/O and never wait for a reporter or acquire a product mutex.
type processOutput struct {
	state             *processObservation
	mu                sync.Mutex
	bytes             [processmodel.MaxOwnerStdoutBytes]byte
	limit             uint32
	accepted, drained uint32
	closed, failed    bool
}

func newProcessOutput(state *processObservation) (*processOutput, bool) {
	if state == nil || state.recorder == nil {
		return nil, false
	}
	limit := uint32(processmodel.MaxOwnerStdoutBytes)
	switch state.mode {
	case processmodel.Owner:
	case processmodel.CLI:
		limit = processmodel.MaxCLIStdoutBytes
	default:
		return nil, false
	}
	return &processOutput{state: state, limit: limit}, true
}

func (o *processOutput) Write(value []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed || o.failed || uint64(len(value)) > uint64(o.limit-o.accepted) {
		o.failed = true
		o.state.invalidate()
		return 0, errProcessOutput
	}
	copy(o.bytes[o.accepted:], value)
	o.accepted += uint32(len(value))
	return len(value), nil
}

// The returned block is a copy. Advancement is not a retry token: failure to
// write the resulting authenticated frame permanently fails the transcript.
func (o *processOutput) drain() (processPipeBlock, processOutputSnapshot) {
	o.mu.Lock()
	defer o.mu.Unlock()
	var block processPipeBlock
	remaining := o.accepted - o.drained
	if remaining > processmodel.MaxStdoutChunk {
		remaining = processmodel.MaxStdoutChunk
	}
	block.length = uint16(remaining)
	copy(block.bytes[:], o.bytes[o.drained:o.drained+remaining])
	o.drained += remaining
	return block, o.snapshotLocked()
}

func (o *processOutput) snapshotLocked() processOutputSnapshot {
	return processOutputSnapshot{accepted: o.accepted, drained: o.drained, closed: o.closed, failed: o.failed}
}

func (o *processOutput) snapshot() processOutputSnapshot {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.snapshotLocked()
}

func (o *processOutput) close() processOutputSnapshot {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.closed = true
	return o.snapshotLocked()
}
