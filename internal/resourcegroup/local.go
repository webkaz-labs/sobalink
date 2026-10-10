package resourcegroup

import (
	"encoding/json"
	"sort"

	"github.com/webkaz-labs/sobalink/internal/resource"
)

// Local command data never authenticates a peer or restores prepared admission.
// Core owns the opaque ID and the actual current-boot prepared object.
const (
	LocalPreviewCommand   = "resource.group.preview"
	LocalSelectCommand    = "resource.group.review.select"
	LocalApplyCommand     = "resource.group.apply"
	LocalStatusCommand    = "resource.group.status"
	LocalRefreshCommand   = "resource.group.status.refresh"
	LocalCancelCommand    = "resource.group.cancel"
	MaxLocalInputBytes    = MaxSelectionBytes + 8*1024
	MaxLocalResponseBytes = MaxReviewBytes + MaxEvidenceBytes + 8*1024
)

type PreviewInput struct {
	SchemaVersion   int       `json:"schemaVersion"`
	Selection       Selection `json:"selection"`
	ReplaceReviewID string    `json:"replaceReviewId,omitempty"`
}

type SelectInput struct {
	SchemaVersion  int      `json:"schemaVersion"`
	ReviewID       string   `json:"reviewId"`
	ReviewRevision string   `json:"reviewRevision"`
	ExecutionPeers []string `json:"executionPeers"`
}

type ApplyInput struct {
	SchemaVersion  int      `json:"schemaVersion"`
	ReviewID       string   `json:"reviewId"`
	ReviewRevision string   `json:"reviewRevision"`
	ExecutionPeers []string `json:"executionPeers"`
	Confirm        bool     `json:"confirm"`
}

type StatusInput struct {
	SchemaVersion int    `json:"schemaVersion"`
	RunID         string `json:"runId"`
}

type RefreshInput struct {
	SchemaVersion int      `json:"schemaVersion"`
	RunID         string   `json:"runId"`
	Peers         []string `json:"peers"`
}

type CancelInput struct {
	SchemaVersion int    `json:"schemaVersion"`
	ReviewID      string `json:"reviewId"`
}

type AdmissionState string
type Activity string

const (
	AdmissionPrepared    AdmissionState = "prepared"
	AdmissionCanceled    AdmissionState = "canceled"
	AdmissionUnavailable AdmissionState = "unavailable"
	ActivityIdle         Activity       = "idle"
	ActivityApplying     Activity       = "applying"
	ActivityRefreshing   Activity       = "refreshing"
)

// PreparedView is presentation data, not a credential. Importing it cannot
// create a prepared object. The required flag is explicit even when false.
type PreparedView struct {
	SchemaVersion            int            `json:"schemaVersion"`
	ReviewID                 string         `json:"reviewId"`
	Review                   ReviewBody     `json:"review"`
	AdmissionState           AdmissionState `json:"admissionState"`
	InitializesLocalEvidence *bool          `json:"initializesLocalEvidence"`
}

// RunView reports one local historical run. It contains no persisted origin
// binding or transport capture. Summary must cover its entire exact review.
type RunView struct {
	SchemaVersion   int             `json:"schemaVersion"`
	RunID           string          `json:"runId"`
	AcceptedAt      int64           `json:"acceptedAt"`
	Review          ReviewBody      `json:"review"`
	Evidence        EvidenceBody    `json:"evidence"`
	Summary         Summary         `json:"summary"`
	LocalDurability LocalDurability `json:"localDurability"`
	Activity        Activity        `json:"activity"`
}

// CancelView has exactly one arm and retains the same known review/run ID.
type CancelView struct {
	SchemaVersion int           `json:"schemaVersion"`
	Prepared      *PreparedView `json:"prepared,omitempty"`
	Run           *RunView      `json:"run,omitempty"`
}

func validLocalPeers(peers []string, allowEmpty bool) bool {
	if peers == nil || len(peers) > MaxMembers || !allowEmpty && len(peers) == 0 {
		return false
	}
	for i, peer := range peers {
		if !resource.ValidDigest(peer) {
			return false
		}
		for j := 0; j < i; j++ {
			if peers[j] == peer {
				return false
			}
		}
	}
	return true
}

func copyLocalPeers(peers []string) []string {
	result := append([]string{}, peers...)
	sort.Strings(result)
	return result
}

func (in PreviewInput) Validate() error {
	if in.SchemaVersion != SchemaVersion || in.Selection.Validate() != nil || in.ReplaceReviewID != "" && !resource.ValidID(in.ReplaceReviewID) || !fits(in, MaxLocalInputBytes) {
		return ErrInvalid
	}
	return nil
}

func (in SelectInput) Validate() error {
	if in.SchemaVersion != SchemaVersion || !resource.ValidID(in.ReviewID) || !resource.ValidDigest(in.ReviewRevision) || !validLocalPeers(in.ExecutionPeers, true) {
		return ErrInvalid
	}
	return nil
}

func (in ApplyInput) Validate() error {
	if !in.Confirm || !validLocalPeers(in.ExecutionPeers, false) || (SelectInput{in.SchemaVersion, in.ReviewID, in.ReviewRevision, in.ExecutionPeers}).Validate() != nil {
		return ErrInvalid
	}
	return nil
}

func (in StatusInput) Validate() error {
	if in.SchemaVersion != SchemaVersion || !resource.ValidID(in.RunID) {
		return ErrInvalid
	}
	return nil
}

func (in RefreshInput) Validate() error {
	if (StatusInput{in.SchemaVersion, in.RunID}).Validate() != nil || !validLocalPeers(in.Peers, false) {
		return ErrInvalid
	}
	return nil
}

func (in CancelInput) Validate() error {
	return (StatusInput{in.SchemaVersion, in.ReviewID}).Validate()
}

func (view PreparedView) Validate() error {
	if view.SchemaVersion != SchemaVersion || !resource.ValidID(view.ReviewID) || view.Review.Validate() != nil || view.InitializesLocalEvidence == nil {
		return ErrInvalid
	}
	switch view.AdmissionState {
	case AdmissionPrepared:
		if len(view.Review.ExecutionPeers) == 0 {
			return ErrInvalid
		}
	case AdmissionCanceled, AdmissionUnavailable:
	default:
		return ErrInvalid
	}
	if !fits(view, MaxLocalResponseBytes) {
		return ErrInvalid
	}
	return nil
}

func (view RunView) Validate() error {
	if view.SchemaVersion != SchemaVersion || !resource.ValidID(view.RunID) || view.AcceptedAt <= 0 || view.AcceptedAt > MaxObservationTime || view.Evidence.SchemaVersion != SchemaVersion || !validLocalDurability(view.LocalDurability) {
		return ErrInvalid
	}
	if view.Activity != ActivityIdle && view.Activity != ActivityApplying && view.Activity != ActivityRefreshing {
		return ErrInvalid
	}
	summary, err := ReduceReview(view.Review, view.Evidence.Members)
	if err != nil || summary != view.Summary || !fits(view, MaxLocalResponseBytes) {
		return ErrInvalid
	}
	return nil
}

func (view CancelView) Validate() error {
	if view.SchemaVersion != SchemaVersion || (view.Prepared == nil) == (view.Run == nil) {
		return ErrInvalid
	}
	if view.Prepared != nil && (view.Prepared.Validate() != nil || view.Prepared.AdmissionState != AdmissionCanceled) || view.Run != nil && view.Run.Validate() != nil {
		return ErrInvalid
	}
	if !fits(view, MaxLocalResponseBytes) {
		return ErrInvalid
	}
	return nil
}

func DecodePreviewInput(data []byte) (PreviewInput, error) {
	var in PreviewInput
	if resource.Decode(data, MaxLocalInputBytes, &in) != nil || in.Validate() != nil {
		return PreviewInput{}, ErrInvalid
	}
	resolved, err := ResolveSelection(in.Selection)
	if err != nil {
		return PreviewInput{}, ErrInvalid
	}
	in.Selection = selectionFromResolved(resolved)
	return in, nil
}

func DecodeSelectInput(data []byte) (SelectInput, error) {
	var in SelectInput
	if resource.Decode(data, MaxLocalInputBytes, &in) != nil || in.Validate() != nil {
		return SelectInput{}, ErrInvalid
	}
	in.ExecutionPeers = copyLocalPeers(in.ExecutionPeers)
	return in, nil
}

func DecodeApplyInput(data []byte) (ApplyInput, error) {
	var in ApplyInput
	if resource.Decode(data, MaxLocalInputBytes, &in) != nil || in.Validate() != nil {
		return ApplyInput{}, ErrInvalid
	}
	in.ExecutionPeers = copyLocalPeers(in.ExecutionPeers)
	return in, nil
}

func DecodeStatusInput(data []byte) (StatusInput, error) {
	var in StatusInput
	if resource.Decode(data, MaxLocalInputBytes, &in) != nil || in.Validate() != nil {
		return StatusInput{}, ErrInvalid
	}
	return in, nil
}

func DecodeRefreshInput(data []byte) (RefreshInput, error) {
	var in RefreshInput
	if resource.Decode(data, MaxLocalInputBytes, &in) != nil || in.Validate() != nil {
		return RefreshInput{}, ErrInvalid
	}
	in.Peers = copyLocalPeers(in.Peers)
	return in, nil
}

func DecodeCancelInput(data []byte) (CancelInput, error) {
	var in CancelInput
	if resource.Decode(data, MaxLocalInputBytes, &in) != nil || in.Validate() != nil {
		return CancelInput{}, ErrInvalid
	}
	return in, nil
}

func DecodePreparedView(data []byte) (PreparedView, error) {
	var view PreparedView
	if resource.Decode(data, MaxLocalResponseBytes, &view) != nil || view.Validate() != nil {
		return PreparedView{}, ErrInvalid
	}
	return canonicalPreparedView(view)
}

func DecodeRunView(data []byte) (RunView, error) {
	var view RunView
	if resource.Decode(data, MaxLocalResponseBytes, &view) != nil || view.Validate() != nil || !completeLocalSummary(data, false) {
		return RunView{}, ErrInvalid
	}
	return canonicalRunView(view)
}

func DecodeCancelView(data []byte) (CancelView, error) {
	var view CancelView
	if resource.Decode(data, MaxLocalResponseBytes, &view) != nil || view.Validate() != nil || view.Run != nil && !completeLocalSummary(data, true) {
		return CancelView{}, ErrInvalid
	}
	if view.Prepared != nil {
		prepared, err := canonicalPreparedView(*view.Prepared)
		if err != nil {
			return CancelView{}, ErrInvalid
		}
		view.Prepared = &prepared
	} else {
		run, err := canonicalRunView(*view.Run)
		if err != nil {
			return CancelView{}, ErrInvalid
		}
		view.Run = &run
	}
	return view, nil
}

func canonicalPreparedView(view PreparedView) (PreparedView, error) {
	review, err := CloneReview(view.Review)
	if err != nil {
		return PreparedView{}, ErrInvalid
	}
	view.Review = review
	flag := *view.InitializesLocalEvidence
	view.InitializesLocalEvidence = &flag
	return view, nil
}

func canonicalRunView(view RunView) (RunView, error) {
	review, err := CloneReview(view.Review)
	if err != nil {
		return RunView{}, ErrInvalid
	}
	rows, err := canonicalEvidence(view.Evidence.Members)
	if err != nil {
		return RunView{}, ErrInvalid
	}
	view.Review = review
	view.Evidence.Members = rows
	return view, nil
}

// Zero counts and false flags are required observations, not optional defaults.
// The normal closed typed decode above runs first. This second fixed-shape
// projection checks presence only; it cannot admit any unknown input fields.
type localSummaryPresence struct {
	SchemaVersion          *int  `json:"schemaVersion"`
	Selected               *int  `json:"selected"`
	Executable             *int  `json:"executable"`
	Excluded               *int  `json:"excluded"`
	ReviewFailures         *int  `json:"reviewFailures"`
	NotAttempted           *int  `json:"notAttempted"`
	Dispatching            *int  `json:"dispatching"`
	DispatchObserved       *int  `json:"dispatchObserved"`
	DispatchUnknown        *int  `json:"dispatchUnknown"`
	Applied                *int  `json:"applied"`
	Failed                 *int  `json:"failed"`
	Canceled               *int  `json:"canceled"`
	SavedNotApplied        *int  `json:"savedNotApplied"`
	TargetUnknown          *int  `json:"targetUnknown"`
	TargetUnobserved       *int  `json:"targetUnobserved"`
	TargetNonDurable       *int  `json:"targetNonDurable"`
	LocalNonDurable        *int  `json:"localNonDurable"`
	StatusFailures         *int  `json:"statusFailures"`
	AdmissionFinished      *bool `json:"admissionFinished"`
	ReconciliationRequired *bool `json:"reconciliationRequired"`
	AllApplied             *bool `json:"allApplied"`
}

func (s localSummaryPresence) complete() bool {
	return s.SchemaVersion != nil && s.Selected != nil && s.Executable != nil && s.Excluded != nil && s.ReviewFailures != nil && s.NotAttempted != nil && s.Dispatching != nil && s.DispatchObserved != nil && s.DispatchUnknown != nil && s.Applied != nil && s.Failed != nil && s.Canceled != nil && s.SavedNotApplied != nil && s.TargetUnknown != nil && s.TargetUnobserved != nil && s.TargetNonDurable != nil && s.LocalNonDurable != nil && s.StatusFailures != nil && s.AdmissionFinished != nil && s.ReconciliationRequired != nil && s.AllApplied != nil
}

func completeLocalSummary(data []byte, cancelReply bool) bool {
	var projection struct {
		Summary localSummaryPresence `json:"summary"`
		Run     *struct {
			Summary localSummaryPresence `json:"summary"`
		} `json:"run"`
	}
	if json.Unmarshal(data, &projection) != nil {
		return false
	}
	if cancelReply {
		return projection.Run != nil && projection.Run.Summary.complete()
	}
	return projection.Summary.complete()
}
