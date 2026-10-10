//go:build resource_process_native

package processmodel

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
)

const (
	FrameHeaderSize = 36
	MaxFramePayload = 8192
	FrameMACSize    = 32
	MaxFrameSize    = FrameHeaderSize + MaxFramePayload + FrameMACSize
	MaxStdoutChunk  = 4096
)

type FrameKind uint8

// NewDecoder deliberately remains the Events/Stdout/Failure subset.
// Only NewTranscriptDecoder accepts the complete closed protocol.
const (
	BootFrame       FrameKind = 1
	EventsFrame     FrameKind = 2
	CheckpointFrame FrameKind = 3
	StdoutFrame     FrameKind = 4
	FailureFrame    FrameKind = 5
	SealFrame       FrameKind = 6
)

type FailureReport struct {
	Flags       FailureFlags
	Reserved    uint64
	PrefixCount uint64
}

type EncodedFrame struct {
	Bytes  [MaxFrameSize]byte
	Length uint16
}

type DecodedFrame struct {
	Kind         FrameKind
	Sequence     uint64
	Batch        EventBatch
	Stdout       [MaxStdoutChunk]byte
	StdoutLength uint16
	Failure      FailureReport
	Boot         BootReport
	Checkpoint   CheckpointReport
	Seal         SealReport
}

// FrameMessageLength checks only an advertised fixed header. It authenticates
// nothing, allocates nothing, and does not read or retain any payload bytes.
func FrameMessageLength(header []byte) (uint16, bool) {
	if len(header) != FrameHeaderSize || header[0] != 'P' || header[1] != '1' || header[2] != 'E' || header[3] != 'V' || header[4] != 1 || header[6] != 0 || header[7] != 0 {
		return 0, false
	}
	var launch [16]byte
	copy(launch[:], header[8:24])
	sequence := binary.BigEndian.Uint64(header[24:32])
	length := binary.BigEndian.Uint32(header[32:36])
	if launch == [16]byte{} || sequence == 0 || sequence == ^uint64(0) || length > MaxFramePayload {
		return 0, false
	}
	switch FrameKind(header[5]) {
	case BootFrame:
		if length != BootPayloadSize {
			return 0, false
		}
	case EventsFrame:
		if length < 4+EventSize || length > MaxEventPayload || (length-4)%EventSize != 0 {
			return 0, false
		}
	case CheckpointFrame:
		if length != CheckpointPayloadSize {
			return 0, false
		}
	case StdoutFrame:
		if length == 0 || length > MaxStdoutChunk {
			return 0, false
		}
	case FailureFrame:
		if length != 24 {
			return 0, false
		}
	case SealFrame:
		if length != SealPayloadSize {
			return 0, false
		}
	default:
		return 0, false
	}
	return uint16(FrameHeaderSize + length + FrameMACSize), true
}

func EncodeEventFrame(mode Mode, launch [16]byte, key [32]byte, sequence uint64, events []Event) (EncodedFrame, bool) {
	payload, ok := EncodeEventBatch(mode, events)
	if !ok {
		return EncodedFrame{}, false
	}
	return encodeFrame(mode, launch, key, sequence, EventsFrame, payload.Bytes[:payload.Length])
}

func EncodeStdoutFrame(mode Mode, launch [16]byte, key [32]byte, sequence uint64, stdout []byte) (EncodedFrame, bool) {
	if len(stdout) == 0 || len(stdout) > MaxStdoutChunk {
		return EncodedFrame{}, false
	}
	return encodeFrame(mode, launch, key, sequence, StdoutFrame, stdout)
}

func EncodeFailureFrame(mode Mode, launch [16]byte, key [32]byte, sequence uint64, failure FailureReport) (EncodedFrame, bool) {
	if !validFailureReport(mode, failure) {
		return EncodedFrame{}, false
	}
	var payload [24]byte
	binary.BigEndian.PutUint32(payload[:4], uint32(failure.Flags))
	binary.BigEndian.PutUint64(payload[8:16], failure.Reserved)
	binary.BigEndian.PutUint64(payload[16:24], failure.PrefixCount)
	return encodeFrame(mode, launch, key, sequence, FailureFrame, payload[:])
}

func validFailureReport(mode Mode, failure FailureReport) bool {
	return mode.capacity() != 0 && validFailureFlags(failure.Flags) && failure.PrefixCount <= mode.capacity() && failure.PrefixCount <= failure.Reserved
}

func encodeFrame(mode Mode, launch [16]byte, key [32]byte, sequence uint64, kind FrameKind, payload []byte) (EncodedFrame, bool) {
	var out EncodedFrame
	if mode.capacity() == 0 || launch == [16]byte{} || key == [32]byte{} || sequence == 0 || sequence == ^uint64(0) || len(payload) > MaxFramePayload {
		return out, false
	}
	copy(out.Bytes[:4], "P1EV")
	out.Bytes[4], out.Bytes[5] = 1, byte(kind)
	copy(out.Bytes[8:24], launch[:])
	binary.BigEndian.PutUint64(out.Bytes[24:32], sequence)
	binary.BigEndian.PutUint32(out.Bytes[32:36], uint32(len(payload)))
	end := FrameHeaderSize + len(payload)
	copy(out.Bytes[FrameHeaderSize:end], payload)
	mac := hmac.New(sha256.New, key[:])
	_, _ = mac.Write(out.Bytes[:end])
	copy(out.Bytes[end:end+FrameMACSize], mac.Sum(nil))
	out.Length = uint16(end + FrameMACSize)
	return out, true
}

// Decoder is single-owner state, not safe for concurrent use. Constructor values
// are immutable, parsing failures are sticky, and there is no reset. Decoding
// does not authenticate an actual process or produce a live ownership seal.
type Decoder struct {
	extended         bool
	mode             Mode
	launch           [16]byte
	key              [32]byte
	nextFrame        uint64
	nextEvent        uint64
	categoryCounts   [3]uint64
	failed           bool
	evidenceFailures FailureFlags
}

func NewDecoder(mode Mode, launch [16]byte, key [32]byte) (*Decoder, bool) {
	return newDecoder(mode, launch, key, false)
}

func newDecoder(mode Mode, launch [16]byte, key [32]byte, extended bool) (*Decoder, bool) {
	if mode.capacity() == 0 || launch == [16]byte{} || key == [32]byte{} {
		return nil, false
	}
	return &Decoder{extended: extended, mode: mode, launch: launch, key: key, nextFrame: 1, nextEvent: 1}, true
}

func (d *Decoder) Failed() bool { return d.failed }

// EvidenceFailures retains authenticated Failure-frame bits separately from
// malformed-stream failure. Neither is a product command or result.
func (d *Decoder) EvidenceFailures() FailureFlags { return d.evidenceFailures }

func (d *Decoder) Decode(input []byte) (DecodedFrame, bool) {
	if d.failed {
		return DecodedFrame{}, false
	}
	frame, ok := d.decode(input)
	if !ok {
		d.failed = true
		return DecodedFrame{}, false
	}
	// Category bounds apply to the whole authenticated stream, not merely to
	// each batch. Reject without partially advancing any accepted counters.
	counts := d.categoryCounts
	if frame.Kind == EventsFrame {
		for i := uint16(0); i < frame.Batch.Count; i++ {
			category := eventCategory(frame.Batch.Events[i].Observation.Kind)
			if category == invalidCategory {
				d.failed = true
				return DecodedFrame{}, false
			}
			limit := uint64(MaxLifecycleEvents)
			if category == resourceCategory {
				limit = MaxResourceEvents
			} else if category == localCategory {
				limit = MaxLocalEvents
			}
			if d.mode == CLI {
				limit = MaxCLIEvents
			}
			counts[category]++
			if counts[category] > limit {
				d.failed = true
				return DecodedFrame{}, false
			}
		}
	}
	d.categoryCounts = counts
	d.nextFrame++
	if frame.Kind == EventsFrame {
		d.nextEvent += uint64(frame.Batch.Count)
	}
	if frame.Kind == FailureFrame {
		d.evidenceFailures |= frame.Failure.Flags
	}
	return frame, true
}

func (d *Decoder) decode(input []byte) (DecodedFrame, bool) {
	var frame DecodedFrame
	if d.mode.capacity() == 0 || d.nextFrame == 0 || d.nextFrame == ^uint64(0) || len(input) < FrameHeaderSize+FrameMACSize || len(input) > MaxFrameSize {
		return frame, false
	}
	length, ok := FrameMessageLength(input[:FrameHeaderSize])
	if !ok || int(length) != len(input) {
		return frame, false
	}
	frame.Kind = FrameKind(input[5])
	if !d.extended && frame.Kind != EventsFrame && frame.Kind != StdoutFrame && frame.Kind != FailureFrame {
		return DecodedFrame{}, false
	}
	var launch [16]byte
	copy(launch[:], input[8:24])
	frame.Sequence = binary.BigEndian.Uint64(input[24:32])
	payloadLength := binary.BigEndian.Uint32(input[32:36])
	if launch != d.launch || frame.Sequence != d.nextFrame || payloadLength > MaxFramePayload || uint64(len(input)) != uint64(FrameHeaderSize+FrameMACSize)+uint64(payloadLength) {
		return DecodedFrame{}, false
	}
	end := FrameHeaderSize + int(payloadLength)
	mac := hmac.New(sha256.New, d.key[:])
	_, _ = mac.Write(input[:end])
	if !hmac.Equal(input[end:], mac.Sum(nil)) {
		return DecodedFrame{}, false
	}
	payload := input[FrameHeaderSize:end]
	switch frame.Kind {
	case BootFrame:
		copy(frame.Boot.BindingDigest[:], payload[:32])
		copy(frame.Boot.ChallengeProof[:], payload[32:])
	case CheckpointFrame:
		value, ok := decodeCheckpointReport(d.mode, payload)
		if !ok {
			return DecodedFrame{}, false
		}
		frame.Checkpoint = value
	case SealFrame:
		value, ok := decodeSealReport(d.mode, payload)
		if !ok {
			return DecodedFrame{}, false
		}
		frame.Seal = value
	case EventsFrame:
		batch, ok := DecodeEventBatch(d.mode, payload)
		if !ok || batch.Events[0].Sequence != d.nextEvent {
			return DecodedFrame{}, false
		}
		frame.Batch = batch
	case StdoutFrame:
		if len(payload) == 0 || len(payload) > MaxStdoutChunk {
			return DecodedFrame{}, false
		}
		copy(frame.Stdout[:], payload)
		frame.StdoutLength = uint16(len(payload))
	case FailureFrame:
		if len(payload) != 24 || payload[4] != 0 || payload[5] != 0 || payload[6] != 0 || payload[7] != 0 {
			return DecodedFrame{}, false
		}
		frame.Failure = FailureReport{Flags: FailureFlags(binary.BigEndian.Uint32(payload[:4])), Reserved: binary.BigEndian.Uint64(payload[8:16]), PrefixCount: binary.BigEndian.Uint64(payload[16:24])}
		if !validFailureReport(d.mode, frame.Failure) {
			return DecodedFrame{}, false
		}
	}
	return frame, true
}
