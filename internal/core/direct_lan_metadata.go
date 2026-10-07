package core

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"math"
	"net/netip"
	"os"
	"reflect"
	"slices"
	"strconv"
	"time"

	"github.com/webkaz-labs/sobalink/internal/directlan"
	"github.com/webkaz-labs/sobalink/internal/endpointmeta"
	"github.com/webkaz-labs/sobalink/internal/transfer"
)

const directLANMetadataStateVersion = 3

// The v3 representation has one copy of every peer. Identity and Selection
// retain their existing owner; all endpoint replay/context records are inside
// this same file. The local public key/scope are derived from that owner.
type directLANMetadataFile struct {
	Version               int                         `json:"version"`
	Identity              directlan.Identity          `json:"identity"`
	Selection             DirectLANSelection          `json:"selection"`
	Revision              string                      `json:"revision"`
	PreviousLocalEndpoint string                      `json:"previous_local_endpoint"`
	ObservedAt            string                      `json:"observed_at"`
	Peers                 []endpointmeta.PeerRecord   `json:"peers"`
	PendingChange         *endpointmeta.PendingChange `json:"pending_change,omitempty"`
}

func (s directLANState) MarshalJSON() ([]byte, error) {
	if s.Version == directLANStateVersion && s.Metadata == nil {
		type legacy directLANState
		return json.Marshal(legacy(s))
	}
	if s.Version != directLANMetadataStateVersion || s.Metadata == nil {
		return nil, endpointmeta.ErrInvalid
	}
	m := s.Metadata
	return json.Marshal(directLANMetadataFile{s.Version, s.Identity, s.Selection, m.Revision, m.PreviousLocalEndpoint, m.ObservedAt, m.Peers, m.PendingChange})
}

func (s *directLANState) UnmarshalJSON(data []byte) error {
	var header struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return err
	}
	switch header.Version {
	case directLANStateVersion:
		type legacy directLANState
		var old legacy
		if err := decodeDirectLANPrivateJSON(data, &old); err != nil {
			return err
		}
		*s = directLANState(old)
	case directLANMetadataStateVersion:
		var saved directLANMetadataFile
		if err := decodeDirectLANPrivateJSON(data, &saved); err != nil {
			return err
		}
		// Ignore indentation only. Duplicates, case aliases, omitted mandatory
		// fields, nulls and alternative field order cannot hide authoritative data.
		var compact bytes.Buffer
		if err := json.Compact(&compact, data); err != nil {
			return err
		}
		canonical, err := json.Marshal(saved)
		if err != nil || !bytes.Equal(compact.Bytes(), canonical) {
			return endpointmeta.ErrInvalid
		}
		next := directLANState{Version: saved.Version, Identity: saved.Identity, Selection: saved.Selection, Peers: make([]directlan.Peer, 0, len(saved.Peers))}
		local, scope, err := directLANLocalMetadata(next)
		if err != nil {
			return err
		}
		for _, record := range saved.Peers {
			peer, err := directLANPeerDTO(record.Peer)
			if err != nil {
				return err
			}
			next.Peers = append(next.Peers, peer)
		}
		next.Metadata = &endpointmeta.Snapshot{Version: 3, Revision: saved.Revision, LocalPeer: local, LocalScope: scope, PreviousLocalEndpoint: saved.PreviousLocalEndpoint, ObservedAt: saved.ObservedAt, Peers: saved.Peers, PendingChange: saved.PendingChange}
		if err := validateDirectLANState(next); err != nil {
			return err
		}
		*s = next
	default:
		return endpointmeta.ErrInvalid
	}
	return nil
}

func decodeDirectLANPrivateJSON(data []byte, value any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return endpointmeta.ErrInvalid
	}
	return nil
}

func cloneDirectLANMetadata(value *endpointmeta.Snapshot) *endpointmeta.Snapshot {
	if value == nil {
		return nil
	}
	// These closed data-only types have no custom marshalers or fallible values.
	data, _ := json.Marshal(value)
	var clone endpointmeta.Snapshot
	_ = json.Unmarshal(data, &clone)
	return &clone
}

func directLANPeerWire(p directlan.Peer) endpointmeta.PeerWire {
	return endpointmeta.PeerWire{Key: p.Key, Name: p.Name, Endpoint: p.Endpoint.String(), TunnelKey: p.TunnelKey}
}

func directLANPeerDTO(p endpointmeta.PeerWire) (directlan.Peer, error) {
	ap, err := netip.ParseAddrPort(p.Endpoint)
	if err != nil || ap.String() != p.Endpoint {
		return directlan.Peer{}, directlan.ErrPolicy
	}
	return directlan.Peer{Key: p.Key, Name: p.Name, Endpoint: ap, TunnelKey: p.TunnelKey}, nil
}

func directLANLocalMetadata(s directLANState) (endpointmeta.PeerWire, endpointmeta.Scope, error) {
	cfg, err := directLANConfig(s)
	if err != nil {
		return endpointmeta.PeerWire{}, endpointmeta.Scope{}, err
	}
	scope := endpointmeta.Scope{Family: "ipv6", Prefixes: []string{}}
	if cfg.Listen.Addr().Is4() {
		scope.Family = "ipv4"
	}
	for _, prefix := range cfg.AllowedPrefixes {
		if prefix.Addr().Is4() == cfg.Listen.Addr().Is4() {
			scope.Prefixes = append(scope.Prefixes, prefix.String())
		}
	}
	slices.Sort(scope.Prefixes)
	local := endpointmeta.PeerWire{Key: s.Identity.PublicKey(), TunnelKey: s.Identity.TunnelKey(), Endpoint: s.Selection.Listen}
	return local, scope, nil
}

func validateDirectLANState(s directLANState) error {
	if _, err := directLANConfig(s); err != nil {
		return err
	}
	if s.Version == directLANStateVersion && s.Metadata == nil {
		return nil
	}
	if s.Version != directLANMetadataStateVersion || s.Metadata == nil {
		return endpointmeta.ErrInvalid
	}
	m := s.Metadata
	local, scope, err := directLANLocalMetadata(s)
	if err != nil || m.LocalPeer != local || !reflect.DeepEqual(m.LocalScope, scope) || len(m.Peers) != len(s.Peers) {
		return endpointmeta.ErrIdentity
	}
	for i, record := range m.Peers {
		if record.Peer != directLANPeerWire(s.Peers[i]) {
			return endpointmeta.ErrIdentity
		}
		if record.PairContext != nil {
			pairScope := record.PairContext.HostScope
			if record.PairContext.JoinerKey == local.Key {
				pairScope = record.PairContext.JoinerScope
			}
			if !reflect.DeepEqual(pairScope, scope) {
				return endpointmeta.ErrPolicy
			}
		}
	}
	return m.Validate()
}

func directLANFileDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// The profile lifecycle owner excludes other supported writers. Like the
// existing protected-store readers, this is not an anti-rollback mechanism or
// a defense against malicious concurrent pathname replacement.
func readDirectLANFile(path string, budget int64) (directLANState, string, error) {
	var state directLANState
	info, err := os.Lstat(path)
	if err != nil {
		return state, "", err
	}
	if !info.Mode().IsRegular() {
		return state, "", endpointmeta.ErrInvalid
	}
	if budget < 1 || info.Size() > budget {
		return state, "", directlan.ErrCapacity
	}
	f, err := (transfer.Source{LocalPath: path}).Open()
	if err != nil {
		return state, "", err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, budget))
	if err != nil {
		return state, "", err
	}
	var extra [1]byte
	n, err := io.ReadFull(f, extra[:])
	if n != 0 {
		return state, "", directlan.ErrCapacity
	}
	if err != io.EOF {
		return state, "", err
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return state, "", err
	}
	return state, directLANFileDigest(data), nil
}

func directLANMetadataManaged(m *endpointmeta.Snapshot) bool {
	if m == nil {
		return false
	}
	if m.PendingChange != nil {
		return true
	}
	for _, peer := range m.Peers {
		if peer.PairContext != nil || peer.UpgradePending != nil || peer.EndpointState != nil || peer.ContextConfirmed {
			return true
		}
	}
	return false
}

func directLANMetadataUnavailable() error {
	return &lanCommandError{"direct_lan_endpoint_integration_pending", "saved endpoint metadata requires a validated runtime and recovery integration; keep direct LAN stopped and preserve protected state"}
}

// runtimeConfig is deliberately narrower than structural validation. No saved
// context/proof is installed through today's exact-endpoint constructor. There
// is no feature flag or public command that can bypass this construction gate.
func (s *directLANStore) runtimeConfig() (directlan.Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.recovery {
		return directlan.Config{}, directlan.ErrRecovery
	}
	if s.state.Metadata != nil && s.state.Metadata.ValidateAt(time.Now()) != nil {
		s.recovery = true
		return directlan.Config{}, directlan.ErrRecovery
	}
	if directLANMetadataManaged(s.state.Metadata) {
		return directlan.Config{}, directLANMetadataUnavailable()
	}
	return directLANConfig(cloneDirectLANState(s.state))
}

// Today's Persist([]Peer) may only edit legacy records. In particular it cannot
// discard a fence, strip proofs, reset a high-water mark or turn a context-aware
// pair into a legacy pair. Pair-revoke/tombstone semantics need a separate
// reviewed transition before managed records can be mutated or activated.
func (s *directLANStore) prepareLegacyEditLocked(next directLANState, now time.Time) (directLANState, error) {
	before := s.state
	if before.Metadata == nil {
		if next.Version != directLANStateVersion || next.Metadata != nil {
			return directLANState{}, endpointmeta.ErrInvalid
		}
		return next, nil
	}
	if next.Version != before.Version || next.Identity != before.Identity || !reflect.DeepEqual(next.Metadata, before.Metadata) {
		return directLANState{}, endpointmeta.ErrInvalid
	}
	if directLANMetadataManaged(before.Metadata) {
		return directLANState{}, directLANMetadataUnavailable()
	}
	if err := before.Metadata.ValidateAt(now); err != nil {
		s.recovery = true
		return directLANState{}, directlan.ErrRecovery
	}
	if reflect.DeepEqual(before.Peers, next.Peers) && reflect.DeepEqual(before.Selection, next.Selection) {
		return next, nil
	}
	m := cloneDirectLANMetadata(before.Metadata)
	revision, err := strconv.ParseUint(m.Revision, 10, 64)
	if err != nil || revision == math.MaxUint64 {
		return directLANState{}, directlan.ErrCapacity
	}
	m.Revision = strconv.FormatUint(revision+1, 10)
	m.ObservedAt = now.UTC().Format(time.RFC3339Nano)
	m.LocalPeer, m.LocalScope, err = directLANLocalMetadata(next)
	if err != nil {
		return directLANState{}, err
	}
	if !reflect.DeepEqual(before.Selection, next.Selection) {
		if len(before.Peers) != 0 || len(next.Peers) != 0 {
			return directLANState{}, directlan.ErrPolicy
		}
		// This is the existing explicit, unpaired offline configuration path,
		// not a signed local movement with a previous endpoint to advertise.
		m.PreviousLocalEndpoint = next.Selection.Listen
	}
	old := make(map[string]endpointmeta.PeerRecord, len(m.Peers))
	for _, record := range m.Peers {
		old[record.Peer.Key] = record
	}
	m.Peers = make([]endpointmeta.PeerRecord, 0, len(next.Peers))
	for _, peer := range next.Peers {
		wire := directLANPeerWire(peer)
		if record, ok := old[peer.Key]; ok {
			if record.Peer != wire {
				return directLANState{}, directlan.ErrUntrusted
			}
			m.Peers = append(m.Peers, record)
		} else {
			m.Peers = append(m.Peers, endpointmeta.PeerRecord{Peer: wire, Revision: m.Revision})
		}
	}
	next.Metadata = m
	return next, nil
}

// DirectLANMigrationReview exposes only format-change facts. Its revision is
// process-local and bound to the exact file/state; it is not an approval token
// for endpoint movement, context establishment or application permissions.
type DirectLANMigrationReview struct {
	Revision                   string `json:"revision"`
	FromVersion                int    `json:"fromVersion"`
	ToVersion                  int    `json:"toVersion"`
	PairedDevices              int    `json:"pairedDevices"`
	ChangesStorageFormat       bool   `json:"changesStorageFormat"`
	PreservesDeviceIdentity    bool   `json:"preservesDeviceIdentity"`
	PreservesEndpoints         bool   `json:"preservesEndpoints"`
	PreservesApplicationGrants bool   `json:"preservesApplicationGrants"`
	AddressFollowingEnabled    bool   `json:"addressFollowingEnabled"`
	RequiresV3Reader           bool   `json:"requiresV3Reader"`
}

func (s *directLANStore) migrationReviewLocked(process string) (DirectLANMigrationReview, error) {
	if s.recovery {
		return DirectLANMigrationReview{}, directlan.ErrRecovery
	}
	if directLANMetadataManaged(s.state.Metadata) {
		return DirectLANMigrationReview{}, directLANMetadataUnavailable()
	}
	current, digest, err := readDirectLANFile(s.path, s.currentCapacity().bytes)
	if err != nil || digest != s.fileDigest || !reflect.DeepEqual(current, s.state) {
		return DirectLANMigrationReview{}, directLANMigrationChanged()
	}
	candidate, err := s.migrationCandidateLocked(time.Now())
	if err != nil {
		return DirectLANMigrationReview{}, err
	}
	encoded, err := json.MarshalIndent(candidate, "", "  ")
	if err != nil {
		return DirectLANMigrationReview{}, err
	}
	if int64(len(encoded))+1 > s.currentCapacity().bytes {
		return DirectLANMigrationReview{}, directlan.ErrCapacity
	}
	view := DirectLANMigrationReview{FromVersion: s.state.Version, ToVersion: directLANMetadataStateVersion, PairedDevices: len(s.state.Peers), ChangesStorageFormat: s.state.Version != directLANMetadataStateVersion, PreservesDeviceIdentity: true, PreservesEndpoints: true, PreservesApplicationGrants: true, RequiresV3Reader: true}
	view.Revision = privateRevision(struct {
		Purpose, Process, FileDigest string
		StoreRevision                uint64
		State                        directLANState
	}{"direct-lan-migration-v1", process, digest, s.reviewRevision, s.state})
	return view, nil
}

func directLANMigrationChanged() error {
	return &lanCommandError{"direct_lan_migration_review_changed", "the direct LAN migration review is stale or saved state changed; keep networking stopped and review the current protected state again"}
}

// Saved-time validation belongs to the store owner: once observed, a recovery
// condition remains latched even if the wall clock later catches up.
func (s *directLANStore) migrationCandidateLocked(now time.Time) (directLANState, error) {
	if s.recovery {
		return directLANState{}, directlan.ErrRecovery
	}
	state := s.state
	if state.Version == directLANMetadataStateVersion {
		if state.Metadata == nil || state.Metadata.ValidateAt(now) != nil {
			s.recovery = true
			return directLANState{}, directlan.ErrRecovery
		}
		return cloneDirectLANState(state), nil
	}
	next := cloneDirectLANState(state)
	local, scope, err := directLANLocalMetadata(next)
	if err != nil {
		return directLANState{}, err
	}
	legacy := endpointmeta.LegacySnapshot{Version: 2, LocalPeer: local, LocalScope: scope, Peers: make([]endpointmeta.PeerWire, 0, len(next.Peers))}
	for _, peer := range next.Peers {
		legacy.Peers = append(legacy.Peers, directLANPeerWire(peer))
	}
	metadata, err := endpointmeta.MigrateLegacy(legacy, now)
	if err != nil {
		return directLANState{}, &lanCommandError{"direct_lan_migration_unavailable", "the saved exact-endpoint configuration cannot be represented by endpoint metadata; keep its current format and review endpoint scope before migrating"}
	}
	next.Version, next.Metadata = directLANMetadataStateVersion, &metadata
	return next, nil
}

// Command already holds Core.op. The process/CLI also holds the exclusive
// profile lock. Do not call this directly from a Node persistence callback.
func (c *Core) directLANMigrationCommand(ctx context.Context, name string, raw json.RawMessage) (any, error) {
	var expected string
	if name == "direct-lan.migration.review" {
		if err := decodePayload(raw, &struct{}{}); err != nil {
			return nil, err
		}
	} else {
		var input struct {
			ExpectedRevision string `json:"expectedRevision"`
		}
		if err := decodePayload(raw, &input); err != nil {
			return nil, err
		}
		if input.ExpectedRevision == "" {
			return nil, directLANMigrationChanged()
		}
		expected = input.ExpectedRevision
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.mu.RLock()
	stopped := c.node == nil && c.attemptedNetwork == "" && !c.closing && c.ctx.Err() == nil
	process, store := c.lanStartNonce, c.directLAN
	c.mu.RUnlock()
	if !stopped {
		return nil, &lanCommandError{"network_restart_required", "stop soba and start with --offline before reviewing or applying direct LAN metadata migration"}
	}
	if store == nil {
		return nil, &lanCommandError{"direct_lan_setup_required", "configure direct LAN before reviewing its saved-state format"}
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	review, err := store.migrationReviewLocked(process)
	if err != nil {
		return nil, codedDirectLANError(err)
	}
	if name == "direct-lan.migration.review" {
		return review, nil
	}
	if expected != review.Revision {
		return nil, directLANMigrationChanged()
	}
	if store.state.Version == directLANMetadataStateVersion {
		return map[string]any{"stateVersion": directLANMetadataStateVersion, "changed": false, "endpointUpdatesEnabled": false}, nil
	}
	next, err := store.migrationCandidateLocked(time.Now())
	if err != nil {
		return nil, codedDirectLANError(err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := store.writeStateLocked(next); err != nil {
		return nil, codedDirectLANError(err)
	}
	return map[string]any{"stateVersion": directLANMetadataStateVersion, "changed": true, "endpointUpdatesEnabled": false}, nil
}
