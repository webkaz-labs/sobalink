package core

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	qrcode "github.com/skip2/go-qrcode"
	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/directlan"
	"github.com/webkaz-labs/sobalink/internal/endpointmeta"
)

// DirectLANSelection explicitly bounds every underlay connection. Its endpoint
// carries encrypted tunnel traffic only; application targets remain scoped by
// the existing service grants and loopback target rules.
type DirectLANSelection struct {
	Listen   string   `json:"listen"`
	Prefixes []string `json:"prefixes"`
}

// Version 2 marks the WireGuard/native-UDP implementation; comparison-only
// TLS application-tunnel state is not silently activated.
const directLANStateVersion = 2

type directLANState struct {
	Version   int                `json:"version"`
	Identity  directlan.Identity `json:"identity"`
	Selection DirectLANSelection `json:"selection"`
	Peers     []directlan.Peer   `json:"peers"`
	// Metadata is represented by the v3 peer records in the same private file.
	// Peers remains a derived public DTO, never a second v3 authority source.
	Metadata *endpointmeta.Snapshot `json:"-"`
}
type directLANStore struct {
	limits         atomic.Pointer[lanStoreLimits]
	mu             sync.Mutex
	path           string
	state          directLANState
	recovery       bool
	removalRetry   *pairRemovalRetry
	write          func(string, []byte) error
	bytes, peers   int64
	fileDigest     string
	reviewRevision uint64
	// Read-only endpoint inspection cannot persist a clock observation. Keep
	// its process-local wall-time floor so a later rollback cannot reuse an
	// expired approval within this owner; explicit recovery never resets it.
	endpointObservedAt      time.Time
	endpointDeadlines       map[directLANEndpointDeadlineKey]directLANEndpointDeadline
	endpointIssuedDeadlines map[string]directLANEndpointDeadline
	// Context metadata has no command/startup caller. These process-only bounds,
	// arm epochs and publication evidence are never reconstructed from a file read.
	contextWindows      map[string]contextPreparationWindow
	endpointTransaction *EndpointTransaction // store.mu; excludes competing whole-file writes
	contextPublication  *contextPublicationReceipt
	contextEpoch        *directlan.ContextEpoch
}

func cloneDirectLANState(s directLANState) directLANState {
	s.Selection.Prefixes = append([]string(nil), s.Selection.Prefixes...)
	s.Peers = append([]directlan.Peer{}, s.Peers...)
	s.Metadata = cloneDirectLANMetadata(s.Metadata)
	return s
}
func directLANConfig(s directLANState) (directlan.Config, error) {
	ap, err := netip.ParseAddrPort(s.Selection.Listen)
	if err != nil || ap.String() != s.Selection.Listen || (ap.Port() >= DiscoveryPort && ap.Port() <= 54545) {
		return directlan.Config{}, directlan.ErrPolicy
	}
	prefixes := make([]netip.Prefix, 0, len(s.Selection.Prefixes))
	seen := map[string]bool{}
	for _, raw := range s.Selection.Prefixes {
		p, err := netip.ParsePrefix(raw)
		if err != nil || p.String() != raw || seen[raw] {
			return directlan.Config{}, directlan.ErrPolicy
		}
		seen[raw] = true
		prefixes = append(prefixes, p)
	}
	cfg := directlan.Config{Identity: s.Identity, Listen: ap, AllowedPrefixes: prefixes, Peers: s.Peers}
	return cfg, cfg.Validate()
}

// ValidateDirectLANSelection performs no network I/O or identity generation.
func ValidateDirectLANSelection(selection DirectLANSelection) error {
	_, err := directLANConfig(directLANState{Selection: selection, Identity: directlan.Identity{Seed: strings.Repeat("0", 64)}})
	return err
}
func readDirectLANStore(path string, bytes, peers int64) (*directLANStore, error) {
	state, digest, err := readDirectLANFile(path, bytes)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, &lanCommandError{"direct_lan_state_invalid", "private direct LAN state could not be read; inspect protected state before restarting"}
	}
	if err := validateDirectLANState(state); err != nil {
		return nil, &lanCommandError{"direct_lan_state_invalid", "invalid private direct LAN state; inspect protected state before restarting"}
	}
	store := &directLANStore{path: path, state: state, bytes: bytes, peers: peers, fileDigest: digest}
	// Reading is evidence, not reconciliation. A persisted fence or detected
	// clock regression stays blocked; neither is cleared by a successful read.
	if state.Metadata != nil {
		store.recovery = state.Metadata.PendingChange != nil || state.Metadata.ValidateAt(time.Now()) != nil
	}
	return store, nil
}
func (s *directLANStore) copy() directLANState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneDirectLANState(s.state)
}
func (s *directLANStore) needsRecovery() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.recovery }
func (s *directLANStore) saveLocked(next directLANState) error {
	if s.recovery {
		return directlan.ErrRecovery
	}
	next, err := s.prepareLegacyEditLocked(next, time.Now())
	if err != nil {
		return err
	}
	return s.writeStateLocked(next)
}

// writeStateLocked is the single whole-file publication boundary. Its callers
// must validate their transition under the store mutex and Core lifecycle owner.
func (s *directLANStore) writeStateLocked(next directLANState) error {
	if s.recovery {
		return directlan.ErrRecovery
	}
	return s.publishStateLocked(next)
}

// publishStateLocked is the one AtomicWrite publisher. Ordinary callers enter
// through writeStateLocked. Only exact, stopped/offline pending reconciliation
// may enter here with recovery latched, after rereading its reviewed file.
func (s *directLANStore) publishStateLocked(next directLANState) error {
	return s.publishStateWithContextLivenessLocked(next, nil)
}

// Closed context and private pair-record reducers supply this guard. The
// recovery, projection, file and capacity checks remain the same publisher.
func (s *directLANStore) writeContextStateLocked(next directLANState, live *contextSaveLiveness) error {
	if s.recovery {
		return directlan.ErrRecovery
	}
	return s.publishStateWithContextLivenessLocked(next, live)
}

func (s *directLANStore) publishStateWithContextLivenessLocked(next directLANState, live *contextSaveLiveness) error {
	if s.endpointTransaction != nil && (live == nil || live.endpoint != s.endpointTransaction) {
		return endpointmeta.ErrReview
	}
	if err := validateDirectLANState(next); err != nil {
		return err
	}
	if len(next.Peers) > len(s.state.Peers) && int64(len(next.Peers)) > s.currentCapacity().peers {
		return directlan.ErrCapacity
	}
	data, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	if int64(len(data))+1 > s.currentCapacity().bytes {
		return directlan.ErrCapacity
	}
	if s.reviewRevision == ^uint64(0) {
		return directlan.ErrCapacity
	}
	s.reviewRevision++
	// This is signal-only: no Node lock, callback or join may run under mu.
	// Every attempted publication, including same-byte or unrelated writes,
	// invalidates all earlier context admissions and response slots.
	if s.contextEpoch != nil {
		s.contextEpoch.Invalidate()
		s.contextEpoch = nil
	}
	s.contextPublication = nil
	write := s.write
	if write == nil {
		write = config.AtomicWrite
	}
	data = append(data, '\n')
	// The final signal-only check is publication admission. Stop completed
	// before this check aborts; cancellation after admission cannot retract
	// a write or roll back a published result. No watcher is needed here.
	if err := live.err(); err != nil {
		live.cancelledBeforeWrite = true
		return err
	}
	err = write(s.path, data)
	if atomicPublished(err) {
		s.state = cloneDirectLANState(next)
		s.fileDigest = directLANFileDigest(data)
	}
	if err != nil {
		s.recovery = true
		return privateAtomicError(directlan.ErrRecovery, err)
	}
	return nil
}
func (s *directLANStore) save(next directLANState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked(next)
}
func (s *directLANStore) persist(peers []directlan.Peer) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := cloneDirectLANState(s.state)
	next.Peers = append([]directlan.Peer{}, peers...)
	err := s.saveLocked(next)
	// The adapter treats every failed persistence as indeterminate. Match that
	// admission boundary even when a capacity failure preceded the actual write.
	if err != nil {
		s.recovery = true
	}
	return err
}
func (c *Core) directLANStoreCopy() *directLANStore {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.directLAN
}
func (c *Core) configureDirectLAN(selection *DirectLANSelection) error {
	saved := c.directLANStoreCopy()
	if saved != nil && saved.needsRecovery() {
		return codedDirectLANError(directlan.ErrRecovery)
	}
	if selection == nil {
		if saved == nil {
			return &lanCommandError{"direct_lan_setup_required", "choose an exact private listen endpoint and allowed prefixes before connecting"}
		}
		return nil
	}
	if err := ValidateDirectLANSelection(*selection); err != nil {
		return codedDirectLANError(err)
	}
	if saved != nil && reflect.DeepEqual(saved.copy().Selection, *selection) {
		return nil
	}
	c.mu.RLock()
	active, attempted := c.node != nil, c.attemptedNetwork != ""
	web := c.web
	conflict := web != nil && web.Port() == mustDirectLANPort(*selection)
	for _, ap := range c.proxyReservedPorts() {
		conflict = conflict || ap.Port() == mustDirectLANPort(*selection)
	}
	c.mu.RUnlock()
	if active || attempted {
		return &lanCommandError{"network_restart_required", "stop soba, then start with --offline before changing the direct LAN endpoint or prefixes"}
	}
	if conflict {
		return &lanCommandError{"direct_lan_port_reserved", "direct LAN tunnel port conflicts with a local management or proxy port; choose another high port"}
	}
	if saved == nil {
		id, err := directlan.GenerateIdentity()
		if err != nil {
			return err
		}
		saved = &directLANStore{path: filepath.Join(c.dir, "direct-lan.json"), write: c.writeAtomic, bytes: c.limit("resources", "lanStateBytes"), peers: c.limit("logical", "trustedPeers"), state: directLANState{Version: directLANStateVersion, Identity: id, Peers: []directlan.Peer{}}}
	}
	next := saved.copy()
	if len(next.Peers) != 0 {
		return &lanCommandError{"direct_lan_pairs_present", "revoke saved direct LAN pairs before changing the endpoint or allowed prefixes; pair and approve application trust separately afterward"}
	}
	next.Selection = *selection
	err := saved.save(next)
	if err == nil || saved.needsRecovery() {
		c.mu.Lock()
		c.directLAN = saved
		c.mu.Unlock()
	}
	return codedDirectLANError(err)
}
func mustDirectLANPort(s DirectLANSelection) uint16 {
	ap, _ := netip.ParseAddrPort(s.Listen)
	return ap.Port()
}
func codedDirectLANError(err error) error {
	if err == nil {
		return nil
	}
	var code, message string
	switch {
	case errors.Is(err, directlan.ErrRemotePairedLocalSave):
		code, message = "direct_lan_remote_paired_local_save", "the other device paired but local state could not be confirmed; stop, inspect protected state and revoke the remote pair before retrying"
	case errors.Is(err, directlan.ErrPairUncertain):
		code, message = "direct_lan_pair_uncertain", "pairing reply was not received; inspect and revoke any pair on the other device before retrying"
	case errors.Is(err, directlan.ErrRecovery):
		code, message = "direct_lan_recovery_required", "direct LAN requires protected-state recovery; stop, review saved state and the system clock, and reconcile before reopening"
	case errors.Is(err, directlan.ErrLocalAddressUnavailable):
		code, message = "direct_lan_address_unavailable", "the selected direct LAN address is no longer on an up local interface; reconnect that network or explicitly reconfigure while stopped"
	case errors.Is(err, directlan.ErrLocalAddressUnknown):
		code, message = "direct_lan_address_unknown", "local interface availability could not be checked; permissions and network state must be reviewed before selecting another path"
	case errors.Is(err, directlan.ErrPolicy):
		code, message = "direct_lan_policy", "choose an exact numeric private or loopback endpoint, an unreserved high port, and canonical allowed private or loopback prefixes"
	case errors.Is(err, directlan.ErrIdentity):
		code, message = "direct_lan_identity", "use the recipient's exact public direct LAN identity"
	case errors.Is(err, directlan.ErrInvitation):
		code, message = "direct_lan_invitation_invalid", "direct LAN invitation is invalid or expired; request a new invitation for this identity"
	case errors.Is(err, directlan.ErrCapacity):
		code, message = "direct_lan_capacity", "direct LAN resource limit reached; remove unused peers or retry after active work finishes"
	case errors.Is(err, directlan.ErrUnavailable):
		code, message = "direct_lan_unavailable", "direct LAN is not ready; check the selected endpoint and start the configured network"
	case errors.Is(err, directlan.ErrUntrusted):
		code, message = "direct_lan_pair_state", "review the saved direct LAN pair; revoke an existing pair before issuing a replacement invitation"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	default:
		var coded interface{ ErrorCode() string }
		if errors.As(err, &coded) {
			return err
		}
		code, message = "direct_lan_failed", "direct LAN operation did not complete; check the invitation, selected addresses and reachability, and cancel an existing invitation before replacing it"
	}
	return privateAtomicError(&lanCommandError{code, message}, err)
}
func (c *Core) directLANStatus() map[string]any {
	status := map[string]any{"configured": false, "listenerReady": false, "pairingReady": false, "recoveryRequired": false, "path": "direct-lan", "peers": []directlan.Peer{}}
	if s := c.directLANStoreCopy(); s != nil {
		state := s.copy()
		status["configured"] = state.Selection.Listen != ""
		status["publicKey"] = state.Identity.PublicKey()
		status["endpoint"] = state.Selection.Listen
		status["prefixes"] = state.Selection.Prefixes
		status["recoveryRequired"] = s.needsRecovery()
		status["peers"] = state.Peers
		status["stateVersion"] = state.Version
		status["endpointUpdatesEnabled"] = false
		status["endpointMetadataPending"] = directLANMetadataManaged(state.Metadata)
	}
	if b, ok := c.nodeCopy().(*directLANBackend); ok {
		status["resourceRestartRequired"] = b.resources != directRuntimeResources(c.capacityPolicy())
		st, err := b.State(c.ctx)
		ready := err == nil && st.Snapshot.Running
		if errors.Is(b.Node.Ready(), directlan.ErrRecovery) {
			status["recoveryRequired"] = true
		}
		status["listenerReady"] = ready
		status["pairingReady"] = ready
	}
	return status
}
func (c *Core) directLANCommand(ctx context.Context, name string, raw json.RawMessage) (any, error) {
	switch name {
	case "direct-lan.endpoint.export.preview", "direct-lan.endpoint.export", "direct-lan.endpoint.reexport.preview", "direct-lan.endpoint.reexport":
		return c.directLANEndpointExportCommand(ctx, name, raw)
	}
	if strings.HasPrefix(name, "direct-lan.endpoint.") {
		return c.directLANEndpointCommand(ctx, name, raw)
	}
	if name == "direct-lan.migration.review" || name == "direct-lan.migration.apply" {
		return c.directLANMigrationCommand(ctx, name, raw)
	}
	if name == "direct-lan.status" || name == "direct-lan.identity" {
		if err := decodePayload(raw, &struct{}{}); err != nil {
			return nil, err
		}
		if name == "direct-lan.status" {
			return c.directLANStatus(), nil
		}
		s := c.directLANStoreCopy()
		if s == nil {
			return nil, &lanCommandError{"direct_lan_setup_required", "configure direct LAN explicitly before reading its public identity"}
		}
		state := s.copy()
		return map[string]string{"publicKey": state.Identity.PublicKey(), "endpoint": state.Selection.Listen}, nil
	}
	if name == "direct-lan.revoke" || name == "direct-lan.revoke.retry" {
		var input struct {
			PeerID string `json:"peerId"`
		}
		if err := decodePayload(raw, &input); err != nil {
			return nil, err
		}
		if !validLANPublicKey(input.PeerID) {
			return nil, codedDirectLANError(directlan.ErrIdentity)
		}
		if s := c.directLANStoreCopy(); s != nil {
			saved := s.copy()
			if saved.Version == directLANPairRecordStateVersion || directLANMetadataManaged(saved.Metadata) {
				return c.removeManagedDirectLANLocked(ctx, input.PeerID)
			}
		}
		return nil, c.revokeDirectLANPeer(input.PeerID)
	}
	var invitation directlan.Invitation
	if name == "direct-lan.inspect" || name == "direct-lan.join" || name == "direct-lan.cancel" {
		var input struct {
			Invitation string `json:"invitation"`
		}
		if err := decodePayload(raw, &input); err != nil {
			return nil, err
		}
		var err error
		invitation, err = directlan.ParseInvitation(input.Invitation)
		if err != nil {
			return nil, codedDirectLANError(err)
		}
		saved := c.directLANStoreCopy()
		if saved == nil {
			return nil, &lanCommandError{"direct_lan_setup_required", "configure direct LAN before reviewing or joining an invitation"}
		}
		state := saved.copy()
		if name != "direct-lan.cancel" {
			if err := invitation.ValidateFor(state.Identity.PublicKey(), time.Now()); err != nil {
				return nil, codedDirectLANError(err)
			}
			state.Peers = []directlan.Peer{invitation.Host}
			if _, err := directLANConfig(state); err != nil {
				return nil, codedDirectLANError(err)
			}
		}
		if name == "direct-lan.inspect" {
			return map[string]any{"hostPublicKey": invitation.Host.Key, "hostName": invitation.Host.Name, "endpoint": invitation.Host.Endpoint.String(), "recipientPublicKey": invitation.RecipientKey, "recipientMatches": true, "expires": invitation.Expires}, nil
		}
	}
	node, ok := c.nodeCopy().(*directLANBackend)
	if !ok {
		return nil, &lanCommandError{"direct_lan_setup_required", "configure and start direct LAN first"}
	}
	switch name {
	case "direct-lan.invite":
		var input struct {
			RecipientPublicKey string `json:"recipientPublicKey"`
			Name               string `json:"name"`
			TTLSeconds         int    `json:"ttlSeconds"`
			QR                 bool   `json:"qr,omitempty"`
		}
		if err := decodePayload(raw, &input); err != nil {
			return nil, err
		}
		if input.Name == "" || len(input.Name) > 128 || !utf8.ValidString(input.Name) || strings.TrimSpace(input.Name) != input.Name || strings.IndexFunc(input.Name, unicode.IsControl) >= 0 {
			return nil, &lanCommandError{"direct_lan_name_invalid", "choose a nonempty display name of at most 128 bytes without control characters"}
		}
		if input.TTLSeconds < 1 || input.TTLSeconds > 600 {
			return nil, codedDirectLANError(directlan.ErrInvitation)
		}
		inv, err := node.IssueNamedInvitation(ctx, directlan.Peer{Key: input.RecipientPublicKey, Name: input.Name}, c.profileCopy().Settings.Hostname, time.Duration(input.TTLSeconds)*time.Second)
		if err != nil {
			return nil, codedDirectLANError(err)
		}
		encoded, err := inv.Encode()
		if err != nil {
			node.CancelInvitation(inv.Token)
			return nil, codedDirectLANError(err)
		}
		result := map[string]any{"invitation": encoded, "expires": inv.Expires, "recipientPublicKey": inv.RecipientKey}
		if input.QR {
			code, e := qrcode.New(encoded, qrcode.Medium)
			if e != nil {
				node.CancelInvitation(inv.Token)
				return nil, &lanCommandError{"direct_lan_qr_failed", "local QR generation failed; create a text invitation instead"}
			}
			result["qr"] = code.Bitmap()
		}
		return result, nil
	case "direct-lan.cancel":
		if invitation.Host.Key != node.PublicKey() {
			return nil, codedDirectLANError(directlan.ErrInvitation)
		}
		node.CancelInvitation(invitation.Token)
		return map[string]bool{"cancelled": true}, nil
	case "direct-lan.join":
		// The public executor owns the operation-specific two-stage admission.
		// Never run pairing while a generic command holds Core.op.
		return nil, codedDirectLANError(directlan.ErrUnavailable)
	default:
		return nil, errors.New("unknown direct LAN action")
	}
}
func (c *Core) revokeDirectLANPeer(id string) error {
	s := c.directLANStoreCopy()
	if s == nil {
		return &lanCommandError{"direct_lan_setup_required", "there is no saved direct LAN identity to revoke"}
	}
	saved := s.copy()
	if saved.Version == directLANPairRecordStateVersion || directLANMetadataManaged(saved.Metadata) {
		_, err := c.removeManagedDirectLANLocked(c.ctx, id)
		return err
	}
	if saved.Version != directLANStateVersion && saved.Version != directLANMetadataStateVersion {
		return codedDirectLANError(directLANMetadataUnavailable())
	}

	active := c.nodeCopy()
	b, ok := active.(*directLANBackend)
	if active != nil && !ok {
		return &lanCommandError{"network_restart_required", "stop soba and start with --offline before changing saved direct LAN pairs"}
	}
	startupErr := errors.Join(c.revokeStartupPeer(id), c.retireMixedTransport("direct-lan", id))
	// A failed startup journal must not preserve inbound/app/transport authority.
	// Continue revoking in memory and closing work, then latch recovery below.
	p := c.profileCopy()
	peers := p.Peers[:0]
	for _, peer := range p.Peers {
		if peer.ID != id || peer.Network != "direct-lan" {
			peers = append(peers, peer)
		}
	}
	p.Peers = peers
	c.mu.Lock()
	c.profile = p
	delete(c.confirmed, id)
	delete(c.discovered, id)
	c.mu.Unlock()
	c.revokePeer(id)
	var transportErr error
	if ok {
		transportErr = b.Revoke(id)
	} else {
		state := s.copy()
		next := state.Peers[:0]
		for _, peer := range state.Peers {
			if peer.Key != id {
				next = append(next, peer)
			}
		}
		state.Peers = next
		transportErr = s.save(state)
	}
	profileErr := c.saveProfile(p)
	if err := errors.Join(startupErr, transportErr, profileErr); err != nil {
		if b != nil {
			_ = b.Close()
		}
		c.networkReady.Store(false)
		c.stopAllServices()
		s.mu.Lock()
		s.recovery = true
		s.mu.Unlock()
		c.mu.Lock()
		c.networkState = "error"
		c.networkError = "direct LAN stopped because durable revocation could not be confirmed; inspect protected state before restarting"
		c.networkFatal = c.networkError
		c.networkErrorCode = "direct_lan_recovery_required"
		c.mu.Unlock()
		return codedDirectLANError(errors.Join(directlan.ErrRecovery, err))
	}
	return nil
}
func (c *Core) reconcileDirectLANTrust() error {
	paired := map[string]bool{}
	if s := c.directLANStoreCopy(); s != nil {
		state := s.copy()
		terminal := map[string]bool{}
		for _, id := range terminalDirectLANPeers(state) {
			terminal[id] = true
		}
		for _, peer := range state.Peers {
			if !terminal[peer.Key] {
				paired[peer.Key] = true
			}
		}
	}
	p := c.profileCopy()
	peers := p.Peers[:0]
	for _, peer := range p.Peers {
		if peer.Network != "direct-lan" || paired[peer.ID] {
			peers = append(peers, peer)
		}
	}
	if len(peers) == len(p.Peers) {
		return nil
	}
	p.Peers = peers
	err := c.saveProfile(p)
	if atomicPublished(err) {
		c.mu.Lock()
		c.profile = p
		c.mu.Unlock()
	}
	return err
}
