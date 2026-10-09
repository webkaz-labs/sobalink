package resourcegrant

import (
	"encoding/json"
	"github.com/webkaz-labs/sobalink/internal/resource"
)

// Local grant DTOs belong only to authenticated local control. They are never
// accepted by the peer inspection protocol and do not themselves grant access.
type GrantInputs struct {
	Target    resource.Target `json:"target"`
	PeerKey   string          `json:"peerKey"`
	ExpiresAt int64           `json:"expiresAt"`
	Actions   []string        `json:"actions"`
	Fields    []string        `json:"fields"`
}
type GrantReview struct {
	Grant            Record `json:"grant"`
	BaseRevision     string `json:"baseRevision"`
	ReviewRevision   string `json:"reviewRevision"`
	InitializesState bool   `json:"initializesState"`
}
type GrantConfirmation struct {
	Review  GrantReview `json:"review"`
	Confirm bool        `json:"confirm"`
}
type GrantRevoke struct {
	Target        resource.Target `json:"target"`
	GrantID       string          `json:"grantId"`
	GrantRevision uint64          `json:"grantRevision"`
	Confirm       bool            `json:"confirm"`
}
type LocalGrantView struct {
	Target            resource.Target    `json:"target"`
	Records           []Record           `json:"records"`
	ManagementRecords []ManagementRecord `json:"managementRecords,omitempty"`
	InitializesState  bool               `json:"initializesState"`
	ListenerReady     bool               `json:"listenerReady"`
	Activation        string             `json:"activation"`
	TimeUncertain     bool               `json:"timeUncertain"`
}

// UnmarshalJSON preserves an explicit false initialization decision. The zero
// value is not a substitute for the field's presence in an unchanged review.
func (r *GrantReview) UnmarshalJSON(data []byte) error {
	type plain GrantReview
	var decoded plain
	if resource.Decode(data, 8192, &decoded) != nil {
		return ErrInvalid
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil {
		return ErrInvalid
	}
	if _, present := fields["initializesState"]; !present {
		return ErrInvalid
	}
	*r = GrantReview(decoded)
	return nil
}

// RemoteInspectInput belongs to local authenticated control. It selects one
// remote grant explicitly and offers no discovery or arbitrary command field.
type RemoteInspectInput struct {
	PeerKey string         `json:"peerKey"`
	Request InspectRequest `json:"request"`
}

func (i RemoteInspectInput) Validate() error {
	if !resource.ValidDigest(i.PeerKey) || i.Request.Validate() != nil {
		return ErrInvalid
	}
	return nil
}
