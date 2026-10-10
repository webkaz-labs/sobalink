//go:build resource_process_native && directlan_activation_native && linux && (amd64 || arm64)

package main

import (
	"io"
	"path/filepath"
	"time"

	pm "github.com/webkaz-labs/sobalink/internal/resourceacceptance/processmodel"
)

type p1Input struct {
	bytes  [pm.MaxBootstrapSize]byte
	length uint16
	done   chan struct{}
}

func (c *p1Child) writeInput() {
	defer close(c.writerDone)
	defer func() {
		if !c.input.joined(c.fixture.cleanup) {
			c.invalidate()
		}
	}()
	for {
		select {
		case <-c.finishInput:
			return
		case request := <-c.requests:
			n, err := c.input.file.Write(request.bytes[:request.length])
			if err != nil || n != int(request.length) {
				c.invalidate()
			}
			close(request.done)
			if c.failed.Load() {
				return
			}
		}
	}
}
func (c *p1Child) retainBytes(file *p1File, raw []byte, kind uint8) bool {
	c.mu.Lock()
	limit := uint64(1 << 20)
	accepted := true
	switch kind {
	case 0:
		if c.invocation.Mode == pm.CLI {
			limit = 64 << 10
			accepted = c.stdoutBytes+c.stderrBytes+uint64(len(raw)) <= limit
		} else {
			accepted = c.stdoutBytes+uint64(len(raw)) <= limit
		}
		if accepted {
			c.stdoutBytes += uint64(len(raw))
		}
	case 1:
		if c.invocation.Mode == pm.CLI {
			limit = 64 << 10
			accepted = c.stdoutBytes+c.stderrBytes+uint64(len(raw)) <= limit
		} else {
			accepted = c.stderrBytes+uint64(len(raw)) <= limit
		}
		if accepted {
			c.stderrBytes += uint64(len(raw))
		}
	case 2:
		limit = 256 << 10
		if c.invocation.Mode == pm.CLI {
			limit = 16 << 10
		}
		accepted = c.observerBytes+uint64(len(raw)) <= limit
		if accepted {
			c.observerBytes += uint64(len(raw))
		}
	default:
		accepted = false
	}
	c.mu.Unlock()
	if !accepted || !c.fixture.charge(len(raw)) {
		c.invalidate()
		return false
	}
	n, err := file.file.Write(raw)
	if err != nil || n != len(raw) {
		c.invalidate()
		return false
	}
	return true
}
func (c *p1Child) readStdout(decoder *pm.TranscriptDecoder) {
	defer close(c.stdoutDone)
	var bytes [pm.MaxFrameSize]byte
	for {
		n, err := io.ReadFull(c.parentOutput.file, bytes[:pm.FrameHeaderSize])
		if err != nil {
			if err == io.EOF && n == 0 {
				summary, ok := decoder.Finish()
				c.mu.Lock()
				c.stdoutEOF = true
				c.summary = summary
				c.mu.Unlock()
				if !ok {
					c.invalidate()
				}
			} else {
				c.invalidate()
			}
			return
		}
		length, ok := pm.FrameMessageLength(bytes[:pm.FrameHeaderSize])
		if !ok {
			c.invalidate()
			return
		}
		if _, err := io.ReadFull(c.parentOutput.file, bytes[pm.FrameHeaderSize:length]); err != nil {
			c.invalidate()
			return
		}
		frame, ok := decoder.Decode(bytes[:length])
		if !ok {
			c.invalidate()
			return
		}
		switch frame.Kind {
		case pm.BootFrame:
			c.mu.Lock()
			c.boot = true
			c.mu.Unlock()
		case pm.EventsFrame:
			var entry bool
			c.mu.Lock()
			for i := uint16(0); i < frame.Batch.Count; i++ {
				event := frame.Batch.Events[i]
				if c.eventCount >= uint64(len(c.events)) || event.Sequence != c.eventCount+1 {
					c.failed.Store(true)
					break
				}
				c.events[c.eventCount] = event
				c.eventCount++
				if event.Observation.Kind == pm.EntryReturned {
					c.entry = true
					entry = true
				}
			}
			c.mu.Unlock()
			encoded, ok := pm.EncodeEventBatch(c.invocation.Mode, frame.Batch.Events[:frame.Batch.Count])
			if !ok || !c.retainBytes(c.eventsFile, encoded.Bytes[:encoded.Length], 2) {
				c.invalidate()
			}
			// The authenticated EntryReturned has actually been decoded by this parent
			// before its retained writer receives permission to close inherited stdin.
			if entry {
				c.finishOnce.Do(func() { close(c.finishInput) })
			}
		case pm.StdoutFrame:
			_ = c.retainBytes(c.stdoutFile, frame.Stdout[:frame.StdoutLength], 0)
		case pm.CheckpointFrame:
			c.mu.Lock()
			c.checkpoint = frame.Checkpoint.Ordinal
			c.mu.Unlock()
		case pm.FailureFrame:
			c.invalidate()
		case pm.SealFrame:
			c.mu.Lock()
			c.sealed = true
			c.mu.Unlock()
		default:
			c.invalidate()
		}
		c.notify()
	}
}
func (c *p1Child) readStderr() {
	defer close(c.stderrDone)
	var raw [4096]byte
	for {
		n, err := c.parentError.file.Read(raw[:])
		if n > 0 {
			_ = c.retainBytes(c.stderrFile, raw[:n], 1)
		}
		if err != nil {
			if err == io.EOF {
				c.mu.Lock()
				c.stderrEOF = true
				c.mu.Unlock()
			} else {
				c.invalidate()
			}
			return
		}
		if n == 0 {
			c.invalidate()
			return
		}
	}
}
func (f *p1Fixture) waitBoot(c *p1Child, until time.Time) {
	for {
		c.mu.Lock()
		boot := c.boot
		c.mu.Unlock()
		f.require(!c.failed.Load(), "bootstrap_evidence")
		if boot {
			return
		}
		f.waitChanged(c, until, "bootstrap_timeout")
	}
}
func (f *p1Fixture) waitChanged(c *p1Child, until time.Time, stage string) {
	f.require(time.Now().Before(until), stage)
	timer := time.NewTimer(time.Until(until))
	defer timer.Stop()
	select {
	case <-c.changed:
	case <-timer.C:
		f.require(false, stage)
	}
}
func (c *p1Child) prefix() []pm.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]pm.Event{}, c.events[:c.eventCount]...)
}
func (c *p1Child) count(kind pm.Kind) uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	var n uint64
	for _, event := range c.events[:c.eventCount] {
		if event.Observation.Kind == kind {
			n++
		}
	}
	return n
}
func (f *p1Fixture) waitReadiness(c *p1Child, until time.Time) {
	for {
		f.healthy()
		events := c.prefix()
		counts := p1LifecycleCounts(events)
		ready := counts[pm.OwnerLockBound] == 1 && counts[pm.CoreBound] == 1 && counts[pm.WebOpened] == 1 && counts[pm.IPCReady] == 1
		if c.role == pm.RoleC2 {
			ready = ready && counts[pm.ManagedConstructorAttempted] == 1 && counts[pm.OrdinaryNodeBound] == 1
		}
		if ready {
			f.assertConstructors(c, events, c.role == pm.RoleC2)
			return
		}
		f.waitChanged(c, until, "owner_readiness_timeout")
	}
}
func p1LifecycleCounts(events []pm.Event) map[pm.Kind]uint64 {
	counts := map[pm.Kind]uint64{}
	for _, e := range events {
		counts[e.Observation.Kind]++
	}
	return counts
}
func (f *p1Fixture) checkpointGroup(group int) {
	f.require(group >= 0 && group <= 8 && f.checkpointCount == []int{0, 3, 5, 8, 11, 14, 17, 20, 23}[group], "checkpoint_group_order")
	var roles []pm.OwnerRole
	switch group {
	case 0:
		roles = []pm.OwnerRole{pm.RoleC0, pm.RoleA0, pm.RoleB0}
	case 1:
		roles = []pm.OwnerRole{pm.RoleC0, pm.RoleA0}
	case 2, 3, 4, 5:
		roles = []pm.OwnerRole{pm.RoleC1, pm.RoleA0, pm.RoleB0}
	default:
		roles = []pm.OwnerRole{pm.RoleC2, pm.RoleA0, pm.RoleB0}
	}
	for _, role := range roles {
		c := f.owner(role)
		phase := f.work
		if group <= 1 {
			phase = f.setup
		}
		until := f.admit(4*time.Second, phase)
		c.mu.Lock()
		ordinal := c.checkpoint + 1
		c.mu.Unlock()
		f.require(ordinal <= uint64(c.plan.Count), "checkpoint_count")
		message, ok := pm.EncodeCheckpointRequest(pm.CheckpointRequest{LaunchID: c.binding.LaunchID, Ordinal: ordinal, Nonce: c.plan.Nonces[ordinal-1]})
		f.require(ok, "checkpoint_encode")
		input := p1Input{length: message.Length, done: make(chan struct{})}
		copy(input.bytes[:], message.Bytes[:message.Length])
		c.requests <- input
		f.require(p1Wait(input.done, until), "checkpoint_write")
		for {
			f.healthy()
			c.mu.Lock()
			seen := c.checkpoint
			c.mu.Unlock()
			if seen == ordinal {
				break
			}
			f.waitChanged(c, until, "checkpoint_timeout")
		}
		f.checkpointCount++
		events := c.prefix()
		if group == 1 {
			f.assertConstructors(c, events, true)
		}
		if group == 2 {
			f.assertConstructors(c, events, true)
		}
		if group == 3 {
			f.baseline[int(role)-1] = p1CountLocalResource(events)
		}
		if group == 4 {
			f.require(f.baseline[int(role)-1] == p1CountLocalResource(events), "dry_run_owner_events")
		}
		if group == 5 {
			f.baseline[int(role)-1] = p1ResourceCount(events)
		}
		if group >= 6 {
			f.assertNoReplay(c)
		}
	}
	if group == 6 {
		f.maintenanceBefore = f.owner(pm.RoleC2).count(pm.MaintenancePassed)
	}
}
func p1ResourceCount(events []pm.Event) uint64 {
	var n uint64
	for _, e := range events {
		if e.Observation.Kind >= pm.GroupAccepted && e.Observation.Kind <= pm.ProviderAdmitted {
			n++
		}
	}
	return n
}
func p1CountLocalResource(events []pm.Event) uint64 {
	var n uint64
	for _, e := range events {
		if e.Observation.Kind >= pm.GroupAccepted && e.Observation.Kind <= pm.ProviderAdmitted || e.Observation.Kind >= pm.LocalCommand && e.Observation.Kind <= pm.IPCRequestDispatched {
			n++
		}
	}
	return n
}
func (f *p1Fixture) assertNoReplay(c *p1Child) {
	baseline := f.baseline[int(c.role)-1]
	if c.role == pm.RoleC2 {
		baseline = 0
	}
	f.require(p1ResourceCount(c.prefix()) == baseline, "management_replayed")
}
func (f *p1Fixture) observeMaintenance() {
	c := f.owner(pm.RoleC2)
	until := f.admit(8*time.Second, f.work)
	for {
		f.healthy()
		for _, role := range []pm.OwnerRole{pm.RoleC2, pm.RoleA0, pm.RoleB0} {
			f.assertNoReplay(f.owner(role))
		}
		n := c.count(pm.MaintenancePassed)
		if n >= f.maintenanceBefore+2 {
			f.maintenanceAfter = n
			return
		}
		f.waitChanged(c, until, "maintenance_timeout")
	}
}
func (f *p1Fixture) readOutput(c *p1Child, stderr bool, limit int) []byte {
	file := c.stdoutFile
	if stderr {
		file = c.stderrFile
	}
	relative, err := filepath.Rel(f.root, file.file.Name())
	f.require(err == nil, "output_relative")
	raw, err := p1ReadRelative(f.rootHandle.file, relative, limit)
	f.require(err == nil, "output_read")
	return raw
}
func (f *p1Fixture) checkCLIStream(c *p1Child, n int) {
	events := c.prefix()
	attempts, completed := 0, 0
	for _, e := range events {
		switch e.Observation.Kind {
		case pm.IPCConnectAttempt:
			attempts++
		case pm.IPCConnectCompleted:
			completed++
		case pm.EntryReturned:
		default:
			f.require(false, "cli_unexpected_observation")
		}
	}
	dry := n >= 17 && n <= 19
	if dry {
		f.require(attempts == 0 && completed == 0, "dry_run_dial")
	} else {
		f.require(attempts == 2 && completed == 2, "cli_negotiated_dials")
	}
	f.require(c.count(pm.EntryReturned) == 1 && c.count(pm.Barrier) == 0, "cli_finality")
	c.cliValidated = true
	c.mu.Lock()
	c.events = nil
	c.mu.Unlock()
}
