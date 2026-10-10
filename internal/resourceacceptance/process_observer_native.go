//go:build resource_process_native

package resourceacceptance

import (
	"crypto/sha256"
	"reflect"
	"sync/atomic"
	"time"

	"github.com/webkaz-labs/sobalink/internal/resourceacceptance/processmodel"
)

const (
	processAdmissionClosed = uint64(1) << 63
	processActiveMask      = processAdmissionClosed - 1
	processAliasReady      = uint32(2)
)

// Only the acceptance actual-entry verifier may install this pointer once,
// after actual inherited bootstrap, process, binary and directory verification.
// Private tests cannot install their value instances or reset live entry state.
var processObserver atomic.Pointer[processObservation]

// Alias payload is written once after a sole CAS claim, before ready publication.
// Readers load ready before comparing the immutable, known-pointer interface.
// No field of the pointed-to product object is ever read or written.
type processAlias struct {
	state atomic.Uint32
	value any
}

func processPointer(value any) bool {
	v := reflect.ValueOf(value)
	return v.IsValid() && v.Kind() == reflect.Ptr && !v.IsNil()
}

func (a *processAlias) equal(value any) bool {
	return a.state.Load() == processAliasReady && a.value == value
}

func (a *processAlias) bind(value any) bool {
	if !processPointer(value) || !a.state.CompareAndSwap(0, 1) {
		return false
	}
	a.value = value
	a.state.Store(processAliasReady)
	return true
}

// All bindings are retained through process exit, including the joined control
// alias. Rebinding cannot hide a retired owner. No map, callback or transport
// access is part of this leaf state, and no snapshot is a lifetime seal.
type processObservation struct {
	// Immutable observation scope only; future entry must verify actual directory identity.
	directory                     string
	mode                          processmodel.Mode
	recorder                      *processmodel.Recorder
	gate                          atomic.Uint64
	lock, core, ordinary, control processAlias
	controlAttempt                atomic.Bool
	ordinaryAttempt               atomic.Uint32
	controlJoined                 atomic.Bool
	webPort                       atomic.Uint32
	ipcReady                      atomic.Bool
	ownersClosed                  atomic.Bool
}

func newProcessObservation(mode processmodel.Mode) (*processObservation, bool) {
	return newScopedProcessObservation(mode, "")
}

const processDirectoryLimit = 512

func newScopedProcessObservation(mode processmodel.Mode, directory string) (*processObservation, bool) {
	if len(directory) > processDirectoryLimit {
		return nil, false
	}
	recorder, ok := processmodel.NewRecorder(mode)
	if !ok {
		return nil, false
	}
	return &processObservation{mode: mode, recorder: recorder, directory: directory}, true
}

// An invalid observation is deliberately handed to the pure recorder's sticky
// validation latch. It cannot be certified as an empty/zero successful prefix.
func (s *processObservation) invalidate() {
	s.recorder.Record(processmodel.Observation{})
}

func (s *processObservation) enter() bool {
	gate := s.gate.Add(1)
	if gate&processAdmissionClosed != 0 || gate&processActiveMask > processmodel.MaxOwnerEvents {
		s.invalidate()
		s.leave()
		return false
	}
	return true
}

func (s *processObservation) leave() { s.gate.Add(^uint64(0)) }

// This closes only observer admission, not product producers. It is private and
// unused by live entry code; eventual finalization must join real producers
// before this operation. Late hooks remain sticky and cannot be a valid seal.
func (s *processObservation) closeAdmission() {
	s.gate.Or(processAdmissionClosed)
	s.recorder.CloseAdmission()
}

// Reporter-only methods neither install an observer nor certify product joins.
// The sole coordinator is their caller; the existing closeAdmission is unchanged.
func (s *processObservation) snapshotThrough(target uint64) processmodel.Prefix {
	return s.recorder.PrefixThrough(target)
}

func (s *processObservation) recordBarrier(ordinal uint64) {
	if !s.enter() {
		return
	}
	defer s.leave()
	s.recorder.Record(processmodel.Observation{Kind: processmodel.Barrier, BarrierOrdinal: ordinal})
}

func (s *processObservation) closeForEntry(exitCode int, cutoff time.Time) bool {
	if (exitCode != 0 && exitCode != 1) || cutoff.IsZero() || !time.Now().Before(cutoff) {
		s.invalidate()
		return false
	}
	prior := s.gate.Or(processAdmissionClosed)
	if prior&processAdmissionClosed != 0 {
		s.invalidate()
		return false
	}
	// Close outer admission first. A late hook latches failure; already-admitted
	// hooks may finish without the recorder rejecting their final observation.
	if s.gate.Load()&processActiveMask != 0 {
		tick := time.NewTicker(time.Millisecond)
		defer tick.Stop()
		deadline := time.NewTimer(time.Until(cutoff))
		defer deadline.Stop()
		for s.gate.Load()&processActiveMask != 0 {
			select {
			case <-tick.C:
			case <-deadline.C:
				s.recorder.FailGap()
				return false
			}
		}
	}
	prefix := s.snapshotThrough(s.snapshotThrough(0).Reserved)
	if !prefix.Complete || prefix.Active != 0 || prefix.AdmissionClosed || prefix.Failures != 0 ||
		s.mode == processmodel.Owner && !s.ownersClosed.Load() {
		s.invalidate()
		s.recorder.CloseAdmission()
		return false
	}
	outcome := processmodel.Success
	if exitCode == 1 {
		outcome = processmodel.Failure
	}
	s.recorder.Record(processmodel.Observation{Kind: processmodel.EntryReturned, Outcome: outcome})
	s.recorder.CloseAdmission()
	prefix = s.snapshotThrough(s.snapshotThrough(0).Reserved)
	return prefix.Complete && prefix.Active == 0 && prefix.Failures == 0 && prefix.AdmissionClosed &&
		prefix.Count > 0 && prefix.Events[prefix.Count-1].Observation.Kind == processmodel.EntryReturned
}

func (s *processObservation) isOwner(core any) bool {
	return s.mode == processmodel.Owner && s.core.equal(core) && !s.ownersClosed.Load()
}

func (s *processObservation) distinct(value any) bool {
	return processPointer(value) && !s.lock.equal(value) && !s.core.equal(value) &&
		!s.ordinary.equal(value) && !s.control.equal(value)
}

func (s *processObservation) ownerLock(lock any) {
	if !s.enter() {
		return
	}
	defer s.leave()
	if s.mode != processmodel.Owner || !s.distinct(lock) || !s.lock.bind(lock) {
		s.invalidate()
		return
	}
	s.recorder.Record(processmodel.Observation{Kind: processmodel.OwnerLockBound})
}

func (s *processObservation) coreConstructed(core, lock any) {
	if !s.enter() {
		return
	}
	defer s.leave()
	if s.mode != processmodel.Owner || !s.lock.equal(lock) || !s.distinct(core) || !s.core.bind(core) {
		s.invalidate()
		return
	}
	s.recorder.Record(processmodel.Observation{Kind: processmodel.CoreBound})
}

func (s *processObservation) constructorAttempt(core any, kind ProcessNodeConstructor) {
	if !s.enter() {
		return
	}
	defer s.leave()
	if !s.isOwner(core) {
		s.invalidate()
		return
	}
	var event processmodel.Kind
	switch kind {
	case ProcessControlNode:
		if s.ordinaryAttempt.Load() != 0 || !s.controlAttempt.CompareAndSwap(false, true) {
			s.invalidate()
			return
		}
		event = processmodel.ControlConstructorAttempted
	case ProcessLegacyNode, ProcessManagedNode, ProcessManagedStartupNode:
		if s.controlAttempt.Load() && !s.controlJoined.Load() || !s.ordinaryAttempt.CompareAndSwap(0, uint32(kind)) {
			s.invalidate()
			return
		}
		switch kind {
		case ProcessLegacyNode:
			event = processmodel.LegacyConstructorAttempted
		case ProcessManagedNode:
			event = processmodel.ManagedConstructorAttempted
		case ProcessManagedStartupNode:
			event = processmodel.ManagedStartupConstructorAttempted
		}
	default:
		s.invalidate()
		return
	}
	s.recorder.Record(processmodel.Observation{Kind: event})
}

func (s *processObservation) controlConstructed(core, node any) {
	if !s.enter() {
		return
	}
	defer s.leave()
	if !s.isOwner(core) || !s.controlAttempt.Load() || s.ordinaryAttempt.Load() != 0 ||
		!s.distinct(node) || !s.control.bind(node) {
		s.invalidate()
		return
	}
	s.recorder.Record(processmodel.Observation{Kind: processmodel.ControlNodeBound})
}

func (s *processObservation) controlNodeJoined(core, node any, closeOK bool) {
	if !s.enter() {
		return
	}
	defer s.leave()
	if !s.isOwner(core) || !s.control.equal(node) || !closeOK {
		s.invalidate()
		return
	}
	// Core may observe the same actual closeOnce result twice. That does not
	// construct another Node or invent a second successful join.
	if s.controlJoined.CompareAndSwap(false, true) {
		s.recorder.Record(processmodel.Observation{Kind: processmodel.ControlNodeJoined})
	}
}

func (s *processObservation) nodeConstructed(core, node any) {
	if !s.enter() {
		return
	}
	defer s.leave()
	if !s.isOwner(core) || s.ordinaryAttempt.Load() == 0 ||
		s.controlAttempt.Load() && !s.controlJoined.Load() || !s.distinct(node) || !s.ordinary.bind(node) {
		s.invalidate()
		return
	}
	s.recorder.Record(processmodel.Observation{Kind: processmodel.OrdinaryNodeBound})
}

func (s *processObservation) maintenancePassed(core any) {
	if !s.enter() {
		return
	}
	defer s.leave()
	if !s.isOwner(core) {
		s.invalidate()
		return
	}
	// Include startup passes after actual Core binding, even before a Node is
	// constructed. Expected post-readiness passes are counted separately later.
	s.recorder.Record(processmodel.Observation{Kind: processmodel.MaintenancePassed})
}

func processWebPort(origin string) (uint32, bool) {
	const prefix = "http://127.0.0.1:"
	if len(origin) <= len(prefix) || len(origin) > len(prefix)+5 || origin[:len(prefix)] != prefix || origin[len(prefix)] == '0' {
		return 0, false
	}
	var port uint32
	for i := len(prefix); i < len(origin); i++ {
		if origin[i] < '0' || origin[i] > '9' {
			return 0, false
		}
		port = port*10 + uint32(origin[i]-'0')
	}
	return port, port > 0 && port <= 65535
}

func (s *processObservation) webOpened(core any, origin string) {
	if !s.enter() {
		return
	}
	defer s.leave()
	port, valid := processWebPort(origin)
	if !s.isOwner(core) || !valid || !s.webPort.CompareAndSwap(0, port) {
		s.invalidate()
		return
	}
	s.recorder.Record(processmodel.Observation{Kind: processmodel.WebOpened})
}

func (s *processObservation) ipcOpened(core any) {
	if !s.enter() {
		return
	}
	defer s.leave()
	if !s.isOwner(core) || s.webPort.Load() == 0 || !s.ipcReady.CompareAndSwap(false, true) {
		s.invalidate()
		return
	}
	s.recorder.Record(processmodel.Observation{Kind: processmodel.IPCReady})
}

func (s *processObservation) ownersJoined(core any, closeOK, lockReleased bool) {
	if !s.enter() {
		return
	}
	defer s.leave()
	if s.mode != processmodel.Owner || !s.core.equal(core) || !s.ownersClosed.CompareAndSwap(false, true) {
		s.invalidate()
		return
	}
	outcome := processmodel.Success
	if !closeOK || !lockReleased {
		outcome = processmodel.Failure
	}
	s.recorder.Record(processmodel.Observation{Kind: processmodel.OwnersClosed, Outcome: outcome})
	if outcome != processmodel.Success || s.controlAttempt.Load() && !s.controlJoined.Load() ||
		s.ordinaryAttempt.Load() == 0 || s.ordinary.state.Load() != processAliasReady || !s.ipcReady.Load() {
		s.invalidate()
	}
}

func (s *processObservation) record(owner any, kind Kind, action, runID, operationID string) {
	if !s.enter() {
		return
	}
	defer s.leave()
	if s.mode != processmodel.Owner || s.ownersClosed.Load() || !s.core.equal(owner) && !s.ordinary.equal(owner) {
		s.invalidate()
		return
	}
	value, valid := processResourceValue(kind, action, runID, operationID)
	if !valid {
		s.invalidate()
		return
	}
	s.recorder.Record(value)
}

func processResourceValue(kind Kind, action, runID, operationID string) (processmodel.Observation, bool) {
	var value processmodel.Observation
	switch action {
	case "":
		value.Action = processmodel.None
	case "inspect":
		value.Action = processmodel.Inspect
	case "preview":
		value.Action = processmodel.Preview
	case "apply":
		value.Action = processmodel.Apply
	case "operation.status":
		value.Action = processmodel.Status
	default:
		return value, false
	}
	needsRun, needsOperation := false, false
	switch kind {
	case GroupAccepted:
		value.Kind, needsRun = processmodel.GroupAccepted, true
		if value.Action != processmodel.None {
			return value, false
		}
	case GroupIntent:
		value.Kind, needsRun, needsOperation = processmodel.GroupIntent, true, true
		if value.Action != processmodel.Apply {
			return value, false
		}
	case ManagementClientInvoked, ManagementFrameAttempted, ManagementFrameWritten:
		if value.Action == processmodel.None {
			return value, false
		}
		value.Kind = processmodel.Kind(kind)
		needsOperation = value.Action == processmodel.Apply || value.Action == processmodel.Status
	case ProviderAdmitted:
		value.Kind, needsOperation = processmodel.ProviderAdmitted, true
		if value.Action != processmodel.Apply {
			return value, false
		}
	default:
		return value, false
	}
	var ok bool
	if needsRun {
		value.RunID, ok = processmodel.ParseRunID(runID)
		if !ok {
			return value, false
		}
	} else if runID != "" {
		return value, false
	}
	if needsOperation {
		value.OperationID, ok = processmodel.ParseOperationID(operationID)
		if !ok {
			return value, false
		}
	} else if operationID != "" {
		return value, false
	}
	return value, true
}

func ProcessOwnerLock(lock any) {
	if s := processObserver.Load(); s != nil {
		s.ownerLock(lock)
	}
}
func CoreConstructed(core, lock any) {
	if s := processObserver.Load(); s != nil {
		s.coreConstructed(core, lock)
	}
}
func ProcessNodeConstructorAttempt(core any, kind ProcessNodeConstructor) {
	if s := processObserver.Load(); s != nil {
		s.constructorAttempt(core, kind)
	}
}
func ProcessControlNodeConstructed(core, node any) {
	if s := processObserver.Load(); s != nil {
		s.controlConstructed(core, node)
	}
}
func ProcessControlNodeJoined(core, node any, closeOK bool) {
	if s := processObserver.Load(); s != nil {
		s.controlNodeJoined(core, node, closeOK)
	}
}
func NodeConstructed(core, node any) {
	if s := processObserver.Load(); s != nil {
		s.nodeConstructed(core, node)
	}
}
func MaintenancePassed(core any) {
	if s := processObserver.Load(); s != nil {
		s.maintenancePassed(core)
	}
}
func ProcessWebOpened(core any, origin string) {
	if s := processObserver.Load(); s != nil {
		s.webOpened(core, origin)
	}
}
func ProcessIPCReady(core any) {
	if s := processObserver.Load(); s != nil {
		s.ipcOpened(core)
	}
}
func ProcessOwnersClosed(core any, closeOK, lockReleased bool) {
	if s := processObserver.Load(); s != nil {
		s.ownersJoined(core, closeOK, lockReleased)
	}
}
func Record(owner any, kind Kind, action, runID, operationID string) {
	if s := processObserver.Load(); s != nil {
		s.record(owner, kind, action, runID, operationID)
	}
}

// Conversion is explicit: another package's integer must not silently become a
// valid recorder command when either enum is edited later.
func processModelCommand(kind ProcessCommandKind) (processmodel.Command, bool) {
	switch kind {
	case ProcessControlLimits:
		return processmodel.ControlLimits, true
	case ProcessLocalStatus:
		return processmodel.LocalStatus, true
	case ProcessLocalStop:
		return processmodel.LocalStop, true
	case ProcessUpgradeIdentity:
		return processmodel.UpgradeIdentity, true
	case ProcessUpgradeReview:
		return processmodel.UpgradeReview, true
	case ProcessUpgradeRun:
		return processmodel.UpgradeRun, true
	case ProcessUpgradeStatus:
		return processmodel.UpgradeStatus, true
	case ProcessResourceList:
		return processmodel.ResourceList, true
	case ProcessResourceInspect:
		return processmodel.ResourceInspect, true
	case ProcessResourcePreview:
		return processmodel.ResourcePreview, true
	case ProcessResourceApply:
		return processmodel.ResourceApply, true
	case ProcessResourceOperationStatus:
		return processmodel.ResourceOperationStatus, true
	case ProcessManagementGrantPreview:
		return processmodel.ManagementGrantPreview, true
	case ProcessManagementGrantConfirm:
		return processmodel.ManagementGrantConfirm, true
	case ProcessManagementGrantInspect:
		return processmodel.ManagementGrantInspect, true
	case ProcessGroupPreview:
		return processmodel.GroupPreview, true
	case ProcessGroupCurrent:
		return processmodel.GroupCurrent, true
	case ProcessGroupApply:
		return processmodel.GroupApply, true
	case ProcessGroupStatus:
		return processmodel.GroupStatus, true
	default:
		return 0, false
	}
}

// Only names entering Core.Command belong here. Literal IPC/lifecycle routes
// are distinct, even when a JSON name happens to spell one of those literals.
func processLocalCommandKind(name string) ProcessCommandKind {
	switch name {
	case "direct-lan.upgrade.review":
		return ProcessUpgradeReview
	case "direct-lan.upgrade.run":
		return ProcessUpgradeRun
	case "direct-lan.upgrade.status":
		return ProcessUpgradeStatus
	case "resource.list":
		return ProcessResourceList
	case "resource.inspect":
		return ProcessResourceInspect
	case "resource.preview":
		return ProcessResourcePreview
	case "resource.apply":
		return ProcessResourceApply
	case "resource.operation.status":
		return ProcessResourceOperationStatus
	case "resource.grant.management.preview":
		return ProcessManagementGrantPreview
	case "resource.grant.management.confirm":
		return ProcessManagementGrantConfirm
	case "resource.grant.inspect":
		return ProcessManagementGrantInspect
	case "resource.group.preview":
		return ProcessGroupPreview
	case "resource.group.review.current":
		return ProcessGroupCurrent
	case "resource.group.apply":
		return ProcessGroupApply
	case "resource.group.status":
		return ProcessGroupStatus
	default:
		return ProcessCommandUnknown
	}
}

func (s *processObservation) localCommand(core any, name, requestID string) {
	if !s.enter() {
		return
	}
	defer s.leave()
	command, valid := processModelCommand(processLocalCommandKind(name))
	if !s.isOwner(core) || !valid || len(requestID) == 0 || len(requestID) > 128 {
		s.invalidate()
		return
	}
	var id [128]byte
	copy(id[:], requestID)
	digest := sha256.Sum256(id[:len(requestID)])
	// The real Core has already checked request ID length and caller context.
	// This event is validated entry, not admission/completion/business success.
	s.recorder.Record(processmodel.Observation{Kind: processmodel.LocalCommand, Command: command, RequestDigest: digest})
}

func (s *processObservation) localLiteral(core any, name string) {
	if !s.enter() {
		return
	}
	defer s.leave()
	var command processmodel.Command
	switch name {
	case "status":
		command = processmodel.LocalStatus
	case "stop":
		command = processmodel.LocalStop
	default:
		s.invalidate()
		return
	}
	if !s.isOwner(core) {
		s.invalidate()
		return
	}
	s.recorder.Record(processmodel.Observation{Kind: processmodel.LocalCommand, Command: command})
}

func (s *processObservation) scopeMatches(directory string) bool {
	return s.directory != "" && s.directory == directory && !s.ownersClosed.Load()
}

func (s *processObservation) ipcDirectoryMatches(directory string) bool {
	if !s.enter() {
		return false
	}
	defer s.leave()
	if !s.scopeMatches(directory) {
		s.invalidate()
		return false
	}
	return true
}

func (s *processObservation) ipcConnect(directory string, completed bool) {
	if !s.enter() {
		return
	}
	defer s.leave()
	if !s.scopeMatches(directory) {
		s.invalidate()
		return
	}
	kind := processmodel.IPCConnectAttempt
	if completed {
		kind = processmodel.IPCConnectCompleted
	}
	s.recorder.Record(processmodel.Observation{Kind: kind})
}

func (s *processObservation) ipcDispatched(directory string, kind ProcessCommandKind) {
	if !s.enter() {
		return
	}
	defer s.leave()
	command, valid := processModelCommand(kind)
	if s.mode != processmodel.Owner || s.core.state.Load() != processAliasReady || !s.scopeMatches(directory) || !valid {
		s.invalidate()
		return
	}
	// This is transport dispatch only. Core may subsequently reject the
	// command's request ID, context, payload, business rules or authority.
	s.recorder.Record(processmodel.Observation{Kind: processmodel.IPCRequestDispatched, Command: command})
}

func ProcessLocalCommand(core any, name, requestID string) {
	if s := processObserver.Load(); s != nil {
		s.localCommand(core, name, requestID)
	}
}
func ProcessLocalLiteral(core any, name string) {
	if s := processObserver.Load(); s != nil {
		s.localLiteral(core, name)
	}
}
func ProcessIPCDirectoryMatches(directory string) bool {
	if s := processObserver.Load(); s != nil {
		return s.ipcDirectoryMatches(directory)
	}
	return false
}
func ProcessIPCConnectAttempt(directory string) {
	if s := processObserver.Load(); s != nil {
		s.ipcConnect(directory, false)
	}
}
func ProcessIPCConnectCompleted(directory string) {
	if s := processObserver.Load(); s != nil {
		s.ipcConnect(directory, true)
	}
}
func ProcessIPCRequestDispatched(directory string, command ProcessCommandKind) {
	if s := processObserver.Load(); s != nil {
		s.ipcDispatched(directory, command)
	}
}
