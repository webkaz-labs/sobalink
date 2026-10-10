//go:build resource_process_native

package processmodel

import (
	"crypto/sha256"
	"encoding/binary"
	"testing"
)

func bootstrapGoldenValue() Bootstrap {
	v := Bootstrap{
		Scenario:                 ScenarioProcessRecovery,
		Target:                   LinuxAMD64,
		Invocation:               Invocation{Mode: Owner, Role: RoleC0},
		SupervisorPID:            10,
		ChildPID:                 11,
		RoleDirectory:            syntheticPath("/r"),
		WorkingDirectory:         syntheticPath("/w"),
		RoleDirectoryIdentity:    DirectoryIdentity{Kind: UnixDirectoryIdentity},
		WorkingDirectoryIdentity: DirectoryIdentity{Kind: UnixDirectoryIdentity},
		Schedule: ScheduleBinding{EpochSeconds: 1, EntrySeconds: 2, EntryNanoseconds: 3,
			SetupCutoffSeconds: 91, WorkCutoffSeconds: 301, CleanupCutoffSeconds: 331,
			OuterCutoffSeconds: 361, OriginalGrantExpirySeconds: 361},
	}
	for i := range v.FixtureID {
		v.FixtureID[i], v.LaunchID[i] = 1, 2
	}
	for i := range v.SourceCommit {
		v.SourceCommit[i], v.SourceTree[i] = 3, 4
	}
	for i := range v.SourceManifestSHA256 {
		v.SourceManifestSHA256[i], v.ArgvSHA256[i] = 5, 6
		v.ExecutableSHA256[i], v.AssetSHA256[i] = 7, 8
		v.Challenge[i], v.AuthenticationKey[i] = 9, 10
	}
	v.RoleDirectoryIdentity.FileID[7] = 1
	v.WorkingDirectoryIdentity.FileID[7] = 2
	return v
}

func bootstrapGoldenBytes(t *testing.T) [511]byte {
	t.Helper()
	const golden = "5031494e01010000000001ff" +
		"000100020001" +
		"0002000101" +
		"0003001001010101010101010101010101010101" +
		"0004001002020202020202020202020202020202" +
		"0005000401010000" +
		"00060008000000000000000a" +
		"00070008000000000000000b" +
		"000800140303030303030303030303030303030303030303" +
		"000900140404040404040404040404040404040404040404" +
		"000a00200505050505050505050505050505050505050505050505050505050505050505" +
		"000b00200606060606060606060606060606060606060606060606060606060606060606" +
		"000c00200707070707070707070707070707070707070707070707070707070707070707" +
		"000d00200808080808080808080808080808080808080808080808080808080808080808" +
		"000e00022f72" +
		"000f00200100000000000000000000000000000000000000000000010000000000000000" +
		"001000022f77" +
		"001100200100000000000000000000000000000000000000000000020000000000000000" +
		"00120040000000000000000100000000000000020000000300000000" +
		"000000000000005b000000000000012d000000000000014b00000000000001690000000000000169" +
		"001300200909090909090909090909090909090909090909090909090909090909090909" +
		"001400200a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a"
	var bytes [511]byte
	if !decodeHex(golden, bytes[:]) {
		t.Fatal("invalid fixed bootstrap vector")
	}
	return bytes
}

func bootstrapFieldOffset(t *testing.T, input []byte, want uint16) int {
	t.Helper()
	cursor := InputHeaderSize
	for id := uint16(1); id <= 20; id++ {
		if cursor > len(input)-4 || binary.BigEndian.Uint16(input[cursor:cursor+2]) != id {
			t.Fatal("invalid canonical bootstrap field layout")
		}
		if id == want {
			return cursor
		}
		cursor += 4 + int(binary.BigEndian.Uint16(input[cursor+2:cursor+4]))
	}
	t.Fatal("missing canonical bootstrap field")
	return 0
}

func bootstrapReject(t *testing.T, input []byte) {
	t.Helper()
	if got, ok := DecodeBootstrap(input); ok || got != (Bootstrap{}) {
		t.Fatal("invalid bootstrap returned a value")
	}
}

func TestBootstrapCodecGoldenAndRoundTrip(t *testing.T) {
	want := bootstrapGoldenValue()
	golden := bootstrapGoldenBytes(t)
	encoded, ok := EncodeBootstrap(want)
	if !ok || encoded.Length != uint16(len(golden)) {
		t.Fatal("canonical bootstrap encoding failed")
	}
	for i, b := range golden {
		if encoded.Bytes[i] != b {
			t.Fatalf("bootstrap golden byte mismatch at %d", i)
		}
	}
	for _, b := range encoded.Bytes[encoded.Length:] {
		if b != 0 {
			t.Fatal("encoded bootstrap tail is nonzero")
		}
	}
	decoded, ok := DecodeBootstrap(golden[:])
	if !ok || decoded != want {
		t.Fatal("bootstrap golden decode mismatch")
	}
	reencoded, ok := EncodeBootstrap(decoded)
	if !ok || reencoded != encoded {
		t.Fatal("bootstrap reencoding changed bytes")
	}
	if golden != bootstrapGoldenBytes(t) {
		t.Fatal("bootstrap decode changed its input")
	}
	for _, target := range []Target{LinuxAMD64, LinuxARM64, DarwinARM64, WindowsAMD64} {
		v := want
		v.Target = target
		if target == WindowsAMD64 {
			v.RoleDirectory, v.WorkingDirectory = syntheticPath("C:\\r"), syntheticPath("D:\\w")
			v.RoleDirectoryIdentity.Kind, v.WorkingDirectoryIdentity.Kind = WindowsDirectoryIdentity, WindowsDirectoryIdentity
			v.RoleDirectoryIdentity.FileID[15], v.WorkingDirectoryIdentity.FileID[15] = 17, 23
		}
		x, encodedOK := EncodeBootstrap(v)
		got, decodedOK := DecodeBootstrap(x.Bytes[:x.Length])
		if !encodedOK || !decodedOK || got != v {
			t.Fatal("target-specific bootstrap round trip failed")
		}
	}
}

func TestBootstrapCodecMessageLength(t *testing.T) {
	golden := bootstrapGoldenBytes(t)
	var header [13]byte
	copy(header[:], golden[:InputHeaderSize])
	for size := 0; size < InputHeaderSize; size++ {
		if length, ok := BootstrapMessageLength(header[:size]); ok || length != 0 {
			t.Fatal("short bootstrap header accepted")
		}
	}
	if length, ok := BootstrapMessageLength(header[:]); ok || length != 0 {
		t.Fatal("extra header byte accepted")
	}
	for _, length := range []uint32{0, 1, 11, 508, 509, 510, 511, 1531, 1532, 4096, 4097, ^uint32(0)} {
		binary.BigEndian.PutUint32(header[8:12], length)
		got, ok := BootstrapMessageLength(header[:InputHeaderSize])
		wantOK := length >= 509 && length <= 1531
		if ok != wantOK || ok && uint32(got) != length || !ok && got != 0 {
			t.Fatal("bootstrap advertised length boundary mismatch")
		}
	}
	for offset := 0; offset < 8; offset++ {
		copy(header[:], golden[:InputHeaderSize])
		header[offset] ^= 0xff
		if length, ok := BootstrapMessageLength(header[:InputHeaderSize]); ok || length != 0 {
			t.Fatal("invalid bootstrap header accepted")
		}
	}
	copy(header[:], golden[:InputHeaderSize])
	header[5] = 2
	if length, ok := BootstrapMessageLength(header[:InputHeaderSize]); ok || length != 0 {
		t.Fatal("checkpoint kind accepted by bootstrap helper")
	}
	before := header
	_, _ = BootstrapMessageLength(header[:InputHeaderSize])
	if before != header {
		t.Fatal("bootstrap header helper changed input")
	}
}

func TestBootstrapCodecRejectsMalformedInput(t *testing.T) {
	golden := bootstrapGoldenBytes(t)
	for end := 0; end < len(golden); end++ {
		bootstrapReject(t, golden[:end])
	}
	var extra [512]byte
	copy(extra[:], golden[:])
	bootstrapReject(t, extra[:])
	binary.BigEndian.PutUint32(extra[8:12], uint32(len(extra)))
	bootstrapReject(t, extra[:])
	var doubled [1022]byte
	copy(doubled[:], golden[:])
	copy(doubled[len(golden):], golden[:])
	bootstrapReject(t, doubled[:])
	binary.BigEndian.PutUint32(doubled[8:12], uint32(len(doubled)))
	bootstrapReject(t, doubled[:])
	var oversized [MaxBootstrapSize + 1]byte
	copy(oversized[:], golden[:])
	for _, size := range []int{MaxEncodedBootstrapSize + 1, MaxBootstrapSize, MaxBootstrapSize + 1} {
		binary.BigEndian.PutUint32(oversized[8:12], uint32(size))
		bootstrapReject(t, oversized[:size])
	}
	for _, advertised := range []uint32{0, 12, 508, 510, 512, 1531, 1532, 4096, ^uint32(0)} {
		bad := golden
		binary.BigEndian.PutUint32(bad[8:12], advertised)
		bootstrapReject(t, bad[:])
	}
	for offset := 0; offset < 8; offset++ {
		bad := golden
		bad[offset] ^= 0xff
		bootstrapReject(t, bad[:])
	}
	for id := uint16(1); id <= 20; id++ {
		offset := bootstrapFieldOffset(t, golden[:], id)
		width := int(binary.BigEndian.Uint16(golden[offset+2 : offset+4]))
		for _, wrongID := range []uint16{0, id - 1, id + 1, 0xffff} {
			bad := golden
			binary.BigEndian.PutUint16(bad[offset:offset+2], wrongID)
			bootstrapReject(t, bad[:])
		}
		for _, wrongWidth := range []uint16{0, uint16(width - 1), uint16(width + 1), 0xffff} {
			bad := golden
			binary.BigEndian.PutUint16(bad[offset+2:offset+4], wrongWidth)
			bootstrapReject(t, bad[:])
		}
		bad := golden
		for i := 0; i < width; i++ {
			bad[offset+4+i] = 0
		}
		bootstrapReject(t, bad[:])
		// Remove this complete field while keeping a coherent outer length.
		bad = golden
		copy(bad[offset:], golden[offset+4+width:])
		newLength := len(golden) - 4 - width
		binary.BigEndian.PutUint32(bad[8:12], uint32(newLength))
		bootstrapReject(t, bad[:newLength])
	}
	// Reorder equal-width fields with their original IDs intact.
	bad := golden
	first, second := bootstrapFieldOffset(t, golden[:], 3), bootstrapFieldOffset(t, golden[:], 4)
	copy(bad[first:first+20], golden[second:second+20])
	copy(bad[second:second+20], golden[first:first+20])
	bootstrapReject(t, bad[:])
	for _, id := range []uint16{15, 17} {
		offset := bootstrapFieldOffset(t, golden[:], id) + 4
		for _, reserved := range []int{1, 2, 3, 4, 5, 6, 7, 24, 25, 26, 27, 28, 29, 30, 31} {
			bad := golden
			bad[offset+reserved] = 1
			bootstrapReject(t, bad[:])
		}
	}
	schedule := bootstrapFieldOffset(t, golden[:], 18) + 4
	for reserved := 20; reserved < 24; reserved++ {
		bad := golden
		bad[schedule+reserved] = 1
		bootstrapReject(t, bad[:])
	}
}

func TestBootstrapCodecMaximumAndCopies(t *testing.T) {
	v := bootstrapGoldenValue()
	var path PathValue
	path.Length = MaxPathBytes
	path.Bytes[0] = '/'
	for i := 1; i < len(path.Bytes); i++ {
		path.Bytes[i] = 'a'
	}
	v.RoleDirectory, v.WorkingDirectory = path, path
	encoded, ok := EncodeBootstrap(v)
	if !ok || encoded.Length != MaxEncodedBootstrapSize {
		t.Fatal("maximum bootstrap path lengths failed")
	}
	decoded, ok := DecodeBootstrap(encoded.Bytes[:encoded.Length])
	if !ok || decoded != v {
		t.Fatal("maximum bootstrap did not round trip")
	}
	decodedBefore := decoded
	before := encoded
	v.RoleDirectory.Bytes[1], v.AuthenticationKey[0] = 'b', 12
	if encoded != before {
		t.Fatal("bootstrap encoder retained caller data")
	}
	encoded.Bytes[100] ^= 1
	if decoded != decodedBefore {
		t.Fatal("bootstrap decoder retained input")
	}
	encodedAfter := encoded
	decoded.RoleDirectory.Bytes[1] = 'c'
	if encoded != encodedAfter {
		t.Fatal("bootstrap returned value aliases encoded input")
	}
	if out, ok := EncodeBootstrap(Bootstrap{}); ok || out != (EncodedBootstrap{}) {
		t.Fatal("zero bootstrap encoded")
	}
	for _, role := range []bool{true, false} {
		bad := bootstrapGoldenValue()
		if role {
			bad.RoleDirectory.Bytes[bad.RoleDirectory.Length] = 1
		} else {
			bad.WorkingDirectory.Bytes[bad.WorkingDirectory.Length] = 1
		}
		if out, ok := EncodeBootstrap(bad); ok || out != (EncodedBootstrap{}) {
			t.Fatal("hidden path tail encoded")
		}
	}
}

func TestBootstrapCodecBindingDigest(t *testing.T) {
	base := bootstrapGoldenValue()
	digest, ok := BootstrapBindingDigest(base)
	if !ok || digest == [32]byte{} {
		t.Fatal("binding digest failed")
	}
	golden := bootstrapGoldenBytes(t)
	for i := 0; i < 32; i++ {
		golden[443+i], golden[479+i] = 0, 0
	}
	h := sha256.New()
	_, _ = h.Write([]byte("sobalink-p1-binding-v1\x00"))
	_, _ = h.Write(golden[:])
	var expected [32]byte
	copy(expected[:], h.Sum(nil))
	if digest != expected {
		t.Fatal("binding digest differs from fixed vector with only secret values zeroed")
	}
	for change := 0; change < 22; change++ {
		v := base
		switch change {
		case 0:
			v.Target = LinuxARM64
		case 1:
			v.FixtureID[0]++
		case 2:
			v.LaunchID[0]++
		case 3:
			v.Invocation.Role = RoleA0
		case 4:
			v.SupervisorPID += 2
		case 5:
			v.ChildPID++
		case 6:
			v.SourceCommit[0]++
		case 7:
			v.SourceTree[0]++
		case 8:
			v.SourceManifestSHA256[0]++
		case 9:
			v.ArgvSHA256[0]++
		case 10:
			v.ExecutableSHA256[0]++
		case 11:
			v.AssetSHA256[0]++
		case 12:
			v.RoleDirectory = syntheticPath("/s")
		case 13:
			v.RoleDirectoryIdentity.DeviceOrVolume++
		case 14:
			v.RoleDirectoryIdentity.FileID[7]++
		case 15:
			v.WorkingDirectory = syntheticPath("/x")
		case 16:
			v.WorkingDirectoryIdentity.DeviceOrVolume++
		case 17:
			v.WorkingDirectoryIdentity.FileID[7]++
		case 18:
			v.Schedule.EntrySeconds++
		case 19:
			v.Schedule.EntryNanoseconds++
		case 20:
			v.Schedule.EpochSeconds++
			v.Schedule.EntrySeconds++
			v.Schedule.SetupCutoffSeconds++
			v.Schedule.WorkCutoffSeconds++
			v.Schedule.CleanupCutoffSeconds++
			v.Schedule.OuterCutoffSeconds++
			v.Schedule.OriginalGrantExpirySeconds++
		case 21:
			v.Invocation = Invocation{Mode: CLI, Ordinal: 1, Operation: CLILocalStatus}
		}
		changed, valid := BootstrapBindingDigest(v)
		if !valid || changed == digest {
			t.Fatalf("identity change %d was not bound", change)
		}
	}
	for _, secret := range []bool{false, true} {
		v := base
		if secret {
			v.AuthenticationKey[0]++
		} else {
			v.Challenge[0]++
		}
		changed, valid := BootstrapBindingDigest(v)
		if !valid || changed != digest {
			t.Fatal("secret value bytes changed binding digest")
		}
	}
	for _, invalid := range []Bootstrap{{}, {Scenario: ScenarioProcessRecovery}} {
		if got, valid := BootstrapBindingDigest(invalid); valid || got != [32]byte{} {
			t.Fatal("invalid bootstrap produced binding digest")
		}
	}
	bad := base
	bad.Challenge = bad.AuthenticationKey
	if got, valid := BootstrapBindingDigest(bad); valid || got != [32]byte{} {
		t.Fatal("binding digest bypassed secret validation")
	}
}
