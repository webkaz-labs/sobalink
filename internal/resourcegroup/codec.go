package resourcegroup

import "github.com/webkaz-labs/sobalink/internal/resource"

// DecodeSelection uses the existing exact-tag, duplicate-key, null, depth and
// trailing-data checks, then validates the closed schema before returning an
// independent canonical copy. No arbitrary property maps enter the contract.
func DecodeSelection(data []byte) (Selection, error) {
	var s Selection
	if resource.Decode(data, MaxSelectionBytes, &s) != nil {
		return Selection{}, ErrInvalid
	}
	r, err := ResolveSelection(s)
	if err != nil {
		return Selection{}, ErrInvalid
	}
	return selectionFromResolved(r), nil
}

func DecodeReview(data []byte) (ReviewBody, error) {
	var r ReviewBody
	if resource.Decode(data, MaxReviewBytes, &r) != nil {
		return ReviewBody{}, ErrInvalid
	}
	return CloneReview(r)
}

// DecodeEvidence validates rows, not their coverage of an owned review. Call
// ValidateEvidence or ReduceReview before treating them as that group's result.
// Deserialization never restores admission, durability or execution authority.
func DecodeEvidence(data []byte) (EvidenceBody, error) {
	var e EvidenceBody
	if resource.Decode(data, MaxEvidenceBytes, &e) != nil || e.SchemaVersion != SchemaVersion {
		return EvidenceBody{}, ErrInvalid
	}
	rows, err := canonicalEvidence(e.Members)
	if err != nil {
		return EvidenceBody{}, ErrInvalid
	}
	return EvidenceBody{SchemaVersion: SchemaVersion, Members: rows}, nil
}
