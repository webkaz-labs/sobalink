package resourcegroup

import (
	"sort"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/resource"
	"github.com/webkaz-labs/sobalink/internal/resourcegrant"
)

func validLocalDurability(d LocalDurability) bool {
	return d == LocalNotSaved || d == LocalDurable || d == LocalUncertain
}

func validStop(s StopReason) bool {
	switch s {
	case StopNone, StopUserCanceled, StopBudgetExhausted, StopContextChanged, StopPersistenceUncertain, StopRestarted:
		return true
	}
	return false
}

func terminalDurable(o *resourcegrant.ManagementOperation) bool {
	return o != nil && o.EvidenceDurable != nil && *o.EvidenceDurable && o.Outcome.Status != "unknown"
}

func (m MemberEvidence) Validate() error {
	if m.SchemaVersion != SchemaVersion || !resource.ValidDigest(m.GroupRevision) || !resource.ValidDigest(m.PeerKey) || !validReviewState(m.Review) || !validLocalDurability(m.LocalDurability) || !validStop(m.AdmissionStop) {
		return ErrInvalid
	}
	if m.Execution != ExecutionSelected && m.Execution != ExecutionExcluded {
		return ErrInvalid
	}
	if m.Review == ReviewReady {
		if m.Request == nil || m.Request.Validate() != nil || m.Request.Action != resourcegrant.ApplyAction {
			return ErrInvalid
		}
	} else if m.Request != nil || m.Execution != ExecutionExcluded {
		return ErrInvalid
	}
	switch m.Dispatch {
	case DispatchNotAttempted:
		if m.Target != nil || m.Status.State != StatusNotQueried {
			return ErrInvalid
		}
	case Dispatching:
		if m.Target != nil || m.Status.State != StatusNotQueried || m.LocalDurability != LocalDurable {
			return ErrInvalid
		}
	case DispatchObserved:
		if m.Target == nil {
			return ErrInvalid
		}
	case DispatchUnknown:
	default:
		return ErrInvalid
	}
	if m.Execution == ExecutionExcluded && (m.Dispatch != DispatchNotAttempted || m.Target != nil || m.Status.State != StatusNotQueried) {
		return ErrInvalid
	}
	if m.Target != nil && (m.Request == nil || m.Target.Validate() != nil || m.Target.OperationID != m.Request.Apply.OperationID) {
		return ErrInvalid
	}
	if m.Dispatch == DispatchUnknown && m.Target != nil && m.Status.State == StatusNotQueried {
		return ErrInvalid
	}
	if m.Status.State == StatusNotQueried {
		if m.Status.Sequence != 0 || m.Status.ObservedAt != 0 || m.Status.Operation != nil {
			return ErrInvalid
		}
	} else {
		if m.Status.Sequence == 0 || m.Status.Sequence > uint64(capacity.MaxJSONInteger) || m.Status.ObservedAt <= 0 || m.Status.ObservedAt > MaxObservationTime || m.Dispatch == DispatchNotAttempted || m.Dispatch == Dispatching {
			return ErrInvalid
		}
		switch m.Status.State {
		case StatusObserved:
			if m.Status.Operation == nil || m.Status.Operation.Validate() != nil || m.Request == nil || m.Status.Operation.OperationID != m.Request.Apply.OperationID || m.Target == nil {
				return ErrInvalid
			}
			if terminalDurable(m.Status.Operation) && (m.Target.Outcome != m.Status.Operation.Outcome || !terminalDurable(m.Target)) {
				return ErrInvalid
			}
			if !terminalDurable(m.Target) && (m.Target.Outcome != m.Status.Operation.Outcome || *m.Target.EvidenceDurable != *m.Status.Operation.EvidenceDurable) {
				return ErrInvalid
			}
		case StatusUnavailable, StatusUnsupported, StatusQueryFailed:
			if m.Status.Operation != nil {
				return ErrInvalid
			}
		default:
			return ErrInvalid
		}
	}
	return nil
}

func cloneOperation(o *resourcegrant.ManagementOperation) *resourcegrant.ManagementOperation {
	if o == nil {
		return nil
	}
	next := *o
	if o.EvidenceDurable != nil {
		durable := *o.EvidenceDurable
		next.EvidenceDurable = &durable
	}
	return &next
}

func cloneEvidence(m MemberEvidence) MemberEvidence {
	if m.Request != nil {
		r := *m.Request
		if m.Request.Apply != nil {
			a := *m.Request.Apply
			a.Settings = cloneSettings(a.Settings)
			r.Apply = &a
		}
		m.Request = &r
	}
	m.Target = cloneOperation(m.Target)
	m.Status.Operation = cloneOperation(m.Status.Operation)
	return m
}

// NewEvidence copies all selected rows, including failed and excluded rows.
// It creates unsaved data, not a run, dispatch intent or execution permission.
func NewEvidence(review ReviewBody) ([]MemberEvidence, error) {
	r, err := CloneReview(review)
	if err != nil {
		return nil, ErrInvalid
	}
	rows := make([]MemberEvidence, len(r.Rows))
	for i, row := range r.Rows {
		m := MemberEvidence{
			SchemaVersion: SchemaVersion, GroupRevision: r.Revision,
			PeerKey: row.PeerKey, Review: row.State, Execution: ExecutionExcluded,
			Dispatch: DispatchNotAttempted, LocalDurability: LocalNotSaved,
			Status: StatusObservation{State: StatusNotQueried}, AdmissionStop: StopNone,
		}
		if row.State == ReviewReady {
			request := requestForRow(row)
			m.Request = &request
		}
		for _, peer := range r.ExecutionPeers {
			if peer == row.PeerKey {
				m.Execution = ExecutionSelected
			}
		}
		rows[i] = m
	}
	return rows, nil
}

func equalRequest(a, b *resourcegrant.ManagementRequest) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.ManagementSelector == b.ManagementSelector && a.Action == b.Action && a.Apply.OperationID == b.Apply.OperationID && a.Apply.BaseRevision == b.Apply.BaseRevision && a.Apply.ReviewRevision == b.Apply.ReviewRevision && equalSettings(a.Apply.Settings, b.Apply.Settings)
}

// ValidateEvidence proves complete coverage and unchanged original requests
// relative to the review. Bare Reduce cannot establish that relationship from
// rows alone. This validation still supplies neither authority nor persistence.
func ValidateEvidence(review ReviewBody, rows []MemberEvidence) error {
	expected, err := NewEvidence(review)
	if err != nil || len(rows) != len(expected) {
		return ErrInvalid
	}
	actual, err := canonicalEvidence(rows)
	if err != nil {
		return ErrInvalid
	}
	for i, want := range expected {
		got := actual[i]
		if got.GroupRevision != want.GroupRevision || got.PeerKey != want.PeerKey || got.Review != want.Review || got.Execution != want.Execution || !equalRequest(got.Request, want.Request) {
			return ErrInvalid
		}
	}
	return nil
}

func canonicalEvidence(rows []MemberEvidence) ([]MemberEvidence, error) {
	if len(rows) == 0 || len(rows) > MaxMembers {
		return nil, ErrInvalid
	}
	result := make([]MemberEvidence, len(rows))
	for i, row := range rows {
		if row.Validate() != nil || row.GroupRevision != rows[0].GroupRevision {
			return nil, ErrInvalid
		}
		result[i] = cloneEvidence(row)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].PeerKey < result[j].PeerKey })
	for i := 1; i < len(result); i++ {
		if result[i-1].PeerKey == result[i].PeerKey {
			return nil, ErrInvalid
		}
	}
	if !fits(EvidenceBody{SchemaVersion: SchemaVersion, Members: result}, MaxEvidenceBytes) {
		return nil, ErrInvalid
	}
	return result, nil
}

// Reduce reports only the supplied rows. Use ReduceReview for a complete group
// summary so omitted members or changed original requests cannot go unnoticed.
// No failure observation is translated to offline, conflict or rollback.
func Reduce(rows []MemberEvidence) (Summary, error) {
	validated, err := canonicalEvidence(rows)
	if err != nil {
		return Summary{}, ErrInvalid
	}
	s := Summary{SchemaVersion: SchemaVersion, Selected: len(validated), AdmissionFinished: true, AllApplied: true}
	for _, row := range validated {
		if row.Review != ReviewReady {
			s.ReviewFailures++
		}
		if row.LocalDurability != LocalDurable {
			s.LocalNonDurable++
		}
		if row.Execution == ExecutionExcluded {
			s.Excluded++
			continue
		}
		s.Executable++
		switch row.Dispatch {
		case DispatchNotAttempted:
			s.NotAttempted++
			if row.AdmissionStop == StopNone {
				s.AdmissionFinished = false
			}
		case Dispatching:
			s.Dispatching++
			s.AdmissionFinished = false
		case DispatchObserved:
			s.DispatchObserved++
		case DispatchUnknown:
			s.DispatchUnknown++
		}
		if row.Status.State == StatusUnavailable || row.Status.State == StatusUnsupported || row.Status.State == StatusQueryFailed {
			s.StatusFailures++
		}
		if row.Target == nil {
			s.TargetUnobserved++
		} else {
			if !*row.Target.EvidenceDurable {
				s.TargetNonDurable++
			}
			switch row.Target.Outcome.Status {
			case "applied":
				s.Applied++
			case "failed":
				s.Failed++
			case "canceled":
				s.Canceled++
			case "saved_not_applied":
				s.SavedNotApplied++
			case "unknown":
				s.TargetUnknown++
			}
		}
		if !terminalDurable(row.Target) || row.Target.Outcome.Status != "applied" {
			s.AllApplied = false
		}
		if row.Dispatch != DispatchNotAttempted && (!terminalDurable(row.Target) || row.LocalDurability != LocalDurable) {
			s.ReconciliationRequired = true
		}
	}
	if s.Executable == 0 {
		s.AllApplied = false
	}
	return s, nil
}

func ReduceReview(review ReviewBody, rows []MemberEvidence) (Summary, error) {
	if ValidateEvidence(review, rows) != nil {
		return Summary{}, ErrInvalid
	}
	return Reduce(rows)
}

// mergeTarget never erases a verified durable terminal result with a weaker
// observation. Conflicting durable terminal evidence is rejected, not guessed.
func mergeTarget(old, next *resourcegrant.ManagementOperation) (*resourcegrant.ManagementOperation, error) {
	if terminalDurable(old) {
		if terminalDurable(next) && old.Outcome != next.Outcome {
			return nil, ErrInvalid
		}
		return cloneOperation(old), nil
	}
	return cloneOperation(next), nil
}

// ObserveDispatchUnknown finishes local observation of a possibly started call
// without a matched response. Cancellation, generic unavailable and response
// loss all leave the target outcome unknown. No unsent row is changed here.
func ObserveDispatchUnknown(row MemberEvidence, local LocalDurability) (MemberEvidence, error) {
	if row.Validate() != nil || row.Dispatch != Dispatching || !validLocalDurability(local) {
		return MemberEvidence{}, ErrInvalid
	}
	next := cloneEvidence(row)
	next.Dispatch = DispatchUnknown
	next.LocalDurability = local
	return next, nil
}

// ObserveApply records one matching response to a possibly started call. The
// owner supplies actual local publication durability; this function does not
// save it. A generic call error must instead leave possible dispatch unknown.
func ObserveApply(row MemberEvidence, reply resourcegrant.ManagementReply, local LocalDurability) (MemberEvidence, error) {
	if row.Validate() != nil || !validLocalDurability(local) || (row.Dispatch != Dispatching && row.Dispatch != DispatchUnknown) || reply.Validate() != nil || reply.Action != resourcegrant.ApplyAction || reply.ManagementSelector != row.Request.ManagementSelector || reply.Operation.OperationID != row.Request.Apply.OperationID {
		return MemberEvidence{}, ErrInvalid
	}
	next := cloneEvidence(row)
	var err error
	next.Target, err = mergeTarget(row.Target, reply.Operation)
	if err != nil {
		return MemberEvidence{}, ErrInvalid
	}
	next.Dispatch = DispatchObserved
	next.LocalDurability = local
	if next.Validate() != nil {
		return MemberEvidence{}, ErrInvalid
	}
	return next, nil
}

// ObserveStatus keeps the original apply selector unchanged. This schema only
// supports that exact selector for queries; any future same-grant revision
// override needs a separate explicit contract, not silent substitution here.
func ObserveStatus(row MemberEvidence, reply resourcegrant.ManagementReply, sequence uint64, observedAt int64, local LocalDurability) (MemberEvidence, error) {
	if row.Validate() != nil || row.Dispatch == DispatchNotAttempted || row.Dispatch == Dispatching || !validLocalDurability(local) || reply.Validate() != nil || reply.Action != resourcegrant.StatusAction || reply.ManagementSelector != row.Request.ManagementSelector || sequence <= row.Status.Sequence {
		return MemberEvidence{}, ErrInvalid
	}
	next := cloneEvidence(row)
	next.Status = StatusObservation{State: StatusUnavailable, Sequence: sequence, ObservedAt: observedAt}
	if reply.Unavailable != nil {
		if reply.Unavailable.OperationID != row.Request.Apply.OperationID {
			return MemberEvidence{}, ErrInvalid
		}
	} else {
		if reply.Operation.OperationID != row.Request.Apply.OperationID {
			return MemberEvidence{}, ErrInvalid
		}
		var err error
		next.Target, err = mergeTarget(row.Target, reply.Operation)
		if err != nil {
			return MemberEvidence{}, ErrInvalid
		}
		next.Status.State = StatusObserved
		next.Status.Operation = cloneOperation(reply.Operation)
	}
	next.LocalDurability = local
	if next.Validate() != nil {
		return MemberEvidence{}, ErrInvalid
	}
	return next, nil
}

// ObserveStatusFailure retains all prior target evidence. Only the authenticated
// existing unsupported result can justify StatusUnsupported; this data helper
// cannot verify that provenance and never derives it from a timeout.
func ObserveStatusFailure(row MemberEvidence, state StatusState, sequence uint64, observedAt int64, local LocalDurability) (MemberEvidence, error) {
	if row.Validate() != nil || row.Dispatch == DispatchNotAttempted || row.Dispatch == Dispatching || !validLocalDurability(local) || sequence <= row.Status.Sequence || (state != StatusUnavailable && state != StatusUnsupported && state != StatusQueryFailed) {
		return MemberEvidence{}, ErrInvalid
	}
	next := cloneEvidence(row)
	next.Status = StatusObservation{State: state, Sequence: sequence, ObservedAt: observedAt}
	next.LocalDurability = local
	if next.Validate() != nil {
		return MemberEvidence{}, ErrInvalid
	}
	return next, nil
}
