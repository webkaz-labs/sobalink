package resourcegrant

import (
	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/resource"
)

const (
	MaxManagementRequestBytes  = 2048
	MaxManagementResponseBytes = 4096
)

// ManagementSelector narrows an authenticated exchange. It cannot supply
// actor identity, grant authority, relationship proof, or managed generation.
// GrantRevision selects current admission authority. The private journal keeps
// the original authorizing revision in its private evidence binding; a status
// query must never rewrite that historical identity. Validation proves neither.
// These DTOs are data models, not transport capabilities or grant authority.
type ManagementSelector struct {
	ProtocolVersion int             `json:"protocolVersion"`
	Target          resource.Target `json:"target"`
	GrantID         string          `json:"grantId"`
	GrantRevision   uint64          `json:"grantRevision"`
}

func (s ManagementSelector) Validate() error {
	if s.ProtocolVersion != ManagementProtocolVersion || s.Target.Validate() != nil || !resource.ValidID(s.GrantID) || s.GrantRevision == 0 || s.GrantRevision > uint64(capacity.MaxJSONInteger) {
		return ErrInvalid
	}
	return nil
}

type ManagementPreviewRequest struct {
	Settings resource.Settings `json:"settings"`
}

// ManagementApplyRequest carries only opaque operation and review digests. The
// server binds them to its private scope and stable managed generation,
// not to a TCP connection; preview and apply use different connections.
type ManagementApplyRequest struct {
	OperationID    string            `json:"operationId"`
	BaseRevision   string            `json:"baseRevision"`
	ReviewRevision string            `json:"reviewRevision"`
	Settings       resource.Settings `json:"settings"`
}
type ManagementStatusRequest struct {
	OperationID string `json:"operationId"`
}

// ManagementRequest is a closed tagged union, not a local command bridge.
// Inspect has no payload; each other action has exactly its matching payload.
type ManagementRequest struct {
	ManagementSelector
	Action  string                    `json:"action"`
	Preview *ManagementPreviewRequest `json:"preview,omitempty"`
	Apply   *ManagementApplyRequest   `json:"apply,omitempty"`
	Status  *ManagementStatusRequest  `json:"status,omitempty"`
}

func (r ManagementRequest) Validate() error {
	if r.ManagementSelector.Validate() != nil {
		return ErrInvalid
	}
	switch r.Action {
	case Inspect:
		if r.Preview != nil || r.Apply != nil || r.Status != nil {
			return ErrInvalid
		}
	case PreviewAction:
		if r.Preview == nil || r.Apply != nil || r.Status != nil || r.Preview.Settings.Validate() != nil {
			return ErrInvalid
		}
	case ApplyAction:
		if r.Apply == nil || r.Preview != nil || r.Status != nil || !resource.ValidDigest(r.Apply.OperationID) || !resource.ValidDigest(r.Apply.BaseRevision) || !resource.ValidDigest(r.Apply.ReviewRevision) || r.Apply.Settings.Validate() != nil {
			return ErrInvalid
		}
	case StatusAction:
		if r.Status == nil || r.Preview != nil || r.Apply != nil || !resource.ValidDigest(r.Status.OperationID) {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}

// ManagementInspection deliberately excludes the local descriptor's provider,
// authority and revision. No projection here makes a disclosure decision.
type ManagementInspection struct {
	Requested resource.Settings  `json:"requested"`
	Effective resource.Effective `json:"effective"`
}

func (v ManagementInspection) Validate() error {
	if v.Requested.Validate() != nil || v.Effective.TransferConcurrentFiles <= 0 || v.Effective.TransferConcurrentFiles > capacity.MaxJSONInteger || v.Effective.TransferConcurrentPerPeer <= 0 || v.Effective.TransferConcurrentPerPeer > capacity.MaxJSONInteger {
		return ErrInvalid
	}
	return nil
}

type ManagementPreview struct {
	OperationID    string             `json:"operationId"`
	BaseRevision   string             `json:"baseRevision"`
	ReviewRevision string             `json:"reviewRevision"`
	Requested      resource.Settings  `json:"requested"`
	Effective      resource.Effective `json:"effective"`
}

func (v ManagementPreview) Validate() error {
	if !resource.ValidDigest(v.OperationID) || !resource.ValidDigest(v.BaseRevision) || !resource.ValidDigest(v.ReviewRevision) || (ManagementInspection{Requested: v.Requested, Effective: v.Effective}).Validate() != nil {
		return ErrInvalid
	}
	return nil
}

// ManagementOperation is historical evidence only. It has no current settings,
// local operation ID, actor, provider, global sequence or journal usage.
// EvidenceDurable refers to terminal operation evidence, not just configuration
// durability. Only owned journal publication/read may establish true; this
// shape validator cannot establish persistence or authorization.
type ManagementOperation struct {
	OperationID     string           `json:"operationId"`
	Outcome         resource.Outcome `json:"outcome"`
	EvidenceDurable *bool            `json:"evidenceDurable"`
}

func (v ManagementOperation) Validate() error {
	if !resource.ValidDigest(v.OperationID) || v.Outcome.Validate() != nil || v.EvidenceDurable == nil {
		return ErrInvalid
	}
	return nil
}

// ManagementReply is an allowlisted tagged response. Unavailable is a single
// status-only classification for absent, evicted, local-owned or other-scope
// evidence; this model does not perform the required pre-lookup authorization.
type ManagementReply struct {
	ManagementSelector
	Action      string                   `json:"action"`
	Inspection  *ManagementInspection    `json:"inspection,omitempty"`
	Preview     *ManagementPreview       `json:"preview,omitempty"`
	Operation   *ManagementOperation     `json:"operation,omitempty"`
	Unavailable *ManagementStatusRequest `json:"unavailable,omitempty"`
}

func (r ManagementReply) Validate() error {
	if r.ManagementSelector.Validate() != nil {
		return ErrInvalid
	}
	switch r.Action {
	case Inspect:
		if r.Inspection == nil || r.Preview != nil || r.Operation != nil || r.Unavailable != nil || r.Inspection.Validate() != nil {
			return ErrInvalid
		}
	case PreviewAction:
		if r.Preview == nil || r.Inspection != nil || r.Operation != nil || r.Unavailable != nil || r.Preview.Validate() != nil {
			return ErrInvalid
		}
	case ApplyAction, StatusAction:
		if r.Inspection != nil || r.Preview != nil {
			return ErrInvalid
		}
		if r.Unavailable != nil {
			if r.Action != StatusAction || r.Operation != nil || !resource.ValidDigest(r.Unavailable.OperationID) {
				return ErrInvalid
			}
		} else if r.Operation == nil || r.Operation.Validate() != nil {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}

func DecodeManagementRequest(data []byte) (ManagementRequest, error) {
	var r ManagementRequest
	if resource.Decode(data, MaxManagementRequestBytes, &r) != nil || r.Validate() != nil {
		return ManagementRequest{}, ErrInvalid
	}
	return r, nil
}
func DecodeManagementReply(data []byte) (ManagementReply, error) {
	var r ManagementReply
	if resource.Decode(data, MaxManagementResponseBytes, &r) != nil || r.Validate() != nil {
		return ManagementReply{}, ErrProtocol
	}
	return r, nil
}
