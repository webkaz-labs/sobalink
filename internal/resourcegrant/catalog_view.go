package resourcegrant

import (
	"time"

	"github.com/webkaz-labs/sobalink/internal/resource"
)

const CatalogViewVersion = 1

// CatalogViewRecord is eligibility for disclosure of exact shared-service
// generations to one managed DirectLAN relationship. Service IDs are the
// publication IDs minted at share activation, never saved configuration IDs.
// It does not authorize service.connect, settings inspection or management.
// Validation and matching do not mint runtime authority or start a listener.
type CatalogViewRecord struct {
	ID           string          `json:"id"`
	Revision     uint64          `json:"revision"`
	Target       resource.Target `json:"target"`
	Relationship Relationship    `json:"relationship"`
	ServiceIDs   []string        `json:"serviceIds"`
	IssuedAt     int64           `json:"issuedAt"`
	ExpiresAt    int64           `json:"expiresAt"`
	State        string          `json:"state"`
}

// commonRecord reuses existing ID, relationship and lifetime bounds for data
// validation only. This private projection is never an inspect grant.
func (r CatalogViewRecord) commonRecord() Record {
	return Record{ID: r.ID, Revision: r.Revision, Target: r.Target,
		ResourceType: resource.Type, Relationship: r.Relationship,
		Actions: []string{Inspect}, Fields: []string{FilesField, PerPeerField},
		IssuedAt: r.IssuedAt, ExpiresAt: r.ExpiresAt, State: r.State}
}

func (r CatalogViewRecord) Validate() error {
	if r.commonRecord().Validate() != nil || len(r.ServiceIDs) == 0 || len(r.ServiceIDs) > MaxRecords {
		return ErrInvalid
	}
	previous := ""
	for _, id := range r.ServiceIDs {
		if !resource.ValidID(id) || id <= previous {
			return ErrInvalid
		}
		previous = id
	}
	return nil
}

// MatchesSelection narrows saved eligibility. The coordinator must separately
// prove ownership, monotonic lifetime, current transport and publication origin
// at final disclosure. A matching value alone never permits network activity.
func (r CatalogViewRecord) MatchesSelection(relationship Relationship, serviceID string, now time.Time) bool {
	if r.Validate() != nil || r.State != Active || relationship != r.Relationship || now.Unix() < r.IssuedAt || now.Unix() >= r.ExpiresAt {
		return false
	}
	for _, id := range r.ServiceIDs {
		if id == serviceID {
			return true
		}
	}
	return false
}

// CatalogViewEnvelope is a separate persistence contract, not an arm in the
// legacy Envelope. Explicit catalog approval must preserve the existing grant
// store, high-water mark and runtime fences. No automatic migration is defined.
// One catalog grant may be active independently of the legacy active grant.
type CatalogViewEnvelope struct {
	Version   int                 `json:"version"`
	Target    resource.Target     `json:"target"`
	HighWater *uint64             `json:"highWater"`
	Records   []CatalogViewRecord `json:"records"`
	Clock     *WallCheckpoint     `json:"clock"`
}

func (e CatalogViewEnvelope) Validate() error {
	if e.Version != CatalogViewVersion || e.Records == nil || len(e.Records) > MaxRecords {
		return ErrInvalid
	}
	common := Envelope{Version: Version, Target: e.Target, HighWater: e.HighWater, Clock: e.Clock, Records: []Record{}}
	for _, record := range e.Records {
		if record.Validate() != nil {
			return ErrInvalid
		}
		common.Records = append(common.Records, record.commonRecord())
	}
	return common.Validate()
}

// DecodeCatalogView rejects legacy and management envelopes and all extra
// fields. It is data decoding only; no runtime authority is activated.
func DecodeCatalogView(data []byte) (CatalogViewEnvelope, error) {
	var e CatalogViewEnvelope
	if resource.Decode(data, MaxBytes, &e) != nil || e.Validate() != nil {
		return CatalogViewEnvelope{}, ErrInvalid
	}
	return e, nil
}
