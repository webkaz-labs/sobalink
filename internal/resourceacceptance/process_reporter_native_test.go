//go:build resource_process_native

package resourceacceptance

import (
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/resourceacceptance/processmodel"
)

// Copied synthetic values do not claim these PIDs, paths or hashes exist.
func processTestBinding() processmodel.Bootstrap {
	epoch := uint64(time.Now().Unix() - 320)
	v := processmodel.Bootstrap{Scenario: processmodel.ScenarioProcessRecovery, Target: processmodel.LinuxAMD64,
		Invocation:    processmodel.Invocation{Mode: processmodel.CLI, Ordinal: 1, Operation: processmodel.CLIResourceList},
		SupervisorPID: 10, ChildPID: 11,
		RoleDirectoryIdentity:    processmodel.DirectoryIdentity{Kind: processmodel.UnixDirectoryIdentity},
		WorkingDirectoryIdentity: processmodel.DirectoryIdentity{Kind: processmodel.UnixDirectoryIdentity},
		Schedule: processmodel.ScheduleBinding{EpochSeconds: epoch, EntrySeconds: epoch + 320,
			SetupCutoffSeconds: epoch + 90, WorkCutoffSeconds: epoch + 300, CleanupCutoffSeconds: epoch + 330,
			OuterCutoffSeconds: epoch + 360, OriginalGrantExpirySeconds: epoch + 360}}
	v.RoleDirectory.Length, v.WorkingDirectory.Length = 2, 2
	copy(v.RoleDirectory.Bytes[:], "/r")
	copy(v.WorkingDirectory.Bytes[:], "/w")
	v.FixtureID[0], v.LaunchID[0] = 1, 2
	v.SourceCommit[0], v.SourceTree[0] = 3, 4
	v.SourceManifestSHA256[0], v.ArgvSHA256[0] = 5, 6
	v.ExecutableSHA256[0], v.AssetSHA256[0] = 7, 8
	v.Challenge[0], v.AuthenticationKey[0] = 9, 10
	v.RoleDirectoryIdentity.FileID[7], v.WorkingDirectoryIdentity.FileID[7] = 1, 2
	return v
}

func TestProcessReporterObserverFinalizationValues(t *testing.T) {
	state, _ := newProcessObservation(processmodel.CLI)
	state.recordBarrier(1)
	if !state.closeForEntry(1, time.Now().Add(time.Second)) {
		t.Fatal("CLI finalization failed")
	}
	prefix := state.snapshotThrough(2)
	if !prefix.Complete || !prefix.AdmissionClosed || prefix.Events[1].Observation.Outcome != processmodel.Failure || prefix.Events[1].Observation.Kind != processmodel.EntryReturned {
		t.Fatal("wrong final event")
	}
	if state.closeForEntry(1, time.Now().Add(time.Second)) || state.snapshotThrough(2).Failures == 0 {
		t.Fatal("duplicate finalization accepted")
	}
	if processObserver.Load() != nil {
		t.Fatal("observer installed")
	}
}

func TestProcessReporterOwnerClosureRequiredValues(t *testing.T) {
	state, _ := newProcessObservation(processmodel.Owner)
	if state.closeForEntry(0, time.Now().Add(time.Second)) || state.snapshotThrough(0).Failures == 0 {
		t.Fatal("unclosed owner finalized")
	}
	state, _ = newProcessObservation(processmodel.CLI)
	state.closeAdmission()
	if state.closeForEntry(0, time.Now().Add(time.Second)) {
		t.Fatal("old closeAdmission bypassed")
	}
}

func TestProcessReporterInvalidConstructorValues(t *testing.T) {
	binding := processTestBinding()
	if _, ok := newProcessReporter(binding, nil, nil, nil, nil); ok {
		t.Fatal("missing owners accepted")
	}
	if processObserver.Load() != nil {
		t.Fatal("global mutation")
	}
	var reporter *processReporter
	if reporter.start() || reporter.finish(0).success {
		t.Fatal("nil reporter accepted")
	}
}

func processTestFrame(t *testing.T, input *processPipe, decoder *processmodel.TranscriptDecoder) processmodel.DecodedFrame {
	t.Helper()
	header, outcome := input.readExact(processmodel.FrameHeaderSize, processBefore(input.cutoff, 2*time.Second))
	if outcome != processPipeOK {
		t.Fatal("frame header failed")
	}
	length, ok := processmodel.FrameMessageLength(header.bytes[:header.length])
	if !ok {
		t.Fatal("invalid frame header")
	}
	body, outcome := input.readExact(length-processmodel.FrameHeaderSize, processBefore(input.cutoff, 2*time.Second))
	if outcome != processPipeOK {
		t.Fatal("frame body failed")
	}
	var message [processmodel.MaxFrameSize]byte
	copy(message[:], header.bytes[:header.length])
	copy(message[processmodel.FrameHeaderSize:], body.bytes[:body.length])
	frame, ok := decoder.Decode(message[:length])
	if !ok {
		t.Fatal("transcript rejected")
	}
	return frame
}

func TestProcessReporterNativeCheckpointAndSeal(t *testing.T) {
	binding := processTestBinding()
	cutoff := time.Unix(int64(binding.Schedule.CleanupCutoffSeconds), 0)
	input, parentInput := processTestPipePair(t, processmodel.CLI, cutoff)
	parentOutput, output := processTestPipePair(t, processmodel.CLI, cutoff)
	state, _ := newScopedProcessObservation(processmodel.CLI, "/r")
	stdout, _ := newProcessOutput(state)
	if _, err := stdout.Write([]byte("synthetic")); err != nil {
		t.Fatal("stdout copy")
	}
	reporter, ok := newProcessReporter(binding, state, input, output, stdout)
	if !ok {
		t.Fatal("reporter construction")
	}
	plan := processmodel.CheckpointPlan{Count: 1}
	plan.Nonces[0][0] = 20
	decoder, ok := processmodel.NewTranscriptDecoder(binding, plan)
	if !ok || !reporter.start() {
		t.Fatal("start")
	}
	var finishDone chan struct{}
	t.Cleanup(func() {
		// On an early assertion failure, close only owned endpoints and join
		// both tasks before considering cleanup complete.
		deadline := processBefore(cutoff, 2*time.Second)
		_ = parentInput.closeAndJoin(deadline)
		_ = parentOutput.closeAndJoin(deadline)
		_ = input.closeAndJoin(deadline)
		_ = output.closeAndJoin(deadline)
		if !processWait(reporter.inputDone, deadline) || !processWait(reporter.done, deadline) {
			t.Error("reporter cleanup unjoined")
		}
		if finishDone != nil && !processWait(finishDone, deadline) {
			t.Error("finish caller unjoined")
		}
	})
	if frame := processTestFrame(t, parentOutput, decoder); frame.Kind != processmodel.BootFrame {
		t.Fatal("Boot not first")
	}
	request, ok := processmodel.EncodeCheckpointRequest(processmodel.CheckpointRequest{LaunchID: binding.LaunchID, Ordinal: 1, Nonce: plan.Nonces[0]})
	if !ok {
		t.Fatal("request encoding")
	}
	block := processPipeBlock{length: request.Length}
	copy(block.bytes[:], request.Bytes[:request.Length])
	if parentInput.writeExact(block, processBefore(cutoff, 2*time.Second)) != processPipeOK {
		t.Fatal("checkpoint write")
	}
	checkpoint := false
	for frames := 0; frames < processmodel.MaxCLIFrames; frames++ {
		frame := processTestFrame(t, parentOutput, decoder)
		if frame.Kind == processmodel.CheckpointFrame {
			checkpoint = true
			break
		}
	}
	if !checkpoint {
		t.Fatal("checkpoint missing")
	}
	done := make(chan struct{})
	finishDone = done
	var result processReporterResult
	go func() { defer close(done); result = reporter.finish(0) }()
	entry, seal := false, false
	for frames := 0; frames < processmodel.MaxCLIFrames; frames++ {
		frame := processTestFrame(t, parentOutput, decoder)
		if frame.Kind == processmodel.EventsFrame && frame.Batch.Events[frame.Batch.Count-1].Observation.Kind == processmodel.EntryReturned {
			entry = true
			if join := parentInput.closeAndJoin(processBefore(cutoff, 2*time.Second)); join.outcome != processPipeOK {
				t.Fatal("parent EOF close")
			}
		}
		if frame.Kind == processmodel.SealFrame {
			seal = true
			break
		}
	}
	if !entry || !seal {
		t.Fatal("final sequence missing")
	}
	if _, outcome := parentOutput.readExact(processmodel.FrameHeaderSize, processBefore(cutoff, 2*time.Second)); outcome != processPipeEOF {
		t.Fatal("raw EOF missing")
	}
	if !processWait(done, cutoff) {
		t.Fatal("finish caller unjoined")
	}
	if !result.success || !result.sealed || !result.inputJoined || !result.coordinatorJoined || result.failures != 0 {
		t.Fatal("reporter failed or unjoined")
	}
	if summary, ok := decoder.Finish(); !ok || summary.Counts.CheckpointCount != 1 || summary.Counts.EventCount != 2 || summary.Counts.StdoutByteCount != 9 {
		t.Fatal("structural final counts")
	}
	if reporter.start() || reporter.finish(0).success {
		t.Fatal("reporter reused")
	}
}

func TestProcessReporterNativePrematureEOFIsFailure(t *testing.T) {
	binding := processTestBinding()
	cutoff := time.Unix(int64(binding.Schedule.CleanupCutoffSeconds), 0)
	input, parentInput := processTestPipePair(t, processmodel.CLI, cutoff)
	parentOutput, output := processTestPipePair(t, processmodel.CLI, cutoff)
	state, _ := newScopedProcessObservation(processmodel.CLI, "/r")
	stdout, _ := newProcessOutput(state)
	reporter, ok := newProcessReporter(binding, state, input, output, stdout)
	decoder, decodeOK := processmodel.NewTranscriptDecoder(binding, processmodel.CheckpointPlan{})
	if !ok || !decodeOK || !reporter.start() {
		t.Fatal("construct/start")
	}
	t.Cleanup(func() {
		deadline := processBefore(cutoff, 2*time.Second)
		_ = input.closeAndJoin(deadline)
		_ = output.closeAndJoin(deadline)
		if !processWait(reporter.inputDone, deadline) || !processWait(reporter.done, deadline) {
			t.Error("failed reporter unjoined")
		}
	})
	if frame := processTestFrame(t, parentOutput, decoder); frame.Kind != processmodel.BootFrame {
		t.Fatal("Boot missing")
	}
	if join := parentInput.closeAndJoin(cutoff); join.outcome != processPipeOK {
		t.Fatal("parent close")
	}
	if frame := processTestFrame(t, parentOutput, decoder); frame.Kind != processmodel.FailureFrame {
		t.Fatal("failure evidence missing")
	}
	if !processWait(reporter.done, cutoff) {
		t.Fatal("coordinator unjoined")
	}
	result := reporter.finish(0)
	if result.success || result.sealed || !result.inputJoined || !result.coordinatorJoined || result.failures == 0 {
		t.Fatal("premature EOF accepted")
	}
}

func TestProcessReporterNativeRejectsAliasingAndRebasedCutoff(t *testing.T) {
	binding := processTestBinding()
	cutoff := time.Unix(int64(binding.Schedule.CleanupCutoffSeconds), 0)
	input, output := processTestPipePair(t, processmodel.CLI, cutoff)
	state, _ := newScopedProcessObservation(processmodel.CLI, "/r")
	stdout, _ := newProcessOutput(state)
	if _, ok := newProcessReporter(binding, state, input, input, stdout); ok {
		t.Fatal("same endpoint accepted")
	}
	copyOutput := &processPipe{file: input.file, direction: processPipeWrite, mode: processmodel.CLI, cutoff: cutoff}
	if _, ok := newProcessReporter(binding, state, input, copyOutput, stdout); ok {
		t.Fatal("same endpoint object accepted")
	}
	binding.Schedule.CleanupCutoffSeconds++
	if _, ok := newProcessReporter(binding, state, input, output, stdout); ok {
		t.Fatal("rebased schedule accepted")
	}
	if processObserver.Load() != nil || input.active != nil || output.active != nil {
		t.Fatal("constructor activated state")
	}
}

func TestProcessReporterEOFPhaseValues(t *testing.T) {
	early := &processReporter{}
	early.publishEOF()
	if early.beginEntryEmission() || early.finalEOFObserved() {
		t.Fatal("early EOF accepted")
	}
	inFlight := &processReporter{}
	if !inFlight.beginEntryEmission() {
		t.Fatal("entry phase not started")
	}
	// This models the legal publication ordering while the coordinator's
	// complete EntryReturned write is still in flight. It is not parent-read
	// provenance; the native transcript test separately closes after reading.
	inFlight.publishEOF()
	if !inFlight.finalEOFObserved() || inFlight.beginEntryEmission() {
		t.Fatal("in-flight EOF phase rejected or reset")
	}
	inFlight.publishEOF()
	if inFlight.finalEOFObserved() {
		t.Fatal("duplicate EOF publication accepted")
	}
}

func TestProcessReporterNativeEOFBeforeFinalizeRejected(t *testing.T) {
	binding := processTestBinding()
	cutoff := time.Unix(int64(binding.Schedule.CleanupCutoffSeconds), 0)
	input, parentInput := processTestPipePair(t, processmodel.CLI, cutoff)
	_, output := processTestPipePair(t, processmodel.CLI, cutoff)
	state, _ := newScopedProcessObservation(processmodel.CLI, "/r")
	stdout, _ := newProcessOutput(state)
	reporter, ok := newProcessReporter(binding, state, input, output, stdout)
	if !ok {
		t.Fatal("reporter construction")
	}
	// Invoke the concrete input task and coordinator finalization separately
	// to force the formerly racy ordering deterministically, without a seam.
	go reporter.readInput()
	t.Cleanup(func() {
		deadline := processBefore(cutoff, 2*time.Second)
		_ = input.closeAndJoin(deadline)
		if !processWait(reporter.inputDone, deadline) {
			t.Error("input caller unjoined")
		}
	})
	if join := parentInput.closeAndJoin(cutoff); join.outcome != processPipeOK {
		t.Fatal("parent EOF close")
	}
	if !processWait(reporter.inputDone, cutoff) || reporter.inputOutcome != processPipeEOF {
		t.Fatal("EOF not published")
	}
	if !reporter.send(processmodel.EncodeBootFrame(binding)) {
		t.Fatal("Boot emission")
	}
	if reporter.finalizeEntry(0) || reporter.sealed || reporter.counts.FrameCount != 1 {
		t.Fatal("pre-published EOF finalized successfully")
	}
}

func TestProcessReporterFinalizationDeadlineValues(t *testing.T) {
	entered := time.Now()
	global := entered.Add(10 * time.Second)
	if got := processFinalizationDeadline(global, entered); !got.Equal(entered.Add(2 * time.Second)) {
		t.Fatal("finalization budget extended")
	}
	for _, cutoff := range []time.Time{entered.Add(time.Second), entered.Add(-time.Second)} {
		if got := processFinalizationDeadline(cutoff, entered); !got.Equal(cutoff) {
			t.Fatal("original cutoff replenished")
		}
	}
	r := &processReporter{cutoff: global}
	request := &processFinalization{deadline: processFinalizationDeadline(global, entered)}
	if !r.finalization.CompareAndSwap(nil, request) || !r.effectiveCutoff().Equal(request.deadline) || !r.cutoff.Equal(global) {
		t.Fatal("publication did not narrow before notification")
	}
	if r.finalization.CompareAndSwap(nil, &processFinalization{deadline: global}) {
		t.Fatal("deadline replaced")
	}
}

func TestProcessReporterLateFinalizationCannotRecoverValues(t *testing.T) {
	for _, alreadyDone := range []bool{false, true} {
		r := &processReporter{cutoff: time.Now().Add(-time.Second), finalize: make(chan struct{}, 1), done: make(chan struct{}), inputDone: make(chan struct{})}
		r.started.Store(true)
		// Deliberately successful-looking private values are not actual joins.
		// Even observing a closed completion after the deadline cannot certify it.
		r.result = processReporterResult{success: true, sealed: true}
		close(r.inputDone)
		if alreadyDone {
			close(r.done)
		}
		result := r.finish(0)
		if result.success || result.failures == 0 || !r.finishTimedOut.Load() {
			t.Fatal("late completion rescued finish")
		}
		if !alreadyDone {
			close(r.done)
		}
		if result := r.finish(0); result.success || result.failures == 0 || !r.finishTimedOut.Load() {
			t.Fatal("timeout latch reset")
		}
	}
}

func TestProcessReporterPreparedFinalizationValues(t *testing.T) {
	original := time.Now().Add(10 * time.Second)
	r := &processReporter{cutoff: original, finalize: make(chan struct{}, 1)}
	r.started.Store(true)
	before := time.Now()
	deadline, ok := r.prepareFinalization(0)
	after := time.Now()
	if !ok || deadline.Before(before.Add(2*time.Second)) || deadline.After(after.Add(2*time.Second)) || !r.cutoff.Equal(original) || !r.effectiveCutoff().Equal(deadline) || len(r.finalize) != 0 {
		t.Fatal("preparation did not publish one deadline before notification")
	}
	retained := r.finalization.Load()
	deadline = deadline.Add(time.Hour)
	if !r.effectiveCutoff().Equal(retained.deadline) || r.effectiveCutoff().Equal(deadline) {
		t.Fatal("returned value mutated the retained deadline")
	}
	if _, ok := r.prepareFinalization(0); ok || !r.finalizationInvalid.Load() || r.finalization.Load() != retained || len(r.finalize) != 0 {
		t.Fatal("duplicate preparation was not terminal")
	}
}

func TestProcessReporterPreparedFinalizationNoRenewalValues(t *testing.T) {
	for _, exitCode := range []int{0, 1, 72} {
		past := time.Now().Add(-time.Second)
		r := &processReporter{cutoff: past, finalize: make(chan struct{}, 1), done: make(chan struct{}), inputDone: make(chan struct{})}
		r.started.Store(true)
		deadline, ok := r.prepareFinalization(0)
		if !ok || !deadline.Equal(past) {
			t.Fatal("original exhausted budget changed")
		}
		retained := r.finalization.Load()
		// Even successful-looking completions already published by private test
		// values cannot recover time used by the entry's preceding producer joins.
		r.result = processReporterResult{success: true, sealed: true}
		close(r.done)
		close(r.inputDone)
		result := r.finish(exitCode)
		if result.success || result.failures == 0 || !r.finishTimedOut.Load() || r.finalization.Load() != retained || !r.effectiveCutoff().Equal(past) || len(r.finalize) != 1 {
			t.Fatal("prepared finalization renewed time or recovered late success")
		}
		if exitCode != 0 && !r.finalizationInvalid.Load() {
			t.Fatal("changed exit code did not latch invalid evidence")
		}
		if result := r.finish(exitCode); result.success || result.failures == 0 || !r.finalizationInvalid.Load() || len(r.finalize) != 1 {
			t.Fatal("duplicate finish re-notified or recovered")
		}
	}
}

func TestProcessReporterPreparedFinalizationInvalidTransitionsValues(t *testing.T) {
	for _, alreadyClaimed := range []bool{false, true} {
		r := &processReporter{cutoff: time.Now().Add(-time.Second), finalize: make(chan struct{}, 1), done: make(chan struct{}), inputDone: make(chan struct{})}
		r.started.Store(true)
		if alreadyClaimed {
			r.finishClaimed.Store(true)
		}
		if _, ok := r.prepareFinalization(72); ok || !r.finalizationInvalid.Load() || len(r.finalize) != 0 {
			t.Fatal("invalid preparation admitted or notified")
		}
		if !alreadyClaimed {
			// Invalid actual code still reaches the one bounded failure-cleanup
			// notification; it does not strand input through an early return.
			if result := r.finish(72); result.success || result.failures == 0 || len(r.finalize) != 1 || !r.finishTimedOut.Load() {
				t.Fatal("invalid actual code bypassed failure cleanup")
			}
		}
	}
}
