package core

import (
	"time"

	"github.com/webkaz-labs/sobalink/internal/lanlink"
)

// SetupScope is a local-only, read-only interface/address choice. ScopeID binds
// all fields; accepting a peer never adds that peer's scope to local consent.
type SetupScope struct {
	linkBinding    string // local-only interface instance (name/hardware); never exported
	ScopeID        string `json:"scopeId"`
	InterfaceIndex int    `json:"interfaceIndex"`
	Interface      string `json:"interface"`
	LocalAddress   string `json:"localAddress"`
	Prefix         string `json:"prefix"`
}

type SetupScopesView struct {
	Kind    string       `json:"kind"` // sobalink-setup-scopes
	Version int          `json:"version"`
	Scopes  []SetupScope `json:"scopes"`
}

type SetupStartInput struct {
	Alias         string `json:"alias"`
	ScopeID       string `json:"scopeId"`
	BootstrapPort uint16 `json:"bootstrapPort"`
	AllowHost     bool   `json:"allowHost"`
	RelayPort     uint16 `json:"relayPort"`
	Consent       bool   `json:"consent"`
}

// SetupContact is public, short-lived contact information, not identity proof,
// network permission, an invitation, pairing or application trust.
type SetupContact struct {
	Kind      string    `json:"kind"` // sobalink-setup-contact
	Version   int       `json:"version"`
	SessionID string    `json:"sessionId"`
	Alias     string    `json:"alias"`
	Bootstrap string    `json:"bootstrap"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type SetupCandidate struct {
	CandidateID  string       `json:"candidateId"`
	Contact      SetupContact `json:"contact"`
	ObservedAt   time.Time    `json:"observedAt"`
	ExpiresAt    time.Time    `json:"expiresAt"`
	Verification string       `json:"verification"` // unverified
}

type SetupPlanView struct {
	Revision         uint64                      `json:"revision"`
	Fingerprint      string                      `json:"fingerprint"`
	Verification     string                      `json:"verification"`     // full-sha256
	VerificationText string                      `json:"verificationText"` // sobalink-setup-verify-v1:<64 lowercase hex>
	VerificationQR   [][]bool                    `json:"verificationQR,omitempty"`
	Backend          string                      `json:"backend"` // lan
	Participants     [2]lanlink.SetupParticipant `json:"participants"`
	HostKey          string                      `json:"hostKey"`
	Relay            LANSelection                `json:"relay"`
	ExpiresAt        time.Time                   `json:"expiresAt"`
}

type SetupProcessView struct {
	State                   string `json:"state"` // ready, stop_required, needs_restart
	ProfilePreserved        bool   `json:"profilePreserved"`
	ApprovalsSurviveRestart bool   `json:"approvalsSurviveRestart"` // always false
}

type SetupDiscoveryView struct {
	State      string `json:"state"` // active, unavailable, stopped
	ReasonCode string `json:"reasonCode,omitempty"`
}

// SetupSnapshot never contains an invitation/token/private identity. Paired,
// mutually reachable and application-trusted are deliberately separate facts.
type SetupSnapshot struct {
	Kind           string             `json:"kind"` // sobalink-setup
	Version        int                `json:"version"`
	SessionID      string             `json:"sessionId,omitempty"`
	ExpiresAt      time.Time          `json:"expiresAt,omitzero"`
	State          string             `json:"state"` // idle, discovering, selecting, authenticating, awaiting_approval, preparing, connecting, connected, recovery_required, cancelling, cancelled, expired, stop_required, needs_restart, failed
	ReasonCode     string             `json:"reasonCode,omitempty"`
	NextAction     string             `json:"nextAction,omitempty"`
	Scope          *SetupScope        `json:"scope,omitempty"`
	Contact        *SetupContact      `json:"contact,omitempty"`
	ContactText    string             `json:"contactText,omitempty"` // sobalink-setup-contact-v1:<base64url canonical JSON>
	ContactQR      [][]bool           `json:"contactQR,omitempty"`
	Candidates     []SetupCandidate   `json:"candidates"`
	Selected       *SetupContact      `json:"selected,omitempty"`
	Plan           *SetupPlanView     `json:"plan,omitempty"`
	LocalApproved  bool               `json:"localApproved"`
	RemoteApproved bool               `json:"remoteApproved"`
	Paired         bool               `json:"paired"`
	Connected      bool               `json:"connected"`
	Trusted        bool               `json:"trusted"` // never changed by setup
	Process        SetupProcessView   `json:"process"`
	Discovery      SetupDiscoveryView `json:"discovery"`
}

type SetupSelectInput struct {
	SessionID   string `json:"sessionId"`
	CandidateID string `json:"candidateId"`
}

type SetupManualInput struct {
	SessionID   string `json:"sessionId"`
	ContactText string `json:"contactText"`
}

type SetupApproveInput struct {
	SessionID   string `json:"sessionId"`
	Revision    uint64 `json:"revision"`
	Fingerprint string `json:"fingerprint"`
	Verified    bool   `json:"verified"`
}

type SetupSessionInput struct {
	SessionID string `json:"sessionId,omitempty"`
}
