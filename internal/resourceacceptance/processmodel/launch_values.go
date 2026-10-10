//go:build resource_process_native

package processmodel

import (
	"crypto/sha256"
	"encoding/binary"
	"unicode/utf8"
)

const (
	ScenarioProcessRecovery  = uint16(1)
	MaxBootstrapSize         = 4096
	MaxEncodedBootstrapSize  = 1531
	MaxCheckpointRequestSize = 256
	CheckpointRequestSize    = 80
	InputHeaderSize          = 12
	MaxPathBytes             = 512
	MaxArgvCount             = 64
	MaxArgvArgumentBytes     = 1024
	MaxArgvBytes             = 8192
)

type Target uint8

const (
	LinuxAMD64 Target = iota + 1
	LinuxARM64
	DarwinARM64
	WindowsAMD64
)

type OwnerRole uint8

const (
	RoleC0 OwnerRole = iota + 1
	RoleA0
	RoleB0
	RoleC1
	RoleC2
)

type CLIOperation uint8

const (
	CLILocalStatus CLIOperation = iota + 1
	CLILocalStop
	CLIResourceList
	CLIManagementGrantPreview
	CLIManagementGrantConfirm
	CLIGrantInspect
	CLIGroupPreview
	CLIGroupApply
	CLIGroupCurrent
	CLIGroupStatus
	CLIDryRunGroupPreview
	CLIDryRunGroupApply
	CLIDryRunGroupStatus
)

// Invocation labels already selected argv. It never requests a product action.
type Invocation struct {
	Mode      Mode
	Role      OwnerRole
	Ordinal   uint8
	Operation CLIOperation
}

type PathValue struct {
	Bytes  [MaxPathBytes]byte
	Length uint16
}

// DirectoryIdentity is only a copied claim, not an OS handle or verified path.
type DirectoryIdentity struct {
	Kind           uint8
	DeviceOrVolume uint64
	FileID         [16]byte
}

const (
	UnixDirectoryIdentity    = uint8(1)
	WindowsDirectoryIdentity = uint8(2)
)

type ScheduleBinding struct {
	EpochSeconds               uint64
	EntrySeconds               uint64
	EntryNanoseconds           uint32
	SetupCutoffSeconds         uint64
	WorkCutoffSeconds          uint64
	CleanupCutoffSeconds       uint64
	OuterCutoffSeconds         uint64
	OriginalGrantExpirySeconds uint64
}

// Bootstrap is private launch material. Never log or publish its secrets or paths.
// Validation and copying do not establish process, filesystem or Core ownership.
type Bootstrap struct {
	Scenario                 uint16
	Target                   Target
	FixtureID                [16]byte
	LaunchID                 [16]byte
	Invocation               Invocation
	SupervisorPID            uint64
	ChildPID                 uint64
	SourceCommit             [20]byte
	SourceTree               [20]byte
	SourceManifestSHA256     [32]byte
	ArgvSHA256               [32]byte
	ExecutableSHA256         [32]byte
	AssetSHA256              [32]byte
	RoleDirectory            PathValue
	RoleDirectoryIdentity    DirectoryIdentity
	WorkingDirectory         PathValue
	WorkingDirectoryIdentity DirectoryIdentity
	Schedule                 ScheduleBinding
	Challenge                [32]byte
	AuthenticationKey        [32]byte
}

type EncodedBootstrap struct {
	Bytes  [MaxBootstrapSize]byte
	Length uint16
}

type EncodedCheckpointRequest struct {
	Bytes  [MaxCheckpointRequestSize]byte
	Length uint16
}

func validBootstrap(v Bootstrap) bool {
	return v.Scenario == ScenarioProcessRecovery && v.Target >= LinuxAMD64 && v.Target <= WindowsAMD64 &&
		v.FixtureID != [16]byte{} && v.LaunchID != [16]byte{} && validInvocation(v.Invocation) &&
		v.SupervisorPID > 0 && v.SupervisorPID <= 2147483647 && v.ChildPID > 0 && v.ChildPID <= 2147483647 && v.SupervisorPID != v.ChildPID &&
		v.SourceCommit != [20]byte{} && v.SourceTree != [20]byte{} && v.SourceManifestSHA256 != [32]byte{} &&
		v.ArgvSHA256 != [32]byte{} && v.ExecutableSHA256 != [32]byte{} && v.AssetSHA256 != [32]byte{} &&
		validPath(v.Target, v.RoleDirectory) && validPath(v.Target, v.WorkingDirectory) &&
		validDirectoryIdentity(v.Target, v.RoleDirectoryIdentity) && validDirectoryIdentity(v.Target, v.WorkingDirectoryIdentity) &&
		validSchedule(v.Schedule) && v.Challenge != [32]byte{} && v.AuthenticationKey != [32]byte{} && v.Challenge != v.AuthenticationKey
}

func validInvocation(v Invocation) bool {
	switch v.Mode {
	case Owner:
		return v.Role >= RoleC0 && v.Role <= RoleC2 && v.Ordinal == 0 && v.Operation == 0
	case CLI:
		return v.Role == 0 && v.Ordinal >= 1 && v.Ordinal <= 64 && v.Operation >= CLILocalStatus && v.Operation <= CLIDryRunGroupStatus
	default:
		return false
	}
}

func validDirectoryIdentity(target Target, v DirectoryIdentity) bool {
	if target == WindowsAMD64 {
		return v.Kind == WindowsDirectoryIdentity && v.FileID != [16]byte{}
	}
	if target < LinuxAMD64 || target > DarwinARM64 || v.Kind != UnixDirectoryIdentity || binary.BigEndian.Uint64(v.FileID[:8]) == 0 {
		return false
	}
	for _, b := range v.FileID[8:] {
		if b != 0 {
			return false
		}
	}
	return true
}

func validSchedule(v ScheduleBinding) bool {
	e := v.EpochSeconds
	return e > 0 && e <= 253402300439 && v.EntryNanoseconds < 1000000000 &&
		v.SetupCutoffSeconds == e+90 && v.WorkCutoffSeconds == e+300 && v.CleanupCutoffSeconds == e+330 &&
		v.OuterCutoffSeconds == e+360 && v.OriginalGrantExpirySeconds == e+360 &&
		v.EntrySeconds >= e && v.EntrySeconds < e+330
}

func validText(value []byte) bool {
	if !utf8.Valid(value) {
		return false
	}
	for _, b := range value {
		if b < 32 || b == 127 {
			return false
		}
	}
	return true
}

func validPath(target Target, v PathValue) bool {
	if v.Length == 0 || v.Length > MaxPathBytes {
		return false
	}
	for _, b := range v.Bytes[v.Length:] {
		if b != 0 {
			return false
		}
	}
	path := v.Bytes[:v.Length]
	if !validText(path) {
		return false
	}
	start, separator := 1, byte('/')
	windows := target == WindowsAMD64
	if windows {
		if len(path) < 4 || path[0] < 'A' || path[0] > 'Z' || path[1] != ':' || path[2] != '\\' {
			return false
		}
		start, separator = 3, '\\'
	} else if target < LinuxAMD64 || target > DarwinARM64 || len(path) < 2 || path[0] != '/' {
		return false
	}
	component := start
	for i := start; i <= len(path); i++ {
		if i < len(path) {
			b := path[i]
			if windows {
				switch b {
				case '/', ':', '*', '?', '"', '<', '>', '|':
					return false
				}
			} else if b == '\\' {
				return false
			}
			if b != separator {
				continue
			}
		}
		if i == component {
			return false
		}
		part := path[component:i]
		if len(part) == 1 && part[0] == '.' || len(part) == 2 && part[0] == '.' && part[1] == '.' {
			return false
		}
		if windows && (part[len(part)-1] == '.' || part[len(part)-1] == ' ' || reservedDOSName(part)) {
			return false
		}
		component = i + 1
	}
	return true
}

func reservedDOSName(component []byte) bool {
	end := len(component)
	for i, b := range component {
		if b == '.' {
			end = i
			break
		}
	}
	for end > 0 && component[end-1] == ' ' {
		end--
	}
	if end != 3 && end != 4 {
		return false
	}
	var name [4]byte
	for i := 0; i < end; i++ {
		b := component[i]
		if b >= 'a' && b <= 'z' {
			b -= 'a' - 'A'
		}
		name[i] = b
	}
	if end == 3 {
		return name == [4]byte{'C', 'O', 'N'} || name == [4]byte{'P', 'R', 'N'} || name == [4]byte{'A', 'U', 'X'} || name == [4]byte{'N', 'U', 'L'}
	}
	return name[3] >= '1' && name[3] <= '9' && (name[0] == 'C' && name[1] == 'O' && name[2] == 'M' || name[0] == 'L' && name[1] == 'P' && name[2] == 'T')
}

// DigestArgv hashes bounded exact argument bytes, including argv[0]. It neither
// validates a product command nor reads actual process arguments.
func DigestArgv(argv []string) ([32]byte, bool) {
	if len(argv) == 0 || len(argv) > MaxArgvCount {
		return [32]byte{}, false
	}
	total := 0
	for _, arg := range argv {
		if len(arg) == 0 || len(arg) > MaxArgvArgumentBytes || len(arg) > MaxArgvBytes-total || !validText([]byte(arg)) {
			return [32]byte{}, false
		}
		total += len(arg)
	}
	h := sha256.New()
	_, _ = h.Write([]byte("sobalink-p1-argv-v1\x00"))
	var size [2]byte
	binary.BigEndian.PutUint16(size[:], uint16(len(argv)))
	_, _ = h.Write(size[:])
	for _, arg := range argv {
		binary.BigEndian.PutUint16(size[:], uint16(len(arg)))
		_, _ = h.Write(size[:])
		_, _ = h.Write([]byte(arg))
	}
	var result [32]byte
	copy(result[:], h.Sum(nil))
	return result, true
}
