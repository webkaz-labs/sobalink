//go:build resource_process_native

package processmodel

import (
	"encoding/binary"
	"testing"
)

func transcriptFixture(t *testing.T, mode Mode, checkpoints uint8) (*TranscriptDecoder, Bootstrap) {
	t.Helper()
	b := syntheticBootstrap()
	if mode == CLI {
		b.Invocation = Invocation{Mode: CLI, Ordinal: 1, Operation: CLIGroupStatus}
	}
	var plan CheckpointPlan
	plan.Count = checkpoints
	for i := uint8(0); i < checkpoints; i++ {
		plan.Nonces[i][0] = i + 1
	}
	d, ok := NewTranscriptDecoder(b, plan)
	if !ok {
		t.Fatal("valid fixture constructor rejected")
	}
	boot, ok := EncodeBootFrame(b)
	if !ok {
		t.Fatal("valid Boot rejected")
	}
	transcriptAccept(t, d, boot)
	return d, b
}

func transcriptAccept(t *testing.T, d *TranscriptDecoder, encoded EncodedFrame) DecodedFrame {
	t.Helper()
	got, ok := d.Decode(encoded.Bytes[:encoded.Length])
	if !ok {
		t.Fatal("canonical transcript frame rejected")
	}
	return got
}

func transcriptEvents(t *testing.T, d *TranscriptDecoder, b Bootstrap, values ...Observation) EncodedFrame {
	t.Helper()
	if len(values) == 0 || len(values) > MaxBatchEvents {
		t.Fatal("invalid test batch bound")
	}
	var events [MaxBatchEvents]Event
	for i, v := range values {
		events[i] = Event{Sequence: d.inner.nextEvent + uint64(i), Observation: v}
	}
	frame, ok := EncodeEventFrame(b.Invocation.Mode, b.LaunchID, b.AuthenticationKey, d.inner.nextFrame, events[:len(values)])
	if !ok {
		t.Fatal("valid test event encoding rejected")
	}
	return frame
}

func transcriptCheckpoint(t *testing.T, d *TranscriptDecoder, b Bootstrap) EncodedFrame {
	t.Helper()
	ordinal := d.counts.CheckpointCount + 1
	if ordinal > uint64(d.plan.Count) {
		t.Fatal("test checkpoint outside fixed plan")
	}
	nonce, _ := CheckpointNonceDigest(CheckpointRequest{LaunchID: b.LaunchID, Ordinal: ordinal, Nonce: d.plan.Nonces[ordinal-1]})
	value := CheckpointReport{Ordinal: ordinal, NonceDigest: nonce, BarrierSequence: d.pendingBarrier, PrefixCount: d.counts.EventCount,
		ResourceCount: d.counts.ResourceCount, LocalCount: d.counts.LocalCount, LifecycleCount: d.counts.LifecycleCount}
	frame, ok := EncodeCheckpointFrame(b.Invocation.Mode, b.LaunchID, b.AuthenticationKey, d.inner.nextFrame, value)
	if !ok {
		t.Fatal("valid test checkpoint encoding rejected")
	}
	return frame
}

func transcriptSeal(t *testing.T, d *TranscriptDecoder, b Bootstrap) EncodedFrame {
	t.Helper()
	value := SealReport{EntryEventSequence: d.entrySequence, EventCount: d.counts.EventCount, ReservedCount: d.counts.EventCount,
		ResourceCount: d.counts.ResourceCount, LocalCount: d.counts.LocalCount, LifecycleCount: d.counts.LifecycleCount,
		CheckpointCount: d.counts.CheckpointCount, StdoutByteCount: d.counts.StdoutByteCount, StdoutChunkCount: d.counts.StdoutChunkCount,
		FrameCount: d.inner.nextFrame, EntryOutcome: d.entryOutcome, TranscriptDigest: d.digest}
	if value.EntryOutcome != Success {
		value.ProductExitCode = 1
	}
	frame, ok := EncodeSealFrame(b.Invocation.Mode, b.LaunchID, b.AuthenticationKey, d.inner.nextFrame, value)
	if !ok {
		t.Fatal("valid test seal encoding rejected")
	}
	return frame
}

func transcriptReject(t *testing.T, d *TranscriptDecoder, input []byte) {
	t.Helper()
	before := *d
	if got, ok := d.Decode(input); ok || got != (DecodedFrame{}) || !d.Failed() {
		t.Error("invalid transcript was accepted or not sticky")
	}
	if d.inner != before.inner || d.counts != before.counts || d.digest != before.digest || d.phase != before.phase || d.pendingBarrier != before.pendingBarrier || d.entrySequence != before.entrySequence || d.failures != before.failures || d.sawOwnersClosed != before.sawOwnersClosed {
		t.Error("rejected frame partially committed accepted state")
	}
	if _, ok := d.Decode(input); ok {
		t.Error("failed parser recovered")
	}
}

func TestTranscriptDecoderCompleteCopies(t *testing.T) {
	b := syntheticBootstrap()
	plan := CheckpointPlan{Count: 1}
	plan.Nonces[0][0] = 12
	d, ok := NewTranscriptDecoder(b, plan)
	if !ok {
		t.Fatal("constructor failed")
	}
	original := b
	b.AuthenticationKey[0], b.LaunchID[0] = b.AuthenticationKey[0]+1, 99
	plan.Nonces[0][0] = 99
	boot, _ := EncodeBootFrame(original)
	got := transcriptAccept(t, d, boot)
	got.Boot.BindingDigest[0] ^= 1
	boot.Bytes[FrameHeaderSize] ^= 1
	transcriptAccept(t, d, transcriptEvents(t, d, original, Observation{Kind: OwnerLockBound}, Observation{Kind: CoreBound}, Observation{Kind: OrdinaryNodeBound}))
	transcriptAccept(t, d, transcriptEvents(t, d, original, Observation{Kind: Barrier, BarrierOrdinal: 1}))
	transcriptAccept(t, d, transcriptCheckpoint(t, d, original))
	stdout := []byte{'a', 0, 'b', '\n'}
	out, _ := EncodeStdoutFrame(Owner, original.LaunchID, original.AuthenticationKey, d.inner.nextFrame, stdout)
	decoded := transcriptAccept(t, d, out)
	stdout[0] = 'x'
	decoded.Stdout[0] = 'y'
	transcriptAccept(t, d, transcriptEvents(t, d, original, Observation{Kind: OwnersClosed, Outcome: Success}, Observation{Kind: EntryReturned, Outcome: Success}))
	last, _ := EncodeStdoutFrame(Owner, original.LaunchID, original.AuthenticationKey, d.inner.nextFrame, []byte("done"))
	transcriptAccept(t, d, last)
	seal := transcriptSeal(t, d, original)
	transcriptAccept(t, d, seal)
	summary, ok := d.Finish()
	if !ok || !summary.StructurallyClosed || summary.Counts.EventCount != 6 || summary.Counts.CheckpointCount != 1 || summary.Counts.StdoutByteCount != 8 || summary.Seal.ProductExitCode != 0 {
		t.Fatal("complete structural transcript mismatch")
	}
	summary.Seal.ProductExitCode = 1
	if d.seal.ProductExitCode != 0 || d.boot.BindingDigest == got.Boot.BindingDigest || d.plan.Nonces[0][0] != 12 {
		t.Error("caller mutation changed retained copied state")
	}
	if _, ok := d.Finish(); ok || !d.Failed() {
		t.Error("Finish was reusable")
	}
	for _, outcome := range []Outcome{Success, TypedError, Failure} {
		cli, binding := transcriptFixture(t, CLI, 0)
		transcriptAccept(t, cli, transcriptEvents(t, cli, binding, Observation{Kind: EntryReturned, Outcome: outcome}))
		transcriptAccept(t, cli, transcriptSeal(t, cli, binding))
		got, ok := cli.Finish()
		if !ok || got.Seal.EntryOutcome != outcome || outcome != Success && got.Seal.ProductExitCode != 1 {
			t.Error("CLI product outcome changed by structural seal")
		}
	}
}

func TestTranscriptDecoderCheckpointAndEntryOrdering(t *testing.T) {
	for mutation := 0; mutation < 13; mutation++ {
		d, b := transcriptFixture(t, Owner, 1)
		var bad EncodedFrame
		switch mutation {
		case 0:
			bad, _ = EncodeBootFrame(b)
			binary.BigEndian.PutUint64(bad.Bytes[24:32], d.inner.nextFrame)
			resignFrame(&bad, b.AuthenticationKey)
		case 1:
			bad = transcriptEvents(t, d, b, Observation{Kind: Barrier, BarrierOrdinal: 2})
		case 2:
			bad = transcriptEvents(t, d, b, Observation{Kind: Barrier, BarrierOrdinal: 1}, Observation{Kind: IPCConnectAttempt})
		case 3:
			bad = transcriptEvents(t, d, b, Observation{Kind: EntryReturned, Outcome: Success})
		case 4:
			bad = transcriptEvents(t, d, b, Observation{Kind: OwnersClosed, Outcome: Failure})
		case 5:
			bad = transcriptEvents(t, d, b, Observation{Kind: OwnersClosed, Outcome: Success}, Observation{Kind: OwnersClosed, Outcome: Success})
		default:
			transcriptAccept(t, d, transcriptEvents(t, d, b, Observation{Kind: Barrier, BarrierOrdinal: 1}))
			switch mutation {
			case 6:
				bad, _ = EncodeStdoutFrame(Owner, b.LaunchID, b.AuthenticationKey, d.inner.nextFrame, []byte{'x'})
			case 7:
				bad = transcriptEvents(t, d, b, Observation{Kind: IPCConnectAttempt})
			case 8:
				bad = transcriptEvents(t, d, b, Observation{Kind: EntryReturned, Outcome: Success})
			case 9:
				bad = transcriptCheckpoint(t, d, b)
				bad.Bytes[FrameHeaderSize+8] ^= 1
				resignFrame(&bad, b.AuthenticationKey)
			case 10:
				bad = transcriptCheckpoint(t, d, b)
				binary.BigEndian.PutUint64(bad.Bytes[FrameHeaderSize:FrameHeaderSize+8], 2)
				resignFrame(&bad, b.AuthenticationKey)
			case 11:
				bad = transcriptCheckpoint(t, d, b)
				binary.BigEndian.PutUint64(bad.Bytes[FrameHeaderSize+40:FrameHeaderSize+48], 2)
				binary.BigEndian.PutUint64(bad.Bytes[FrameHeaderSize+48:FrameHeaderSize+56], 2)
				binary.BigEndian.PutUint64(bad.Bytes[FrameHeaderSize+72:FrameHeaderSize+80], 2)
				resignFrame(&bad, b.AuthenticationKey)
			case 12:
				transcriptAccept(t, d, transcriptCheckpoint(t, d, b))
				bad = transcriptEvents(t, d, b, Observation{Kind: OwnersClosed, Outcome: Success}, Observation{Kind: EntryReturned, Outcome: Success}, Observation{Kind: IPCConnectAttempt})
			}
		}
		transcriptReject(t, d, bad.Bytes[:bad.Length])
	}
	for _, mode := range []Mode{Owner, CLI} {
		d, b := transcriptFixture(t, mode, 0)
		bad := transcriptEvents(t, d, b, Observation{Kind: Barrier, BarrierOrdinal: 1})
		transcriptReject(t, d, bad.Bytes[:bad.Length])
	}
	for _, count := range []uint8{4, 32} {
		mode := Owner
		if count == 4 {
			mode = CLI
		}
		d, b := transcriptFixture(t, mode, count)
		for ordinal := uint64(1); ordinal <= uint64(count); ordinal++ {
			transcriptAccept(t, d, transcriptEvents(t, d, b, Observation{Kind: Barrier, BarrierOrdinal: ordinal}))
			transcriptAccept(t, d, transcriptCheckpoint(t, d, b))
		}
		if mode == Owner {
			transcriptAccept(t, d, transcriptEvents(t, d, b, Observation{Kind: OwnersClosed, Outcome: Success}))
		}
		transcriptAccept(t, d, transcriptEvents(t, d, b, Observation{Kind: EntryReturned, Outcome: Success}))
		transcriptAccept(t, d, transcriptSeal(t, d, b))
		if _, ok := d.Finish(); !ok {
			t.Error("complete maximum checkpoint plan rejected")
		}
	}
}

func TestTranscriptDecoderTerminalAndMalformed(t *testing.T) {
	b := syntheticBootstrap()
	unbooted, _ := NewTranscriptDecoder(b, CheckpointPlan{})
	first, _ := EncodeEventFrame(Owner, b.LaunchID, b.AuthenticationKey, 1, []Event{{Sequence: 1, Observation: Observation{Kind: OwnersClosed, Outcome: Success}}})
	transcriptReject(t, unbooted, first.Bytes[:first.Length])
	for mutation := 0; mutation < 7; mutation++ {
		d, b := transcriptFixture(t, CLI, 0)
		transcriptAccept(t, d, transcriptEvents(t, d, b, Observation{Kind: EntryReturned, Outcome: TypedError}))
		seal := transcriptSeal(t, d, b)
		switch mutation {
		case 0:
			seal.Bytes[FrameHeaderSize+96] ^= 1
			resignFrame(&seal, b.AuthenticationKey)
		case 1:
			binary.BigEndian.PutUint64(seal.Bytes[FrameHeaderSize+72:FrameHeaderSize+80], 99)
			resignFrame(&seal, b.AuthenticationKey)
		case 2:
			seal.Bytes[FrameHeaderSize+84] = byte(Failure)
			resignFrame(&seal, b.AuthenticationKey)
		case 3:
			binary.BigEndian.PutUint64(seal.Bytes[FrameHeaderSize+56:FrameHeaderSize+64], 1)
			binary.BigEndian.PutUint64(seal.Bytes[FrameHeaderSize+64:FrameHeaderSize+72], 1)
			resignFrame(&seal, b.AuthenticationKey)
		case 4:
			seal = transcriptEvents(t, d, b, Observation{Kind: IPCConnectAttempt})
		case 5:
			transcriptAccept(t, d, seal)
		case 6:
			transcriptAccept(t, d, seal)
			if _, ok := d.Finish(); !ok {
				t.Fatal("valid Finish failed")
			}
		}
		transcriptReject(t, d, seal.Bytes[:seal.Length])
	}
	d, _ := transcriptFixture(t, CLI, 0)
	if _, ok := d.Finish(); ok || !d.Failed() {
		t.Error("missing Seal became successful Finish")
	}
	zero := new(TranscriptDecoder)
	if _, ok := zero.Decode(nil); ok || !zero.Failed() {
		t.Error("zero decoder accepted input")
	}
	var absent *TranscriptDecoder
	if _, ok := absent.Decode(nil); ok || !absent.Failed() {
		t.Error("nil decoder accepted input")
	}
	if _, ok := absent.Finish(); ok {
		t.Error("nil decoder finished")
	}
	plan := CheckpointPlan{Count: 1}
	if _, ok := NewTranscriptDecoder(b, plan); ok {
		t.Error("zero planned nonce accepted")
	}
	plan.Count = 0
	plan.Nonces[31][0] = 1
	if _, ok := NewTranscriptDecoder(b, plan); ok {
		t.Error("hidden unused nonce accepted")
	}
}

func TestTranscriptDecoderStickyEvidenceFailure(t *testing.T) {
	d, b := transcriptFixture(t, CLI, 0)
	for _, flag := range []FailureFlags{Invalid, Overflow, PrefixGap, Late, SequenceFailure} {
		frame, _ := EncodeFailureFrame(CLI, b.LaunchID, b.AuthenticationKey, d.inner.nextFrame, FailureReport{Flags: flag})
		transcriptAccept(t, d, frame)
	}
	if d.Failed() || d.EvidenceFailures() != allFailureFlags {
		t.Error("authenticated failure conflated with parser error or lost bits")
	}
	repeat, _ := EncodeFailureFrame(CLI, b.LaunchID, b.AuthenticationKey, d.inner.nextFrame, FailureReport{Flags: Invalid})
	transcriptReject(t, d, repeat.Bytes[:repeat.Length])
	pending, b := transcriptFixture(t, CLI, 1)
	transcriptAccept(t, pending, transcriptEvents(t, pending, b, Observation{Kind: Barrier, BarrierOrdinal: 1}))
	failure, _ := EncodeFailureFrame(CLI, b.LaunchID, b.AuthenticationKey, pending.inner.nextFrame, FailureReport{Flags: PrefixGap, Reserved: 1, PrefixCount: 1})
	transcriptAccept(t, pending, failure)
	out, _ := EncodeStdoutFrame(CLI, b.LaunchID, b.AuthenticationKey, pending.inner.nextFrame, []byte{'x'})
	transcriptAccept(t, pending, out)
	checkpoint := transcriptCheckpoint(t, pending, b)
	copyPending := *pending
	transcriptReject(t, &copyPending, checkpoint.Bytes[:checkpoint.Length])
	transcriptAccept(t, pending, transcriptEvents(t, pending, b, Observation{Kind: EntryReturned, Outcome: Failure}))
	seal := transcriptSeal(t, pending, b)
	transcriptReject(t, pending, seal.Bytes[:seal.Length])
	mismatch, b := transcriptFixture(t, CLI, 0)
	bad, _ := EncodeFailureFrame(CLI, b.LaunchID, b.AuthenticationKey, mismatch.inner.nextFrame, FailureReport{Flags: Invalid, Reserved: 1, PrefixCount: 1})
	transcriptReject(t, mismatch, bad.Bytes[:bad.Length])
}

func TestTranscriptDecoderOutputAndCounterBounds(t *testing.T) {
	for _, mode := range []Mode{Owner, CLI} {
		for _, byChunks := range []bool{false, true} {
			d, b := transcriptFixture(t, mode, 0)
			bytes, chunks, _, _ := stdoutLimits(mode)
			length, frames := MaxStdoutChunk, int(bytes/MaxStdoutChunk)
			if byChunks {
				length, frames = 1, int(chunks)
			}
			var output [MaxStdoutChunk]byte
			for i := 0; i < frames; i++ {
				frame, _ := EncodeStdoutFrame(mode, b.LaunchID, b.AuthenticationKey, d.inner.nextFrame, output[:length])
				transcriptAccept(t, d, frame)
			}
			over, _ := EncodeStdoutFrame(mode, b.LaunchID, b.AuthenticationKey, d.inner.nextFrame, []byte{1})
			transcriptReject(t, d, over.Bytes[:over.Length])
		}
		// These independent counter guards are exercised with private synthetic
		// state; that state is not claimed to be a feasible successful stream.
		for guard := 0; guard < 5; guard++ {
			d, b := transcriptFixture(t, mode, 0)
			_, _, observer, frames := stdoutLimits(mode)
			switch guard {
			case 0:
				d.counts.ObserverByteCount = observer - 68
			case 1:
				d.counts.FrameCount = frames - 1
				d.inner.nextFrame = frames
			case 2:
				d.counts.ObserverByteCount = ^uint64(0)
			case 3:
				d.counts.StdoutByteCount = ^uint64(0)
			case 4:
				d.counts.FrameCount = ^uint64(0)
			}
			frame, _ := EncodeStdoutFrame(mode, b.LaunchID, b.AuthenticationKey, d.inner.nextFrame, []byte{1})
			if guard < 2 {
				transcriptAccept(t, d, frame)
				frame, _ = EncodeStdoutFrame(mode, b.LaunchID, b.AuthenticationKey, d.inner.nextFrame, []byte{1})
			}
			transcriptReject(t, d, frame.Bytes[:frame.Length])
		}
	}
}

func TestTranscriptDecoderCumulativeEventBounds(t *testing.T) {
	for _, test := range []struct {
		mode  Mode
		kind  Kind
		count int
	}{
		{Owner, GroupAccepted, MaxResourceEvents}, {Owner, IPCConnectAttempt, MaxLocalEvents}, {Owner, MaintenancePassed, MaxLifecycleEvents},
	} {
		d, b := transcriptFixture(t, test.mode, 0)
		remaining := test.count
		for remaining > 0 {
			size := remaining
			if size > MaxBatchEvents {
				size = MaxBatchEvents
			}
			var values [MaxBatchEvents]Observation
			for i := 0; i < size; i++ {
				values[i].Kind = test.kind
			}
			transcriptAccept(t, d, transcriptEvents(t, d, b, values[:size]...))
			remaining -= size
		}
		// A preceding valid category increment in the same rejected batch must
		// not partially commit inner or outer state.
		other := Observation{Kind: IPCConnectAttempt}
		if test.kind == IPCConnectAttempt {
			other.Kind = MaintenancePassed
		}
		bad := transcriptEvents(t, d, b, other, Observation{Kind: test.kind})
		transcriptReject(t, d, bad.Bytes[:bad.Length])
	}
	for _, mode := range []Mode{Owner, CLI} {
		d, b := transcriptFixture(t, mode, 0)
		for n := uint64(0); n < mode.capacity(); {
			kind, categoryEnd := IPCConnectAttempt, mode.capacity()
			if mode == Owner {
				switch {
				case n < MaxResourceEvents:
					kind, categoryEnd = GroupAccepted, MaxResourceEvents
				case n < MaxResourceEvents+MaxLocalEvents:
					categoryEnd = MaxResourceEvents + MaxLocalEvents
				default:
					kind = MaintenancePassed
				}
			}
			size := categoryEnd - n
			if size > MaxBatchEvents {
				size = MaxBatchEvents
			}
			var values [MaxBatchEvents]Observation
			for i := uint64(0); i < size; i++ {
				values[i].Kind = kind
			}
			transcriptAccept(t, d, transcriptEvents(t, d, b, values[:size]...))
			n += size
		}
		payload, _ := EncodeEventBatch(mode, []Event{{Sequence: mode.capacity(), Observation: Observation{Kind: IPCConnectAttempt}}})
		binary.BigEndian.PutUint64(payload.Bytes[4:12], mode.capacity()+1)
		bad, _ := encodeFrame(mode, b.LaunchID, b.AuthenticationKey, d.inner.nextFrame, EventsFrame, payload.Bytes[:payload.Length])
		transcriptReject(t, d, bad.Bytes[:bad.Length])
	}
}
