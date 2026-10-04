// Package transfer receives bounded, explicitly accepted batches from peers whose
// identity has already been verified by the transport. It never opens received
// files, preserves executable attributes, or resumes partial bytes. Batch state
// and acknowledgements are process-local; callers must start a new transfer after
// a receiver restart.
package transfer

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/webkaz-labs/sobalink/internal/diskspace"
)

var (
	ErrInvalidManifest = errors.New("invalid transfer manifest")
	ErrLimit           = errors.New("transfer limit exceeded")
	ErrMetadataLimit   = fmt.Errorf("%w: manifest metadata budget exceeded", ErrLimit)
	ErrUnknownPeer     = errors.New("peer identity is not bound")
	ErrPeerChanged     = errors.New("peer identity generation changed")
	ErrPeerPaused      = errors.New("peer transfers are paused")
	ErrNotFound        = errors.New("transfer or file not found")
	ErrConflict        = errors.New("transfer identity or destination conflicts")
	ErrState           = errors.New("transfer operation is not allowed in this state")
	ErrBusy            = errors.New("concurrent transfer limit reached")
	ErrIntegrity       = errors.New("received size or SHA-256 does not match manifest")
	ErrCancelled       = errors.New("transfer was cancelled")
	ErrClosed          = errors.New("transfer manager is closed")
	ErrReceiveRecovery = errors.New("receive recovery is required")
	ErrUnsafePath      = errors.New("transfer path is unsafe")
)

// Peer is an immutable identity and trust generation supplied by a verified
// transport, never a display name or an untrusted request body. BindPeer must be
// called only after identity verification. A generation change revokes previous
// batches and receive policies; generations must increase when trust is renewed.
type Peer struct {
	ID         string `json:"id"`
	Generation uint64 `json:"generation"`
}

type Kind string

const (
	File      Kind = "file"
	Directory Kind = "directory" // Only empty directories appear explicitly.
)

type Entry struct {
	ID     string `json:"id"`
	Path   string `json:"path"` // Portable, slash-separated relative path.
	Kind   Kind   `json:"kind"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256,omitempty"` // Lowercase hexadecimal for files.
}

type Manifest struct {
	ID      string  `json:"id"`
	Entries []Entry `json:"entries"`
}

type BatchState string
type FileState string

const (
	Pending   BatchState = "pending"
	Accepted  BatchState = "accepted"
	Receiving BatchState = "receiving"
	Partial   BatchState = "partial"
	Completed BatchState = "completed"
	Cancelled BatchState = "cancelled"
	Rejected  BatchState = "rejected"

	FilePending   FileState = "pending"
	FileReceiving FileState = "receiving"
	FileFailed    FileState = "failed"
	FileSaved     FileState = "saved"
	FileCancelled FileState = "cancelled"
)

type FileStatus struct {
	Entry
	State          FileState `json:"state"`
	StoredName     string    `json:"storedName,omitempty"`
	CompletedBytes int64     `json:"completedBytes"`
	Error          string    `json:"error,omitempty"` // Stable ErrorCode; never local paths.
}

type Batch struct {
	ID             string       `json:"id"`
	Peer           Peer         `json:"peer"`
	State          BatchState   `json:"state"`
	Destination    string       `json:"destination,omitempty"`
	Files          []FileStatus `json:"files"`
	TotalBytes     int64        `json:"totalBytes"`
	CompletedBytes int64        `json:"completedBytes"`
	CreatedAt      time.Time    `json:"createdAt"`
}

// FileAck is returned only after the verified file has been saved. Repeating
// ReceiveFile for a saved entry returns the same acknowledgement without reading
// or rewriting the payload, including after a lost network acknowledgement.
type FileAck struct {
	BatchID    string `json:"batchID"`
	FileID     string `json:"fileID"`
	StoredName string `json:"storedName"`
	Size       int64  `json:"size"`
	SHA256     string `json:"sha256"`
}

// Limits bound metadata, accepted storage reservations and streaming memory.
// A zero field uses its default. Positive values may raise a default; callers
// must supply finite metadata, reservation, and concurrency budgets. Defaults
// are initial choices rather than hidden maximums.
type Limits struct {
	MaxBatches           int
	MaxPeers             int
	MaxPendingBatches    int
	MaxPendingPerPeer    int // All unfinished reservations, including accepted batches.
	MaxEntries           int
	MaxDepth             int
	MaxPathBytes         int
	MaxManifestBytes     int64
	MaxMetadataBytes     int64
	MaxFileBytes         int64
	MaxBatchBytes        int64
	MaxReservedBytes     int64
	DiskReserveBytes     int64 // Observed filesystem free-space safety margin.
	MaxConcurrentFiles   int
	MaxConcurrentPerPeer int
}

func DefaultLimits() Limits {
	return Limits{MaxBatches: 256, MaxPeers: 256, MaxPendingBatches: 32,
		MaxPendingPerPeer: 8, MaxEntries: 1024, MaxDepth: 16,
		MaxPathBytes: 4096, MaxManifestBytes: 1 << 20, MaxMetadataBytes: 16 << 20,
		MaxFileBytes: 8 << 30, MaxBatchBytes: 32 << 30,
		MaxReservedBytes: 64 << 30, DiskReserveBytes: diskspace.DefaultReserveBytes, MaxConcurrentFiles: 4, MaxConcurrentPerPeer: 2}
}

type ReceivePolicy struct {
	Peer        Peer   `json:"peer"`
	Destination string `json:"destination"`
	AutoAccept  bool   `json:"autoAccept"`
}

// PolicyStore must load and atomically save private, integrity-protected local
// configuration (for example, the caller's SecureDir-protected profile). It must
// not transmit destinations externally or call back into Manager. Save must
// either commit the complete snapshot or return an error. A nil store keeps
// explicitly approved policies in memory only. Batch data is never persisted.
type PolicyStore interface {
	LoadPolicies() ([]ReceivePolicy, error)
	SavePolicies([]ReceivePolicy) error
}

// ReceiveAccountingStore persists only receiver-owned roots and names of
// active temporary files. Implementations must not call back into Manager.
// The independent singleton guard is mandatory for durable implementations.
// WithReceiveAccountingLimits must preserve the adapter's behavior/dependencies;
// decorators must override it rather than returning their embedded base store.
type ReceiveAccountingStore interface {
	LoadReceiveAccounting() (ReceiveAccounting, error)
	SaveReceiveAccounting(ReceiveAccounting, ...ReceiveRetirementLease) error
	WithReceiveAccountingLimits(AccountingLimits) ReceiveAccountingStore
	LoadReceiveRetirementGuard(AccountingLimits) (*ReceiveRetirementGuard, ReceiveRetirementLease, error)
	AcquireReceiveRetirementGuard(ReceiveRetirementGuard, AccountingLimits) (ReceiveRetirementLease, error)
}

// ReceiveAccounting is private local storage, never a wire or export contract.
type ReceiveAccounting struct {
	Version     int                 `json:"version"`
	Roots       []ReceiveRoot       `json:"roots"`
	Preparation *ReceivePreparation `json:"preparation,omitempty"`
}

// ReceivePreparation identifies a planned creation, never authority to delete it.
type ReceivePreparation struct {
	Destination         string `json:"destination"`
	DestinationIdentity string `json:"destinationIdentity"`
	Root                string `json:"root"`
	Stage               string `json:"stage"`
	OwnerToken          string `json:"ownerToken"`
}

type ReceiveRoot struct {
	Destination         string `json:"destination"`
	DestinationIdentity string `json:"destinationIdentity"`
	OwnedRoot           string `json:"ownedRoot"`
	RootIdentity        string `json:"rootIdentity"`
	OwnerToken          string `json:"ownerToken"`
	Stage               string `json:"stage"`
	StageIdentity       string `json:"stageIdentity"`
}

// AccountingLimits bound the complete private index and inventory, including
// unrecorded names in owned staging. Zero fields select finite defaults.
type AccountingLimits struct {
	MaxBytes     int64
	MaxEntries   int64
	MaxDepth     int64
	MaxPathBytes int64
}

type Options struct {
	Context          context.Context
	Limits           Limits
	PolicyStore      PolicyStore
	DiskSpace        *diskspace.Guard // nil uses the shared process guard.
	AccountingStore  ReceiveAccountingStore
	AccountingLimits AccountingLimits
	// ExistingState requires explicit local review when the index is absent.
	ExistingState bool
}
