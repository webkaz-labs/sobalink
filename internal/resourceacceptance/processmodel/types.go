//go:build resource_process_native

// Package processmodel provides passive, bounded values for a separately gated
// process-observation fixture. Values never establish ownership or authority.
// Only separately tagged passive instrumentation imports these values.
package processmodel

type Mode uint8

const (
	Owner Mode = iota + 1
	CLI
	MaxOwnerEvents     = 640
	MaxCLIEvents       = 64
	MaxResourceEvents  = 128
	MaxLocalEvents     = 256
	MaxLifecycleEvents = 256
	MaxBatchEvents     = 78
	EventSize          = 104
	MaxEventPayload    = 4 + MaxBatchEvents*EventSize
)

func (m Mode) capacity() uint64 {
	switch m {
	case Owner:
		return MaxOwnerEvents
	case CLI:
		return MaxCLIEvents
	default:
		return 0
	}
}

type Kind uint8

const (
	GroupAccepted Kind = iota + 1
	GroupIntent
	ManagementClientInvoked
	ManagementFrameAttempted
	ManagementFrameWritten
	ProviderAdmitted
)

const (
	LocalCommand Kind = iota + 16
	IPCConnectAttempt
	IPCConnectCompleted
	IPCRequestDispatched
)

const (
	MaintenancePassed Kind = iota + 32
	Barrier
	EntryReturned
	OwnerLockBound
	CoreBound
	ControlConstructorAttempted
	LegacyConstructorAttempted
	ManagedConstructorAttempted
	ManagedStartupConstructorAttempted
	ControlNodeBound
	OrdinaryNodeBound
	ControlNodeJoined
	WebOpened
	IPCReady
	OwnersClosed
)

type Action uint8

const (
	None Action = iota
	Inspect
	Preview
	Apply
	Status
)

type Command uint8

const (
	ControlLimits Command = iota + 1
	LocalStatus
	LocalStop
	UpgradeIdentity
	UpgradeReview
	UpgradeRun
	UpgradeStatus
	ResourceList
	ResourceInspect
	ResourcePreview
	ResourceApply
	ResourceOperationStatus
	ManagementGrantPreview
	ManagementGrantConfirm
	ManagementGrantInspect
	GroupPreview
	GroupCurrent
	GroupApply
	GroupStatus
)

type Outcome uint8

const (
	Success Outcome = iota + 1
	TypedError
	Failure
)

// Observation contains no references, raw payload, identity, path, or callback.
// Unused fields must be zero. A zero-filled ID is canonical lowercase hex too;
// its presence is determined by the closed event shape, not by its contents.
type Observation struct {
	Kind           Kind
	Action         Action
	Command        Command
	Outcome        Outcome
	RunID          [16]byte
	OperationID    [32]byte
	RequestDigest  [32]byte
	BarrierOrdinal uint64
}

type Event struct {
	Sequence    uint64
	Observation Observation
}

type category uint8

const (
	resourceCategory category = iota
	localCategory
	lifecycleCategory
	invalidCategory
)

func eventCategory(kind Kind) category {
	switch {
	case kind >= GroupAccepted && kind <= ProviderAdmitted:
		return resourceCategory
	case kind >= LocalCommand && kind <= IPCRequestDispatched:
		return localCategory
	case kind >= MaintenancePassed && kind <= OwnersClosed:
		return lifecycleCategory
	default:
		return invalidCategory
	}
}

func validObservation(mode Mode, v Observation) bool {
	if mode.capacity() == 0 || mode == CLI && (eventCategory(v.Kind) == resourceCategory || v.Kind == MaintenancePassed || v.Kind >= OwnerLockBound && v.Kind <= OwnersClosed) {
		return false
	}
	want := Observation{Kind: v.Kind}
	switch v.Kind {
	case GroupAccepted:
		want.RunID = v.RunID
	case GroupIntent:
		want.Action, want.RunID, want.OperationID = Apply, v.RunID, v.OperationID
	case ManagementClientInvoked, ManagementFrameAttempted, ManagementFrameWritten:
		if v.Action < Inspect || v.Action > Status {
			return false
		}
		want.Action = v.Action
		if v.Action == Apply || v.Action == Status {
			want.OperationID = v.OperationID
		}
	case ProviderAdmitted:
		want.Action, want.OperationID = Apply, v.OperationID
	case LocalCommand, IPCRequestDispatched:
		if v.Command < ControlLimits || v.Command > GroupStatus {
			return false
		}
		want.Command = v.Command
		if v.Kind == LocalCommand {
			want.RequestDigest = v.RequestDigest
		}
	case IPCConnectAttempt, IPCConnectCompleted, MaintenancePassed,
		OwnerLockBound, CoreBound, ControlConstructorAttempted, LegacyConstructorAttempted,
		ManagedConstructorAttempted, ManagedStartupConstructorAttempted, ControlNodeBound,
		OrdinaryNodeBound, ControlNodeJoined, WebOpened, IPCReady:
	case OwnersClosed:
		if v.Outcome != Success && v.Outcome != Failure {
			return false
		}
		want.Outcome = v.Outcome
	case Barrier:
		limit := uint64(32)
		if mode == CLI {
			limit = 4
		}
		if v.BarrierOrdinal == 0 || v.BarrierOrdinal > limit {
			return false
		}
		want.BarrierOrdinal = v.BarrierOrdinal
	case EntryReturned:
		if v.Outcome < Success || v.Outcome > Failure {
			return false
		}
		want.Outcome = v.Outcome
	default:
		return false
	}
	return v == want
}

func validEvent(mode Mode, e Event) bool {
	return e.Sequence > 0 && e.Sequence <= mode.capacity() && validObservation(mode, e.Observation)
}

// ParseRunID rejects wrong-length, uppercase, or nonhex values without retaining
// or rendering any input. Parsing a value is not proof of a product operation.
func ParseRunID(value string) ([16]byte, bool) {
	var result [16]byte
	if !decodeHex(value, result[:]) {
		return [16]byte{}, false
	}
	return result, true
}

func ParseOperationID(value string) ([32]byte, bool) {
	var result [32]byte
	if !decodeHex(value, result[:]) {
		return [32]byte{}, false
	}
	return result, true
}

func decodeHex(value string, destination []byte) bool {
	if len(value) != len(destination)*2 {
		return false
	}
	for i := range destination {
		hi, okHi := hexNibble(value[2*i])
		lo, okLo := hexNibble(value[2*i+1])
		if !okHi || !okLo {
			return false
		}
		destination[i] = hi<<4 | lo
	}
	return true
}

func hexNibble(b byte) (byte, bool) {
	switch {
	case b >= '0' && b <= '9':
		return b - '0', true
	case b >= 'a' && b <= 'f':
		return b - 'a' + 10, true
	default:
		return 0, false
	}
}

type FailureFlags uint32

const (
	Invalid FailureFlags = 1 << iota
	Overflow
	PrefixGap
	Late
	SequenceFailure
	allFailureFlags = Invalid | Overflow | PrefixGap | Late | SequenceFailure
)

func validFailureFlags(flags FailureFlags) bool {
	return flags != 0 && flags & ^allFailureFlags == 0
}
