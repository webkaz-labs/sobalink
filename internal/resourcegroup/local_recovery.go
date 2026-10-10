package resourcegroup

import "github.com/webkaz-labs/sobalink/internal/resource"

// Current-review lookup is an explicit authenticated-local read. It never
// enumerates runs, creates a review or establishes execution authority.
const LocalCurrentReviewCommand = "resource.group.review.current"

type CurrentReviewInput struct {
	SchemaVersion int `json:"schemaVersion"`
}

type CurrentReviewState string

const (
	CurrentReviewNone    CurrentReviewState = "none"
	CurrentReviewCurrent CurrentReviewState = "current"
)

// None means only that no unused prepared review remains. It makes no claim
// about saved runs, target execution or unresolved historical evidence.
type CurrentReviewView struct {
	SchemaVersion int                `json:"schemaVersion"`
	State         CurrentReviewState `json:"state"`
	Prepared      *PreparedView      `json:"prepared,omitempty"`
}

func (in CurrentReviewInput) Validate() error {
	if in.SchemaVersion != SchemaVersion {
		return ErrInvalid
	}
	return nil
}

func (view CurrentReviewView) Validate() error {
	if view.SchemaVersion != SchemaVersion || !fits(view, MaxLocalResponseBytes) {
		return ErrInvalid
	}
	switch view.State {
	case CurrentReviewNone:
		if view.Prepared != nil {
			return ErrInvalid
		}
	case CurrentReviewCurrent:
		if view.Prepared == nil || view.Prepared.Validate() != nil || view.Prepared.AdmissionState != AdmissionPrepared && view.Prepared.AdmissionState != AdmissionUnavailable {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}

func DecodeCurrentReviewInput(data []byte) (CurrentReviewInput, error) {
	var in CurrentReviewInput
	if resource.Decode(data, MaxLocalInputBytes, &in) != nil || in.Validate() != nil {
		return CurrentReviewInput{}, ErrInvalid
	}
	return in, nil
}

func DecodeCurrentReviewView(data []byte) (CurrentReviewView, error) {
	var view CurrentReviewView
	if resource.Decode(data, MaxLocalResponseBytes, &view) != nil || view.Validate() != nil {
		return CurrentReviewView{}, ErrInvalid
	}
	if view.Prepared != nil {
		prepared, err := canonicalPreparedView(*view.Prepared)
		if err != nil {
			return CurrentReviewView{}, ErrInvalid
		}
		view.Prepared = &prepared
	}
	return view, nil
}
