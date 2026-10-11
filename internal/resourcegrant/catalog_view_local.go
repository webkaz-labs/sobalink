package resourcegrant

import (
	"encoding/json"

	"github.com/webkaz-labs/sobalink/internal/resource"
)

type CatalogViewInputs struct {
	Target     resource.Target `json:"target"`
	PeerKey    string          `json:"peerKey"`
	ServiceIDs []string        `json:"serviceIds"`
	ExpiresAt  int64           `json:"expiresAt"`
}

type CatalogViewReview struct {
	Grant            CatalogViewRecord `json:"grant"`
	BaseRevision     string            `json:"baseRevision"`
	ReviewRevision   string            `json:"reviewRevision"`
	InitializesState bool              `json:"initializesState"`
}

func (r *CatalogViewReview) UnmarshalJSON(data []byte) error {
	type plain CatalogViewReview
	var decoded plain
	if resource.Decode(data, MaxBytes, &decoded) != nil || decoded.Grant.Validate() != nil {
		return ErrInvalid
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil {
		return ErrInvalid
	}
	if _, exists := fields["initializesState"]; !exists {
		return ErrInvalid
	}
	*r = CatalogViewReview(decoded)
	return nil
}

type CatalogViewConfirmation struct {
	Review  CatalogViewReview `json:"review"`
	Confirm bool              `json:"confirm"`
}

type CatalogViewLocalView struct {
	Target           resource.Target     `json:"target"`
	Records          []CatalogViewRecord `json:"records"`
	InitializesState bool                `json:"initializesState"`
	TimeUncertain    bool                `json:"timeUncertain"`
}

// Local remote-list input selects one exact peer and grant; peer identity is
// separately authenticated by transport and never taken from a wire claim.
type RemoteCatalogViewInput struct {
	PeerKey string              `json:"peerKey"`
	Request CatalogViewSelector `json:"request"`
}

func (i RemoteCatalogViewInput) Validate() error {
	if !resource.ValidDigest(i.PeerKey) || i.Request.Validate() != nil {
		return ErrInvalid
	}
	return nil
}
