//go:build resource_process_native

package processmodel

import (
	"crypto/sha256"
	"encoding/binary"
)

// BootstrapMessageLength checks only an advertised input shape. It neither
// authenticates a process nor reads or retains a message remainder.
func BootstrapMessageLength(header []byte) (uint16, bool) {
	return inputMessageLength(header, 1, 509, MaxEncodedBootstrapSize)
}

func inputMessageLength(header []byte, kind byte, minimum, maximum uint32) (uint16, bool) {
	if len(header) != InputHeaderSize || header[0] != 'P' || header[1] != '1' ||
		header[2] != 'I' || header[3] != 'N' || header[4] != 1 || header[5] != kind ||
		header[6] != 0 || header[7] != 0 {
		return 0, false
	}
	length := binary.BigEndian.Uint32(header[8:12])
	if length < minimum || length > maximum {
		return 0, false
	}
	return uint16(length), true
}

func encodeInputHeader(output []byte, kind byte, length uint16) {
	copy(output[:4], "P1IN")
	output[4], output[5] = 1, kind
	binary.BigEndian.PutUint32(output[8:12], uint32(length))
}

// encodeInputField is used only with validated fixed bounded field sizes and a
// zero-initialized encoded value. Its returned view never escapes the encoder.
func encodeInputField(output []byte, cursor *int, id uint16, length int) []byte {
	start := *cursor
	binary.BigEndian.PutUint16(output[start:start+2], id)
	binary.BigEndian.PutUint16(output[start+2:start+4], uint16(length))
	*cursor = start + 4 + length
	return output[start+4 : *cursor]
}

func decodeInputField(input []byte, cursor *int, id uint16, minimum, maximum int) ([]byte, bool) {
	start := *cursor
	if start < 0 || start > len(input) || len(input)-start < 4 {
		return nil, false
	}
	if binary.BigEndian.Uint16(input[start:start+2]) != id {
		return nil, false
	}
	length := int(binary.BigEndian.Uint16(input[start+2 : start+4]))
	if length < minimum || length > maximum || length > len(input)-start-4 {
		return nil, false
	}
	*cursor = start + 4 + length
	return input[start+4 : *cursor], true
}

func EncodeBootstrap(value Bootstrap) (EncodedBootstrap, bool) {
	if !validBootstrap(value) {
		return EncodedBootstrap{}, false
	}
	var out EncodedBootstrap
	out.Length = 507 + value.RoleDirectory.Length + value.WorkingDirectory.Length
	encodeInputHeader(out.Bytes[:], 1, out.Length)
	cursor := InputHeaderSize
	binary.BigEndian.PutUint16(encodeInputField(out.Bytes[:], &cursor, 1, 2), value.Scenario)
	encodeInputField(out.Bytes[:], &cursor, 2, 1)[0] = byte(value.Target)
	copy(encodeInputField(out.Bytes[:], &cursor, 3, 16), value.FixtureID[:])
	copy(encodeInputField(out.Bytes[:], &cursor, 4, 16), value.LaunchID[:])
	invocation := encodeInputField(out.Bytes[:], &cursor, 5, 4)
	invocation[0], invocation[1] = byte(value.Invocation.Mode), byte(value.Invocation.Role)
	invocation[2], invocation[3] = value.Invocation.Ordinal, byte(value.Invocation.Operation)
	binary.BigEndian.PutUint64(encodeInputField(out.Bytes[:], &cursor, 6, 8), value.SupervisorPID)
	binary.BigEndian.PutUint64(encodeInputField(out.Bytes[:], &cursor, 7, 8), value.ChildPID)
	copy(encodeInputField(out.Bytes[:], &cursor, 8, 20), value.SourceCommit[:])
	copy(encodeInputField(out.Bytes[:], &cursor, 9, 20), value.SourceTree[:])
	copy(encodeInputField(out.Bytes[:], &cursor, 10, 32), value.SourceManifestSHA256[:])
	copy(encodeInputField(out.Bytes[:], &cursor, 11, 32), value.ArgvSHA256[:])
	copy(encodeInputField(out.Bytes[:], &cursor, 12, 32), value.ExecutableSHA256[:])
	copy(encodeInputField(out.Bytes[:], &cursor, 13, 32), value.AssetSHA256[:])
	copy(encodeInputField(out.Bytes[:], &cursor, 14, int(value.RoleDirectory.Length)), value.RoleDirectory.Bytes[:value.RoleDirectory.Length])
	encodeDirectoryIdentity(encodeInputField(out.Bytes[:], &cursor, 15, 32), value.RoleDirectoryIdentity)
	copy(encodeInputField(out.Bytes[:], &cursor, 16, int(value.WorkingDirectory.Length)), value.WorkingDirectory.Bytes[:value.WorkingDirectory.Length])
	encodeDirectoryIdentity(encodeInputField(out.Bytes[:], &cursor, 17, 32), value.WorkingDirectoryIdentity)
	encodeScheduleBinding(encodeInputField(out.Bytes[:], &cursor, 18, 64), value.Schedule)
	copy(encodeInputField(out.Bytes[:], &cursor, 19, 32), value.Challenge[:])
	copy(encodeInputField(out.Bytes[:], &cursor, 20, 32), value.AuthenticationKey[:])
	return out, true
}

func encodeDirectoryIdentity(output []byte, value DirectoryIdentity) {
	output[0] = value.Kind
	binary.BigEndian.PutUint64(output[8:16], value.DeviceOrVolume)
	copy(output[16:32], value.FileID[:])
}

func encodeScheduleBinding(output []byte, value ScheduleBinding) {
	binary.BigEndian.PutUint64(output[0:8], value.EpochSeconds)
	binary.BigEndian.PutUint64(output[8:16], value.EntrySeconds)
	binary.BigEndian.PutUint32(output[16:20], value.EntryNanoseconds)
	binary.BigEndian.PutUint64(output[24:32], value.SetupCutoffSeconds)
	binary.BigEndian.PutUint64(output[32:40], value.WorkCutoffSeconds)
	binary.BigEndian.PutUint64(output[40:48], value.CleanupCutoffSeconds)
	binary.BigEndian.PutUint64(output[48:56], value.OuterCutoffSeconds)
	binary.BigEndian.PutUint64(output[56:64], value.OriginalGrantExpirySeconds)
}

func bootstrapFieldWidth(id uint16) (int, int) {
	switch id {
	case 1:
		return 2, 2
	case 2:
		return 1, 1
	case 3, 4:
		return 16, 16
	case 5:
		return 4, 4
	case 6, 7:
		return 8, 8
	case 8, 9:
		return 20, 20
	case 10, 11, 12, 13, 15, 17, 19, 20:
		return 32, 32
	case 14, 16:
		return 1, MaxPathBytes
	case 18:
		return 64, 64
	default:
		return 0, 0
	}
}

func DecodeBootstrap(input []byte) (Bootstrap, bool) {
	if len(input) < InputHeaderSize || len(input) > MaxBootstrapSize {
		return Bootstrap{}, false
	}
	length, ok := BootstrapMessageLength(input[:InputHeaderSize])
	if !ok || int(length) != len(input) {
		return Bootstrap{}, false
	}
	var value Bootstrap
	cursor := InputHeaderSize
	for id := uint16(1); id <= 20; id++ {
		minimum, maximum := bootstrapFieldWidth(id)
		field, fieldOK := decodeInputField(input, &cursor, id, minimum, maximum)
		if !fieldOK {
			return Bootstrap{}, false
		}
		switch id {
		case 1:
			value.Scenario = binary.BigEndian.Uint16(field)
		case 2:
			value.Target = Target(field[0])
		case 3:
			copy(value.FixtureID[:], field)
		case 4:
			copy(value.LaunchID[:], field)
		case 5:
			value.Invocation = Invocation{Mode: Mode(field[0]), Role: OwnerRole(field[1]), Ordinal: field[2], Operation: CLIOperation(field[3])}
		case 6:
			value.SupervisorPID = binary.BigEndian.Uint64(field)
		case 7:
			value.ChildPID = binary.BigEndian.Uint64(field)
		case 8:
			copy(value.SourceCommit[:], field)
		case 9:
			copy(value.SourceTree[:], field)
		case 10:
			copy(value.SourceManifestSHA256[:], field)
		case 11:
			copy(value.ArgvSHA256[:], field)
		case 12:
			copy(value.ExecutableSHA256[:], field)
		case 13:
			copy(value.AssetSHA256[:], field)
		case 14:
			value.RoleDirectory.Length = uint16(len(field))
			copy(value.RoleDirectory.Bytes[:], field)
		case 15, 17:
			identity, identityOK := decodeDirectoryIdentity(field)
			if !identityOK {
				return Bootstrap{}, false
			}
			if id == 15 {
				value.RoleDirectoryIdentity = identity
			} else {
				value.WorkingDirectoryIdentity = identity
			}
		case 16:
			value.WorkingDirectory.Length = uint16(len(field))
			copy(value.WorkingDirectory.Bytes[:], field)
		case 18:
			if field[20] != 0 || field[21] != 0 || field[22] != 0 || field[23] != 0 {
				return Bootstrap{}, false
			}
			value.Schedule = ScheduleBinding{
				EpochSeconds:               binary.BigEndian.Uint64(field[0:8]),
				EntrySeconds:               binary.BigEndian.Uint64(field[8:16]),
				EntryNanoseconds:           binary.BigEndian.Uint32(field[16:20]),
				SetupCutoffSeconds:         binary.BigEndian.Uint64(field[24:32]),
				WorkCutoffSeconds:          binary.BigEndian.Uint64(field[32:40]),
				CleanupCutoffSeconds:       binary.BigEndian.Uint64(field[40:48]),
				OuterCutoffSeconds:         binary.BigEndian.Uint64(field[48:56]),
				OriginalGrantExpirySeconds: binary.BigEndian.Uint64(field[56:64]),
			}
		case 19:
			copy(value.Challenge[:], field)
		case 20:
			copy(value.AuthenticationKey[:], field)
		}
	}
	if cursor != len(input) || !validBootstrap(value) {
		return Bootstrap{}, false
	}
	return value, true
}

func decodeDirectoryIdentity(input []byte) (DirectoryIdentity, bool) {
	for _, b := range input[1:8] {
		if b != 0 {
			return DirectoryIdentity{}, false
		}
	}
	value := DirectoryIdentity{Kind: input[0], DeviceOrVolume: binary.BigEndian.Uint64(input[8:16])}
	copy(value.FileID[:], input[16:32])
	return value, true
}

// BootstrapBindingDigest binds validated identity values without hashing the
// secret value bytes. It does not verify actual launch or filesystem facts.
func BootstrapBindingDigest(value Bootstrap) ([32]byte, bool) {
	encoded, ok := EncodeBootstrap(value)
	if !ok {
		return [32]byte{}, false
	}
	end := int(encoded.Length)
	for i := 0; i < 32; i++ {
		encoded.Bytes[end-68+i] = 0
		encoded.Bytes[end-32+i] = 0
	}
	h := sha256.New()
	_, _ = h.Write([]byte("sobalink-p1-binding-v1\x00"))
	_, _ = h.Write(encoded.Bytes[:encoded.Length])
	var digest [32]byte
	copy(digest[:], h.Sum(nil))
	return digest, true
}
