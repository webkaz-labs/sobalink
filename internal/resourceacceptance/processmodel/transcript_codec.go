//go:build resource_process_native

package processmodel

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
)

const (
	BootPayloadSize       = 64
	CheckpointPayloadSize = 80
	SealPayloadSize       = 128
	MaxOwnerStdoutBytes   = 1 << 20
	MaxCLIStdoutBytes     = 64 << 10
	MaxOwnerStdoutFrames  = 1024
	MaxCLIStdoutFrames    = 32
	MaxOwnerObserverBytes = 256 << 10
	MaxCLIObserverBytes   = 16 << 10
	MaxOwnerFrames        = 1703
	MaxCLIFrames          = 107
)

type BootReport struct {
	BindingDigest  [32]byte
	ChallengeProof [32]byte
}

type CheckpointReport struct {
	Ordinal         uint64
	NonceDigest     [32]byte
	BarrierSequence uint64
	PrefixCount     uint64
	ResourceCount   uint64
	LocalCount      uint64
	LifecycleCount  uint64
}

// SealReport is only an authenticated value. It cannot certify a live recorder,
// pipe EOF, a task join, process exit, or the absence of a future late caller.
type SealReport struct {
	EntryEventSequence   uint64
	EventCount           uint64
	ReservedCount        uint64
	ResourceCount        uint64
	LocalCount           uint64
	LifecycleCount       uint64
	CheckpointCount      uint64
	StdoutByteCount      uint64
	StdoutChunkCount     uint64
	FrameCount           uint64
	ProductExitCode      uint32
	EntryOutcome         Outcome
	RecorderFailureFlags FailureFlags
	TranscriptDigest     [32]byte
}

func bootReport(v Bootstrap) (BootReport, bool) {
	binding, ok := BootstrapBindingDigest(v)
	if !ok {
		return BootReport{}, false
	}
	result := BootReport{BindingDigest: binding}
	mac := hmac.New(sha256.New, v.AuthenticationKey[:])
	_, _ = mac.Write([]byte("sobalink-p1-boot-v1\x00"))
	_, _ = mac.Write(v.Challenge[:])
	_, _ = mac.Write(binding[:])
	copy(result.ChallengeProof[:], mac.Sum(nil))
	return result, true
}

func EncodeBootFrame(v Bootstrap) (EncodedFrame, bool) {
	report, ok := bootReport(v)
	if !ok {
		return EncodedFrame{}, false
	}
	var payload [BootPayloadSize]byte
	copy(payload[:32], report.BindingDigest[:])
	copy(payload[32:], report.ChallengeProof[:])
	return encodeFrame(v.Invocation.Mode, v.LaunchID, v.AuthenticationKey, 1, BootFrame, payload[:])
}

func checkpointLimit(mode Mode) uint64 {
	switch mode {
	case Owner:
		return 32
	case CLI:
		return 4
	default:
		return 0
	}
}

func validReportCounts(mode Mode, total, resource, local, lifecycle uint64) bool {
	if mode.capacity() == 0 || total > mode.capacity() || resource > total || local > total-resource || lifecycle != total-resource-local {
		return false
	}
	if mode == CLI {
		return resource == 0 && local <= MaxCLIEvents && lifecycle <= MaxCLIEvents
	}
	return resource <= MaxResourceEvents && local <= MaxLocalEvents && lifecycle <= MaxLifecycleEvents
}

func validCheckpointReport(mode Mode, v CheckpointReport) bool {
	return v.Ordinal > 0 && v.Ordinal <= checkpointLimit(mode) && v.NonceDigest != [32]byte{} &&
		v.BarrierSequence > 0 && v.BarrierSequence == v.PrefixCount && v.LifecycleCount > 0 &&
		validReportCounts(mode, v.PrefixCount, v.ResourceCount, v.LocalCount, v.LifecycleCount)
}

func EncodeCheckpointFrame(mode Mode, launch [16]byte, key [32]byte, sequence uint64, v CheckpointReport) (EncodedFrame, bool) {
	if !validCheckpointReport(mode, v) {
		return EncodedFrame{}, false
	}
	var p [CheckpointPayloadSize]byte
	binary.BigEndian.PutUint64(p[:8], v.Ordinal)
	copy(p[8:40], v.NonceDigest[:])
	binary.BigEndian.PutUint64(p[40:48], v.BarrierSequence)
	binary.BigEndian.PutUint64(p[48:56], v.PrefixCount)
	binary.BigEndian.PutUint64(p[56:64], v.ResourceCount)
	binary.BigEndian.PutUint64(p[64:72], v.LocalCount)
	binary.BigEndian.PutUint64(p[72:80], v.LifecycleCount)
	return encodeFrame(mode, launch, key, sequence, CheckpointFrame, p[:])
}

func decodeCheckpointReport(mode Mode, p []byte) (CheckpointReport, bool) {
	if len(p) != CheckpointPayloadSize {
		return CheckpointReport{}, false
	}
	v := CheckpointReport{
		Ordinal:         binary.BigEndian.Uint64(p[:8]),
		BarrierSequence: binary.BigEndian.Uint64(p[40:48]),
		PrefixCount:     binary.BigEndian.Uint64(p[48:56]),
		ResourceCount:   binary.BigEndian.Uint64(p[56:64]),
		LocalCount:      binary.BigEndian.Uint64(p[64:72]),
		LifecycleCount:  binary.BigEndian.Uint64(p[72:80]),
	}
	copy(v.NonceDigest[:], p[8:40])
	if !validCheckpointReport(mode, v) {
		return CheckpointReport{}, false
	}
	return v, true
}

func stdoutLimits(mode Mode) (bytes, chunks, observer, frames uint64) {
	switch mode {
	case Owner:
		return MaxOwnerStdoutBytes, MaxOwnerStdoutFrames, MaxOwnerObserverBytes, MaxOwnerFrames
	case CLI:
		return MaxCLIStdoutBytes, MaxCLIStdoutFrames, MaxCLIObserverBytes, MaxCLIFrames
	default:
		return 0, 0, 0, 0
	}
}

func validSealReport(mode Mode, v SealReport) bool {
	stdout, chunks, _, frames := stdoutLimits(mode)
	if v.EntryEventSequence == 0 || v.EntryEventSequence != v.EventCount || v.ReservedCount != v.EventCount ||
		!validReportCounts(mode, v.EventCount, v.ResourceCount, v.LocalCount, v.LifecycleCount) || v.LifecycleCount == 0 ||
		v.CheckpointCount > checkpointLimit(mode) || v.StdoutByteCount > stdout || v.StdoutChunkCount > chunks ||
		v.StdoutByteCount < v.StdoutChunkCount || v.StdoutByteCount > v.StdoutChunkCount*MaxStdoutChunk ||
		v.FrameCount == 0 || v.FrameCount > frames || v.RecorderFailureFlags != 0 ||
		v.EntryOutcome < Success || v.EntryOutcome > Failure {
		return false
	}
	if v.EntryOutcome == Success {
		return v.ProductExitCode == 0
	}
	return v.ProductExitCode == 1
}

func EncodeSealFrame(mode Mode, launch [16]byte, key [32]byte, sequence uint64, v SealReport) (EncodedFrame, bool) {
	if !validSealReport(mode, v) || v.FrameCount != sequence {
		return EncodedFrame{}, false
	}
	var p [SealPayloadSize]byte
	binary.BigEndian.PutUint64(p[:8], v.EntryEventSequence)
	binary.BigEndian.PutUint64(p[8:16], v.EventCount)
	binary.BigEndian.PutUint64(p[16:24], v.ReservedCount)
	binary.BigEndian.PutUint64(p[24:32], v.ResourceCount)
	binary.BigEndian.PutUint64(p[32:40], v.LocalCount)
	binary.BigEndian.PutUint64(p[40:48], v.LifecycleCount)
	binary.BigEndian.PutUint64(p[48:56], v.CheckpointCount)
	binary.BigEndian.PutUint64(p[56:64], v.StdoutByteCount)
	binary.BigEndian.PutUint64(p[64:72], v.StdoutChunkCount)
	binary.BigEndian.PutUint64(p[72:80], v.FrameCount)
	binary.BigEndian.PutUint32(p[80:84], v.ProductExitCode)
	p[84] = byte(v.EntryOutcome)
	// Reserved bytes and the required zero failure flags stay zero.
	copy(p[96:128], v.TranscriptDigest[:])
	return encodeFrame(mode, launch, key, sequence, SealFrame, p[:])
}

func decodeSealReport(mode Mode, p []byte) (SealReport, bool) {
	if len(p) != SealPayloadSize {
		return SealReport{}, false
	}
	for _, offset := range [...]int{85, 86, 87, 92, 93, 94, 95} {
		if p[offset] != 0 {
			return SealReport{}, false
		}
	}
	v := SealReport{
		EntryEventSequence:   binary.BigEndian.Uint64(p[:8]),
		EventCount:           binary.BigEndian.Uint64(p[8:16]),
		ReservedCount:        binary.BigEndian.Uint64(p[16:24]),
		ResourceCount:        binary.BigEndian.Uint64(p[24:32]),
		LocalCount:           binary.BigEndian.Uint64(p[32:40]),
		LifecycleCount:       binary.BigEndian.Uint64(p[40:48]),
		CheckpointCount:      binary.BigEndian.Uint64(p[48:56]),
		StdoutByteCount:      binary.BigEndian.Uint64(p[56:64]),
		StdoutChunkCount:     binary.BigEndian.Uint64(p[64:72]),
		FrameCount:           binary.BigEndian.Uint64(p[72:80]),
		ProductExitCode:      binary.BigEndian.Uint32(p[80:84]),
		EntryOutcome:         Outcome(p[84]),
		RecorderFailureFlags: FailureFlags(binary.BigEndian.Uint32(p[88:92])),
	}
	copy(v.TranscriptDigest[:], p[96:128])
	if !validSealReport(mode, v) {
		return SealReport{}, false
	}
	return v, true
}

func InitialTranscriptDigest() [32]byte {
	return sha256.Sum256([]byte("sobalink-p1-transcript-v1\x00"))
}

// AdvanceTranscriptDigest hashes a bounded representation only. It does not
// authenticate the frame. The full decoder calls it after HMAC acceptance.
func AdvanceTranscriptDigest(previous [32]byte, input []byte) ([32]byte, bool) {
	if len(input) < FrameHeaderSize+FrameMACSize || len(input) > MaxFrameSize {
		return [32]byte{}, false
	}
	length, ok := FrameMessageLength(input[:FrameHeaderSize])
	if !ok || int(length) != len(input) || FrameKind(input[5]) == SealFrame {
		return [32]byte{}, false
	}
	h := sha256.New()
	_, _ = h.Write([]byte("sobalink-p1-chain-v1\x00"))
	_, _ = h.Write(previous[:])
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(input)))
	_, _ = h.Write(size[:])
	_, _ = h.Write(input)
	var digest [32]byte
	copy(digest[:], h.Sum(nil))
	return digest, true
}
