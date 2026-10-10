//go:build resource_process_native

package resourceacceptance

import (
	"sync/atomic"
	"time"

	"github.com/webkaz-labs/sobalink/internal/resourceacceptance/processmodel"
)

const (
	processEntryEmissionStarted = uint32(1)
	processEOFObserved          = uint32(2)
)

type processFinalization struct {
	actualCode int
	deadline   time.Time
}

func processFinalizationDeadline(cutoff, entered time.Time) time.Time {
	deadline := entered.Add(2 * time.Second)
	if cutoff.Before(deadline) {
		return cutoff
	}
	return deadline
}

type processReporterResult struct {
	success, sealed                bool
	inputJoined, coordinatorJoined bool
	inputPipe, outputPipe          processPipeJoin
	failures                       processmodel.FailureFlags
	counts                         processmodel.TranscriptCounts
}

// All transcript state below belongs to the coordinator. The input task only
// publishes copied requests and its completion; product hooks never wait here.
// This private value cannot install an observer or certify actual entry binding.
type processReporter struct {
	binding             processmodel.Bootstrap
	state               *processObservation
	input, output       *processPipe
	stdout              *processOutput
	decoder             *processmodel.CheckpointRequestDecoder
	cutoff              time.Time
	started             atomic.Bool
	finalization        atomic.Pointer[processFinalization]
	finishTimedOut      atomic.Bool
	finishClaimed       atomic.Bool
	finalizationInvalid atomic.Bool
	requests            chan processmodel.CheckpointRequest
	finalize            chan struct{}
	inputDone, done     chan struct{}
	decoded             atomic.Uint64
	inputPhase          atomic.Uint32
	eofPriorPhase       uint32
	inputOutcome        processPipeOutcome
	result              processReporterResult
	counts              processmodel.TranscriptCounts
	digest              [32]byte
	sent                uint64
	gapSequence         uint64
	gapDeadline         time.Time
	bootSent, sealed    bool
}

func newProcessReporter(binding processmodel.Bootstrap, state *processObservation, input, output *processPipe, stdout *processOutput) (*processReporter, bool) {
	decoder, ok := processmodel.NewCheckpointRequestDecoder(binding)
	if !ok || state == nil || state.recorder == nil || input == nil || output == nil || input == output ||
		input.file == nil || output.file == nil || input.file == output.file || stdout == nil || stdout.state != state ||
		state.mode != binding.Invocation.Mode || state.directory != string(binding.RoleDirectory.Bytes[:binding.RoleDirectory.Length]) ||
		input.direction != processPipeRead || output.direction != processPipeWrite || input.mode != state.mode || output.mode != state.mode {
		return nil, false
	}
	cutoff := time.Unix(int64(binding.Schedule.CleanupCutoffSeconds), 0)
	if !input.cutoff.Equal(cutoff) || !output.cutoff.Equal(cutoff) || !time.Now().Before(cutoff) {
		return nil, false
	}
	// Preserve the once-derived monotonic component of the input cutoff. The
	// wall schedule equality above rejects a freshly replenished budget.
	return &processReporter{binding: binding, state: state, input: input, output: output, stdout: stdout,
		decoder: decoder, cutoff: input.cutoff, requests: make(chan processmodel.CheckpointRequest, 1),
		finalize: make(chan struct{}, 1), inputDone: make(chan struct{}), done: make(chan struct{}),
		digest: processmodel.InitialTranscriptDigest()}, true
}

func (r *processReporter) start() bool {
	if r == nil || !r.started.CompareAndSwap(false, true) {
		return false
	}
	// Both completion records already exist before either task starts.
	go r.readInput()
	go r.coordinate()
	return true
}

// Publication narrows every new operation, including work already running in
// the coordinator before it consumes the notification. The original cutoff is
// immutable and the one private request is never replaced or replenished.
func (r *processReporter) effectiveCutoff() time.Time {
	request := r.finalization.Load()
	if request != nil && request.deadline.Before(r.cutoff) {
		return request.deadline
	}
	return r.cutoff
}

// Invalid preparation/finish transitions stay terminal even if a caller later
// supplies matching values. This is private evidence, never an admission setter.
func (r *processReporter) invalidateFinalization() {
	if r == nil {
		return
	}
	r.finalizationInvalid.Store(true)
	if r.state != nil {
		r.state.invalidate()
	}
}

// This helper publishes an internally captured immutable value. A competing
// publication fails; it never adopts that competition as successful preparation.
func (r *processReporter) publishFinalization(exitCode int) (*processFinalization, bool) {
	entered := time.Now()
	request := &processFinalization{actualCode: exitCode, deadline: processFinalizationDeadline(r.cutoff, entered)}
	if !r.finalization.CompareAndSwap(nil, request) {
		r.invalidateFinalization()
		return r.finalization.Load(), false
	}
	if exitCode != 0 && exitCode != 1 {
		r.invalidateFinalization()
		return request, false
	}
	return request, true
}

// Entry uses only the returned deadline value to join its owned producers. The
// coordinator cannot emit EntryReturned until finish sends its sole notice.
func (r *processReporter) prepareFinalization(exitCode int) (time.Time, bool) {
	if r == nil || !r.started.Load() {
		r.invalidateFinalization()
		return time.Time{}, false
	}
	if r.finishClaimed.Load() {
		r.invalidateFinalization()
		return r.effectiveCutoff(), false
	}
	request, ok := r.publishFinalization(exitCode)
	// Concurrent prepare/finish is not an alternative actual-entry call graph.
	if r.finishClaimed.Load() {
		r.invalidateFinalization()
		ok = false
	}
	return request.deadline, ok && !r.finalizationInvalid.Load()
}

func (r *processReporter) finish(exitCode int) processReporterResult {
	if r == nil || !r.started.Load() {
		r.invalidateFinalization()
		return processReporterResult{failures: processmodel.Invalid}
	}
	if !r.finishClaimed.CompareAndSwap(false, true) {
		r.invalidateFinalization()
		return processReporterResult{failures: processmodel.Invalid}
	}
	request := r.finalization.Load()
	if request == nil {
		request, _ = r.publishFinalization(exitCode)
	}
	if request.actualCode != exitCode || exitCode != 0 && exitCode != 1 {
		r.invalidateFinalization()
	}
	// Even invalid values notify bounded failure cleanup exactly once. Entry's
	// producer joins consumed this same retained budget; no new grace is added.
	r.finalize <- struct{}{}
	if !processWait(r.done, request.deadline) {
		r.finishTimedOut.Store(true)
		input := r.input.closeAndJoin(request.deadline)
		output := r.output.closeAndJoin(request.deadline)
		return processReporterResult{failures: processmodel.PrefixGap | processmodel.Invalid, inputPipe: input, outputPipe: output,
			inputJoined: processWait(r.inputDone, request.deadline), coordinatorJoined: processWait(r.done, request.deadline)}
	}
	result := r.result
	result.coordinatorJoined = true
	prefix := r.state.snapshotThrough(r.state.snapshotThrough(0).Reserved)
	stdout := r.stdout.snapshot()
	active := r.state.gate.Load() & processActiveMask
	if !time.Now().Before(request.deadline) {
		r.finishTimedOut.Store(true)
	}
	if r.finishTimedOut.Load() || r.finalizationInvalid.Load() || prefix.Failures != 0 || prefix.Active != 0 || !prefix.Complete || !prefix.AdmissionClosed ||
		active != 0 || stdout.failed {
		result.success = false
		result.failures |= prefix.Failures | processmodel.Invalid
	}
	return result
}

func (r *processReporter) readInput() {
	defer close(r.inputDone)
	for {
		header, outcome := r.input.readExact(processmodel.InputHeaderSize, r.effectiveCutoff())
		if outcome != processPipeOK {
			if outcome == processPipeEOF {
				r.publishEOF()
			}
			r.inputOutcome = outcome
			return
		}
		length, ok := processmodel.CheckpointMessageLength(header.bytes[:header.length])
		if !ok || length < processmodel.InputHeaderSize || length > processmodel.MaxCheckpointRequestSize {
			r.inputOutcome = processPipeInvalid
			return
		}
		body, outcome := r.input.readExact(length-processmodel.InputHeaderSize, processBefore(r.effectiveCutoff(), 2*time.Second))
		if outcome != processPipeOK {
			r.inputOutcome = processPipeInvalid
			return
		}
		var message [processmodel.MaxCheckpointRequestSize]byte
		copy(message[:], header.bytes[:header.length])
		copy(message[processmodel.InputHeaderSize:], body.bytes[:body.length])
		request, ok := r.decoder.Decode(message[:length])
		if !ok {
			r.inputOutcome = processPipeInvalid
			return
		}
		r.decoded.Add(1)
		select {
		case r.requests <- request:
		default:
			r.inputOutcome = processPipeInvalid
			return
		}
	}
}

// Reader publication records only an observed ordering. It does not decide
// finality or prove that the parent causally read an EntryReturned frame.
func (r *processReporter) publishEOF() {
	r.eofPriorPhase = r.inputPhase.Or(processEOFObserved)
}

func (r *processReporter) beginEntryEmission() bool {
	return r.inputPhase.CompareAndSwap(0, processEntryEmissionStarted)
}

// Read only after the actual input task joins. There is no phase reset.
func (r *processReporter) finalEOFObserved() bool {
	return r.eofPriorPhase == processEntryEmissionStarted &&
		r.inputPhase.Load() == processEntryEmissionStarted|processEOFObserved
}

func (r *processReporter) coordinate() {
	// The coordinator does not wait on itself. Its caller observes done after
	// this retained result and every possible post-Seal failure are published.
	defer close(r.done)
	ok := r.run()
	if !ok && r.bootSent && !r.sealed {
		prefix := r.state.snapshotThrough(0)
		failure := processmodel.FailureReport{Flags: prefix.Failures | processmodel.Invalid, Reserved: prefix.Reserved, PrefixCount: r.sent}
		// At most one best-effort failure frame; an unusable output rejects the
		// attempt without retry and cannot turn the result back into success.
		_ = r.send(processmodel.EncodeFailureFrame(r.state.mode, r.binding.LaunchID, r.binding.AuthenticationKey, r.counts.FrameCount+1, failure))
	}
	r.stdout.close()
	deadline := processBefore(r.effectiveCutoff(), 2*time.Second)
	input := r.input.closeAndJoin(deadline)
	output := r.output.closeAndJoin(deadline)
	inputJoined := processWait(r.inputDone, deadline)
	prefix := r.state.snapshotThrough(r.state.snapshotThrough(0).Reserved)
	failures := prefix.Failures
	stdout := r.stdout.snapshot()
	active := r.state.gate.Load() & processActiveMask
	if !ok || r.finishTimedOut.Load() || r.finalizationInvalid.Load() || !time.Now().Before(r.effectiveCutoff()) || !inputJoined || input.outcome != processPipeOK || output.outcome != processPipeOK ||
		!input.ioJoined || !input.closeJoined || !output.ioJoined || !output.closeJoined || stdout.failed ||
		!prefix.Complete || prefix.Active != 0 || !prefix.AdmissionClosed || active != 0 {
		failures |= processmodel.Invalid
	}
	r.result = processReporterResult{success: ok && failures == 0, sealed: r.sealed, inputJoined: inputJoined,
		inputPipe: input, outputPipe: output, failures: failures, counts: r.counts}
}

func (r *processReporter) run() bool {
	if !r.send(processmodel.EncodeBootFrame(r.binding)) {
		return false
	}
	r.bootSent = true
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	deadline := time.NewTimer(time.Until(r.effectiveCutoff()))
	defer deadline.Stop()
	for {
		select {
		case request := <-r.requests:
			if !r.checkpoint(request) {
				return false
			}
		case <-r.finalize:
			finalization := r.finalization.Load()
			if finalization == nil {
				return false
			}
			// Complete a request already admitted to the single pending slot.
			// A request decoded during or after finalization is rejected below.
			select {
			case request := <-r.requests:
				if !r.checkpoint(request) {
					return false
				}
			default:
			}
			return r.finalizeEntry(finalization.actualCode)
		case <-r.inputDone:
			// EOF in Running, even with no pending checkpoint, is premature.
			return false
		case <-tick.C:
			prefix, ok := r.scan()
			if !ok || !r.emitEvents(prefix, uint64(prefix.Count)) || !r.drainOutput(false) {
				return false
			}
		case <-deadline.C:
			return false
		}
	}
}

func (r *processReporter) scan() (processmodel.Prefix, bool) {
	prefix := r.state.snapshotThrough(r.state.snapshotThrough(0).Reserved)
	if prefix.Failures != 0 || uint64(prefix.Count) < r.sent || !time.Now().Before(r.effectiveCutoff()) {
		return prefix, false
	}
	if uint64(prefix.Count) < prefix.Reserved {
		missing := uint64(prefix.Count) + 1
		if r.gapSequence != missing {
			r.gapSequence = missing
			r.gapDeadline = processBefore(r.effectiveCutoff(), time.Second)
		}
		if !time.Now().Before(r.gapDeadline) {
			r.state.recorder.FailGap()
			return prefix, false
		}
	} else {
		r.gapSequence = 0
		r.gapDeadline = time.Time{}
	}
	return prefix, true
}

func (r *processReporter) checkpoint(request processmodel.CheckpointRequest) bool {
	if request.Ordinal != r.counts.CheckpointCount+1 {
		return false
	}
	r.state.recordBarrier(request.Ordinal)
	for {
		prefix, ok := r.scan()
		if !ok {
			return false
		}
		var barrier uint64
		// Record returns no ticket. Concurrent hook reservations can precede
		// or follow it, so find the actual immutable barrier slot, never guess.
		for i := uint16(0); i < prefix.Count; i++ {
			event := prefix.Events[i]
			if event.Observation.Kind == processmodel.Barrier && event.Observation.BarrierOrdinal == request.Ordinal {
				if barrier != 0 {
					return false
				}
				barrier = event.Sequence
			}
		}
		if barrier != 0 {
			if barrier <= r.sent || !r.emitEvents(prefix, barrier) {
				return false
			}
			nonce, ok := processmodel.CheckpointNonceDigest(request)
			if !ok {
				return false
			}
			report := processmodel.CheckpointReport{Ordinal: request.Ordinal, NonceDigest: nonce, BarrierSequence: barrier,
				PrefixCount: r.sent, ResourceCount: r.counts.ResourceCount, LocalCount: r.counts.LocalCount, LifecycleCount: r.counts.LifecycleCount}
			if !r.send(processmodel.EncodeCheckpointFrame(r.state.mode, r.binding.LaunchID, r.binding.AuthenticationKey, r.counts.FrameCount+1, report)) {
				return false
			}
			r.counts.CheckpointCount++
			return true
		}
		// This wait does not consume another request or renew the missing-slot
		// budget. Input queue exhaustion remains a bounded sticky failure.
		wait := processBefore(r.effectiveCutoff(), 20*time.Millisecond)
		if r.gapSequence != 0 && r.gapDeadline.Before(wait) {
			wait = r.gapDeadline
		}
		timer := time.NewTimer(time.Until(wait))
		select {
		case <-timer.C:
		case <-r.inputDone:
			timer.Stop()
			return false
		}
	}
}

func (r *processReporter) emitEvents(prefix processmodel.Prefix, through uint64) bool {
	if through > uint64(prefix.Count) || through < r.sent {
		return false
	}
	for r.sent < through {
		end := r.sent + processmodel.MaxBatchEvents
		if end > through {
			end = through
		}
		// A barrier or EntryReturned can only be last in a batch, with the
		// caller's exact boundary preventing unrelated stdout interleaving.
		for i := r.sent; i < end; i++ {
			kind := prefix.Events[i].Observation.Kind
			if (kind == processmodel.Barrier || kind == processmodel.EntryReturned) && i+1 != through {
				return false
			}
		}
		if prefix.Events[end-1].Observation.Kind == processmodel.EntryReturned && !r.beginEntryEmission() {
			return false
		}
		if !r.send(processmodel.EncodeEventFrame(r.state.mode, r.binding.LaunchID, r.binding.AuthenticationKey, r.counts.FrameCount+1, prefix.Events[r.sent:end])) {
			return false
		}
		for i := r.sent; i < end; i++ {
			kind := prefix.Events[i].Observation.Kind
			switch {
			case kind >= processmodel.GroupAccepted && kind <= processmodel.ProviderAdmitted:
				r.counts.ResourceCount++
			case kind >= processmodel.LocalCommand && kind <= processmodel.IPCRequestDispatched:
				r.counts.LocalCount++
			case kind >= processmodel.MaintenancePassed && kind <= processmodel.OwnersClosed:
				r.counts.LifecycleCount++
			default:
				return false
			}
		}
		r.sent = end
		r.counts.EventCount = end
	}
	return true
}

func (r *processReporter) drainOutput(all bool) bool {
	for {
		block, snapshot := r.stdout.drain()
		if snapshot.failed {
			return false
		}
		if block.length == 0 {
			return true
		}
		if !r.send(processmodel.EncodeStdoutFrame(r.state.mode, r.binding.LaunchID, r.binding.AuthenticationKey, r.counts.FrameCount+1, block.bytes[:block.length])) {
			return false
		}
		if !all {
			return true
		}
	}
}

func (r *processReporter) finalizeEntry(code int) bool {
	// EOF/error already published before any EntryReturned emission is early,
	// even when finalize and inputDone were simultaneously ready in run.
	select {
	case <-r.inputDone:
		return false
	default:
	}
	if !r.state.closeForEntry(code, r.effectiveCutoff()) {
		return false
	}
	prefix, ok := r.scan()
	if !ok || !prefix.Complete || !r.emitEvents(prefix, uint64(prefix.Count)) {
		return false
	}
	// Only after the EntryReturned frame completes may parent EOF be accepted.
	deadline := time.NewTimer(time.Until(r.effectiveCutoff()))
	defer deadline.Stop()
	select {
	case <-r.requests:
		return false
	case <-r.inputDone:
	case <-deadline.C:
		return false
	}
	if r.inputOutcome != processPipeEOF || !r.finalEOFObserved() || r.decoded.Load() != r.counts.CheckpointCount {
		return false
	}
	select {
	case <-r.requests:
		return false
	default:
	}
	if r.stdout.close().failed || !r.drainOutput(true) {
		return false
	}
	prefix = r.state.snapshotThrough(r.state.snapshotThrough(0).Reserved)
	stdout := r.stdout.snapshot()
	if !prefix.Complete || prefix.Active != 0 || !prefix.AdmissionClosed || prefix.Failures != 0 ||
		prefix.Reserved != r.sent || uint64(prefix.Count) != r.sent || stdout.failed || stdout.accepted != stdout.drained ||
		uint64(stdout.accepted) != r.counts.StdoutByteCount || r.state.gate.Load()&processActiveMask != 0 {
		return false
	}
	outcome := processmodel.Success
	if code == 1 {
		outcome = processmodel.Failure
	}
	report := processmodel.SealReport{EntryEventSequence: r.sent, EventCount: r.sent, ReservedCount: prefix.Reserved,
		ResourceCount: r.counts.ResourceCount, LocalCount: r.counts.LocalCount, LifecycleCount: r.counts.LifecycleCount,
		CheckpointCount: r.counts.CheckpointCount, StdoutByteCount: r.counts.StdoutByteCount, StdoutChunkCount: r.counts.StdoutChunkCount,
		FrameCount: r.counts.FrameCount + 1, ProductExitCode: uint32(code), EntryOutcome: outcome, TranscriptDigest: r.digest}
	if !r.send(processmodel.EncodeSealFrame(r.state.mode, r.binding.LaunchID, r.binding.AuthenticationKey, r.counts.FrameCount+1, report)) {
		return false
	}
	r.sealed = true
	return true
}

func (r *processReporter) send(frame processmodel.EncodedFrame, valid bool) bool {
	if !valid || frame.Length < processmodel.FrameHeaderSize+processmodel.FrameMACSize || int(frame.Length) > len(frame.Bytes) {
		return false
	}
	kind := processmodel.FrameKind(frame.Bytes[5])
	maxBytes, maxChunks := uint64(processmodel.MaxOwnerStdoutBytes), uint64(processmodel.MaxOwnerStdoutFrames)
	maxObserver, maxFrames := uint64(processmodel.MaxOwnerObserverBytes), uint64(processmodel.MaxOwnerFrames)
	if r.state.mode == processmodel.CLI {
		maxBytes, maxChunks = processmodel.MaxCLIStdoutBytes, processmodel.MaxCLIStdoutFrames
		maxObserver, maxFrames = processmodel.MaxCLIObserverBytes, processmodel.MaxCLIFrames
	}
	observer := uint64(frame.Length)
	stdout := uint64(0)
	if kind == processmodel.StdoutFrame {
		stdout = observer - processmodel.FrameHeaderSize - processmodel.FrameMACSize
		observer -= stdout
		if r.counts.StdoutByteCount > maxBytes || stdout > maxBytes-r.counts.StdoutByteCount || r.counts.StdoutChunkCount >= maxChunks {
			return false
		}
	}
	if r.counts.FrameCount >= maxFrames || r.counts.ObserverByteCount > maxObserver || observer > maxObserver-r.counts.ObserverByteCount {
		return false
	}
	block := processPipeBlock{bytes: frame.Bytes, length: frame.Length}
	if r.output.writeExact(block, processBefore(r.effectiveCutoff(), 2*time.Second)) != processPipeOK {
		return false
	}
	if kind != processmodel.SealFrame {
		digest, ok := processmodel.AdvanceTranscriptDigest(r.digest, frame.Bytes[:frame.Length])
		if !ok {
			return false
		}
		r.digest = digest
	}
	r.counts.FrameCount++
	r.counts.ObserverByteCount += observer
	if kind == processmodel.StdoutFrame {
		r.counts.StdoutByteCount += stdout
		r.counts.StdoutChunkCount++
	}
	return true
}
