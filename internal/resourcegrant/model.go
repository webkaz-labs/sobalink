// Package resourcegrant defines private inspection and inactive management grant data.
// Valid data is not authentication or authority: callers must independently
// prove the current managed transport relationship and local ownership.
package resourcegrant

import (
	"encoding/json"
	"errors"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/resource"
)

const (
	Version           = 2
	ManagementVersion = 3
	MaxRecords        = 16
	MaxBytes          = 32 * 1024
	Backend           = "directlan-managed"
	Inspect           = "inspect"
	Active            = "active"
	Revoked           = "revoked"
	Expired           = "expired"
	FilesField        = "transferConcurrentFiles"
	PerPeerField      = "transferConcurrentPerPeer"
	maxTimestamp      = int64(253402300799)
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
// Each arm is ordered by its last authority revision; IDs and revisions are unique
// across both arms. There is one global active grant and revisions never wrap.
type Envelope struct {
	Version           int                `json:"version"`
	Target            resource.Target    `json:"target"`
	HighWater         *uint64            `json:"highWater"`
	Records           []Record           `json:"records"`
	Clock             *WallCheckpoint    `json:"clock"`
	ManagementRecords []ManagementRecord `json:"managementRecords,omitempty"`
}

func (e Envelope) Validate() error {
	if e.Clock == nil || e.Clock.Validate() != nil || (e.Version != Version && e.Version != ManagementVersion) || e.Target.Validate() != nil || e.HighWater == nil || *e.HighWater > uint64(capacity.MaxJSONInteger) || e.Records == nil || len(e.Records)+len(e.ManagementRecords) > MaxRecords {
		return ErrInvalid
	}
	if e.Version == Version && e.ManagementRecords != nil || e.Version == ManagementVersion && len(e.ManagementRecords) == 0 {
		return ErrInvalid
	}
	var maximum uint64
	active := 0
	seen := make(map[string]bool)
	revisions := make(map[uint64]bool)
	check := func(r Record, previous uint64) error {
		if r.Target != e.Target || seen[r.ID] || revisions[r.Revision] || r.Revision <= previous || r.Revision > *e.HighWater {
			return ErrInvalid
		}
		seen[r.ID], revisions[r.Revision] = true, true
		if r.Revision > maximum {
			maximum = r.Revision
		}
		if r.State == Active {
			active++
		}
		return nil
	}
	var previous uint64
	for _, r := range e.Records {
		if r.Validate() != nil || check(r, previous) != nil {
			return ErrInvalid
		}
		previous = r.Revision
	}
	previous = 0
	for _, r := range e.ManagementRecords {
		if r.Validate() != nil || check(r.Record, previous) != nil {
			return ErrInvalid
		}
		previous = r.Record.Revision
	}
	if active > 1 || maximum != *e.HighWater {
		return ErrInvalid
	}
	return nil
}

// Decode leaves the destination untouched on any malformed or ambiguous input.
func Decode(data []byte) (Envelope, error) {
	if len(data) == 0 || len(data) > MaxBytes {
		return Envelope{}, ErrInvalid
	}
	// Decode the complete selected wire shape. Legacy version 2 has no new arm,
	// even when an input supplies an empty or null managementRecords member.
	var header struct {
		Version int `json:"version"`
	}
	if json.Unmarshal(data, &header) != nil {
		return Envelope{}, ErrInvalid
	}
	var e Envelope
	switch header.Version {
	case Version:
		var legacy struct {
			Version   int             `json:"version"`
			Target    resource.Target `json:"target"`
			HighWater *uint64         `json:"highWater"`
			Records   []Record        `json:"records"`
			Clock     *WallCheckpoint `json:"clock"`
		}
		if resource.Decode(data, MaxBytes, &legacy) != nil {
			return Envelope{}, ErrInvalid
		}
		e = Envelope{Version: legacy.Version, Target: legacy.Target, HighWater: legacy.HighWater, Records: legacy.Records, Clock: legacy.Clock}
	case ManagementVersion:
		if resource.Decode(data, MaxBytes, &e) != nil {
			return Envelope{}, ErrInvalid
		}
	default:
		return Envelope{}, ErrInvalid
	}
	if e.Validate() != nil {
		return Envelope{}, ErrInvalid
	}
	return e, nil
}
