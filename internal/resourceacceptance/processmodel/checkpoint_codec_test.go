//go:build resource_process_native

package processmodel

import (
	"crypto/sha256"
	"encoding/binary"
	"testing"
)

func checkpointSyntheticRequest(launch [16]byte, ordinal uint64) CheckpointRequest {
	v := CheckpointRequest{LaunchID: launch, Ordinal: ordinal}
	v.Nonce[0] = byte(ordinal)
	v.Nonce[31] = 0x7f
	return v
}

func checkpointRequestBytes(t *testing.T, value CheckpointRequest) EncodedCheckpointRequest {
	t.Helper()
	encoded, ok := EncodeCheckpointRequest(value)
	if !ok {
		t.Fatal("synthetic checkpoint request encoding failed")
	}
	return encoded
}

func checkpointReject(t *testing.T, input []byte) {
	t.Helper()
	if got, ok := DecodeCheckpointRequest(input); ok || got != (CheckpointRequest{}) {
		t.Fatal("invalid checkpoint request returned a value")
	}
}

func TestCheckpointCodecGoldenAndCopies(t *testing.T) {
	v := CheckpointRequest{Ordinal: 1}
	for i := range v.LaunchID {
		v.LaunchID[i] = 0x11
	}
	for i := range v.Nonce {
		v.Nonce[i] = 0x22
	}
	const hex = "5031494e0102000000000050" +
		"0001001011111111111111111111111111111111" +
		"000200080000000000000001" +
		"000300202222222222222222222222222222222222222222222222222222222222222222"
	var golden [CheckpointRequestSize]byte
	if !decodeHex(hex, golden[:]) {
		t.Fatal("invalid fixed checkpoint vector")
	}
	encoded := checkpointRequestBytes(t, v)
	if encoded.Length != CheckpointRequestSize {
		t.Fatal("checkpoint request length mismatch")
	}
	for i, b := range golden {
		if encoded.Bytes[i] != b {
			t.Fatalf("checkpoint golden byte mismatch at %d", i)
		}
	}
	for _, b := range encoded.Bytes[encoded.Length:] {
		if b != 0 {
			t.Fatal("checkpoint encoded tail is nonzero")
		}
	}
	before := golden
	decoded, ok := DecodeCheckpointRequest(golden[:])
	if !ok || decoded != v || golden != before {
		t.Fatal("checkpoint round trip or input preservation failed")
	}
	if reencoded := checkpointRequestBytes(t, decoded); reencoded != encoded {
		t.Fatal("checkpoint reencoding changed bytes")
	}
	v.LaunchID[0], v.Nonce[0] = 3, 4
	golden[16], golden[48] = 5, 6
	if decoded.LaunchID[0] != 0x11 || decoded.Nonce[0] != 0x22 || encoded.Bytes[16] != 0x11 || encoded.Bytes[48] != 0x22 {
		t.Fatal("checkpoint value retained caller storage")
	}
	decoded.Nonce[0] = 7
	if encoded.Bytes[48] != 0x22 || golden[48] != 6 {
		t.Fatal("checkpoint output aliases encoded input")
	}
}

func TestCheckpointCodecHeaderAndMalformedInput(t *testing.T) {
	v := checkpointSyntheticRequest(syntheticBootstrap().LaunchID, 1)
	encoded := checkpointRequestBytes(t, v)
	for end := 0; end < int(encoded.Length); end++ {
		checkpointReject(t, encoded.Bytes[:end])
	}
	for size := 0; size < InputHeaderSize; size++ {
		if length, ok := CheckpointMessageLength(encoded.Bytes[:size]); ok || length != 0 {
			t.Fatal("short checkpoint header accepted")
		}
	}
	if length, ok := CheckpointMessageLength(encoded.Bytes[:InputHeaderSize+1]); ok || length != 0 {
		t.Fatal("extra checkpoint header byte accepted")
	}
	if length, ok := CheckpointMessageLength(encoded.Bytes[:InputHeaderSize]); !ok || length != CheckpointRequestSize {
		t.Fatal("canonical checkpoint header rejected")
	}
	for offset := 0; offset < 8; offset++ {
		bad := encoded
		bad.Bytes[offset] ^= 0xff
		checkpointReject(t, bad.Bytes[:bad.Length])
		if length, ok := CheckpointMessageLength(bad.Bytes[:InputHeaderSize]); ok || length != 0 {
			t.Fatal("malformed checkpoint header accepted")
		}
	}
	for _, advertised := range []uint32{0, 12, 79, 81, 255, 256, 257, ^uint32(0)} {
		bad := encoded
		binary.BigEndian.PutUint32(bad.Bytes[8:12], advertised)
		checkpointReject(t, bad.Bytes[:bad.Length])
		if length, ok := CheckpointMessageLength(bad.Bytes[:InputHeaderSize]); ok || length != 0 {
			t.Fatal("noncanonical checkpoint advertised length accepted")
		}
	}
	for _, size := range []int{81, 160, MaxCheckpointRequestSize} {
		bad := encoded
		if size == 160 {
			copy(bad.Bytes[80:160], encoded.Bytes[:80])
		}
		checkpointReject(t, bad.Bytes[:size])
		binary.BigEndian.PutUint32(bad.Bytes[8:12], uint32(size))
		checkpointReject(t, bad.Bytes[:size])
	}
	var oversized [MaxCheckpointRequestSize + 1]byte
	copy(oversized[:], encoded.Bytes[:])
	binary.BigEndian.PutUint32(oversized[8:12], uint32(len(oversized)))
	checkpointReject(t, oversized[:])
	for i, offset := range []int{12, 32, 44} {
		width := []uint16{16, 8, 32}[i]
		for _, id := range []uint16{0, uint16(i), uint16(i + 2), 0xffff} {
			bad := encoded
			binary.BigEndian.PutUint16(bad.Bytes[offset:offset+2], id)
			checkpointReject(t, bad.Bytes[:bad.Length])
		}
		for _, length := range []uint16{0, width - 1, width + 1, 0xffff} {
			bad := encoded
			binary.BigEndian.PutUint16(bad.Bytes[offset+2:offset+4], length)
			checkpointReject(t, bad.Bytes[:bad.Length])
		}
		bad := encoded
		for j := 0; j < int(width); j++ {
			bad.Bytes[offset+4+j] = 0
		}
		checkpointReject(t, bad.Bytes[:bad.Length])
		bad = encoded
		copy(bad.Bytes[offset:], encoded.Bytes[offset+4+int(width):encoded.Length])
		newLength := int(encoded.Length) - 4 - int(width)
		binary.BigEndian.PutUint32(bad.Bytes[8:12], uint32(newLength))
		checkpointReject(t, bad.Bytes[:newLength])
	}
	for _, ordinal := range []uint64{0, 33, ^uint64(0)} {
		bad := encoded
		binary.BigEndian.PutUint64(bad.Bytes[36:44], ordinal)
		checkpointReject(t, bad.Bytes[:bad.Length])
	}
	// Swap complete fields, preserving their IDs, widths, and the total length.
	reordered := encoded
	copy(reordered.Bytes[12:24], encoded.Bytes[32:44])
	copy(reordered.Bytes[24:44], encoded.Bytes[12:32])
	checkpointReject(t, reordered.Bytes[:reordered.Length])
	bad := encoded
	bad.Bytes[5] = 1
	checkpointReject(t, bad.Bytes[:bad.Length])
	if length, ok := CheckpointMessageLength(bad.Bytes[:InputHeaderSize]); ok || length != 0 {
		t.Fatal("bootstrap kind accepted by checkpoint helper")
	}
	before := encoded
	_, _ = CheckpointMessageLength(encoded.Bytes[:InputHeaderSize])
	if before != encoded {
		t.Fatal("checkpoint header helper changed input")
	}
}

func TestCheckpointCodecInvalidValuesAndNonceDigest(t *testing.T) {
	base := checkpointSyntheticRequest(syntheticBootstrap().LaunchID, 1)
	for change := 0; change < 5; change++ {
		bad := base
		switch change {
		case 0:
			bad.LaunchID = [16]byte{}
		case 1:
			bad.Ordinal = 0
		case 2:
			bad.Ordinal = 33
		case 3:
			bad.Ordinal = ^uint64(0)
		case 4:
			bad.Nonce = [32]byte{}
		}
		if got, ok := EncodeCheckpointRequest(bad); ok || got != (EncodedCheckpointRequest{}) {
			t.Fatal("invalid checkpoint value encoded")
		}
		if got, ok := CheckpointNonceDigest(bad); ok || got != [32]byte{} {
			t.Fatal("invalid checkpoint value hashed")
		}
	}
	digest, ok := CheckpointNonceDigest(base)
	if !ok || digest == [32]byte{} {
		t.Fatal("checkpoint nonce digest failed")
	}
	h := sha256.New()
	_, _ = h.Write([]byte("sobalink-p1-checkpoint-v1\x00"))
	_, _ = h.Write(base.LaunchID[:])
	_, _ = h.Write([]byte{0, 0, 0, 0, 0, 0, 0, 1})
	_, _ = h.Write(base.Nonce[:])
	var expected [32]byte
	copy(expected[:], h.Sum(nil))
	if expected != digest {
		t.Fatal("checkpoint digest domain or field framing mismatch")
	}
	for change := 0; change < 3; change++ {
		v := base
		switch change {
		case 0:
			v.LaunchID[0] ^= 0xff
		case 1:
			v.Ordinal++
		case 2:
			v.Nonce[0]++
		}
		changed, valid := CheckpointNonceDigest(v)
		if !valid || changed == digest {
			t.Fatal("checkpoint digest omitted a binding")
		}
	}
	base.Ordinal = 32
	encoded := checkpointRequestBytes(t, base)
	if got, valid := DecodeCheckpointRequest(encoded.Bytes[:encoded.Length]); !valid || got != base {
		t.Fatal("maximum standalone ordinal rejected")
	}
}

func TestCheckpointCodecRequestSequenceLimits(t *testing.T) {
	for _, mode := range []Mode{Owner, CLI} {
		bootstrap := syntheticBootstrap()
		if mode == CLI {
			bootstrap.Invocation = Invocation{Mode: CLI, Ordinal: 1, Operation: CLILocalStatus}
		}
		d, ok := NewCheckpointRequestDecoder(bootstrap)
		if !ok || d == nil || d.Failed() {
			t.Fatal("valid request decoder constructor failed")
		}
		limit := uint64(32)
		if mode == CLI {
			limit = 4
		}
		for ordinal := uint64(1); ordinal <= limit; ordinal++ {
			value := checkpointSyntheticRequest(bootstrap.LaunchID, ordinal)
			encoded := checkpointRequestBytes(t, value)
			got, accepted := d.Decode(encoded.Bytes[:encoded.Length])
			if !accepted || got != value || d.Failed() || d.nextOrdinal != ordinal+1 {
				t.Fatal("contiguous checkpoint request rejected")
			}
		}
		before := *d
		extra := checkpointRequestBytes(t, checkpointSyntheticRequest(bootstrap.LaunchID, 1))
		binary.BigEndian.PutUint64(extra.Bytes[36:44], limit+1)
		if got, accepted := d.Decode(extra.Bytes[:extra.Length]); accepted || got != (CheckpointRequest{}) || !d.Failed() {
			t.Fatal("checkpoint request limit did not fail closed")
		}
		if before.nextOrdinal != d.nextOrdinal || before.nonces != d.nonces {
			t.Fatal("excess request advanced decoder")
		}
	}
}

func TestCheckpointCodecStickyRejectionAndNoPartialAdvance(t *testing.T) {
	bootstrap := syntheticBootstrap()
	first := checkpointRequestBytes(t, checkpointSyntheticRequest(bootstrap.LaunchID, 1))
	second := checkpointRequestBytes(t, checkpointSyntheticRequest(bootstrap.LaunchID, 2))
	for change := 0; change < 9; change++ {
		d, ok := NewCheckpointRequestDecoder(bootstrap)
		if !ok {
			t.Fatal("request decoder constructor failed")
		}
		if _, accepted := d.Decode(first.Bytes[:first.Length]); !accepted {
			t.Fatal("first checkpoint request failed")
		}
		before := *d
		bad := second
		inputLength := int(bad.Length)
		switch change {
		case 0:
			bad.Bytes[16] ^= 0xff
		case 1:
			binary.BigEndian.PutUint64(bad.Bytes[36:44], 1)
		case 2:
			binary.BigEndian.PutUint64(bad.Bytes[36:44], 3)
		case 3:
			copy(bad.Bytes[48:80], first.Bytes[48:80])
		case 4:
			bad.Bytes[0] = 0
		case 5:
			inputLength--
		case 6:
			inputLength++
		case 7:
			bad.Bytes[5] = 1
		case 8:
			binary.BigEndian.PutUint16(bad.Bytes[44:46], 2)
		}
		if got, accepted := d.Decode(bad.Bytes[:inputLength]); accepted || got != (CheckpointRequest{}) || !d.Failed() {
			t.Fatalf("bad request %d did not fail closed", change)
		}
		if d.nextOrdinal != before.nextOrdinal || d.nonces != before.nonces || d.mode != before.mode || d.launch != before.launch {
			t.Fatal("rejected request partially advanced state")
		}
		if got, accepted := d.Decode(second.Bytes[:second.Length]); accepted || got != (CheckpointRequest{}) || !d.Failed() {
			t.Fatal("request decoder recovered from sticky failure")
		}
	}
	for _, ordinal := range []uint64{2, 32} {
		d, _ := NewCheckpointRequestDecoder(bootstrap)
		gap := checkpointRequestBytes(t, checkpointSyntheticRequest(bootstrap.LaunchID, ordinal))
		if got, ok := d.Decode(gap.Bytes[:gap.Length]); ok || got != (CheckpointRequest{}) || !d.Failed() || d.nextOrdinal != 1 || d.nonces != [32][32]byte{} {
			t.Fatal("initial ordinal gap accepted or advanced state")
		}
	}
}

func TestCheckpointCodecDecoderCopiesAndInvalidConstruction(t *testing.T) {
	bootstrap := syntheticBootstrap()
	original := bootstrap
	d, ok := NewCheckpointRequestDecoder(bootstrap)
	if !ok {
		t.Fatal("request decoder constructor failed")
	}
	bootstrap.LaunchID[0] ^= 0xff
	bootstrap.Invocation = Invocation{Mode: CLI, Ordinal: 1, Operation: CLILocalStatus}
	for ordinal := uint64(1); ordinal <= 5; ordinal++ {
		v := checkpointSyntheticRequest(original.LaunchID, ordinal)
		encoded := checkpointRequestBytes(t, v)
		got, accepted := d.Decode(encoded.Bytes[:encoded.Length])
		if !accepted || got != v {
			t.Fatal("request decoder retained mutable bootstrap")
		}
		encoded.Bytes[48] ^= 0xff
		got.Nonce[0] ^= 0xff
		if d.nonces[ordinal-1] != v.Nonce {
			t.Fatal("request decoder retained mutable input/output nonce")
		}
	}
	for _, invalid := range []Bootstrap{{}, {Scenario: ScenarioProcessRecovery}} {
		if got, valid := NewCheckpointRequestDecoder(invalid); valid || got != nil {
			t.Fatal("invalid request decoder constructed")
		}
	}
	bad := original
	bad.AuthenticationKey = [32]byte{}
	if got, valid := NewCheckpointRequestDecoder(bad); valid || got != nil {
		t.Fatal("request decoder bypassed complete bootstrap validation")
	}
	var zero CheckpointRequestDecoder
	if !zero.Failed() {
		t.Fatal("zero request decoder appeared valid")
	}
	if got, accepted := zero.Decode(nil); accepted || got != (CheckpointRequest{}) || !zero.Failed() || !zero.failed {
		t.Fatal("zero request decoder did not fail closed")
	}
	var absent *CheckpointRequestDecoder
	if !absent.Failed() {
		t.Fatal("nil request decoder appeared valid")
	}
	if got, accepted := absent.Decode(nil); accepted || got != (CheckpointRequest{}) {
		t.Fatal("nil request decoder did not fail closed")
	}
}

func TestCheckpointCodecPlanBoundsAndUnusedRows(t *testing.T) {
	for _, mode := range []Mode{Owner, CLI} {
		limit := 32
		if mode == CLI {
			limit = 4
		}
		for count := 0; count <= limit; count++ {
			plan := CheckpointPlan{Count: uint8(count)}
			for i := 0; i < count; i++ {
				plan.Nonces[i][0] = byte(i + 1)
			}
			before := plan
			if !validCheckpointPlan(mode, plan) || plan != before {
				t.Fatal("valid checkpoint plan rejected or mutated")
			}
			for i := count; i < len(plan.Nonces); i++ {
				bad := plan
				bad.Nonces[i][31] = 1
				if validCheckpointPlan(mode, bad) {
					t.Fatal("nonzero unused checkpoint nonce accepted")
				}
			}
			for i := 0; i < count; i++ {
				bad := plan
				bad.Nonces[i] = [32]byte{}
				if validCheckpointPlan(mode, bad) {
					t.Fatal("zero active checkpoint nonce accepted")
				}
				for j := 0; j < i; j++ {
					bad = plan
					bad.Nonces[i] = bad.Nonces[j]
					if validCheckpointPlan(mode, bad) {
						t.Fatal("repeated active checkpoint nonce accepted")
					}
				}
			}
		}
		for _, count := range []uint8{uint8(limit + 1), 33, 255} {
			if validCheckpointPlan(mode, CheckpointPlan{Count: count}) {
				t.Fatal("excess checkpoint plan count accepted")
			}
		}
	}
	for _, mode := range []Mode{0, 3, 255} {
		if validCheckpointPlan(mode, CheckpointPlan{}) {
			t.Fatal("unknown checkpoint plan mode accepted")
		}
	}
}
