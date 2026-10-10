//go:build resource_process_native

package processmodel

import (
	"crypto/sha256"
	"encoding/binary"
)

type CheckpointRequest struct {
	LaunchID [16]byte
	Ordinal  uint64
	Nonce    [32]byte
}

// CheckpointPlan is a copied finite set of passive observations. These values
// neither request a product action nor prove a request was sent or timely.
type CheckpointPlan struct {
	Count  uint8
	Nonces [32][32]byte
}

func validCheckpointPlan(mode Mode, plan CheckpointPlan) bool {
	limit := checkpointLimit(mode)
	if limit == 0 || uint64(plan.Count) > limit {
		return false
	}
	for i, nonce := range plan.Nonces {
		if i >= int(plan.Count) {
			if nonce != [32]byte{} {
				return false
			}
			continue
		}
		if nonce == [32]byte{} {
			return false
		}
		for j := 0; j < i; j++ {
			if nonce == plan.Nonces[j] {
				return false
			}
		}
	}
	return true
}

func validCheckpointRequest(value CheckpointRequest) bool {
	return value.LaunchID != [16]byte{} && value.Ordinal > 0 && value.Ordinal <= 32 && value.Nonce != [32]byte{}
}

// CheckpointMessageLength checks only the fixed advertised input shape.
func CheckpointMessageLength(header []byte) (uint16, bool) {
	return inputMessageLength(header, 2, CheckpointRequestSize, CheckpointRequestSize)
}

func EncodeCheckpointRequest(value CheckpointRequest) (EncodedCheckpointRequest, bool) {
	if !validCheckpointRequest(value) {
		return EncodedCheckpointRequest{}, false
	}
	var out EncodedCheckpointRequest
	out.Length = CheckpointRequestSize
	encodeInputHeader(out.Bytes[:], 2, out.Length)
	cursor := InputHeaderSize
	copy(encodeInputField(out.Bytes[:], &cursor, 1, 16), value.LaunchID[:])
	binary.BigEndian.PutUint64(encodeInputField(out.Bytes[:], &cursor, 2, 8), value.Ordinal)
	copy(encodeInputField(out.Bytes[:], &cursor, 3, 32), value.Nonce[:])
	return out, true
}

func DecodeCheckpointRequest(input []byte) (CheckpointRequest, bool) {
	if len(input) < InputHeaderSize || len(input) > MaxCheckpointRequestSize {
		return CheckpointRequest{}, false
	}
	length, ok := CheckpointMessageLength(input[:InputHeaderSize])
	if !ok || int(length) != len(input) {
		return CheckpointRequest{}, false
	}
	var value CheckpointRequest
	cursor := InputHeaderSize
	launch, ok := decodeInputField(input, &cursor, 1, 16, 16)
	if !ok {
		return CheckpointRequest{}, false
	}
	copy(value.LaunchID[:], launch)
	ordinal, ok := decodeInputField(input, &cursor, 2, 8, 8)
	if !ok {
		return CheckpointRequest{}, false
	}
	value.Ordinal = binary.BigEndian.Uint64(ordinal)
	nonce, ok := decodeInputField(input, &cursor, 3, 32, 32)
	if !ok {
		return CheckpointRequest{}, false
	}
	copy(value.Nonce[:], nonce)
	if cursor != len(input) || !validCheckpointRequest(value) {
		return CheckpointRequest{}, false
	}
	return value, true
}

// CheckpointRequestDecoder is single-owner, non-concurrent passive state. It
// retains only copied launch/mode and a bounded accepted-nonce ledger.
type CheckpointRequestDecoder struct {
	mode        Mode
	launch      [16]byte
	nextOrdinal uint64
	nonces      [32][32]byte
	failed      bool
}

func NewCheckpointRequestDecoder(value Bootstrap) (*CheckpointRequestDecoder, bool) {
	if !validBootstrap(value) {
		return nil, false
	}
	return &CheckpointRequestDecoder{mode: value.Invocation.Mode, launch: value.LaunchID, nextOrdinal: 1}, true
}

func (d *CheckpointRequestDecoder) Failed() bool {
	return d == nil || d.failed || checkpointLimit(d.mode) == 0 || d.launch == [16]byte{} ||
		d.nextOrdinal == 0 || d.nextOrdinal > checkpointLimit(d.mode)+1
}

func (d *CheckpointRequestDecoder) Decode(input []byte) (CheckpointRequest, bool) {
	if d == nil {
		return CheckpointRequest{}, false
	}
	if d.Failed() || d.nextOrdinal > checkpointLimit(d.mode) {
		d.failed = true
		return CheckpointRequest{}, false
	}
	value, ok := DecodeCheckpointRequest(input)
	if !ok || value.LaunchID != d.launch || value.Ordinal != d.nextOrdinal {
		d.failed = true
		return CheckpointRequest{}, false
	}
	for i := uint64(0); i < d.nextOrdinal-1; i++ {
		if value.Nonce == d.nonces[i] {
			d.failed = true
			return CheckpointRequest{}, false
		}
	}
	d.nonces[d.nextOrdinal-1] = value.Nonce
	d.nextOrdinal++
	return value, true
}

func CheckpointNonceDigest(value CheckpointRequest) ([32]byte, bool) {
	if !validCheckpointRequest(value) {
		return [32]byte{}, false
	}
	h := sha256.New()
	_, _ = h.Write([]byte("sobalink-p1-checkpoint-v1\x00"))
	_, _ = h.Write(value.LaunchID[:])
	var ordinal [8]byte
	binary.BigEndian.PutUint64(ordinal[:], value.Ordinal)
	_, _ = h.Write(ordinal[:])
	_, _ = h.Write(value.Nonce[:])
	var digest [32]byte
	copy(digest[:], h.Sum(nil))
	return digest, true
}
