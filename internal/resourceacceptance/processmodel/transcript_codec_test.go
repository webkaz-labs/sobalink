//go:build resource_process_native

package processmodel

import (
	"crypto/sha256"
	"encoding/binary"
	"testing"
)

func wireTestHeader(kind FrameKind, payload uint32) [FrameHeaderSize]byte {
	var h [FrameHeaderSize]byte
	copy(h[:4], "P1EV")
	h[4], h[5], h[8] = 1, byte(kind), 2
	binary.BigEndian.PutUint64(h[24:32], 1)
	binary.BigEndian.PutUint32(h[32:36], payload)
	return h
}

func TestFrameMessageLengthClosedHeader(t *testing.T) {
	for _, test := range []struct {
		kind    FrameKind
		payload uint32
	}{
		{BootFrame, 64}, {EventsFrame, 108}, {EventsFrame, 212}, {EventsFrame, 8116},
		{CheckpointFrame, 80}, {StdoutFrame, 1}, {StdoutFrame, 4096}, {FailureFrame, 24}, {SealFrame, 128},
	} {
		header := wireTestHeader(test.kind, test.payload)
		before := header
		length, ok := FrameMessageLength(header[:])
		if !ok || uint32(length) != 68+test.payload || header != before {
			t.Error("valid header shape failed or input mutated")
		}
	}
	base := wireTestHeader(BootFrame, 64)
	for length := 0; length < FrameHeaderSize; length++ {
		if n, ok := FrameMessageLength(base[:length]); ok || n != 0 {
			t.Error("short header accepted")
		}
	}
	var extra [FrameHeaderSize + 1]byte
	copy(extra[:], base[:])
	if _, ok := FrameMessageLength(extra[:]); ok {
		t.Error("header trailing byte accepted")
	}
	for _, offset := range []int{0, 1, 2, 3, 4, 6, 7} {
		bad := base
		bad[offset] ^= 1
		if _, ok := FrameMessageLength(bad[:]); ok {
			t.Error("invalid fixed header accepted")
		}
	}
	for _, kind := range []FrameKind{0, 7, 255} {
		bad := base
		bad[5] = byte(kind)
		if _, ok := FrameMessageLength(bad[:]); ok {
			t.Error("unknown frame kind accepted")
		}
	}
	bad := base
	bad[8] = 0
	if _, ok := FrameMessageLength(bad[:]); ok {
		t.Error("zero launch accepted")
	}
	for _, sequence := range []uint64{0, ^uint64(0)} {
		bad := base
		binary.BigEndian.PutUint64(bad[24:32], sequence)
		if _, ok := FrameMessageLength(bad[:]); ok {
			t.Error("invalid sequence accepted")
		}
	}
	changed := base
	changed[8] = 99
	binary.BigEndian.PutUint64(changed[24:32], 23)
	if _, ok := FrameMessageLength(changed[:]); !ok {
		t.Error("header helper incorrectly claims binding authentication")
	}
	for _, test := range []struct {
		kind    FrameKind
		payload uint32
	}{
		{BootFrame, 0}, {BootFrame, 63}, {BootFrame, 65}, {CheckpointFrame, 79}, {CheckpointFrame, 81},
		{EventsFrame, 0}, {EventsFrame, 107}, {EventsFrame, 109}, {EventsFrame, 8117}, {EventsFrame, 8192},
		{StdoutFrame, 0}, {StdoutFrame, 4097}, {FailureFrame, 23}, {FailureFrame, 25}, {SealFrame, 127}, {SealFrame, 129},
		{BootFrame, 8193}, {EventsFrame, ^uint32(0)},
	} {
		h := wireTestHeader(test.kind, test.payload)
		if n, ok := FrameMessageLength(h[:]); ok || n != 0 {
			t.Error("invalid advertised payload accepted")
		}
	}
	d, _ := NewTranscriptDecoder(syntheticBootstrap(), CheckpointPlan{})
	if _, ok := d.Decode(base[:]); ok {
		t.Error("header-only value became an authenticated frame")
	}
}

func TestTranscriptPayloadCodecCanonical(t *testing.T) {
	b := syntheticBootstrap()
	boot, ok := EncodeBootFrame(b)
	if !ok || boot.Length != 132 {
		t.Fatal("Boot64 did not encode")
	}
	if boot.Bytes[5] != 1 || boot.Bytes[4] != 1 || binary.BigEndian.Uint64(boot.Bytes[24:32]) != 1 {
		t.Error("Boot envelope changed")
	}
	wantBoot, _ := bootReport(b)
	d, _ := newDecoder(Owner, b.LaunchID, b.AuthenticationKey, true)
	got, ok := d.Decode(boot.Bytes[:boot.Length])
	if !ok || got.Boot != wantBoot {
		t.Fatal("Boot did not copy exactly")
	}
	checkpoint := CheckpointReport{Ordinal: 1, NonceDigest: [32]byte{3}, BarrierSequence: 2, PrefixCount: 2, LocalCount: 1, LifecycleCount: 1}
	cp, ok := EncodeCheckpointFrame(Owner, b.LaunchID, b.AuthenticationKey, 2, checkpoint)
	if !ok || cp.Length != 148 {
		t.Fatal("Checkpoint80 did not encode")
	}
	if got, ok := d.Decode(cp.Bytes[:cp.Length]); !ok || got.Checkpoint != checkpoint {
		t.Error("checkpoint payload roundtrip changed")
	}
	seal := SealReport{EntryEventSequence: 3, EventCount: 3, ReservedCount: 3, LocalCount: 1, LifecycleCount: 2,
		CheckpointCount: 1, FrameCount: 3, EntryOutcome: Success, TranscriptDigest: [32]byte{4}}
	encoded, ok := EncodeSealFrame(Owner, b.LaunchID, b.AuthenticationKey, 3, seal)
	if !ok || encoded.Length != 196 {
		t.Fatal("Seal128 did not encode")
	}
	if got, ok := d.Decode(encoded.Bytes[:encoded.Length]); !ok || got.Seal != seal {
		t.Error("seal payload roundtrip changed")
	}
	for _, offset := range []int{85, 86, 87, 88, 89, 90, 91, 92, 93, 94, 95} {
		p := encoded.Bytes[FrameHeaderSize : FrameHeaderSize+SealPayloadSize]
		var bad [SealPayloadSize]byte
		copy(bad[:], p)
		bad[offset] = 1
		if _, ok := decodeSealReport(Owner, bad[:]); ok {
			t.Error("noncanonical reserved/failure seal byte accepted")
		}
	}
	for mutation := 0; mutation < 11; mutation++ {
		bad := checkpoint
		switch mutation {
		case 0:
			bad.Ordinal = 0
		case 1:
			bad.Ordinal = 33
		case 2:
			bad.NonceDigest = [32]byte{}
		case 3:
			bad.BarrierSequence = 0
		case 4:
			bad.PrefixCount++
		case 5:
			bad.ResourceCount = ^uint64(0)
		case 6:
			bad.LocalCount = ^uint64(0)
		case 7:
			bad.LifecycleCount = 0
		case 8:
			bad.LocalCount++
		case 9:
			bad.BarrierSequence = 641
			bad.PrefixCount = 641
			bad.LifecycleCount = 640
		case 10:
			bad.LifecycleCount = ^uint64(0)
		}
		if _, ok := EncodeCheckpointFrame(Owner, b.LaunchID, b.AuthenticationKey, 2, bad); ok {
			t.Error("invalid checkpoint encoded")
		}
	}
	for mutation := 0; mutation < 14; mutation++ {
		bad := seal
		switch mutation {
		case 0:
			bad.EntryEventSequence = 0
		case 1:
			bad.ReservedCount++
		case 2:
			bad.ResourceCount = ^uint64(0)
		case 3:
			bad.LocalCount++
		case 4:
			bad.CheckpointCount = 33
		case 5:
			bad.StdoutByteCount = MaxOwnerStdoutBytes + 1
		case 6:
			bad.StdoutChunkCount = ^uint64(0)
		case 7:
			bad.FrameCount = MaxOwnerFrames + 1
		case 8:
			bad.EntryOutcome = 0
		case 9:
			bad.ProductExitCode = 1
		case 10:
			bad.EntryOutcome = TypedError
		case 11:
			bad.RecorderFailureFlags = Late
		case 12:
			bad.StdoutByteCount = 1
		case 13:
			bad.FrameCount++
		}
		if _, ok := EncodeSealFrame(Owner, b.LaunchID, b.AuthenticationKey, 3, bad); ok {
			t.Error("invalid seal encoded")
		}
	}
	seal.EntryOutcome, seal.ProductExitCode = TypedError, 1
	if _, ok := EncodeSealFrame(CLI, b.LaunchID, b.AuthenticationKey, 3, seal); !ok {
		t.Error("joined CLI typed error not representable")
	}
}

func TestTranscriptBootBindingAndDigest(t *testing.T) {
	b := syntheticBootstrap()
	boot, _ := EncodeBootFrame(b)
	for mutation := 0; mutation < 16; mutation++ {
		changed := b
		switch mutation {
		case 0:
			changed.LaunchID[0]++
		case 1:
			changed.AuthenticationKey[0]++
		case 2:
			changed.Challenge[1] = 1
		case 3:
			changed.SourceCommit[0]++
		case 4:
			changed.SourceTree[0]++
		case 5:
			changed.SourceManifestSHA256[0]++
		case 6:
			changed.ArgvSHA256[0]++
		case 7:
			changed.ExecutableSHA256[0]++
		case 8:
			changed.AssetSHA256[0]++
		case 9:
			changed.RoleDirectory = syntheticPath("/other/state/c")
		case 10:
			changed.WorkingDirectory = syntheticPath("/other")
		case 11:
			changed.RoleDirectoryIdentity.FileID[7]++
		case 12:
			changed.WorkingDirectoryIdentity.FileID[7]++
		case 13:
			changed.Schedule.EntryNanoseconds++
		case 14:
			changed.ChildPID++
		case 15:
			changed.Invocation.Role = RoleC1
		}
		d, ok := NewTranscriptDecoder(changed, CheckpointPlan{})
		if !ok {
			t.Fatal("binding mutation should remain syntactically valid")
		}
		if _, ok := d.Decode(boot.Bytes[:boot.Length]); ok {
			t.Error("Boot accepted mismatched immutable binding")
		}
	}
	for i := 0; i < int(boot.Length); i++ {
		bad := boot
		bad.Bytes[i] ^= 1
		d, _ := NewTranscriptDecoder(b, CheckpointPlan{})
		if _, ok := d.Decode(bad.Bytes[:bad.Length]); ok {
			t.Error("tampered Boot accepted")
		}
	}
	secretPayload := boot.Bytes[FrameHeaderSize : FrameHeaderSize+BootPayloadSize]
	for start := 0; start+32 <= len(secretPayload); start++ {
		var candidate [32]byte
		copy(candidate[:], secretPayload[start:start+32])
		if candidate == b.Challenge || candidate == b.AuthenticationKey {
			t.Error("raw bootstrap secret echoed")
		}
	}
	golden, ok := ParseOperationID("d46b9b676312819ba7d2df0182edc2351750dfef19a3386081c666a951f8cdee")
	if !ok || InitialTranscriptDigest() != golden {
		t.Fatal("transcript initial golden vector changed")
	}
	digest, ok := AdvanceTranscriptDigest(golden, boot.Bytes[:boot.Length])
	if !ok {
		t.Fatal("bounded complete frame did not hash")
	}
	h := sha256.New()
	_, _ = h.Write([]byte("sobalink-p1-chain-v1\x00"))
	_, _ = h.Write(golden[:])
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(boot.Length))
	_, _ = h.Write(length[:])
	_, _ = h.Write(boot.Bytes[:boot.Length])
	var expected [32]byte
	copy(expected[:], h.Sum(nil))
	if digest != expected {
		t.Error("transcript chain framing changed")
	}
	bad := boot
	bad.Bytes[boot.Length-1] ^= 1
	other, ok := AdvanceTranscriptDigest(golden, bad.Bytes[:bad.Length])
	if !ok || other == digest {
		t.Error("digest helper omitted MAC or claimed authentication")
	}
	for _, input := range [][]byte{nil, boot.Bytes[:boot.Length-1], boot.Bytes[:boot.Length+1]} {
		if _, ok := AdvanceTranscriptDigest(golden, input); ok {
			t.Error("incomplete/extra frame hashed")
		}
	}
	seal := SealReport{EntryEventSequence: 1, EventCount: 1, ReservedCount: 1, LifecycleCount: 1, FrameCount: 2, EntryOutcome: Success}
	sealed, _ := EncodeSealFrame(CLI, b.LaunchID, b.AuthenticationKey, 2, seal)
	if _, ok := AdvanceTranscriptDigest(golden, sealed.Bytes[:sealed.Length]); ok {
		t.Error("Seal included in its own digest")
	}
}
