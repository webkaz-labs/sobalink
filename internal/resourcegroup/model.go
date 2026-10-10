// Package resourcegroup defines data-only fixed selections and historical
// evidence. A digest, decoded body or constructed request grants no authority.
// The owning coordinator must separately authenticate, capture and recheck the
// local origin, obtain confirmation, durably admit a run and prevent replay.
// This package performs no I/O, persistence, dispatch or confirmation.
package resourcegroup

import (
	"errors"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/resource"
	"github.com/webkaz-labs/sobalink/internal/resourcegrant"
)

// These are conservative bounds for this schema, not a transport envelope or
// a reservation of coordinator storage. Integration must budget both separately.
const (
	SchemaVersion      = 1
	MaxMembers         = 16
	MaxSelectionBytes  = 32 * 1024
	MaxReviewBytes     = 64 * 1024
	MaxEvidenceBytes   = 64 * 1024
	MaxObservationTime = int64(253402300799)
)

var ErrInvalid = errors.New("invalid resource group data")

type Template struct {
	SchemaVersion int               `json:"schemaVersion"`
	Settings      resource.Settings `json:"settings"`
	Revision      string            `json:"revision"`
}

// Override omission means inheritance; an explicit default remains an override
// even when its resolved value equals the template. Empty overrides are invalid.
type Override struct {
	TransferConcurrentFiles   *capacity.Choice `json:"transferConcurrentFiles,omitempty"`
	TransferConcurrentPerPeer *capacity.Choice `json:"transferConcurrentPerPeer,omitempty"`
}

type Member struct {
	PeerKey  string                           `json:"peerKey"`
	Selector resourcegrant.ManagementSelector `json:"selector"`
	Override *Override                        `json:"override,omitempty"`
}

type Selection struct {
	SchemaVersion int      `json:"schemaVersion"`
	Template      Template `json:"template"`
	Members       []Member `json:"members"`
}

type ResolvedMember struct {
	PeerKey   string                           `json:"peerKey"`
	Selector  resourcegrant.ManagementSelector `json:"selector"`
	Override  *Override                        `json:"override,omitempty"`
	Requested resource.Settings                `json:"requested"`
}

type ResolvedSelection struct {
	SchemaVersion int              `json:"schemaVersion"`
	Template      Template         `json:"template"`
	Members       []ResolvedMember `json:"members"`
}

// Unavailable deliberately conveys no remote cause or reachability. Unsupported
// requires the existing authenticated unsupported result; shape validation here
// cannot establish that provenance. Neither state is remote conflict evidence.
type ReviewState string

const (
	ReviewReady        ReviewState = "ready"
	ReviewUnavailable  ReviewState = "unavailable"
	ReviewUnsupported  ReviewState = "unsupported"
	ReviewInvalidReply ReviewState = "invalid_reply"
	ReviewCanceled     ReviewState = "canceled_before_preview"
)

// Reply is present exactly for ready rows. The owner must associate it with the
// authenticated exchange for PeerKey; DTO validation cannot authenticate a peer.
type ReviewRow struct {
	SchemaVersion int                            `json:"schemaVersion"`
	PeerKey       string                         `json:"peerKey"`
	State         ReviewState                    `json:"state"`
	Reply         *resourcegrant.ManagementReply `json:"reply,omitempty"`
}

// ReviewBody deliberately excludes the opaque Core-issued review ID, profile,
// pair binding and approval. Revision binds this entire canonical body except
// itself; it never admits execution. Every selected row remains in the review.
type ReviewBody struct {
	SchemaVersion  int               `json:"schemaVersion"`
	Selection      ResolvedSelection `json:"selection"`
	Rows           []ReviewRow       `json:"rows"`
	ExecutionPeers []string          `json:"executionPeers"`
	Revision       string            `json:"revision"`
}

type ExecutionState string

// Dispatching records a durably published local intent and a call that may have
// started, never target admission. Unknown preserves that uncertainty after a
// lost response or cancellation. No observation may silently reset it to unsent.
type DispatchState string
type LocalDurability string
type StatusState string
type StopReason string

const (
	ExecutionSelected        ExecutionState  = "selected"
	ExecutionExcluded        ExecutionState  = "excluded"
	DispatchNotAttempted     DispatchState   = "not_attempted"
	Dispatching              DispatchState   = "dispatching"
	DispatchObserved         DispatchState   = "observed"
	DispatchUnknown          DispatchState   = "unknown"
	LocalNotSaved            LocalDurability = "not_saved"
	LocalDurable             LocalDurability = "durable"
	LocalUncertain           LocalDurability = "uncertain"
	StatusNotQueried         StatusState     = "not_queried"
	StatusObserved           StatusState     = "observed"
	StatusUnavailable        StatusState     = "unavailable"
	StatusUnsupported        StatusState     = "unsupported"
	StatusQueryFailed        StatusState     = "query_failed"
	StopNone                 StopReason      = "none"
	StopUserCanceled         StopReason      = "user_canceled"
	StopBudgetExhausted      StopReason      = "budget_exhausted"
	StopContextChanged       StopReason      = "context_changed"
	StopPersistenceUncertain StopReason      = "persistence_uncertain"
	StopRestarted            StopReason      = "restarted"
)

// Sequence and ObservedAt are bounded local observations, never remote clocks
// or expiry. not_queried requires both zero; every query requires both positive.
type StatusObservation struct {
	State      StatusState                        `json:"state"`
	Sequence   uint64                             `json:"sequence"`
	ObservedAt int64                              `json:"observedAt"`
	Operation  *resourcegrant.ManagementOperation `json:"operation,omitempty"`
}

// MemberEvidence retains the immutable original apply request, including its
// original selector and all opaque tokens. Request exists exactly for ready
// rows, including ready rows explicitly excluded from execution. Target is the
// best retained matching observation, not proof of current settings. Local
// publication durability never substitutes for Target.EvidenceDurable.
type MemberEvidence struct {
	SchemaVersion   int                                `json:"schemaVersion"`
	GroupRevision   string                             `json:"groupRevision"`
	PeerKey         string                             `json:"peerKey"`
	Review          ReviewState                        `json:"review"`
	Execution       ExecutionState                     `json:"execution"`
	Request         *resourcegrant.ManagementRequest   `json:"request,omitempty"`
	Dispatch        DispatchState                      `json:"dispatch"`
	LocalDurability LocalDurability                    `json:"localDurability"`
	Target          *resourcegrant.ManagementOperation `json:"target,omitempty"`
	Status          StatusObservation                  `json:"status"`
	AdmissionStop   StopReason                         `json:"admissionStop"`
}

// EvidenceBody is only a bounded DTO. It is not the future owned sidecar format
// and importing it must never recreate approval or restore dispatchable work.
type EvidenceBody struct {
	SchemaVersion int              `json:"schemaVersion"`
	Members       []MemberEvidence `json:"members"`
}

// Summary counts historical observations independently. AllApplied can be true
// for an explicit successful subset while Excluded remains nonzero. It means
// every executed row has durable terminal applied evidence, never compliance.
type Summary struct {
	SchemaVersion          int  `json:"schemaVersion"`
	Selected               int  `json:"selected"`
	Executable             int  `json:"executable"`
	Excluded               int  `json:"excluded"`
	ReviewFailures         int  `json:"reviewFailures"`
	NotAttempted           int  `json:"notAttempted"`
	Dispatching            int  `json:"dispatching"`
	DispatchObserved       int  `json:"dispatchObserved"`
	DispatchUnknown        int  `json:"dispatchUnknown"`
	Applied                int  `json:"applied"`
	Failed                 int  `json:"failed"`
	Canceled               int  `json:"canceled"`
	SavedNotApplied        int  `json:"savedNotApplied"`
	TargetUnknown          int  `json:"targetUnknown"`
	TargetUnobserved       int  `json:"targetUnobserved"`
	TargetNonDurable       int  `json:"targetNonDurable"`
	LocalNonDurable        int  `json:"localNonDurable"`
	StatusFailures         int  `json:"statusFailures"`
	AdmissionFinished      bool `json:"admissionFinished"`
	ReconciliationRequired bool `json:"reconciliationRequired"`
	AllApplied             bool `json:"allApplied"`
}

func validReviewState(s ReviewState) bool {
	switch s {
	case ReviewReady, ReviewUnavailable, ReviewUnsupported, ReviewInvalidReply, ReviewCanceled:
		return true
	}
	return false
}

func cloneChoice(c capacity.Choice) capacity.Choice {
	if c.Value != nil {
		v := *c.Value
		c.Value = &v
	}
	return c
}

func cloneSettings(s resource.Settings) resource.Settings {
	s.TransferConcurrentFiles = cloneChoice(s.TransferConcurrentFiles)
	s.TransferConcurrentPerPeer = cloneChoice(s.TransferConcurrentPerPeer)
	return s
}

func cloneOverride(o *Override) *Override {
	if o == nil {
		return nil
	}
	next := *o
	if o.TransferConcurrentFiles != nil {
		c := cloneChoice(*o.TransferConcurrentFiles)
		next.TransferConcurrentFiles = &c
	}
	if o.TransferConcurrentPerPeer != nil {
		c := cloneChoice(*o.TransferConcurrentPerPeer)
		next.TransferConcurrentPerPeer = &c
	}
	return &next
}

func equalChoice(a, b capacity.Choice) bool {
	return a.Mode == b.Mode && ((a.Value == nil && b.Value == nil) || (a.Value != nil && b.Value != nil && *a.Value == *b.Value))
}

func equalSettings(a, b resource.Settings) bool {
	return equalChoice(a.TransferConcurrentFiles, b.TransferConcurrentFiles) && equalChoice(a.TransferConcurrentPerPeer, b.TransferConcurrentPerPeer)
}
