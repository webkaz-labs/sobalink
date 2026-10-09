package resourcegrant

import "github.com/webkaz-labs/sobalink/internal/resource"

// RemoteManagementInput belongs only to authenticated local control. Confirm
// acknowledges a reviewed apply, not a grant or wire authentication credential.
// It is deliberately absent from the remote ManagementRequest protocol.
type RemoteManagementInput struct {
	PeerKey string            `json:"peerKey"`
	Request ManagementRequest `json:"request"`
	Confirm bool              `json:"confirm"`
}

func (i RemoteManagementInput) Validate() error {
	if !resource.ValidDigest(i.PeerKey) || i.Request.Validate() != nil || i.Confirm != (i.Request.Action == ApplyAction) {
		return ErrInvalid
	}
	return nil
}
