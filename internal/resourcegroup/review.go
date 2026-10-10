package resourcegroup

import (
	"sort"

	"github.com/webkaz-labs/sobalink/internal/resource"
	"github.com/webkaz-labs/sobalink/internal/resourcegrant"
)

// BuildReview never chooses a subset implicitly. executionPeers must explicitly
// name only ready rows; [] is allowed for a non-executable review, nil is not.
// Changing a ready-row exclusion creates a different review revision. Neither
// construction nor that revision constitutes user confirmation or admission.
func BuildReview(selection ResolvedSelection, rows []ReviewRow, executionPeers []string) (ReviewBody, error) {
	s, err := canonicalResolved(selection)
	if err != nil || len(rows) != len(s.Members) || executionPeers == nil || len(executionPeers) > len(s.Members) {
		return ReviewBody{}, ErrInvalid
	}
	r := ReviewBody{SchemaVersion: SchemaVersion, Selection: s, Rows: make([]ReviewRow, len(rows)), ExecutionPeers: append([]string{}, executionPeers...)}
	for i, row := range rows {
		if row.SchemaVersion != SchemaVersion || !resource.ValidDigest(row.PeerKey) || !validReviewState(row.State) {
			return ReviewBody{}, ErrInvalid
		}
		if row.State == ReviewReady {
			if row.Reply == nil || row.Reply.Validate() != nil || row.Reply.Action != resourcegrant.PreviewAction {
				return ReviewBody{}, ErrInvalid
			}
			copyReply := *row.Reply
			preview := *row.Reply.Preview
			preview.Requested = cloneSettings(preview.Requested)
			copyReply.Preview = &preview
			row.Reply = &copyReply
		} else if row.Reply != nil {
			return ReviewBody{}, ErrInvalid
		}
		r.Rows[i] = row
	}
	sort.Slice(r.Rows, func(i, j int) bool { return r.Rows[i].PeerKey < r.Rows[j].PeerKey })
	for i, member := range s.Members {
		row := r.Rows[i]
		if row.PeerKey != member.PeerKey || row.State == ReviewReady && (row.Reply.ManagementSelector != member.Selector || !equalSettings(row.Reply.Preview.Requested, member.Requested)) {
			return ReviewBody{}, ErrInvalid
		}
	}
	sort.Strings(r.ExecutionPeers)
	for i, peer := range r.ExecutionPeers {
		if !resource.ValidDigest(peer) || i > 0 && peer == r.ExecutionPeers[i-1] {
			return ReviewBody{}, ErrInvalid
		}
		found := false
		for _, row := range r.Rows {
			if row.PeerKey == peer && row.State == ReviewReady {
				found = true
			}
		}
		if !found {
			return ReviewBody{}, ErrInvalid
		}
	}
	r.Revision = reviewDigest(r)
	if !fits(r, MaxReviewBytes) {
		return ReviewBody{}, ErrInvalid
	}
	return r, nil
}

func reviewDigest(r ReviewBody) string {
	return digest("sobalink.resourcegroup.review.v1\x00", struct {
		SchemaVersion  int               `json:"schemaVersion"`
		Selection      ResolvedSelection `json:"selection"`
		Rows           []ReviewRow       `json:"rows"`
		ExecutionPeers []string          `json:"executionPeers"`
	}{r.SchemaVersion, r.Selection, r.Rows, r.ExecutionPeers})
}

// ReviewRevision validates and canonicalizes the body, excluding its Revision
// field. Use ReviewBody.Validate or CloneReview to verify a supplied revision.
func ReviewRevision(r ReviewBody) (string, error) {
	if r.SchemaVersion != SchemaVersion {
		return "", ErrInvalid
	}
	next, err := BuildReview(r.Selection, r.Rows, r.ExecutionPeers)
	if err != nil {
		return "", ErrInvalid
	}
	return next.Revision, nil
}

func (r ReviewBody) Validate() error {
	revision, err := ReviewRevision(r)
	if err != nil || !resource.ValidDigest(r.Revision) || revision != r.Revision {
		return ErrInvalid
	}
	return nil
}

// CloneReview validates the supplied revision and returns an independent,
// canonical copy. Exported DTOs are mutable; owners must keep this frozen copy
// private and return separate copies to presentation or untrusted callers.
func CloneReview(r ReviewBody) (ReviewBody, error) {
	if r.Validate() != nil {
		return ReviewBody{}, ErrInvalid
	}
	return BuildReview(r.Selection, r.Rows, r.ExecutionPeers)
}

func requestForRow(row ReviewRow) resourcegrant.ManagementRequest {
	p := row.Reply.Preview
	return resourcegrant.ManagementRequest{
		ManagementSelector: row.Reply.ManagementSelector,
		Action:             resourcegrant.ApplyAction,
		Apply: &resourcegrant.ManagementApplyRequest{
			OperationID:    p.OperationID,
			BaseRevision:   p.BaseRevision,
			ReviewRevision: p.ReviewRevision,
			Settings:       cloneSettings(p.Requested),
		},
	}
}

// ApplyRequestFor copies one exact reviewed remote payload. It neither returns
// a local Confirm field nor sends anything. The caller must use a separately
// admitted Core-owned review and must never execute an imported body directly.
func ApplyRequestFor(review ReviewBody, peerKey string) (resourcegrant.ManagementRequest, error) {
	r, err := CloneReview(review)
	if err != nil || len(r.ExecutionPeers) == 0 {
		return resourcegrant.ManagementRequest{}, ErrInvalid
	}
	selected := false
	for _, peer := range r.ExecutionPeers {
		if peer == peerKey {
			selected = true
		}
	}
	if !selected {
		return resourcegrant.ManagementRequest{}, ErrInvalid
	}
	for _, row := range r.Rows {
		if row.PeerKey == peerKey && row.State == ReviewReady {
			return requestForRow(row), nil
		}
	}
	return resourcegrant.ManagementRequest{}, ErrInvalid
}
