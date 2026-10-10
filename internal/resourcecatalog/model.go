// Package resourcecatalog models an authenticated-local, full catalog view.
// It performs no I/O, authentication, permission checks, persistence or dispatch.
// A source epoch is caller-owned correlation data, never an authority token.
// In particular this package is not a peer-wire catalog protocol: existing
// inspection and management grants do not grant catalog disclosure permission.
package resourcecatalog

import (
	"errors"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/resource"
)

const SchemaVersion = 1

const (
	LocalSettings    = "local_settings"
	RemoteSettingsV1 = "remote_settings_v1"
	RemoteSettingsV2 = "remote_settings_v2"
	LocalService     = "local_service"
	RemoteService    = "remote_service"
	TransferActivity = "transfer_activity"
	Persistent       = "persistent"
	Activation       = "activation"
	Process          = "process"
)

var (
	ErrInvalid = errors.New("invalid catalog data")
	ErrLimited = errors.New("catalog budget exhausted")
	ErrChanged = errors.New("catalog source changed")
)

// Limits are integration-owned finite budgets, not logical user settings.
// There are no implicit defaults or ties to the target's grant-record budget.
// MaxBytes includes snapshot framing. MaxPageBytes bounds each normalized page.
// The caller must also bound and validate the original provider response before
// projecting it; projection cannot retroactively bound an upstream allocation.
type Limits struct {
	MaxSources       int
	MaxRemoteTargets int
	MaxRows          int
	MaxPageRows      int
	MaxPages         int
	MaxBytes         int
	MaxPageBytes     int
	MaxStringBytes   int
}

func (l Limits) Validate() error {
	for _, n := range []int{l.MaxSources, l.MaxRemoteTargets, l.MaxRows, l.MaxPageRows, l.MaxPages, l.MaxBytes, l.MaxPageBytes, l.MaxStringBytes} {
		if n <= 0 || uint64(n) > uint64(capacity.MaxJSONInteger) {
			return ErrInvalid
		}
	}
	if l.MaxPageRows > l.MaxRows || l.MaxPageBytes > l.MaxBytes || l.MaxStringBytes > l.MaxPageBytes {
		return ErrInvalid
	}
	return nil
}

// Selection names exactly one source in one caller-owned authenticated scope.
// IDs are never inferred from names/routes. PeerKey for settings is the exact
// managed peer key; PeerKey for discovery is its existing selected peer ID.
// Resource/grant selectors narrow an existing read and cannot authorize it.
// Epoch must change when the owner invalidates authentication or selection.
// ProcessID is required only for process-local transfer activity.
type Selection struct {
	SourceID      string          `json:"sourceId"`
	Kind          string          `json:"kind"`
	Epoch         string          `json:"epoch"`
	PeerKey       string          `json:"peerKey"`
	Target        resource.Target `json:"target"`
	GrantID       string          `json:"grantId"`
	GrantRevision uint64          `json:"grantRevision"`
	ProcessID     string          `json:"processId"`
}

func (s Selection) Validate(l Limits) error {
	if l.Validate() != nil || !text(s.SourceID, l) || !text(s.Epoch, l) {
		return ErrInvalid
	}
	remoteSettings := s.Kind == RemoteSettingsV1 || s.Kind == RemoteSettingsV2
	settings := s.Kind == LocalSettings || remoteSettings
	if settings {
		if s.Target.Validate() != nil {
			return ErrInvalid
		}
	} else if s.Target != (resource.Target{}) {
		return ErrInvalid
	}
	if remoteSettings {
		if !resource.ValidDigest(s.PeerKey) || !resource.ValidID(s.GrantID) || s.GrantRevision == 0 || s.GrantRevision > uint64(capacity.MaxJSONInteger) {
			return ErrInvalid
		}
	} else if s.GrantID != "" || s.GrantRevision != 0 {
		return ErrInvalid
	}
	if s.Kind == RemoteService {
		if !peerID(s.PeerKey) || !text(s.PeerKey, l) {
			return ErrInvalid
		}
	} else if !remoteSettings && s.PeerKey != "" {
		return ErrInvalid
	}
	if s.Kind == TransferActivity {
		if !text(s.ProcessID, l) {
			return ErrInvalid
		}
	} else if s.ProcessID != "" {
		return ErrInvalid
	}
	switch s.Kind {
	case LocalSettings, RemoteSettingsV1, RemoteSettingsV2, LocalService, RemoteService, TransferActivity:
		return nil
	}
	return ErrInvalid
}

type Identity struct {
	ID       string `json:"id"`
	Lifetime string `json:"lifetime"`
	// Direction disambiguates transfer batches with the same original ID.
	Direction string `json:"direction"`
}

// Row is a closed sum. The source fixes its arm and identity namespace.
// Local values cannot be converted to remotely observed values by relabeling.
// No raw fields, paths, endpoints, arbitrary provider actions or apply payloads
// are accepted. Labels remain local display text and must never be HTML.
type Row struct {
	Identity         Identity             `json:"identity"`
	LocalSettings    *resource.Descriptor `json:"localSettings,omitempty"`
	RemoteSettingsV1 *SettingsValues      `json:"remoteSettingsV1,omitempty"`
	RemoteSettingsV2 *SettingsValues      `json:"remoteSettingsV2,omitempty"`
	LocalService     *SavedService        `json:"localService,omitempty"`
	RemoteService    *SharedService       `json:"remoteService,omitempty"`
	TransferActivity *Transfer            `json:"transferActivity,omitempty"`
}

type SettingsValues struct {
	Requested resource.Settings  `json:"requested"`
	Effective resource.Effective `json:"effective"`
}

type SavedService struct {
	Name        string `json:"name"`
	Direction   string `json:"direction"`
	Network     string `json:"network"`
	Ports       string `json:"ports"`
	Lifetime    string `json:"lifetime"`
	State       string `json:"state"`
	Application string `json:"application"`
}

// SharedService is the existing minimal discovery allowlist. ReviewRevision is
// a locally retained workflow reference, not a new remotely disclosed field.
// There is deliberately no saved ID, private name, target, owner or peer list.
type SharedService struct {
	Purpose        string `json:"purpose"`
	Network        string `json:"network"`
	Ports          string `json:"ports"`
	Lifetime       string `json:"lifetime"`
	ExpiresAt      int64  `json:"expiresAt"`
	Application    string `json:"application"`
	ReviewRevision string `json:"reviewRevision"`
}

// Transfer describes local batch activity, never a durable share or directory.
// The original ID in Identity is passed to the existing workflow, not a paged
// direction:id key. File names, stored paths and raw errors are excluded.
type Transfer struct {
	PeerID         string `json:"peerId"`
	TotalBytes     int64  `json:"totalBytes"`
	CompletedBytes int64  `json:"completedBytes"`
	State          string `json:"state"`
}

func (r Row) Validate(s Selection, l Limits) error {
	if s.Validate(l) != nil || !text(r.Identity.ID, l) {
		return ErrInvalid
	}
	arms := 0
	for _, yes := range []bool{r.LocalSettings != nil, r.RemoteSettingsV1 != nil, r.RemoteSettingsV2 != nil, r.LocalService != nil, r.RemoteService != nil, r.TransferActivity != nil} {
		if yes {
			arms++
		}
	}
	if arms != 1 {
		return ErrInvalid
	}
	expectedLifetime := Persistent
	if s.Kind == RemoteService {
		expectedLifetime = Activation
	}
	if s.Kind == TransferActivity {
		expectedLifetime = Process
	}
	if r.Identity.Lifetime != expectedLifetime || s.Kind != TransferActivity && r.Identity.Direction != "" {
		return ErrInvalid
	}
	switch s.Kind {
	case LocalSettings:
		if r.LocalSettings == nil || r.Identity.ID != s.Target.ResourceID || r.LocalSettings.Target != s.Target || validateDescriptor(*r.LocalSettings) != nil {
			return ErrInvalid
		}
	case RemoteSettingsV1:
		if r.RemoteSettingsV1 == nil || r.Identity.ID != s.Target.ResourceID || r.RemoteSettingsV1.validate() != nil {
			return ErrInvalid
		}
	case RemoteSettingsV2:
		if r.RemoteSettingsV2 == nil || r.Identity.ID != s.Target.ResourceID || r.RemoteSettingsV2.validate() != nil {
			return ErrInvalid
		}
	case LocalService:
		v := r.LocalService
		if v == nil || !peerID(r.Identity.ID) || !text(v.Name, l) || !ports(v.Network, v.Ports, l) || !oneOf(v.Direction, "share", "forward") || !oneOf(v.Lifetime, "", "finite", "until-revoked", "until-stopped") || !oneOf(v.State, "saved", "starting", "active", "reconnecting", "failed", "expired", "stopped") || v.Application != "unverified" {
			return ErrInvalid
		}
		if v.Lifetime == "until-stopped" && v.Direction != "forward" || v.Lifetime == "until-revoked" && v.Direction != "share" {
			return ErrInvalid
		}
	case RemoteService:
		v := r.RemoteService
		if v == nil || !peerID(r.Identity.ID) || !ports(v.Network, v.Ports, l) || !oneOf(v.Purpose, "", "generic", "custom", "web", "ssh", "db", "postgres", "ai", "local-ai", "desktop", "rustdesk") || v.Application != "unverified" || !text(v.ReviewRevision, l) {
			return ErrInvalid
		}
		if v.Lifetime == "until-revoked" {
			if v.ExpiresAt != 0 {
				return ErrInvalid
			}
		} else if !oneOf(v.Lifetime, "", "finite") || !timestamp(v.ExpiresAt) {
			return ErrInvalid
		}
	case TransferActivity:
		v := r.TransferActivity
		if v == nil || !transferID(r.Identity.ID) || !oneOf(r.Identity.Direction, "incoming", "outgoing") || !transferPeer(v.PeerID, l) || v.TotalBytes < 0 || v.TotalBytes > capacity.MaxJSONInteger || v.CompletedBytes < 0 || v.CompletedBytes > v.TotalBytes || !oneOf(v.State, "awaiting-acceptance", "queued", "transferring", "saving", "failed", "completed", "cancelled", "declined") {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}

func (v SettingsValues) validate() error {
	if v.Requested.Validate() != nil || v.Effective.TransferConcurrentFiles <= 0 || v.Effective.TransferConcurrentFiles > capacity.MaxJSONInteger || v.Effective.TransferConcurrentPerPeer <= 0 || v.Effective.TransferConcurrentPerPeer > capacity.MaxJSONInteger {
		return ErrInvalid
	}
	return nil
}

func validateDescriptor(v resource.Descriptor) error {
	if v.Target.Validate() != nil || v.Type != resource.Type || v.Authority != "local" || v.Provider != "local" || !resource.ValidDigest(v.Revision) || (SettingsValues{v.Requested, v.Effective}).validate() != nil || len(v.Operations) != 5 {
		return ErrInvalid
	}
	for i, op := range []string{"list", "inspect", "preview", "apply", "operation.status"} {
		if v.Operations[i] != op {
			return ErrInvalid
		}
	}
	return nil
}

func text(s string, l Limits) bool {
	if s == "" || len(s) > l.MaxStringBytes || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r < 32 || r == 127 {
			return false
		}
	}
	return true
}
func peerID(s string) bool {
	if len(s) == 0 || len(s) > 128 {
		return false
	}
	for i, r := range s {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			continue
		}
		if i == 0 || r != ':' && r != '_' && r != '-' {
			return false
		}
	}
	return true
}
func oneOf(s string, choices ...string) bool {
	for _, choice := range choices {
		if s == choice {
			return true
		}
	}
	return false
}

// All timestamps are Unix milliseconds, exactly representable in machine JSON.
func timestamp(n int64) bool { return n > 0 && n <= 253402300799000 }
func ports(network, value string, l Limits) bool {
	if !oneOf(network, "tcp", "udp") || !text(value, l) {
		return false
	}
	for _, part := range strings.Split(value, ",") {
		ends := strings.Split(strings.TrimSpace(part), "-")
		if len(ends) > 2 {
			return false
		}
		first := 0
		for i, end := range ends {
			end = strings.TrimSpace(end)
			for _, digit := range end {
				if digit < '0' || digit > '9' {
					return false
				}
			}
			n, err := strconv.Atoi(end)
			if err != nil || n < 1 || n > 65535 || i == 1 && n < first {
				return false
			}
			first = n
		}
	}
	return true
}

func transferID(s string) bool {
	if len(s) == 0 || len(s) > 128 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}
func transferPeer(s string, l Limits) bool {
	if !text(s, l) || len(s) > 256 {
		return false
	}
	for _, r := range s {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return false
		}
	}
	return true
}
