//go:build resource_process_native

package processmodel

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"testing"
)

func TestFrameCodecRoundTrip(t *testing.T) {
	launch, key := [16]byte{1}, [32]byte{2}
	d, ok := NewDecoder(Owner, launch, key)
	if !ok {
		t.Fatal("decoder rejected fixed synthetic values")
	}
	event := Event{Sequence: 1, Observation: Observation{Kind: GroupAccepted, RunID: [16]byte{3}}}
	one, ok := EncodeEventFrame(Owner, launch, key, 1, []Event{event})
	if !ok {
		t.Fatal("event frame encode failed")
	}
	decoded, ok := d.Decode(one.Bytes[:one.Length])
	if !ok || decoded.Kind != EventsFrame || decoded.Sequence != 1 || decoded.Batch.Events[0] != event {
		t.Fatal("event frame decode failed")
	}
	one.Bytes[FrameHeaderSize+20] ^= 1
	if decoded.Batch.Events[0] != event {
		t.Fatal("decoded frame aliases input")
	}
	decoded.Batch.Events[0].Sequence = 600
	stdout := []byte{'o', 'k', 0, '\n'}
	two, ok := EncodeStdoutFrame(Owner, launch, key, 2, stdout)
	if !ok {
		t.Fatal("stdout frame encode failed")
	}
	stdout[0] = 'x'
	got, ok := d.Decode(two.Bytes[:two.Length])
	if !ok || got.Kind != StdoutFrame || got.StdoutLength != 4 || got.Stdout[0] != 'o' || got.Stdout[2] != 0 {
		t.Fatal("stdout bytes were not copied exactly")
	}
	event.Sequence = 2
	three, ok := EncodeEventFrame(Owner, launch, key, 3, []Event{event})
	if !ok {
		t.Fatal("second event frame encode failed")
	}
	if got, ok := d.Decode(three.Bytes[:three.Length]); !ok || got.Batch.Events[0] != event {
		t.Fatal("output mutation changed decoder progression")
	}
	failure := FailureReport{Flags: PrefixGap | Overflow, Reserved: 3, PrefixCount: 2}
	four, ok := EncodeFailureFrame(Owner, launch, key, 4, failure)
	if !ok {
		t.Fatal("failure frame encode failed")
	}
	if got, ok := d.Decode(four.Bytes[:four.Length]); !ok || got.Failure != failure || d.EvidenceFailures() != failure.Flags || d.Failed() {
		t.Fatal("failure evidence did not remain distinct from parse failure")
	}
	more, ok := EncodeFailureFrame(Owner, launch, key, 5, FailureReport{Flags: Late, Reserved: 3, PrefixCount: 2})
	if !ok {
		t.Fatal("second failure frame encode failed")
	}
	if _, ok := d.Decode(more.Bytes[:more.Length]); !ok || d.EvidenceFailures() != PrefixGap|Overflow|Late {
		t.Fatal("failure evidence was cleared")
	}
	if encoded, ok := EncodeStdoutFrame(CLI, launch, key, 1, make([]byte, MaxStdoutChunk)); !ok {
		t.Fatal("exact stdout ceiling rejected")
	} else {
		cli, _ := NewDecoder(CLI, launch, key)
		if got, ok := cli.Decode(encoded.Bytes[:encoded.Length]); !ok || got.StdoutLength != MaxStdoutChunk {
			t.Fatal("exact stdout ceiling did not decode")
		}
	}
	// Constructor values are copied, not aliased to caller-owned arrays.
	bound, _ := NewDecoder(CLI, launch, key)
	last, _ := EncodeStdoutFrame(CLI, launch, key, 1, []byte{'a'})
	launch[0], key[0] = 8, 9
	if _, ok := bound.Decode(last.Bytes[:last.Length]); !ok {
		t.Fatal("constructor retained mutable caller references")
	}
	for _, mode := range []Mode{Owner, CLI} {
		full, ok := NewDecoder(mode, launch, key)
		if !ok {
			t.Fatal("boundary decoder rejected")
		}
		fillDecoder(t, full, launch, key)
		if full.Failed() || full.nextEvent != mode.capacity()+1 {
			t.Fatal("exact cumulative event boundary rejected")
		}
		if mode == Owner && full.categoryCounts != [3]uint64{MaxResourceEvents, MaxLocalEvents, MaxLifecycleEvents} {
			t.Fatal("exact owner category boundaries not retained")
		}
		if mode == CLI && full.categoryCounts != [3]uint64{0, MaxCLIEvents, 0} {
			t.Fatal("exact CLI event boundary not retained")
		}
	}
}

func feedDecoderEvents(t *testing.T, d *Decoder, launch [16]byte, key [32]byte, value Observation, count int) {
	t.Helper()
	if count < 0 || count > MaxOwnerEvents {
		t.Fatal("unbounded test event count")
	}
	for count > 0 {
		size := count
		if size > MaxBatchEvents {
			size = MaxBatchEvents
		}
		var events [MaxBatchEvents]Event
		for i := 0; i < size; i++ {
			events[i] = Event{Sequence: d.nextEvent + uint64(i), Observation: value}
		}
		frame, ok := EncodeEventFrame(d.mode, launch, key, d.nextFrame, events[:size])
		if !ok {
			t.Fatal("valid boundary batch did not encode")
		}
		if got, ok := d.Decode(frame.Bytes[:frame.Length]); !ok || int(got.Batch.Count) != size {
			t.Fatal("valid boundary batch did not decode")
		}
		count -= size
	}
}

func fillDecoder(t *testing.T, d *Decoder, launch [16]byte, key [32]byte) {
	t.Helper()
	if d.mode == CLI {
		feedDecoderEvents(t, d, launch, key, Observation{Kind: IPCConnectAttempt}, MaxCLIEvents)
		return
	}
	feedDecoderEvents(t, d, launch, key, Observation{Kind: GroupAccepted}, MaxResourceEvents)
	feedDecoderEvents(t, d, launch, key, Observation{Kind: IPCConnectAttempt}, MaxLocalEvents)
	feedDecoderEvents(t, d, launch, key, Observation{Kind: MaintenancePassed}, MaxLifecycleEvents)
}

func resignFrame(frame *EncodedFrame, key [32]byte) {
	end := int(frame.Length) - FrameMACSize
	mac := hmac.New(sha256.New, key[:])
	_, _ = mac.Write(frame.Bytes[:end])
	copy(frame.Bytes[end:int(frame.Length)], mac.Sum(nil))
}

func rejectFrame(t *testing.T, mode Mode, launch [16]byte, key [32]byte, input []byte, valid EncodedFrame) {
	t.Helper()
	d, ok := NewDecoder(mode, launch, key)
	if !ok {
		t.Fatal("invalid test decoder")
	}
	if _, ok := d.Decode(input); ok || !d.Failed() {
		t.Error("malformed frame accepted or failure not sticky")
	}
	if _, ok := d.Decode(valid.Bytes[:valid.Length]); ok {
		t.Error("decoder recovered from invalid evidence")
	}
}

func TestFrameCodecRejectsMalformed(t *testing.T) {
	launch, key := [16]byte{1}, [32]byte{2}
	base, ok := EncodeEventFrame(Owner, launch, key, 1, []Event{{Sequence: 1, Observation: Observation{Kind: IPCConnectAttempt}}})
	if !ok {
		t.Fatal("valid test frame rejected")
	}
	for length := 0; length < int(base.Length); length++ {
		rejectFrame(t, Owner, launch, key, base.Bytes[:length], base)
	}
	rejectFrame(t, Owner, launch, key, base.Bytes[:base.Length+1], base)
	rejectFrame(t, Owner, launch, key, make([]byte, MaxFrameSize+1), base)
	for offset := 0; offset < int(base.Length); offset++ {
		bad := base
		bad.Bytes[offset] ^= 1
		rejectFrame(t, Owner, launch, key, bad.Bytes[:bad.Length], base)
	}
	for _, mutation := range []struct {
		offset int
		value  byte
	}{
		{0, 'X'}, {4, 2}, {5, 0}, {5, 1}, {5, 3}, {5, 6}, {5, 255}, {6, 1}, {7, 1}, {8, 2},
		{32, 255}, {FrameHeaderSize + 2, 1}, {FrameHeaderSize + 4 + 8, 255},
	} {
		bad := base
		bad.Bytes[mutation.offset] = mutation.value
		resignFrame(&bad, key)
		rejectFrame(t, Owner, launch, key, bad.Bytes[:bad.Length], base)
	}
	for _, sequence := range []uint64{0, 2, ^uint64(0)} {
		bad := base
		binary.BigEndian.PutUint64(bad.Bytes[24:32], sequence)
		resignFrame(&bad, key)
		rejectFrame(t, Owner, launch, key, bad.Bytes[:bad.Length], base)
	}
	for _, mode := range []Mode{0, 255} {
		if _, ok := NewDecoder(mode, launch, key); ok {
			t.Error("unknown decoder mode accepted")
		}
		if _, ok := EncodeStdoutFrame(mode, launch, key, 1, []byte{'x'}); ok {
			t.Error("unknown encoder mode accepted")
		}
	}
	if _, ok := NewDecoder(Owner, [16]byte{}, key); ok {
		t.Error("zero launch accepted")
	}
	if _, ok := NewDecoder(Owner, launch, [32]byte{}); ok {
		t.Error("zero key accepted")
	}
	for _, sequence := range []uint64{0, ^uint64(0)} {
		if _, ok := EncodeStdoutFrame(Owner, launch, key, sequence, []byte{'x'}); ok {
			t.Error("invalid sequence encoded")
		}
	}
	if _, ok := EncodeStdoutFrame(Owner, [16]byte{}, key, 1, []byte{'x'}); ok {
		t.Error("zero launch encoded")
	}
	if _, ok := EncodeStdoutFrame(Owner, launch, [32]byte{}, 1, []byte{'x'}); ok {
		t.Error("zero key encoded")
	}
	for _, stdout := range [][]byte{nil, make([]byte, MaxStdoutChunk+1)} {
		if _, ok := EncodeStdoutFrame(Owner, launch, key, 1, stdout); ok {
			t.Error("invalid stdout length encoded")
		}
		bad, ok := encodeFrame(Owner, launch, key, 1, StdoutFrame, stdout)
		if !ok {
			t.Fatal("test raw frame could not be formed")
		}
		rejectFrame(t, Owner, launch, key, bad.Bytes[:bad.Length], base)
	}
	for _, failure := range []FailureReport{
		{}, {Flags: FailureFlags(1 << 31)}, {Flags: PrefixGap, Reserved: 0, PrefixCount: 1}, {Flags: Overflow, Reserved: 641, PrefixCount: 641},
	} {
		if _, ok := EncodeFailureFrame(Owner, launch, key, 1, failure); ok {
			t.Error("invalid failure report encoded")
		}
	}
	validFailure, _ := EncodeFailureFrame(Owner, launch, key, 1, FailureReport{Flags: Invalid, Reserved: 1})
	for _, offset := range []int{FrameHeaderSize, FrameHeaderSize + 4, FrameHeaderSize + 5, FrameHeaderSize + 6, FrameHeaderSize + 7, FrameHeaderSize + 16} {
		bad := validFailure
		bad.Bytes[offset] = 255
		resignFrame(&bad, key)
		rejectFrame(t, Owner, launch, key, bad.Bytes[:bad.Length], base)
	}
	for _, value := range []Observation{{Kind: GroupAccepted}, {Kind: MaintenancePassed}, {Kind: Barrier, BarrierOrdinal: 5}} {
		ownerFrame, ok := EncodeEventFrame(Owner, launch, key, 1, []Event{{Sequence: 1, Observation: value}})
		if !ok {
			t.Fatal("valid owner-only event rejected")
		}
		rejectFrame(t, CLI, launch, key, ownerFrame.Bytes[:ownerFrame.Length], base)
		if _, ok := EncodeEventFrame(CLI, launch, key, 1, []Event{{Sequence: 1, Observation: value}}); ok {
			t.Error("CLI encoder accepted owner-only event")
		}
	}
	for _, eventSequence := range []uint64{1, 3} {
		d, _ := NewDecoder(Owner, launch, key)
		if _, ok := d.Decode(base.Bytes[:base.Length]); !ok {
			t.Fatal("valid first frame failed")
		}
		next, _ := EncodeEventFrame(Owner, launch, key, 2, []Event{{Sequence: eventSequence, Observation: Observation{Kind: IPCConnectAttempt}}})
		if _, ok := d.Decode(next.Bytes[:next.Length]); ok || !d.Failed() {
			t.Error("event replay/gap accepted")
		}
	}
	d, _ := NewDecoder(Owner, launch, key)
	if _, ok := d.Decode(base.Bytes[:base.Length]); !ok {
		t.Fatal("valid first frame failed")
	}
	if _, ok := d.Decode(base.Bytes[:base.Length]); ok || !d.Failed() {
		t.Error("frame replay accepted")
	}
	wrapped, _ := NewDecoder(Owner, launch, key)
	wrapped.nextFrame = ^uint64(0)
	reject := base
	binary.BigEndian.PutUint64(reject.Bytes[24:32], ^uint64(0))
	resignFrame(&reject, key)
	if _, ok := wrapped.Decode(reject.Bytes[:reject.Length]); ok || !wrapped.Failed() {
		t.Error("sequence exhaustion accepted")
	}
	for _, test := range []struct {
		value Observation
		count int
	}{
		{Observation{Kind: GroupAccepted}, MaxResourceEvents},
		{Observation{Kind: IPCConnectAttempt}, MaxLocalEvents},
		{Observation{Kind: MaintenancePassed}, MaxLifecycleEvents},
	} {
		limited, _ := NewDecoder(Owner, launch, key)
		feedDecoderEvents(t, limited, launch, key, test.value, test.count)
		counts, nextFrame, nextEvent := limited.categoryCounts, limited.nextFrame, limited.nextEvent
		overflow, ok := EncodeEventFrame(Owner, launch, key, nextFrame, []Event{{Sequence: nextEvent, Observation: test.value}})
		if !ok {
			t.Fatal("category overflow vector is not otherwise canonical")
		}
		if _, ok := limited.Decode(overflow.Bytes[:overflow.Length]); ok || !limited.Failed() {
			t.Error("cross-batch category overflow accepted")
		}
		if limited.categoryCounts != counts || limited.nextFrame != nextFrame || limited.nextEvent != nextEvent {
			t.Error("rejected category overflow advanced accepted state")
		}
	}
	transactional, _ := NewDecoder(Owner, launch, key)
	feedDecoderEvents(t, transactional, launch, key, Observation{Kind: GroupAccepted}, MaxResourceEvents)
	counts, nextFrame, nextEvent := transactional.categoryCounts, transactional.nextFrame, transactional.nextEvent
	mixed, ok := EncodeEventFrame(Owner, launch, key, nextFrame, []Event{
		{Sequence: nextEvent, Observation: Observation{Kind: IPCConnectAttempt}},
		{Sequence: nextEvent + 1, Observation: Observation{Kind: GroupAccepted}},
	})
	if !ok {
		t.Fatal("mixed category overflow vector is not otherwise canonical")
	}
	if _, ok := transactional.Decode(mixed.Bytes[:mixed.Length]); ok || !transactional.Failed() {
		t.Error("mixed category overflow accepted")
	}
	if transactional.categoryCounts != counts || transactional.nextFrame != nextFrame || transactional.nextEvent != nextEvent {
		t.Error("invalid batch partially committed provisional counts")
	}
	validAfter, _ := EncodeStdoutFrame(Owner, launch, key, nextFrame, []byte{'x'})
	if _, ok := transactional.Decode(validAfter.Bytes[:validAfter.Length]); ok {
		t.Error("category failure was not sticky")
	}
	for _, mode := range []Mode{Owner, CLI} {
		full, _ := NewDecoder(mode, launch, key)
		fillDecoder(t, full, launch, key)
		counts, nextFrame, nextEvent := full.categoryCounts, full.nextFrame, full.nextEvent
		tooLate := Event{Sequence: nextEvent, Observation: Observation{Kind: IPCConnectAttempt}}
		if _, ok := EncodeEventFrame(mode, launch, key, nextFrame, []Event{tooLate}); ok {
			t.Error("out-of-bound cumulative event encoded")
		}
		// Deliberately form an authenticated malformed value in this test only.
		payload, ok := EncodeEventBatch(mode, []Event{{Sequence: mode.capacity(), Observation: tooLate.Observation}})
		if !ok {
			t.Fatal("maximum sequence vector rejected")
		}
		binary.BigEndian.PutUint64(payload.Bytes[4:12], nextEvent)
		overflow, ok := encodeFrame(mode, launch, key, nextFrame, EventsFrame, payload.Bytes[:payload.Length])
		if !ok {
			t.Fatal("bounded malformed total-overflow frame could not be formed")
		}
		if _, ok := full.Decode(overflow.Bytes[:overflow.Length]); ok || !full.Failed() {
			t.Error("cumulative 640/64 event overflow accepted")
		}
		if full.categoryCounts != counts || full.nextFrame != nextFrame || full.nextEvent != nextEvent {
			t.Error("total overflow advanced accepted state")
		}
	}
}
