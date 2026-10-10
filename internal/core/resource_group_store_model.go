package core

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/resource"
	"github.com/webkaz-labs/sobalink/internal/resourcegrant"
	"github.com/webkaz-labs/sobalink/internal/resourcegroup"
)

const (
	resourceGroupStoreVersion      = 1
	resourceGroupMaxRuns           = 16
	resourceGroupMaxRunBytes       = 64 * 1024
	resourceGroupMaxStoreBytes     = 1024 * 1024
	resourceGroupOriginCaptured    = "captured"
	resourceGroupOriginUnavailable = "unavailable"
	resourceGroupAdmissionActive   = "active"
	resourceGroupAdmissionFinished = "finished"
)

var errResourceGroupState = errors.New("invalid group evidence state")
var errResourceGroupCapacity = errors.New("group evidence capacity exhausted")
var errResourceGroupReview = errors.New("group review does not match owned evidence")

// Origin is private correlation to the original owned pair, not a transport
// capability. An unavailable selected row has no fabricated relationship.
type resourceGroupOrigin struct {
	PeerKey      string                      `json:"peerKey"`
	State        string                      `json:"state"`
	Relationship *resourcegrant.Relationship `json:"relationship,omitempty"`
}

type resourceGroupRecord struct {
	RunID            string                     `json:"runId"`
	AcceptedSequence uint64                     `json:"acceptedSequence"`
	AcceptedAt       int64                      `json:"acceptedAt"`
	Review           resourcegroup.ReviewBody   `json:"review"`
	Origins          []resourceGroupOrigin      `json:"origins"`
	InputHash        string                     `json:"inputHash"`
	Evidence         resourcegroup.EvidenceBody `json:"evidence"`
	Admission        string                     `json:"admission"`
	UpdateSequence   uint64                     `json:"updateSequence"`
}

// No prepared review, approval, runtime capture or runnable outbox is persisted.
// A decoded record is evidence only; the actual owner must certify durability.
type resourceGroupEnvelope struct {
	SchemaVersion        int                   `json:"schemaVersion"`
	ControllerResourceID string                `json:"controllerResourceId"`
	HighWater            *uint64               `json:"highWater"`
	Runs                 []resourceGroupRecord `json:"runs"`
}

func resourceGroupJSONEqual(a, b any) bool {
	x, ex := json.Marshal(a)
	y, ey := json.Marshal(b)
	return ex == nil && ey == nil && string(x) == string(y)
}

func resourceGroupInputHash(in resourcegroup.ApplyInput) string {
	// Correlates the exact local apply input only. ControllerResourceID is a
	// separate required envelope binding, compared by the actual local owner.
	in.ExecutionPeers = append([]string{}, in.ExecutionPeers...)
	sort.Strings(in.ExecutionPeers)
	data, _ := json.Marshal(in)
	h := sha256.New()
	h.Write([]byte("sobalink.resourcegroup.accepted.v1\x00"))
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}

func (r resourceGroupRecord) acceptedInput() resourcegroup.ApplyInput {
	return resourcegroup.ApplyInput{SchemaVersion: resourcegroup.SchemaVersion, ReviewID: r.RunID, ReviewRevision: r.Review.Revision, ExecutionPeers: append([]string{}, r.Review.ExecutionPeers...), Confirm: true}
}

func (r resourceGroupRecord) validate() error {
	if !resource.ValidID(r.RunID) || r.AcceptedSequence == 0 || r.AcceptedSequence > uint64(capacity.MaxJSONInteger) || r.AcceptedAt <= 0 || r.AcceptedAt > resourcegroup.MaxObservationTime || r.UpdateSequence == 0 || r.UpdateSequence > uint64(capacity.MaxJSONInteger) || r.Evidence.SchemaVersion != resourcegroup.SchemaVersion || r.acceptedInput().Validate() != nil || resourcegroup.ValidateEvidence(r.Review, r.Evidence.Members) != nil {
		return errResourceGroupState
	}
	canonical, err := resourcegroup.CloneReview(r.Review)
	if err != nil || !resourceGroupJSONEqual(canonical, r.Review) || r.InputHash != resourceGroupInputHash(r.acceptedInput()) || len(r.Origins) != len(r.Review.Rows) || len(r.Evidence.Members) != len(r.Review.Rows) {
		return errResourceGroupState
	}
	localKey := ""
	dispatching := 0
	needsNormalization := r.Admission == resourceGroupAdmissionActive
	for i, row := range r.Review.Rows {
		origin, evidence := r.Origins[i], r.Evidence.Members[i]
		if origin.PeerKey != row.PeerKey || evidence.PeerKey != row.PeerKey {
			return errResourceGroupState
		}
		switch origin.State {
		case resourceGroupOriginCaptured:
			if origin.Relationship == nil || origin.Relationship.Validate() != nil || origin.Relationship.PeerKey != origin.PeerKey {
				return errResourceGroupState
			}
			if localKey != "" && localKey != origin.Relationship.TargetKey {
				return errResourceGroupState
			}
			localKey = origin.Relationship.TargetKey
		case resourceGroupOriginUnavailable:
			if origin.Relationship != nil || row.State == resourcegroup.ReviewReady || evidence.Execution == resourcegroup.ExecutionSelected {
				return errResourceGroupState
			}
		default:
			return errResourceGroupState
		}
		if evidence.Dispatch == resourcegroup.Dispatching {
			dispatching++
		}
		if evidence.LocalDurability != resourcegroup.LocalDurable {
			needsNormalization = true
		}
	}
	summary, err := resourcegroup.ReduceReview(r.Review, r.Evidence.Members)
	if err != nil || dispatching > 1 || r.Admission != resourceGroupAdmissionActive && r.Admission != resourceGroupAdmissionFinished || r.Admission == resourceGroupAdmissionFinished && !summary.AdmissionFinished {
		return errResourceGroupState
	}
	// An owned reopen may need one normalization update. A dispatch intent
	// additionally reserves completion and final admission reduction; exhaustion
	// must be detected before sending, not after a provider may have run.
	if needsNormalization && r.UpdateSequence >= uint64(capacity.MaxJSONInteger) || dispatching != 0 && r.UpdateSequence > uint64(capacity.MaxJSONInteger)-2 {
		return errResourceGroupState
	}
	data, err := json.Marshal(r)
	if err != nil || len(data) > resourceGroupMaxRunBytes {
		return errResourceGroupState
	}
	return nil
}

func cloneResourceGroupRecord(r resourceGroupRecord) (resourceGroupRecord, error) {
	if r.validate() != nil {
		return resourceGroupRecord{}, errResourceGroupState
	}
	data, err := json.Marshal(r)
	if err != nil {
		return resourceGroupRecord{}, errResourceGroupState
	}
	var copy resourceGroupRecord
	if resource.Decode(data, resourceGroupMaxRunBytes, &copy) != nil || copy.validate() != nil {
		return resourceGroupRecord{}, errResourceGroupState
	}
	return copy, nil
}

func (e resourceGroupEnvelope) validate() error {
	if e.SchemaVersion != resourceGroupStoreVersion || !resource.ValidID(e.ControllerResourceID) || e.HighWater == nil || *e.HighWater > uint64(capacity.MaxJSONInteger) || e.Runs == nil || len(e.Runs) > resourceGroupMaxRuns || (*e.HighWater == 0) != (len(e.Runs) == 0) {
		return errResourceGroupState
	}
	var previous uint64
	active := 0
	for i, run := range e.Runs {
		if run.validate() != nil || run.AcceptedSequence <= previous || run.AcceptedSequence > *e.HighWater {
			return errResourceGroupState
		}
		for j := 0; j < i; j++ {
			if e.Runs[j].RunID == run.RunID {
				return errResourceGroupState
			}
		}
		if run.Admission == resourceGroupAdmissionActive {
			active++
		}
		previous = run.AcceptedSequence
	}
	if previous != *e.HighWater || active > 1 {
		return errResourceGroupState
	}
	data, err := json.Marshal(e)
	if err != nil || len(data) > resourceGroupMaxStoreBytes {
		return errResourceGroupState
	}
	return nil
}

func decodeResourceGroupEnvelope(data []byte) (resourceGroupEnvelope, error) {
	var e resourceGroupEnvelope
	if resource.Decode(data, resourceGroupMaxStoreBytes, &e) != nil || e.validate() != nil {
		return resourceGroupEnvelope{}, errResourceGroupState
	}
	if _, err := resourceGroupReservedBytes(e); err != nil {
		return resourceGroupEnvelope{}, err
	}
	return e, nil
}

func cloneResourceGroupEnvelope(e resourceGroupEnvelope) (resourceGroupEnvelope, error) {
	if e.validate() != nil {
		return resourceGroupEnvelope{}, errResourceGroupState
	}
	data, err := json.Marshal(e)
	if err != nil {
		return resourceGroupEnvelope{}, errResourceGroupState
	}
	return decodeResourceGroupEnvelope(data)
}

func newResourceGroupEnvelope(controllerID string) (resourceGroupEnvelope, error) {
	zero := uint64(0)
	e := resourceGroupEnvelope{SchemaVersion: resourceGroupStoreVersion, ControllerResourceID: controllerID, HighWater: &zero, Runs: []resourceGroupRecord{}}
	if e.validate() != nil {
		return resourceGroupEnvelope{}, errResourceGroupState
	}
	return e, nil
}

// Desired snapshot only. A successful return neither admits execution nor
// certifies that its LocalDurable fields have actually been published.
func newResourceGroupRecord(id string, review resourcegroup.ReviewBody, origins []resourceGroupOrigin, sequence uint64, acceptedAt int64) (resourceGroupRecord, error) {
	frozen, err := resourcegroup.CloneReview(review)
	if err != nil || len(origins) != len(frozen.Rows) {
		return resourceGroupRecord{}, errResourceGroupState
	}
	rows, err := resourcegroup.NewEvidence(frozen)
	if err != nil {
		return resourceGroupRecord{}, errResourceGroupState
	}
	for i := range rows {
		rows[i].LocalDurability = resourcegroup.LocalDurable
	}
	r := resourceGroupRecord{RunID: id, AcceptedSequence: sequence, AcceptedAt: acceptedAt, Review: frozen, Origins: append([]resourceGroupOrigin{}, origins...), Evidence: resourcegroup.EvidenceBody{SchemaVersion: resourcegroup.SchemaVersion, Members: rows}, Admission: resourceGroupAdmissionActive, UpdateSequence: 1}
	for i := range r.Origins {
		if r.Origins[i].Relationship != nil {
			copy := *r.Origins[i].Relationship
			r.Origins[i].Relationship = &copy
		}
	}
	sort.Slice(r.Origins, func(i, j int) bool { return r.Origins[i].PeerKey < r.Origins[j].PeerKey })
	r.InputHash = resourceGroupInputHash(r.acceptedInput())
	if r.validate() != nil {
		return resourceGroupRecord{}, errResourceGroupState
	}
	return r, nil
}

func resourceGroupTerminalDurable(operation *resourcegrant.ManagementOperation) bool {
	return operation != nil && operation.EvidenceDurable != nil && *operation.EvidenceDurable && operation.Outcome.Status != "unknown"
}

func (r resourceGroupRecord) pinned() bool {
	if r.Admission == resourceGroupAdmissionActive {
		return true
	}
	for _, row := range r.Evidence.Members {
		if row.LocalDurability != resourcegroup.LocalDurable || row.Dispatch != resourcegroup.DispatchNotAttempted && !resourceGroupTerminalDurable(row.Target) {
			return true
		}
	}
	return false
}

// All target/status tokens and requests are already fixed. This projection
// reserves longest permitted local strings, safe-JSON counters, timestamps and
// two complete operation observations per executable row. It is never stored.
// canceled/not_attempted is the longest valid resource.Outcome JSON shape.
func resourceGroupWorstRecord(r resourceGroupRecord) (resourceGroupRecord, error) {
	worst, err := cloneResourceGroupRecord(r)
	if err != nil {
		return resourceGroupRecord{}, err
	}
	worst.UpdateSequence = uint64(capacity.MaxJSONInteger) - 1 // Same encoded width, with normalization headroom.
	worst.Admission = resourceGroupAdmissionFinished
	for i := range worst.Evidence.Members {
		row := &worst.Evidence.Members[i]
		row.LocalDurability = resourcegroup.LocalUncertain
		row.AdmissionStop = resourcegroup.StopPersistenceUncertain
		if row.Execution != resourcegroup.ExecutionSelected {
			continue
		}
		falseTarget, falseStatus := false, false
		outcome := resource.Outcome{Status: "canceled", Configuration: "not_attempted", Accounting: "not_attempted", Transfer: "not_attempted"}
		row.Dispatch = resourcegroup.DispatchObserved
		row.Target = &resourcegrant.ManagementOperation{OperationID: row.Request.Apply.OperationID, Outcome: outcome, EvidenceDurable: &falseTarget}
		row.Status = resourcegroup.StatusObservation{State: resourcegroup.StatusObserved, Sequence: uint64(capacity.MaxJSONInteger), ObservedAt: resourcegroup.MaxObservationTime, Operation: &resourcegrant.ManagementOperation{OperationID: row.Request.Apply.OperationID, Outcome: outcome, EvidenceDurable: &falseStatus}}
	}
	if worst.validate() != nil {
		return resourceGroupRecord{}, errResourceGroupCapacity
	}
	actual, _ := json.Marshal(r)
	reserved, _ := json.Marshal(worst)
	if len(actual) > len(reserved) || len(reserved) > resourceGroupMaxRunBytes {
		return resourceGroupRecord{}, errResourceGroupCapacity
	}
	return worst, nil
}

func resourceGroupReservedBytes(e resourceGroupEnvelope) (int, error) {
	if e.validate() != nil {
		return 0, errResourceGroupState
	}
	worst := e
	worst.Runs = make([]resourceGroupRecord, len(e.Runs))
	maximum := uint64(capacity.MaxJSONInteger)
	worst.HighWater = &maximum // Header reservation only, never decoded as evidence.
	for i, run := range e.Runs {
		var err error
		worst.Runs[i], err = resourceGroupWorstRecord(run)
		if err != nil {
			return 0, err
		}
	}
	data, err := json.Marshal(worst)
	if err != nil || len(data) > resourceGroupMaxStoreBytes {
		return 0, errResourceGroupCapacity
	}
	return len(data), nil
}

func withResourceGroupRun(e resourceGroupEnvelope, run resourceGroupRecord) (resourceGroupEnvelope, error) {
	if e.validate() != nil || run.validate() != nil || *e.HighWater >= uint64(capacity.MaxJSONInteger) || run.AcceptedSequence != *e.HighWater+1 || run.Admission != resourceGroupAdmissionActive || run.UpdateSequence != 1 {
		return resourceGroupEnvelope{}, errResourceGroupState
	}
	for _, row := range run.Evidence.Members {
		if row.Dispatch != resourcegroup.DispatchNotAttempted || row.LocalDurability != resourcegroup.LocalDurable || row.AdmissionStop != resourcegroup.StopNone {
			return resourceGroupEnvelope{}, errResourceGroupState
		}
	}
	next, err := cloneResourceGroupEnvelope(e)
	if err != nil {
		return resourceGroupEnvelope{}, err
	}
	for _, previous := range next.Runs {
		if previous.RunID == run.RunID || previous.Admission == resourceGroupAdmissionActive {
			return resourceGroupEnvelope{}, errResourceGroupReview
		}
	}
	copy, err := cloneResourceGroupRecord(run)
	if err != nil {
		return resourceGroupEnvelope{}, err
	}
	*next.HighWater = copy.AcceptedSequence
	next.Runs = append(next.Runs, copy)
	for {
		if len(next.Runs) <= resourceGroupMaxRuns && next.validate() == nil {
			if _, err := resourceGroupReservedBytes(next); err == nil {
				return next, nil
			}
		}
		evict := -1
		for i := 0; i < len(next.Runs)-1; i++ {
			if !next.Runs[i].pinned() {
				evict = i
				break
			}
		}
		if evict < 0 {
			return resourceGroupEnvelope{}, errResourceGroupCapacity
		}
		next.Runs = append(next.Runs[:evict], next.Runs[evict+1:]...)
	}
}

// Retained matches return data only. Missing/evicted IDs never become fresh
// admission here; only the coordinator's current unused prepared object can.
func matchResourceGroupRun(e resourceGroupEnvelope, in resourcegroup.ApplyInput) (resourceGroupRecord, bool, error) {
	if e.validate() != nil || in.Validate() != nil {
		return resourceGroupRecord{}, false, errResourceGroupState
	}
	for _, run := range e.Runs {
		if run.RunID == in.ReviewID {
			if run.InputHash != resourceGroupInputHash(in) {
				return resourceGroupRecord{}, false, errResourceGroupReview
			}
			copy, err := cloneResourceGroupRecord(run)
			return copy, err == nil, err
		}
	}
	return resourceGroupRecord{}, false, nil
}

func validResourceGroupTransition(before, next resourceGroupRecord) bool {
	if before.validate() != nil || next.validate() != nil || before.RunID != next.RunID || before.AcceptedSequence != next.AcceptedSequence || before.AcceptedAt != next.AcceptedAt || before.InputHash != next.InputHash || !resourceGroupJSONEqual(before.Review, next.Review) || !resourceGroupJSONEqual(before.Origins, next.Origins) || before.UpdateSequence >= uint64(capacity.MaxJSONInteger) || next.UpdateSequence != before.UpdateSequence+1 || before.Admission == resourceGroupAdmissionFinished && next.Admission != resourceGroupAdmissionFinished {
		return false
	}
	for i, old := range before.Evidence.Members {
		row := next.Evidence.Members[i]
		switch old.Dispatch {
		case resourcegroup.DispatchNotAttempted:
			if row.Dispatch != resourcegroup.DispatchNotAttempted && (row.Dispatch != resourcegroup.Dispatching || before.Admission != resourceGroupAdmissionActive || old.AdmissionStop != resourcegroup.StopNone || old.LocalDurability != resourcegroup.LocalDurable) {
				return false
			}
		case resourcegroup.Dispatching:
			if row.Dispatch != resourcegroup.Dispatching && row.Dispatch != resourcegroup.DispatchObserved && row.Dispatch != resourcegroup.DispatchUnknown {
				return false
			}
		case resourcegroup.DispatchObserved:
			if row.Dispatch != resourcegroup.DispatchObserved {
				return false
			}
		case resourcegroup.DispatchUnknown:
			if row.Dispatch != resourcegroup.DispatchUnknown && row.Dispatch != resourcegroup.DispatchObserved {
				return false
			}
		}
		if old.AdmissionStop != resourcegroup.StopNone && row.AdmissionStop != old.AdmissionStop || row.Status.Sequence < old.Status.Sequence || row.Status.Sequence == old.Status.Sequence && !resourceGroupJSONEqual(row.Status, old.Status) {
			return false
		}
		if resourceGroupTerminalDurable(old.Target) && (!resourceGroupTerminalDurable(row.Target) || row.Target.Outcome != old.Target.Outcome) {
			return false
		}
	}
	return true
}

func withResourceGroupUpdate(e resourceGroupEnvelope, run resourceGroupRecord) (resourceGroupEnvelope, error) {
	next, err := cloneResourceGroupEnvelope(e)
	if err != nil {
		return resourceGroupEnvelope{}, err
	}
	for i, previous := range next.Runs {
		if previous.RunID != run.RunID {
			continue
		}
		if !validResourceGroupTransition(previous, run) {
			return resourceGroupEnvelope{}, errResourceGroupReview
		}
		next.Runs[i], err = cloneResourceGroupRecord(run)
		if err != nil {
			return resourceGroupEnvelope{}, err
		}
		if _, err := resourceGroupReservedBytes(next); err != nil {
			return resourceGroupEnvelope{}, err
		}
		return next, nil
	}
	return resourceGroupEnvelope{}, errResourceGroupReview
}

// Produces only a desired stopped snapshot for owned republishing. It restores
// no prepared review, capability or dispatch. The writer must certify saving.
func normalizeResourceGroupEnvelope(e resourceGroupEnvelope) (resourceGroupEnvelope, error) {
	next, err := cloneResourceGroupEnvelope(e)
	if err != nil {
		return resourceGroupEnvelope{}, err
	}
	for i := range next.Runs {
		run := &next.Runs[i]
		before, err := cloneResourceGroupRecord(*run)
		if err != nil {
			return resourceGroupEnvelope{}, err
		}
		run.Admission = resourceGroupAdmissionFinished
		for j := range run.Evidence.Members {
			row := &run.Evidence.Members[j]
			if row.Dispatch == resourcegroup.Dispatching {
				row.Dispatch = resourcegroup.DispatchUnknown
			}
			if row.Execution == resourcegroup.ExecutionSelected && row.Dispatch == resourcegroup.DispatchNotAttempted && row.AdmissionStop == resourcegroup.StopNone {
				row.AdmissionStop = resourcegroup.StopRestarted
			}
			row.LocalDurability = resourcegroup.LocalDurable
		}
		if !resourceGroupJSONEqual(before, *run) {
			if run.UpdateSequence >= uint64(capacity.MaxJSONInteger) {
				return resourceGroupEnvelope{}, errResourceGroupState
			}
			run.UpdateSequence++
		}
	}
	if _, err := resourceGroupReservedBytes(next); err != nil {
		return resourceGroupEnvelope{}, err
	}
	return next, nil
}

type resourceGroupBlocker struct {
	RunID       string
	PeerKey     string
	OperationID string
}

// A grant/pair replacement cannot make old possible execution safe to repeat.
// Current resource identity is distinct from authority scope: this barrier
// compares peer+resource across scopes, but status still needs the original pair.
func resourceGroupUnresolved(e resourceGroupEnvelope, selection resourcegroup.ResolvedSelection) ([]resourceGroupBlocker, error) {
	if e.validate() != nil || selection.Validate() != nil {
		return nil, errResourceGroupState
	}
	blockers := []resourceGroupBlocker{}
	for _, run := range e.Runs {
		for _, row := range run.Evidence.Members {
			if row.Dispatch == resourcegroup.DispatchNotAttempted || resourceGroupTerminalDurable(row.Target) {
				continue
			}
			for _, member := range selection.Members {
				if row.PeerKey == member.PeerKey && row.Request != nil && row.Request.Target == member.Selector.Target {
					blockers = append(blockers, resourceGroupBlocker{RunID: run.RunID, PeerKey: row.PeerKey, OperationID: row.Request.Apply.OperationID})
				}
			}
		}
	}
	return blockers, nil
}
