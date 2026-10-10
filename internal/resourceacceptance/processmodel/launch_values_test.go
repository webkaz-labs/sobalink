//go:build resource_process_native

package processmodel

import (
	"encoding/binary"
	"testing"
)

func syntheticPath(value string) PathValue {
	var path PathValue
	copy(path.Bytes[:], value)
	path.Length = uint16(len(value))
	return path
}

func syntheticBootstrap() Bootstrap {
	identity := DirectoryIdentity{Kind: UnixDirectoryIdentity, DeviceOrVolume: 7}
	binary.BigEndian.PutUint64(identity.FileID[:8], 41)
	cwd := identity
	binary.BigEndian.PutUint64(cwd.FileID[:8], 42)
	return Bootstrap{
		Scenario: ScenarioProcessRecovery, Target: LinuxAMD64, FixtureID: [16]byte{1}, LaunchID: [16]byte{2},
		Invocation: Invocation{Mode: Owner, Role: RoleC0}, SupervisorPID: 101, ChildPID: 202,
		SourceCommit: [20]byte{3}, SourceTree: [20]byte{4}, SourceManifestSHA256: [32]byte{5},
		ArgvSHA256: [32]byte{6}, ExecutableSHA256: [32]byte{7}, AssetSHA256: [32]byte{8},
		RoleDirectory: syntheticPath("/fixture/state/c"), RoleDirectoryIdentity: identity,
		WorkingDirectory: syntheticPath("/fixture"), WorkingDirectoryIdentity: cwd,
		Schedule: ScheduleBinding{EpochSeconds: 1000000, EntrySeconds: 1000001, EntryNanoseconds: 12,
			SetupCutoffSeconds: 1000090, WorkCutoffSeconds: 1000300, CleanupCutoffSeconds: 1000330,
			OuterCutoffSeconds: 1000360, OriginalGrantExpirySeconds: 1000360},
		Challenge: [32]byte{9}, AuthenticationKey: [32]byte{10},
	}
}

func TestLaunchValuesClosedBindings(t *testing.T) {
	base := syntheticBootstrap()
	if !validBootstrap(base) {
		t.Fatal("synthetic launch invalid")
	}
	for role := RoleC0; role <= RoleC2; role++ {
		v := base
		v.Invocation.Role = role
		if !validBootstrap(v) {
			t.Error("closed owner role rejected")
		}
	}
	for op := CLILocalStatus; op <= CLIDryRunGroupStatus; op++ {
		for _, ordinal := range []uint8{1, 64} {
			v := base
			v.Invocation = Invocation{Mode: CLI, Ordinal: ordinal, Operation: op}
			if !validBootstrap(v) {
				t.Error("closed CLI operation rejected")
			}
		}
	}
	for _, target := range []Target{LinuxAMD64, LinuxARM64, DarwinARM64, WindowsAMD64} {
		v := base
		v.Target = target
		if target == WindowsAMD64 {
			v.RoleDirectory, v.WorkingDirectory = syntheticPath(`C:\fixture\state\c`), syntheticPath(`C:\fixture`)
			v.RoleDirectoryIdentity.Kind, v.WorkingDirectoryIdentity.Kind = WindowsDirectoryIdentity, WindowsDirectoryIdentity
			v.RoleDirectoryIdentity.FileID[15], v.WorkingDirectoryIdentity.FileID[15] = 1, 2
		}
		if !validBootstrap(v) {
			t.Error("closed target rejected")
		}
	}
	for mutation := 0; mutation < 33; mutation++ {
		v := base
		switch mutation {
		case 0:
			v.Scenario = 0
		case 1:
			v.Target = 0
		case 2:
			v.Target = 5
		case 3:
			v.FixtureID = [16]byte{}
		case 4:
			v.LaunchID = [16]byte{}
		case 5:
			v.Invocation.Mode = 0
		case 6:
			v.Invocation.Role = 0
		case 7:
			v.Invocation.Role = 6
		case 8:
			v.Invocation.Ordinal = 1
		case 9:
			v.Invocation.Operation = 1
		case 10:
			v.Invocation = Invocation{Mode: CLI, Role: RoleC0, Ordinal: 1, Operation: 1}
		case 11:
			v.Invocation = Invocation{Mode: CLI, Ordinal: 0, Operation: 1}
		case 12:
			v.Invocation = Invocation{Mode: CLI, Ordinal: 65, Operation: 1}
		case 13:
			v.Invocation = Invocation{Mode: CLI, Ordinal: 1, Operation: 0}
		case 14:
			v.Invocation = Invocation{Mode: CLI, Ordinal: 1, Operation: 14}
		case 15:
			v.SupervisorPID = 0
		case 16:
			v.ChildPID = 0
		case 17:
			v.ChildPID = v.SupervisorPID
		case 18:
			v.SupervisorPID = 2147483648
		case 19:
			v.ChildPID = 2147483648
		case 20:
			v.SourceCommit = [20]byte{}
		case 21:
			v.SourceTree = [20]byte{}
		case 22:
			v.SourceManifestSHA256 = [32]byte{}
		case 23:
			v.ArgvSHA256 = [32]byte{}
		case 24:
			v.ExecutableSHA256 = [32]byte{}
		case 25:
			v.AssetSHA256 = [32]byte{}
		case 26:
			v.Challenge = [32]byte{}
		case 27:
			v.AuthenticationKey = [32]byte{}
		case 28:
			v.AuthenticationKey = v.Challenge
		case 29:
			v.RoleDirectoryIdentity.Kind = WindowsDirectoryIdentity
		case 30:
			v.RoleDirectoryIdentity.FileID = [16]byte{}
		case 31:
			v.WorkingDirectoryIdentity.FileID[15] = 1
		case 32:
			v.RoleDirectory.Bytes[v.RoleDirectory.Length] = 1
		}
		if validBootstrap(v) {
			t.Errorf("invalid binding %d accepted", mutation)
		}
	}
	base.SupervisorPID, base.ChildPID = 2147483647, 2147483646
	base.RoleDirectoryIdentity.DeviceOrVolume = 0
	if !validBootstrap(base) {
		t.Error("PID/device exact bound rejected")
	}
}

func TestLaunchValuesPathLexicalBounds(t *testing.T) {
	for _, value := range []string{"", "/", "relative", "/a/", "/a//b", "/a/./b", "/a/../b", "/a\\b", "/a\x00b", "/a\nb", "/a\x7fb", "/a\xff"} {
		if validPath(LinuxAMD64, syntheticPath(value)) {
			t.Error("invalid Unix path accepted")
		}
	}
	for _, value := range []string{`c:\fixture`, `C:fixture`, `C:\`, `\\host\share`, `\\?\C:\fixture`, `C:\a\`, `C:\a\\b`, `C:\a\.\b`, `C:\a\..\b`, `C:\a/b`, `C:\a:b`, `C:\a*b`, `C:\a?b`, `C:\a"b`, `C:\a<b`, `C:\a>b`, `C:\a|b`, `C:\a.`, `C:\a `} {
		if validPath(WindowsAMD64, syntheticPath(value)) {
			t.Error("invalid Windows path accepted")
		}
	}
	for _, value := range []string{"/fixture/日本語", "/fixture/con", "/fixture/.cache"} {
		if !validPath(LinuxAMD64, syntheticPath(value)) {
			t.Error("valid Unix exact bytes rejected")
		}
	}
	var exact [MaxPathBytes]byte
	for i := range exact {
		exact[i] = 'a'
	}
	exact[0] = '/'
	p := syntheticPath(string(exact[:]))
	if !validPath(LinuxAMD64, p) {
		t.Error("exact512 path rejected")
	}
	p.Length++
	if validPath(LinuxAMD64, p) {
		t.Error("path overflow accepted")
	}
	exact[0], exact[1], exact[2] = 'C', ':', '\\'
	if !validPath(WindowsAMD64, syntheticPath(string(exact[:]))) {
		t.Error("exact512 Windows path rejected")
	}
}

func TestLaunchValuesWindowsDeviceNames(t *testing.T) {
	var names [22]string
	names[0], names[1], names[2], names[3] = "CON", "PRN", "AUX", "NUL"
	for n := 1; n <= 9; n++ {
		names[3+n] = string([]byte{'C', 'O', 'M', byte('0' + n)})
		names[12+n] = string([]byte{'L', 'P', 'T', byte('0' + n)})
	}
	for _, name := range names {
		for style := 0; style < 3; style++ {
			var letters [4]byte
			copy(letters[:], name)
			for i := 0; i < len(name); i++ {
				if letters[i] >= 'A' && letters[i] <= 'Z' && (style == 1 || style == 2 && i%2 == 0) {
					letters[i] += 'a' - 'A'
				}
			}
			for _, extension := range []string{"", ".txt", ".data.bin", " .txt"} {
				value := string(letters[:len(name)]) + extension
				for _, path := range []string{`C:\fixture\` + value, `C:\fixture\` + value + `\state`} {
					if validPath(WindowsAMD64, syntheticPath(path)) {
						t.Error("reserved device component accepted")
					}
				}
			}
		}
	}
	for _, name := range []string{"CONSOLE", "COM10", "COM0", "LPT10", "XCON", "AUXILIARY", ".con", "XCOM1", "LPT0"} {
		if !validPath(WindowsAMD64, syntheticPath(`C:\fixture\`+name)) {
			t.Error("device-name prefix overmatched")
		}
	}
}

func TestLaunchValuesScheduleAndArgv(t *testing.T) {
	base := syntheticBootstrap()
	for mutation := 0; mutation < 12; mutation++ {
		v := base.Schedule
		switch mutation {
		case 0:
			v.EpochSeconds = 0
		case 1:
			v.EpochSeconds = ^uint64(0)
		case 2:
			v.EntrySeconds = v.EpochSeconds - 1
		case 3:
			v.EntrySeconds = v.CleanupCutoffSeconds
		case 4:
			v.EntryNanoseconds = 1000000000
		case 5:
			v.SetupCutoffSeconds++
		case 6:
			v.WorkCutoffSeconds++
		case 7:
			v.CleanupCutoffSeconds++
		case 8:
			v.OuterCutoffSeconds++
		case 9:
			v.OriginalGrantExpirySeconds++
		case 10:
			v.EntrySeconds = ^uint64(0)
		case 11:
			v.EpochSeconds = 253402300440
		}
		if validSchedule(v) {
			t.Error("invalid original time binding accepted")
		}
	}
	later := base.Schedule
	later.EntrySeconds = later.CleanupCutoffSeconds - 1
	later.EntryNanoseconds = 999999999
	if !validSchedule(later) || later.OriginalGrantExpirySeconds != base.Schedule.OriginalGrantExpirySeconds {
		t.Error("later entry changed original cutoff")
	}
	last := uint64(253402300439)
	if !validSchedule(ScheduleBinding{EpochSeconds: last, EntrySeconds: last, SetupCutoffSeconds: last + 90, WorkCutoffSeconds: last + 300, CleanupCutoffSeconds: last + 330, OuterCutoffSeconds: last + 360, OriginalGrantExpirySeconds: last + 360}) {
		t.Error("last representable epoch rejected")
	}
	one, ok := DigestArgv([]string{"/fixture/soba", "a", "bc"})
	if !ok {
		t.Fatal("valid argv rejected")
	}
	two, _ := DigestArgv([]string{"/fixture/soba", "ab", "c"})
	three, _ := DigestArgv([]string{"/other/soba", "a", "bc"})
	if one == two || one == three {
		t.Error("argument framing or argv0 not bound")
	}
	for _, args := range [][]string{nil, {}, {""}, {"a\x00"}, {"a\n"}, {"a\x7f"}, {"\xff"}} {
		if _, ok := DigestArgv(args); ok {
			t.Error("malformed argv accepted")
		}
	}
	var bytes [MaxArgvArgumentBytes + 1]byte
	for i := range bytes {
		bytes[i] = 'a'
	}
	if _, ok := DigestArgv([]string{string(bytes[:])}); ok {
		t.Error("per-argument overflow accepted")
	}
	var args [MaxArgvCount + 1]string
	for i := range args {
		args[i] = "x"
	}
	if _, ok := DigestArgv(args[:MaxArgvCount]); !ok {
		t.Error("argc exact cap rejected")
	}
	if _, ok := DigestArgv(args[:]); ok {
		t.Error("argc overflow accepted")
	}
	for i := 0; i < 8; i++ {
		args[i] = string(bytes[:MaxArgvArgumentBytes])
	}
	if _, ok := DigestArgv(args[:8]); !ok {
		t.Error("argv exact byte cap rejected")
	}
	if _, ok := DigestArgv(args[:9]); ok {
		t.Error("argv aggregate overflow accepted")
	}
}
