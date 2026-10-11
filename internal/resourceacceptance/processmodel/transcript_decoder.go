//go:build resource_process_native

package processmodel

import "crypto/hmac"

type transcriptPhase uint8

const (
	awaitBoot transcriptPhase = iota + 1
	transcriptRunning
	awaitCheckpointReply
	transcriptEntryReturned
	transcriptSealed
	transcriptFinished
)

type TranscriptCounts struct {
	FrameCount        uint64
	EventCount        uint64
	ResourceCount     uint64
	LocalCount        uint64
	LifecycleCount    uint64
	CheckpointCount   uint64
	StdoutByteCount   uint64
	StdoutChunkCount  uint64
	ObserverByteCount uint64
}

// TranscriptSummary is a copied structural result, never proof of native EOF,
// process exit or producer/reporter joins. It deliberately excludes secrets,
// paths, PIDs and raw stdout. Later input can still invalidate the decoder.
type TranscriptSummary struct {
	Seal               SealReport
	BootBindingDigest  [32]byte
	Counts             TranscriptCounts
	StructurallyClosed bool
}

// TranscriptDecoder is single-owner copied-value state. No API installs an
// observer, changes the immutable binding/plan, or supplies product authority.
type TranscriptDecoder struct {
	inner             Decoder
	boot              BootReport
	plan              CheckpointPlan
	phase             transcriptPhase
	counts            TranscriptCounts
	digest            [32]byte
	pendingBarrier    uint64
	entrySequence     uint64
	entryOutcome      Outcome
	sawOwnersClosed   bool
	ownersClosedClean bool
	failureFrames     uint8
	failures          FailureFlags
	seal              SealReport
	failed            bool
}

func NewTranscriptDecoder(v Bootstrap, plan CheckpointPlan) (*TranscriptDecoder, bool) {
	boot, ok := bootReport(v)
	if !ok || !validCheckpointPlan(v.Invocation.Mode, plan) {
		return nil, false
	}
	inner, ok := newDecoder(v.Invocation.Mode, v.LaunchID, v.AuthenticationKey, true)
	if !ok {
		return nil, false
	}
	return &TranscriptDecoder{inner: *inner, boot: boot, plan: plan, phase: awaitBoot, digest: InitialTranscriptDigest()}, true
}

func (d *TranscriptDecoder) Failed() bool { return d == nil || d.failed }
func (d *TranscriptDecoder) EvidenceFailures() FailureFlags {
	if d == nil {
		return 0
	}
	return d.failures
}

func (d *TranscriptDecoder) Decode(input []byte) (DecodedFrame, bool) {
	if d == nil || d.failed {
		return DecodedFrame{}, false
	}
	if d.phase == transcriptSealed || d.phase == transcriptFinished {
		d.failed = true
		return DecodedFrame{}, false
	}
	// Inner and outer state are committed together only after every check. A
	// rejected frame cannot advance accepted counters, phase or digest.
	trial := *d
	frame, ok := trial.inner.Decode(input)
	if !ok || !trial.acceptFrame(frame, input) {
		d.failed = true
		return DecodedFrame{}, false
	}
	*d = trial
	return frame, true
}

func (d *TranscriptDecoder) acceptFrame(frame DecodedFrame, input []byte) bool {
	maxStdout, maxChunks, maxObserver, maxFrames := stdoutLimits(d.inner.mode)
	if maxFrames == 0 || d.counts.FrameCount >= maxFrames {
		return false
	}
	observer := uint64(len(input))
	if frame.Kind == StdoutFrame {
		length := uint64(frame.StdoutLength)
		if d.counts.StdoutByteCount > maxStdout || length > maxStdout-d.counts.StdoutByteCount ||
			d.counts.StdoutChunkCount >= maxChunks || length > observer {
			return false
		}
		d.counts.StdoutByteCount += length
		d.counts.StdoutChunkCount++
		observer -= length
	}
	if d.counts.ObserverByteCount > maxObserver || observer > maxObserver-d.counts.ObserverByteCount {
		return false
	}
	d.counts.ObserverByteCount += observer
	d.counts.FrameCount++
	if frame.Sequence != d.counts.FrameCount {
		return false
	}
	d.counts.EventCount = d.inner.nextEvent - 1
	d.counts.ResourceCount = d.inner.categoryCounts[resourceCategory]
	d.counts.LocalCount = d.inner.categoryCounts[localCategory]
	d.counts.LifecycleCount = d.inner.categoryCounts[lifecycleCategory]
	if d.phase == awaitBoot {
		if frame.Kind != BootFrame || frame.Sequence != 1 || frame.Boot.BindingDigest != d.boot.BindingDigest || !hmac.Equal(frame.Boot.ChallengeProof[:], d.boot.ChallengeProof[:]) {
			return false
		}
		d.phase = transcriptRunning
	} else {
		switch frame.Kind {
		case EventsFrame:
			if d.phase != transcriptRunning && !(d.failures != 0 && d.phase == awaitCheckpointReply) {
				return false
			}
			if !d.acceptEvents(frame.Batch) {
				return false
			}
		case StdoutFrame:
			if d.phase != transcriptRunning && d.phase != transcriptEntryReturned && !(d.failures != 0 && d.phase == awaitCheckpointReply) {
				return false
			}
		case CheckpointFrame:
			if !d.acceptCheckpoint(frame.Checkpoint) {
				return false
			}
		case FailureFrame:
			if d.phase != transcriptRunning && d.phase != awaitCheckpointReply && d.phase != transcriptEntryReturned {
				return false
			}
			if frame.Failure.PrefixCount != d.counts.EventCount || frame.Failure.Flags & ^d.failures == 0 || d.failureFrames >= 5 {
				return false
			}
			d.failures |= frame.Failure.Flags
			d.failureFrames++
		case SealFrame:
			if !d.acceptSeal(frame.Seal) {
				return false
			}
		default:
			return false
		}
	}
	if frame.Kind != SealFrame {
		digest, ok := AdvanceTranscriptDigest(d.digest, input)
		if !ok {
			return false
		}
		d.digest = digest
	}
	return true
}

func (d *TranscriptDecoder) acceptEvents(batch EventBatch) bool {
	for i := uint16(0); i < batch.Count; i++ {
		event := batch.Events[i]
		v := event.Observation
		switch v.Kind {
		case OwnersClosed:
			if d.sawOwnersClosed {
				return false
			}
			d.sawOwnersClosed = true
			d.ownersClosedClean = v.Outcome == Success
			if !d.ownersClosedClean && d.failures == 0 {
				return false
			}
		case Barrier:
			if i+1 != batch.Count || d.pendingBarrier != 0 || d.counts.CheckpointCount >= uint64(d.plan.Count) || v.BarrierOrdinal != d.counts.CheckpointCount+1 {
				return false
			}
			d.pendingBarrier = event.Sequence
			d.phase = awaitCheckpointReply
		case EntryReturned:
			if i+1 != batch.Count || d.entrySequence != 0 {
				return false
			}
			if d.failures == 0 && (d.pendingBarrier != 0 || d.counts.CheckpointCount != uint64(d.plan.Count) ||
				d.inner.mode == Owner && (!d.sawOwnersClosed || !d.ownersClosedClean)) {
				return false
			}
			d.entrySequence, d.entryOutcome = event.Sequence, v.Outcome
			d.phase = transcriptEntryReturned
		}
	}
	return true
}

func (d *TranscriptDecoder) acceptCheckpoint(v CheckpointReport) bool {
	if d.failures != 0 || d.phase != awaitCheckpointReply || d.pendingBarrier == 0 || d.counts.CheckpointCount >= uint64(d.plan.Count) {
		return false
	}
	ordinal := d.counts.CheckpointCount + 1
	request := CheckpointRequest{LaunchID: d.inner.launch, Ordinal: ordinal, Nonce: d.plan.Nonces[ordinal-1]}
	nonce, ok := CheckpointNonceDigest(request)
	if !ok || v.Ordinal != ordinal || v.NonceDigest != nonce || v.BarrierSequence != d.pendingBarrier ||
		v.PrefixCount != d.counts.EventCount || v.BarrierSequence != d.counts.EventCount ||
		v.ResourceCount != d.counts.ResourceCount || v.LocalCount != d.counts.LocalCount || v.LifecycleCount != d.counts.LifecycleCount {
		return false
	}
	d.counts.CheckpointCount++
	d.pendingBarrier = 0
	d.phase = transcriptRunning
	return true
}

func (d *TranscriptDecoder) acceptSeal(v SealReport) bool {
	if d.failures != 0 || d.phase != transcriptEntryReturned || d.pendingBarrier != 0 ||
		d.counts.CheckpointCount != uint64(d.plan.Count) || d.entrySequence == 0 ||
		v.EntryEventSequence != d.entrySequence || v.EntryOutcome != d.entryOutcome ||
		v.EventCount != d.counts.EventCount || v.ReservedCount != d.counts.EventCount ||
		v.ResourceCount != d.counts.ResourceCount || v.LocalCount != d.counts.LocalCount || v.LifecycleCount != d.counts.LifecycleCount ||
		v.CheckpointCount != d.counts.CheckpointCount || v.StdoutByteCount != d.counts.StdoutByteCount ||
		v.StdoutChunkCount != d.counts.StdoutChunkCount || v.FrameCount != d.counts.FrameCount || v.TranscriptDigest != d.digest {
		return false
	}
	d.seal = v
	d.phase = transcriptSealed
	return true
}

// Finish is a one-shot structural close. The caller must separately establish
// real EOF, all native/task joins and process exit before using any final receipt.
func (d *TranscriptDecoder) Finish() (TranscriptSummary, bool) {
	if d == nil {
		return TranscriptSummary{}, false
	}
	if d.failed || d.failures != 0 || d.phase != transcriptSealed {
		d.failed = true
		return TranscriptSummary{}, false
	}
	d.phase = transcriptFinished
	return TranscriptSummary{Seal: d.seal, BootBindingDigest: d.boot.BindingDigest, Counts: d.counts, StructurallyClosed: true}, true
}
