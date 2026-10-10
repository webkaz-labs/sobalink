package resourcecatalog

import (
	"github.com/webkaz-labs/sobalink/internal/resource"
)

// InspectionV1 contains exactly the existing inspection response values. The
// exchange owner must correlate it to Selection: v1 does not echo the grant.
// No local Descriptor or grant record may be supplied as a substitute.
type InspectionV1 struct {
	ProtocolVersion int                `json:"protocolVersion"`
	Target          resource.Target    `json:"target"`
	Requested       resource.Settings  `json:"requested"`
	Effective       resource.Effective `json:"effective"`
}

// InspectionV2 contains only the existing inspect reply arm, including the
// echoed selector. It cannot represent previews, operations or unavailable.
type InspectionV2 struct {
	ProtocolVersion int             `json:"protocolVersion"`
	Target          resource.Target `json:"target"`
	GrantID         string          `json:"grantId"`
	GrantRevision   uint64          `json:"grantRevision"`
	Action          string          `json:"action"`
	Inspection      SettingsValues  `json:"inspection"`
}

func ProjectLocalSettings(s Selection, value resource.Descriptor, l Limits) (Row, error) {
	return project(s, Row{Identity: Identity{ID: value.ResourceID, Lifetime: Persistent}, LocalSettings: &value}, l)
}
func ProjectInspectionV1(s Selection, value InspectionV1, l Limits) (Row, error) {
	if s.Kind != RemoteSettingsV1 || value.ProtocolVersion != 1 || value.Target != s.Target {
		return Row{}, ErrInvalid
	}
	values := SettingsValues{value.Requested, value.Effective}
	return project(s, Row{Identity: Identity{ID: value.Target.ResourceID, Lifetime: Persistent}, RemoteSettingsV1: &values}, l)
}
func ProjectInspectionV2(s Selection, value InspectionV2, l Limits) (Row, error) {
	if s.Kind != RemoteSettingsV2 || value.ProtocolVersion != 2 || value.Target != s.Target || value.GrantID != s.GrantID || value.GrantRevision != s.GrantRevision || value.Action != "inspect" {
		return Row{}, ErrInvalid
	}
	return project(s, Row{Identity: Identity{ID: value.Target.ResourceID, Lifetime: Persistent}, RemoteSettingsV2: &value.Inspection}, l)
}
func ProjectSavedService(s Selection, savedID string, value SavedService, l Limits) (Row, error) {
	return project(s, Row{Identity: Identity{ID: savedID, Lifetime: Persistent}, LocalService: &value}, l)
}
func ProjectSharedService(s Selection, discoveryID string, value SharedService, l Limits) (Row, error) {
	return project(s, Row{Identity: Identity{ID: discoveryID, Lifetime: Activation}, RemoteService: &value}, l)
}
func ProjectTransfer(s Selection, originalID, direction string, value Transfer, l Limits) (Row, error) {
	return project(s, Row{Identity: Identity{ID: originalID, Lifetime: Process, Direction: direction}, TransferActivity: &value}, l)
}
func project(s Selection, r Row, l Limits) (Row, error) {
	if r.Validate(s, l) != nil {
		return Row{}, ErrInvalid
	}
	data, err := encodeBounded(r, l.MaxPageBytes, l)
	if err != nil {
		return Row{}, err
	}
	var copy Row
	if decodeClosed(data, l.MaxPageBytes, l, &copy) != nil {
		return Row{}, ErrInvalid
	}
	return copy, nil
}

// Workflow is advisory navigation to an existing review. It is never a command
// or grant. The selected workflow must freshly reauthorize and obtain its own
// exact review before any mutation. No apply, transfer acceptance or retry is
// advertised by a catalog observation. IDs remain in their original namespace.
type Workflow struct {
	Kind           string `json:"kind"`
	SourceID       string `json:"sourceId"`
	ID             string `json:"id"`
	Direction      string `json:"direction"`
	ReviewRevision string `json:"reviewRevision"`
}

// Workflows requires a complete current source and caller-supplied time. For
// discovery it preserves the existing 15s freshness/5s future-clock allowance.
// This clock check only removes advisory controls; it never extends admission.
func Workflows(v SourceView, r Row, now int64, l Limits) []Workflow {
	out := []Workflow{}
	if v.validate(l) != nil || r.Validate(v.Selection, l) != nil || !timestamp(now) || v.State != "current" || !v.Complete {
		return out
	}
	found := false
	for _, row := range v.Rows {
		if row.Identity == r.Identity && equalRow(row, r) {
			found = true
			break
		}
	}
	if !found {
		return out
	}
	ref := Workflow{SourceID: v.Selection.SourceID, ID: r.Identity.ID, Direction: r.Identity.Direction}
	switch v.Selection.Kind {
	case LocalSettings, RemoteSettingsV2:
		ref.Kind = "inspect_settings"
		out = append(out, ref)
		ref.Kind = "review_settings"
		out = append(out, ref)
	case RemoteSettingsV1:
		ref.Kind = "inspect_settings"
		out = append(out, ref)
	case LocalService:
		ref.Kind = "review_saved_service"
		out = append(out, ref)
		if oneOf(r.LocalService.State, "saved", "stopped", "expired") {
			ref.Kind = "review_service_start"
			out = append(out, ref)
		} else if oneOf(r.LocalService.State, "starting", "active", "reconnecting") {
			ref.Kind = "review_service_stop"
			out = append(out, ref)
		}
		// Failed can describe an inactive startup failure or an active runtime
		// failure. The fresh saved-service workflow resolves that distinction.
	case RemoteService:
		if v.CheckedAt > now+5000 || now-v.CheckedAt > 15000 || r.RemoteService.ExpiresAt != 0 && r.RemoteService.ExpiresAt <= now {
			return out
		}
		ref.Kind = "review_service_connect"
		ref.ReviewRevision = r.RemoteService.ReviewRevision
		out = append(out, ref)
	case TransferActivity:
		ref.Kind = "open_transfer"
		out = append(out, ref)
	}
	return out
}

// HistoricalResult is independent of current observations, source completeness
// and dispatch evidence. Local operation IDs and remote opaque IDs are distinct.
// It contains neither current settings nor private/global journal usage.
type HistoricalResult struct {
	Selection       Selection        `json:"selection"`
	OperationID     string           `json:"operationId"`
	Outcome         resource.Outcome `json:"outcome"`
	EvidenceDurable *bool            `json:"evidenceDurable"`
}

func (r HistoricalResult) Validate(l Limits) error {
	if r.Selection.Validate(l) != nil || r.Outcome.Validate() != nil || r.EvidenceDurable == nil {
		return ErrInvalid
	}
	switch r.Selection.Kind {
	case LocalSettings:
		id, _, _, err := resource.ParseOperationID(r.OperationID)
		if err != nil || id != r.Selection.Target.ResourceID {
			return ErrInvalid
		}
	case RemoteSettingsV2:
		if !resource.ValidDigest(r.OperationID) {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	if !text(r.OperationID, l) {
		return ErrInvalid
	}
	return nil
}

func ProjectLocalResult(s Selection, value resource.Operation, l Limits) (HistoricalResult, error) {
	if s.Kind != LocalSettings || value.Target != s.Target {
		return HistoricalResult{}, ErrInvalid
	}
	// Current and Journal are intentionally not copied or interpreted as evidence.
	durable := value.EvidenceDurable
	return projectResult(HistoricalResult{s, value.OperationID, value.Outcome, &durable}, l)
}

// ProjectRemoteResult receives an already matched management operation response.
// The owning adapter must validate its full reply and requested operation ID
// before extracting these fields. This function grants no disclosure permission.
func ProjectRemoteResult(s Selection, expectedOperationID, operationID string, outcome resource.Outcome, evidenceDurable *bool, l Limits) (HistoricalResult, error) {
	if s.Kind != RemoteSettingsV2 || expectedOperationID != operationID {
		return HistoricalResult{}, ErrInvalid
	}
	return projectResult(HistoricalResult{s, operationID, outcome, evidenceDurable}, l)
}
func projectResult(r HistoricalResult, l Limits) (HistoricalResult, error) {
	if r.Validate(l) != nil {
		return HistoricalResult{}, ErrInvalid
	}
	data, err := encodeBounded(r, l.MaxPageBytes, l)
	if err != nil {
		return HistoricalResult{}, err
	}
	var copy HistoricalResult
	if decodeClosed(data, l.MaxPageBytes, l, &copy) != nil {
		return HistoricalResult{}, ErrInvalid
	}
	return copy, nil
}

// ResultObservation distinguishes unavailable operation evidence from an
// observed historical outcome. Unavailable never means absent, failed, evicted
// or not executed. This shape is separate from descriptor freshness and has no
// implicit status-to-apply transition.
type ResultObservation struct {
	Selection   Selection         `json:"selection"`
	OperationID string            `json:"operationId"`
	CheckedAt   int64             `json:"checkedAt"`
	State       string            `json:"state"`
	Result      *HistoricalResult `json:"result,omitempty"`
}

func (v ResultObservation) Validate(l Limits) error {
	if v.Selection.Validate(l) != nil || !timestamp(v.CheckedAt) || !text(v.OperationID, l) {
		return ErrInvalid
	}
	if v.State == "unavailable" {
		if v.Selection.Kind != RemoteSettingsV2 || !resource.ValidDigest(v.OperationID) || v.Result != nil {
			return ErrInvalid
		}
		return nil
	}
	if v.State != "observed" || v.Result == nil || v.Result.Validate(l) != nil || v.Result.Selection != v.Selection || v.Result.OperationID != v.OperationID {
		return ErrInvalid
	}
	return nil
}
