// Package resourcegrant defines private, inspect-only resource grant data.
// Valid data is not authentication or authority: callers must independently
// prove the current managed transport relationship and local ownership.
package resourcegrant

import (
	"errors"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/resource"
)

const (
	Version      = 2
	MaxRecords   = 16
	MaxBytes     = 32 * 1024
	Backend      = "directlan-managed"
	Inspect      = "inspect"
	Active       = "active"
	Revoked      = "revoked"
	Expired      = "expired"
	FilesField   = "transferConcurrentFiles"
	PerPeerField = "transferConcurrentPerPeer"
	maxTimestamp = int64(253402300799)
)

var ErrInvalid = errors.New("invalid resource grant state")

// Relationship identifies saved eligibility, not an authenticated connection.
// PairBinding is the immutable PairContext binding, never a current IP address.
type Relationship struct {
	Backend     string `json:"backend"`
	TargetKey   string `json:"targetKey"`
	PeerKey     string `json:"peerKey"`
	PairBinding string `json:"pairBinding"`
}

func (r Relationship) Validate() error {
	if r.Backend != Backend || !resource.ValidDigest(r.TargetKey) || !resource.ValidDigest(r.PeerKey) || r.TargetKey == r.PeerKey || !resource.ValidDigest(r.PairBinding) {
		return ErrInvalid
	}
	return nil
}

// Record deliberately permits only inspection of both transfer fields. No
// write, catalog, history, wildcard action, or default-value write permission
// can be represented. ID and Revision are preconditions, not bearer secrets.
type Record struct {
	ID           string          `json:"id"`
	Revision     uint64          `json:"revision"`
	Target       resource.Target `json:"target"`
	ResourceType string          `json:"resourceType"`
	Relationship Relationship    `json:"relationship"`
	Actions      []string        `json:"actions"`
	Fields       []string        `json:"fields"`
	IssuedAt     int64           `json:"issuedAt"`
	ExpiresAt    int64           `json:"expiresAt"`
	State        string          `json:"state"`
}

func (r Record) Validate() error {
	if !resource.ValidID(r.ID) || r.Revision == 0 || r.Revision > uint64(capacity.MaxJSONInteger) || r.Target.Validate() != nil || r.ResourceType != resource.Type || r.Relationship.Validate() != nil ||
		len(r.Actions) != 1 || r.Actions[0] != Inspect || len(r.Fields) != 2 || r.Fields[0] != FilesField || r.Fields[1] != PerPeerField ||
		r.IssuedAt <= 0 || r.ExpiresAt <= r.IssuedAt || r.ExpiresAt > maxTimestamp || r.ExpiresAt-r.IssuedAt > capacity.MaxDurationSeconds ||
		(r.State != Active && r.State != Revoked && r.State != Expired) {
		return ErrInvalid
	}
	return nil
}

// Envelope is bounded private eligibility state. Runtime activation and restart
// policy are intentionally separate; decoding this does not start a listener.
// Records are ordered by their last authority revision. Revisions never wrap.
type Envelope struct {
	Version   int             `json:"version"`
	Target    resource.Target `json:"target"`
	HighWater *uint64         `json:"highWater"`
	Records   []Record        `json:"records"`
	Clock     *WallCheckpoint `json:"clock"`
}

func (e Envelope) Validate() error {
	if e.Clock == nil || e.Clock.Validate() != nil || e.Version != Version || e.Target.Validate() != nil || e.HighWater == nil || *e.HighWater > uint64(capacity.MaxJSONInteger) || e.Records == nil || len(e.Records) > MaxRecords {
		return ErrInvalid
	}
	var previous uint64
	active := 0
	seen := make(map[string]bool, len(e.Records))
	for _, r := range e.Records {
		if r.Validate() != nil || r.Target != e.Target || seen[r.ID] || r.Revision <= previous || r.Revision > *e.HighWater {
			return ErrInvalid
		}
		seen[r.ID] = true
		previous = r.Revision
		if r.State == Active {
			active++
		}
	}
	if active > 1 || previous != *e.HighWater {
		return ErrInvalid
	}
	return nil
}

// Decode leaves the destination untouched on any malformed or ambiguous input.
func Decode(data []byte) (Envelope, error) {
	var e Envelope
	if resource.Decode(data, MaxBytes, &e) != nil || e.Validate() != nil {
		return Envelope{}, ErrInvalid
	}
	return e, nil
}
