package resourcegrant

import (
	"encoding/json"
	"github.com/webkaz-labs/sobalink/internal/resource"
)

const Management = "management"

// ManagementRecord is a separate, explicit storage arm. Its Record remains
// invalid to the legacy inspect-only validator and cannot mint DisclosureFence.
// Validation and decoding are eligibility data only, never runtime authority.
type ManagementRecord struct {
	Scope  string `json:"scope"`
	Record Record `json:"record"`
}

func (m ManagementRecord) Validate() error {
	r := m.Record
	if m.Scope != Management || (ManagementScope{Actions: r.Actions, Fields: r.Fields}).Validate() != nil {
		return ErrInvalid
	}
	// Reuse the legacy common bounds without widening its public validator.
	r.Actions = []string{Inspect}
	return r.Validate()
}

// ManagementGrantInputs and reviews are local-only; legacy grant DTOs remain
// physically unchanged and reject this explicitly tagged arm.
type ManagementGrantInputs struct {
	Scope  string      `json:"scope"`
	Inputs GrantInputs `json:"inputs"`
}
type ManagementGrantReview struct {
	Grant            ManagementRecord `json:"grant"`
	BaseRevision     string           `json:"baseRevision"`
	ReviewRevision   string           `json:"reviewRevision"`
	InitializesState bool             `json:"initializesState"`
	UpgradesFormat   bool             `json:"upgradesFormat"`
}
type ManagementGrantConfirmation struct {
	Review  ManagementGrantReview `json:"review"`
	Confirm bool                  `json:"confirm"`
}

func (r *ManagementGrantReview) UnmarshalJSON(data []byte) error {
	type plain ManagementGrantReview
	var decoded plain
	if resource.Decode(data, 8192, &decoded) != nil {
		return ErrInvalid
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil {
		return ErrInvalid
	}
	for _, field := range []string{"initializesState", "upgradesFormat"} {
		if _, ok := fields[field]; !ok {
			return ErrInvalid
		}
	}
	if decoded.Grant.Validate() != nil {
		return ErrInvalid
	}
	*r = ManagementGrantReview(decoded)
	return nil
}

// ActiveRecord is shared lifetime/revocation bookkeeping, not admission. A
// management record returned here still fails Record.Validate and fence minting.
func (e Envelope) ActiveRecord() (Record, bool) {
	for _, r := range e.Records {
		if r.State == Active {
			return r, false
		}
	}
	for _, m := range e.ManagementRecords {
		if m.Record.State == Active {
			return m.Record, true
		}
	}
	return Record{}, false
}
