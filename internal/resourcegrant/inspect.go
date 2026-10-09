package resourcegrant

import (
	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/resource"
)

const MaxRequestBytes = 1024
const MaxResponseBytes = 4096

// InspectRequest has no caller-supplied actor, relationship, provider or local
// command. The transport must supply authentication separately. Grant selectors
// only narrow an existing authority and never create it.
type InspectRequest struct {
	ProtocolVersion int             `json:"protocolVersion"`
	Target          resource.Target `json:"target"`
	GrantID         string          `json:"grantId"`
	GrantRevision   uint64          `json:"grantRevision"`
}

func (r InspectRequest) Validate() error {
	if r.ProtocolVersion != ProtocolVersion || r.Target.Validate() != nil || !resource.ValidID(r.GrantID) || r.GrantRevision == 0 || r.GrantRevision > uint64(capacity.MaxJSONInteger) {
		return ErrInvalid
	}
	return nil
}

func DecodeInspectRequest(data []byte) (InspectRequest, error) {
	var r InspectRequest
	if resource.Decode(data, MaxRequestBytes, &r) != nil || r.Validate() != nil {
		return InspectRequest{}, ErrInvalid
	}
	return r, nil
}

// Inspection is an explicit allowlist. Never embed a local Descriptor or
// Operation here: those contain authority, provider, revision and journal data.
// No serializer or projection is an authorization decision.
type Inspection struct {
	ProtocolVersion int                `json:"protocolVersion"`
	Target          resource.Target    `json:"target"`
	Requested       resource.Settings  `json:"requested"`
	Effective       resource.Effective `json:"effective"`
}

func (r Inspection) Validate() error {
	if r.ProtocolVersion != ProtocolVersion || r.Target.Validate() != nil || r.Requested.Validate() != nil ||
		r.Effective.TransferConcurrentFiles <= 0 || r.Effective.TransferConcurrentFiles > capacity.MaxJSONInteger ||
		r.Effective.TransferConcurrentPerPeer <= 0 || r.Effective.TransferConcurrentPerPeer > capacity.MaxJSONInteger {
		return ErrInvalid
	}
	return nil
}

func DecodeInspection(data []byte) (Inspection, error) {
	var r Inspection
	if resource.Decode(data, MaxResponseBytes, &r) != nil || r.Validate() != nil {
		return Inspection{}, ErrInvalid
	}
	return r, nil
}
