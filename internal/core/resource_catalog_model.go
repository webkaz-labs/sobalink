package core

import (
	"encoding/json"
	"sort"
	"unicode/utf8"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/resource"
	"github.com/webkaz-labs/sobalink/internal/resourcecatalog"
)

const resourceCatalogRequestBytes = 4096

func resourceCatalogInvalid() error {
	return &localCommandError{"resource_catalog_invalid", "invalid catalog schema or exact source selection"}
}
func resourceCatalogUnavailable() error {
	return &localCommandError{"resource_catalog_unavailable", "catalog owning local context is unavailable; verify the owning agent and refresh"}
}
func resourceCatalogCapacity() error {
	return &localCommandError{"resource_catalog_capacity", "catalog framing exceeds its finite view budget; select fewer sources or review the page budget"}
}

type resourceCatalogSource struct {
	Kind          string          `json:"kind"`
	Target        resource.Target `json:"target,omitempty"`
	PeerID        string          `json:"peerId,omitempty"`
	PeerKey       string          `json:"peerKey,omitempty"`
	GrantID       string          `json:"grantId,omitempty"`
	GrantRevision uint64          `json:"grantRevision,omitempty"`
}

// A value struct is not omitted by encoding/json's omitempty. Emit each exact
// selector arm explicitly so local/service requests never acquire a zero target.
func (s resourceCatalogSource) MarshalJSON() ([]byte, error) {
	var arm any
	switch s.Kind {
	case resourcecatalog.LocalService, resourcecatalog.TransferActivity:
		arm = struct {
			Kind string `json:"kind"`
		}{s.Kind}
	case resourcecatalog.LocalSettings:
		arm = struct {
			Kind   string          `json:"kind"`
			Target resource.Target `json:"target"`
		}{s.Kind, s.Target}
	case resourcecatalog.RemoteService:
		arm = struct {
			Kind   string `json:"kind"`
			PeerID string `json:"peerId"`
		}{s.Kind, s.PeerID}
	case resourcecatalog.RemoteSettingsV1, resourcecatalog.RemoteSettingsV2:
		arm = struct {
			Kind          string          `json:"kind"`
			PeerKey       string          `json:"peerKey"`
			Target        resource.Target `json:"target"`
			GrantID       string          `json:"grantId"`
			GrantRevision uint64          `json:"grantRevision"`
		}{s.Kind, s.PeerKey, s.Target, s.GrantID, s.GrantRevision}
	default:
		return nil, resourceCatalogInvalid()
	}
	raw, err := json.Marshal(arm)
	if err != nil {
		return nil, resourceCatalogInvalid()
	}
	var checked resourceCatalogSource
	if checked.UnmarshalJSON(raw) != nil || checked != s {
		return nil, resourceCatalogInvalid()
	}
	return raw, nil
}

// Decode each exact arm separately. A selector cannot smuggle even a zero-valued
// field from another arm. resource.Decode checks duplicate/case/null keys first.
func (s *resourceCatalogSource) UnmarshalJSON(raw []byte) error {
	type wire resourceCatalogSource
	var all wire
	if resource.Decode(raw, resourceCatalogRequestBytes, &all) != nil {
		return resourceCatalogInvalid()
	}
	var keys map[string]json.RawMessage
	if json.Unmarshal(raw, &keys) != nil {
		return resourceCatalogInvalid()
	}
	allowed := []string{"kind"}
	switch all.Kind {
	case resourcecatalog.LocalSettings:
		allowed = append(allowed, "target")
		if all.Target.Validate() != nil {
			return resourceCatalogInvalid()
		}
	case resourcecatalog.LocalService, resourcecatalog.TransferActivity:
	case resourcecatalog.RemoteService:
		allowed = append(allowed, "peerId")
		if !config.ValidPeerID(all.PeerID) {
			return resourceCatalogInvalid()
		}
	case resourcecatalog.RemoteSettingsV1, resourcecatalog.RemoteSettingsV2:
		allowed = append(allowed, "peerKey", "target", "grantId", "grantRevision")
		if !resource.ValidDigest(all.PeerKey) || all.Target.Validate() != nil || !resource.ValidID(all.GrantID) || all.GrantRevision == 0 || all.GrantRevision > uint64(capacity.MaxJSONInteger) {
			return resourceCatalogInvalid()
		}
	default:
		return resourceCatalogInvalid()
	}
	if len(keys) != len(allowed) {
		return resourceCatalogInvalid()
	}
	for _, key := range allowed {
		if _, ok := keys[key]; !ok {
			return resourceCatalogInvalid()
		}
	}
	*s = resourceCatalogSource(all)
	return nil
}

type resourceCatalogRequest struct {
	SchemaVersion int                     `json:"schemaVersion"`
	Sources       []resourceCatalogSource `json:"sources"`
}

func decodeResourceCatalogRequest(raw []byte) (resourceCatalogRequest, error) {
	var v resourceCatalogRequest
	if !utf8.Valid(raw) || resource.Decode(raw, resourceCatalogRequestBytes, &v) != nil || v.SchemaVersion != 1 || len(v.Sources) == 0 || len(v.Sources) > 5 {
		return v, resourceCatalogInvalid()
	}
	seen := map[string]bool{}
	remote := ""
	for _, source := range v.Sources {
		key := source.Kind
		if key == resourcecatalog.RemoteSettingsV1 || key == resourcecatalog.RemoteSettingsV2 {
			key = "remote_settings"
		}
		if seen[key] {
			return v, resourceCatalogInvalid()
		}
		seen[key] = true
		peer := source.PeerKey
		if source.Kind == resourcecatalog.RemoteService {
			peer = source.PeerID
		}
		if peer != "" {
			if remote != "" && remote != peer {
				return v, resourceCatalogInvalid()
			}
			remote = peer
		}
	}
	sort.Slice(v.Sources, func(i, j int) bool { return v.Sources[i].Kind < v.Sources[j].Kind })
	return v, nil
}

// ValidateResourceCatalogRequest supports offline CLI dry-run validation only.
func ValidateResourceCatalogRequest(raw []byte) error {
	_, err := decodeResourceCatalogRequest(raw)
	return err
}

type resourceCatalogViewLimits struct {
	MaxSources       int `json:"maxSources"`
	MaxRemoteTargets int `json:"maxRemoteTargets"`
	MaxRows          int `json:"maxRows"`
	MaxPageRows      int `json:"maxPageRows"`
	MaxPages         int `json:"maxPages"`
	MaxBytes         int `json:"maxBytes"`
	MaxPageBytes     int `json:"maxPageBytes"`
	MaxStringBytes   int `json:"maxStringBytes"`
}

func (v resourceCatalogViewLimits) CatalogLimits() resourcecatalog.Limits {
	return resourcecatalog.Limits{MaxSources: v.MaxSources, MaxRemoteTargets: v.MaxRemoteTargets, MaxRows: v.MaxRows, MaxPageRows: v.MaxPageRows, MaxPages: v.MaxPages, MaxBytes: v.MaxBytes, MaxPageBytes: v.MaxPageBytes, MaxStringBytes: v.MaxStringBytes}
}
func resourceCatalogViewOf(l resourcecatalog.Limits) resourceCatalogViewLimits {
	return resourceCatalogViewLimits{l.MaxSources, l.MaxRemoteTargets, l.MaxRows, l.MaxPageRows, l.MaxPages, l.MaxBytes, l.MaxPageBytes, l.MaxStringBytes}
}

type resourceCatalogResponse struct {
	SchemaVersion int                       `json:"schemaVersion"`
	Limits        resourceCatalogViewLimits `json:"limits"`
	Snapshot      resourcecatalog.Snapshot  `json:"snapshot"`
}
type ResourceCatalogResponse = resourceCatalogResponse
type ResourceCatalogViewLimits = resourceCatalogViewLimits

// DecodeResourceCatalogResponse validates the independent C1 digest and closed
// snapshot, and requires every local envelope/limit field in its exact spelling.
func DecodeResourceCatalogResponse(raw []byte) (ResourceCatalogResponse, error) {
	var wire struct {
		SchemaVersion int                       `json:"schemaVersion"`
		Limits        resourceCatalogViewLimits `json:"limits"`
		Snapshot      json.RawMessage           `json:"snapshot"`
	}
	if !utf8.Valid(raw) || len(raw) == 0 || resource.Decode(raw, len(raw), &wire) != nil || wire.SchemaVersion != 1 || wire.Limits.CatalogLimits().Validate() != nil {
		return ResourceCatalogResponse{}, resourceCatalogInvalid()
	}
	// All limit fields are positive, so absent ones are rejected above. The
	// decoder never relies on Go's case-insensitive field matching.
	snapshot, err := resourcecatalog.DecodeSnapshot(wire.Snapshot, wire.Limits.CatalogLimits())
	if err != nil {
		return ResourceCatalogResponse{}, resourceCatalogInvalid()
	}
	response := ResourceCatalogResponse{wire.SchemaVersion, wire.Limits, snapshot}
	allowance, err := resourceCatalogEnvelopeAllowance(wire.Limits.CatalogLimits())
	if err != nil || int64(len(raw)) > int64(wire.Limits.MaxBytes)+allowance {
		return ResourceCatalogResponse{}, resourceCatalogCapacity()
	}
	return response, nil
}

type resourceCatalogCapture struct {
	Selection resourcecatalog.Selection
	State     string
	CheckedAt int64
	Revision  string
	Total     *int64
	Rows      []resourcecatalog.Row
}
