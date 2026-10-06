package core

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/identity"
	"github.com/webkaz-labs/sobalink/internal/lanlink"
	"github.com/webkaz-labs/sobalink/internal/lanpolicy"
	"github.com/webkaz-labs/sobalink/internal/policy"
)

const maxLANInvitation = 64 << 10

type lanCommandError struct{ code, message string }

func (e *lanCommandError) Error() string     { return e.message }
func (e *lanCommandError) ErrorCode() string { return e.code }

func networkErrorCode(err error) string {
	var coded interface{ ErrorCode() string }
	if errors.As(err, &coded) {
		return coded.ErrorCode()
	}
	return ""
}

func codedLANError(err error) error {
	switch {
	case errors.Is(err, lanlink.ErrProxyEnvironment):
		return &lanCommandError{"lan_environment_proxy", "remove proxy environment variables for this process, then start LAN again"}
	case errors.Is(err, lanlink.ErrEnvironmentOverride):
		return &lanCommandError{"lan_environment_override", "remove Tailscale environment overrides for this process, then start LAN again"}
	case errors.Is(err, lanlink.ErrRelayMismatch):
		return &lanCommandError{"lan_relay_mismatch", "review the selected numeric relay address and exact certificate pin against the invitation"}
	default:
		return err
	}
}

// LANSelection is public configuration. A relay is always an explicit numeric
// endpoint and certificate pin; no public relay map or fallback is selected.
type LANSelection struct {
	Kind              string `json:"kind"`
	Address           string `json:"address"`
	CertificateSHA256 string `json:"certificateSHA256,omitempty"`
}

// All private transport material belongs to this one protected atomic file.
// Application trust is a separate explicit approval in the ordinary profile.
type lanState struct {
	WANCandidates     *WANCandidateConfig      `json:"wan_candidates,omitempty"`
	Version           int                      `json:"version"`
	DestinationPolicy lanpolicy.Config         `json:"destination_policy,omitzero"`
	Identity          lanlink.Identity         `json:"identity"`
	Selection         *LANSelection            `json:"selection,omitempty"`
	RelayIdentity     *lanlink.RelayIdentity   `json:"relayIdentity,omitempty"`
	Trust             lanlink.Snapshot         `json:"trust"`
	Remotes           []lanlink.RemotePeer     `json:"remotes"`
	RouteCandidates   []lanlink.RouteCandidate `json:"route_candidates,omitempty"`
}

type lanStore struct {
	mu            sync.Mutex
	path          string
	write         func(string, []byte) error
	state         lanState
	routeRecovery bool
	limits        atomic.Pointer[lanStoreLimits]
}

type lanStoreLimits struct{ peers, bytes int64 }

func selectedLANLimits(policy capacity.Policy) *lanStoreLimits {
	return &lanStoreLimits{peers: policy.Number("logical", "trustedPeers"), bytes: policy.Number("resources", "lanStateBytes")}
}

func (s *lanStore) currentLimits() *lanStoreLimits {
	if limits := s.limits.Load(); limits != nil {
		return limits
	}
	return selectedLANLimits(capacity.Defaults())
}

func (s *lanStore) peerLimit() int64 { return s.currentLimits().peers }

func readLANStore(path string) (*lanStore, error) {
	policy, err := readCapacityPolicy(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	return readLANStoreWithPolicy(path, policy)
}

func readLANStoreWithPolicy(path string, policy capacity.Policy) (*lanStore, error) {
	limits := selectedLANLimits(policy)
	var state lanState
	if err := readBoundedPrivateJSON(path, limits.bytes, &state); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		if networkErrorCode(err) == "profile_capacity" {
			return nil, &lanCommandError{"lan_state_capacity", fmt.Sprintf("private LAN state exceeds the %d-byte lanStateBytes budget; raise that storage budget before loading", limits.bytes)}
		}
		return nil, err
	}
	if err := validateLANState(state); err != nil {
		return nil, err
	}
	store := &lanStore{path: path, state: state}
	store.limits.Store(limits)
	return store, nil
}

func strictLANJSON(raw []byte, value any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		return err
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return errors.New("unexpected data after JSON")
	}
	return nil
}

func supportedLANStateVersion(version int) bool { return version >= 1 && version <= 5 }

func validateLANState(s lanState) error {
	if !supportedLANStateVersion(s.Version) || s.Identity.Validate() != nil {
		return errors.New("invalid private LAN state")
	}
	if err := validateWANCandidateState(s); err != nil {
		return err
	}
	if err := validateLANDestinationPolicy(s); err != nil {
		return err
	}
	if err := validateLANRoutes(s); err != nil {
		return err
	}
	book := lanlink.NewBook()
	if err := book.Restore(s.Trust); err != nil {
		return errors.New("invalid saved LAN pair identities")
	}
	if len(s.Trust.Peers) != len(s.Remotes) {
		return errors.New("saved LAN pair identities disagree")
	}
	if s.Selection == nil {
		if s.RelayIdentity != nil || len(s.Remotes) != 0 {
			return errors.New("saved LAN pairs require a selected relay")
		}
		return nil
	}
	_, err := storedLANRelay(s)
	return err
}

func savedLANRelay(s lanState) (lanlink.TrustedRelay, error) {
	relay, err := storedLANRelay(s)
	if err != nil {
		return lanlink.TrustedRelay{}, err
	}
	if s.Selection.Kind == "host" {
		if _, err := s.RelayIdentity.Endpoint(relay.Address); err != nil {
			return lanlink.TrustedRelay{}, &lanCommandError{"lan_certificate_expired", "saved relay certificate is expired or not yet valid; check the clock, or revoke saved pairs and configure the local relay again"}
		}
	}
	return relay, nil
}

// Loading and revoking private state remains possible after certificate expiry.
// Current validity is checked separately before starting the transport.
func storedLANRelay(s lanState) (lanlink.TrustedRelay, error) {
	if s.Selection == nil {
		return lanlink.TrustedRelay{}, errors.New("select an exact relay endpoint and certificate before connecting LAN")
	}
	ap, err := netip.ParseAddrPort(s.Selection.Address)
	if err != nil {
		return lanlink.TrustedRelay{}, errors.New("explicit numeric relay IP and port required")
	}
	relay := lanlink.TrustedRelay{Address: ap, CertificateSHA256: s.Selection.CertificateSHA256}
	if err := relay.Validate(); err != nil {
		return lanlink.TrustedRelay{}, err
	}
	switch s.Selection.Kind {
	case "relay":
		if s.RelayIdentity != nil {
			return lanlink.TrustedRelay{}, errors.New("external relay must not have a local relay identity")
		}
	case "host":
		if ap.Port() < 1024 || (!ap.Addr().IsPrivate() && !ap.Addr().IsLoopback()) || s.RelayIdentity == nil {
			return lanlink.TrustedRelay{}, errors.New("local relay requires a chosen private IP, high port and saved identity")
		}
		if ap.Port() >= DiscoveryPort && ap.Port() <= lanlink.PairingPort {
			return lanlink.TrustedRelay{}, errors.New("relay port conflicts with a reserved application port")
		}
		cert, err := tls.X509KeyPair(s.RelayIdentity.CertificatePEM, s.RelayIdentity.PrivateKeyPEM)
		if err != nil || len(cert.Certificate) != 1 || s.RelayIdentity.Key.IsZero() {
			return lanlink.TrustedRelay{}, errors.New("invalid saved relay identity")
		}
		parsed, err := x509.ParseCertificate(cert.Certificate[0])
		if err != nil || parsed.VerifyHostname(ap.Addr().String()) != nil {
			return lanlink.TrustedRelay{}, codedLANError(lanlink.ErrRelayMismatch)
		}
		hash := sha256.Sum256(parsed.Raw)
		if hex.EncodeToString(hash[:]) != relay.CertificateSHA256 {
			return lanlink.TrustedRelay{}, codedLANError(lanlink.ErrRelayMismatch)
		}
	default:
		return lanlink.TrustedRelay{}, errors.New("choose relay or host LAN setup")
	}
	return relay, nil
}

func (s *lanStore) copy() lanState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneLANState(s.state)
}

func cloneLANState(s lanState) lanState {
	s.WANCandidates = cloneWANCandidates(s.WANCandidates)
	s.DestinationPolicy.Prefixes = append([]string(nil), s.DestinationPolicy.Prefixes...)
	if s.Selection != nil {
		selection := *s.Selection
		s.Selection = &selection
	}
	if s.RelayIdentity != nil {
		relay := *s.RelayIdentity
		relay.CertificatePEM = append([]byte(nil), relay.CertificatePEM...)
		relay.PrivateKeyPEM = append([]byte(nil), relay.PrivateKeyPEM...)
		s.RelayIdentity = &relay
	}
	s.Trust.Peers = append([]lanlink.Peer(nil), s.Trust.Peers...)
	s.Remotes = lanlink.CloneRemotePeers(s.Remotes)
	s.RouteCandidates = append([]lanlink.RouteCandidate(nil), s.RouteCandidates...)
	return s
}

func (s *lanStore) saveLocked(next lanState) error {
	if s.routeRecovery {
		return config.ErrAtomicRecovery
	}
	limits := s.currentLimits()
	if len(next.Remotes) > len(s.state.Remotes) && int64(len(next.Remotes)) > limits.peers {
		return &lanlink.PeerCapacityError{Limit: limits.peers}
	}
	if err := validateLANState(next); err != nil {
		return err
	}
	data, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	if int64(len(data))+1 > limits.bytes {
		return &lanCommandError{"lan_state_capacity", fmt.Sprintf("private LAN state needs %d bytes; raise the %d-byte lanStateBytes budget before saving", len(data)+1, limits.bytes)}
	}
	write := s.write
	if write == nil {
		write = config.AtomicWrite
	}
	saveErr := write(s.path, append(data, '\n'))
	if atomicPublished(saveErr) {
		s.state = cloneLANState(next)
	}
	if errors.Is(saveErr, config.ErrAtomicCommitted) {
		if next.Version >= 2 {
			s.routeRecovery = true
		}
		return fmt.Errorf("private LAN state was replaced, but durability could not be confirmed; inspect private state before retrying: %w", config.ErrAtomicCommitted)
	}
	if saveErr != nil {
		return privateAtomicError(errors.New("could not save private LAN state; check its directory permissions and available space"), saveErr)
	}
	return nil
}

func (s *lanStore) save(next lanState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked(next)
}

// Persist never calls Node or Book: the transport holds its commit locks.
func (s *lanStore) persist(trust lanlink.Snapshot, remotes []lanlink.RemotePeer) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := cloneLANState(s.state)
	next.Trust, next.Remotes = trust, lanlink.CloneRemotePeers(remotes)
	for _, remote := range remotes {
		if remote.Routes != nil {
			if next.Version < 2 {
				next.Version = 2
			}
			if remote.Routes.Version >= 2 && next.Version < 3 {
				next.Version = 3
			}
		}
	}
	return s.saveLocked(next)
}

func (c *Core) lanStoreCopy() *lanStore {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.lan
}

func (c *Core) ensureLANIdentity() (*lanStore, error) {
	if saved := c.lanStoreCopy(); saved != nil {
		return saved, nil
	}
	s := &lanStore{path: filepath.Join(c.dir, "lan.json"), write: c.writeAtomic}
	s.limits.Store(selectedLANLimits(c.capacityPolicy()))
	initial := lanState{Version: 1, Identity: lanlink.GenerateIdentity(), Trust: lanlink.Snapshot{Version: 1, Peers: []lanlink.Peer{}}, Remotes: []lanlink.RemotePeer{}}
	saveErr := s.save(initial)
	if !atomicPublished(saveErr) {
		return nil, saveErr
	}
	c.mu.Lock()
	c.lan = s
	c.mu.Unlock()
	return s, saveErr
}

func (c *Core) configureLAN(selection *LANSelection) error {
	return c.configureLANWithOptions(selection, false)
}

func (c *Core) configureLANWithOptions(selection *LANSelection, rotateCertificate bool) error {
	return c.configureLANWithPolicy(selection, rotateCertificate, nil)
}

func (c *Core) configureLANWithPolicy(selection *LANSelection, rotateCertificate bool, requestedPolicy *lanpolicy.Config) error {
	canonicalPolicy, err := canonicalLANSetupPolicy(requestedPolicy)
	if err != nil {
		return err
	}
	if rotateCertificate && (selection == nil || selection.Kind != "host" || selection.CertificateSHA256 != "") {
		return &lanCommandError{"lan_certificate_rotation_invalid", "certificate rotation requires an explicitly selected existing local relay"}
	}
	saved := c.lanStoreCopy()
	existingStore := saved != nil
	if saved != nil && saved.routesNeedRecovery() {
		return config.ErrAtomicRecovery
	}
	var existingRelay *lanlink.RelayIdentity
	if selection == nil {
		if saved == nil || saved.copy().Selection == nil {
			return errors.New("select an exact relay or a private local relay address")
		}
		if canonicalPolicy == nil {
			return nil
		}
		chosen := *saved.copy().Selection
		selection = &chosen
	}
	if rotateCertificate && (saved == nil || saved.copy().RelayIdentity == nil) {
		return &lanCommandError{"lan_certificate_rotation_invalid", "certificate rotation requires an explicitly selected existing local relay"}
	}
	if saved != nil {
		state := saved.copy()
		existingRelay = state.RelayIdentity
		if !rotateCertificate && state.Selection != nil && (*state.Selection == *selection || (selection.Kind == "host" && state.Selection.Kind == "host" && selection.Address == state.Selection.Address && selection.CertificateSHA256 == "")) {
			_, err := savedLANRelay(state)
			if err == nil {
				return c.configureExistingLANPolicy(saved, state, canonicalPolicy)
			}
			if selection.Kind == "host" {
				return relayRotationRequired()
			}
			return err
		}
		if c.nodeCopy() != nil {
			return &lanCommandError{"network_restart_required", "stop soba, then start with --offline before changing the selected relay"}
		}
		if len(state.Remotes) != 0 {
			return &lanCommandError{"lan_relay_pairs_present", "revoke current LAN pairs before changing the selected relay or rotating its certificate; pair again and approve application trust separately afterward"}
		}
	}
	ap, err := netip.ParseAddrPort(selection.Address)
	if err != nil || ap.String() != selection.Address {
		return errors.New("explicit numeric relay IP and port required")
	}
	choice := *selection
	var relayIdentity *lanlink.RelayIdentity
	switch choice.Kind {
	case "relay":
		if err := (lanlink.TrustedRelay{Address: ap, CertificateSHA256: choice.CertificateSHA256}).Validate(); err != nil {
			return err
		}
	case "host":
		if choice.CertificateSHA256 != "" || ap.Port() < 1024 || (!ap.Addr().IsPrivate() && !ap.Addr().IsLoopback()) || (ap.Port() >= DiscoveryPort && ap.Port() <= lanlink.PairingPort) {
			return errors.New("choose a private local relay IP and unreserved high port without a supplied certificate")
		}
		c.mu.RLock()
		conflict := c.web != nil && ap.Port() == c.web.Port()
		c.mu.RUnlock()
		if conflict {
			return errors.New("relay port conflicts with the local management port")
		}
		var id lanlink.RelayIdentity
		if existingRelay != nil && !rotateCertificate {
			id = *existingRelay
			if _, err := id.Endpoint(ap); err != nil {
				return relayRotationRequired()
			}
		} else {
			id, err = lanlink.GenerateRelayIdentity(ap.Addr())
			if err != nil {
				return err
			}
		}
		relay, err := id.Endpoint(ap)
		if err != nil {
			return err
		}
		choice.CertificateSHA256, relayIdentity = relay.CertificateSHA256, &id
	default:
		return errors.New("choose relay or host LAN setup")
	}
	if saved == nil {
		saved = &lanStore{path: filepath.Join(c.dir, "lan.json"), write: c.writeAtomic, state: lanState{Version: 1, Identity: lanlink.GenerateIdentity(), Trust: lanlink.Snapshot{Version: 1, Peers: []lanlink.Peer{}}, Remotes: []lanlink.RemotePeer{}}}
		saved.limits.Store(selectedLANLimits(c.capacityPolicy()))
	}
	next := saved.copy()
	next.Selection, next.RelayIdentity = &choice, relayIdentity
	if err := applyLANSetupPolicy(&next, canonicalPolicy); err != nil {
		return err
	}
	saveErr := saved.save(next)
	if saveErr != nil && canonicalPolicy != nil && existingStore {
		saved.requireRouteRecovery()
		saveErr = errors.Join(saveErr, config.ErrAtomicRecovery)
	}
	if !atomicPublished(saveErr) {
		return saveErr
	}
	c.mu.Lock()
	c.lan = saved
	c.mu.Unlock()
	return saveErr
}

type lanNetworkBackend interface {
	NetworkBackend
	PublicKey() string
	IssueInvitation(context.Context, lanlink.Peer, string, time.Duration) (lanlink.Invitation, error)
	CancelInvitation(string)
	PairInvitation(context.Context, lanlink.Invitation) error
	Revoke(string) error
	PublicPeers() []lanlink.PublicPeerSnapshot
}

type lanEngine interface {
	lanlink.DataPlane
	PublicKey() string
	OverlayAddr() netip.Addr
	PublicPeers() []lanlink.PublicPeerSnapshot
	ListenPacket(context.Context, uint16) (net.PacketConn, error)
	RegisterTCPFallback(func(netip.AddrPort, netip.AddrPort) (func(net.Conn), bool)) (func(), error)
	IssueInvitation(context.Context, lanlink.Peer, string, time.Duration) (lanlink.Invitation, error)
	CancelInvitation(string)
	PairInvitation(context.Context, lanlink.Invitation) error
	Revoke(string) error
}

type lanBackend struct {
	lanEngine
	mu        sync.Mutex
	ctx       context.Context
	start     func() (io.Closer, error)
	relay     io.Closer
	ready     bool
	closed    bool
	reserved  []uint16
	routeNode *lanlink.Node
}

func (c *Core) newLANBackend(store *lanStore) (lanNetworkBackend, error) {
	if store.routesNeedRecovery() {
		return nil, config.ErrAtomicRecovery
	}
	state := store.copy()
	relay, err := savedLANRelay(state)
	if err != nil {
		return nil, err
	}
	book := lanlink.NewBookWithPeerLimit(store.peerLimit)
	if err := book.Restore(state.Trust); err != nil {
		return nil, err
	}
	candidates, err := ownLANCandidates(state)
	if err != nil {
		return nil, err
	}
	wan, err := wanTransportConfig(state.WANCandidates)
	if err != nil {
		return nil, err
	}
	resources, err := selectedRelayResources(c.capacityPolicy())
	if err != nil {
		return nil, err
	}
	node, err := lanlink.NewNode(lanlink.NodeConfig{Identity: state.Identity, RelayResources: resources, WANCandidates: wan, DestinationPolicy: state.DestinationPolicy, Relay: relay, Candidates: candidates, Trust: book, Remotes: state.Remotes, Persist: store.persist, EmbeddedRelay: state.Selection.Kind == "host"})
	if err != nil {
		return nil, err
	}
	b := &lanBackend{lanEngine: node, routeNode: node, ctx: c.ctx, reserved: []uint16{lanlink.PairingPort}}
	if state.Selection.Kind == "host" {
		b.reserved = append(b.reserved, relay.Address.Port())
	}
	b.start = func() (io.Closer, error) {
		var local *lanlink.LocalRelay
		if state.Selection.Kind == "host" {
			local, err = lanlink.StartLocalRelayWithResources(c.ctx, relay.Address, *state.RelayIdentity, node.AllowRelayKey, resources, node.AuthorizeRelayBootstrap)
			if err != nil {
				return nil, err
			}
			b.reserved = append(b.reserved, local.ReservedPorts()...)
		}
		if _, err := node.ServePairing(c.ctx); err != nil {
			if local != nil {
				_ = local.Close()
			}
			return nil, err
		}
		if local != nil {
			return local, nil
		}
		return nil, nil
	}
	return b, nil
}

func (b *lanBackend) Start() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return net.ErrClosed
	}
	if b.ready {
		return nil
	}
	if b.start == nil {
		return errors.New("LAN pairing listener is unavailable")
	}
	relay, err := b.start()
	if err != nil {
		return err
	}
	b.relay, b.ready = relay, true
	return nil
}

func (b *lanBackend) State(ctx context.Context) (identity.State, error) {
	if err := ctx.Err(); err != nil {
		return identity.State{}, err
	}
	b.mu.Lock()
	ready, closed := b.ready, b.closed
	reserved := append([]uint16(nil), b.reserved...)
	b.mu.Unlock()
	if closed || b.ctx.Err() != nil {
		return identity.State{}, net.ErrClosed
	}
	state := identity.State{Backend: "starting", IPs: []netip.Addr{b.OverlayAddr()}, ReservedPorts: reserved, Snapshot: policy.Snapshot{Running: ready}}
	if ready {
		state.Backend = "ready"
	}
	for _, peer := range b.PublicPeers() {
		// Endpoint is first for outgoing resolution. The remaining source role
		// addresses only validate authenticated inbound peers; DialIP rejects them.
		ips := []netip.Addr{peer.Endpoint}
		for _, source := range peer.SourceIPs {
			if source != peer.Endpoint {
				ips = append(ips, source)
			}
		}
		state.Snapshot.Peers = append(state.Snapshot.Peers, policy.Peer{ID: peer.Key, DNSName: peer.Key + ".lan.sobalink", IPs: ips})
	}
	return state, nil
}

func (b *lanBackend) Login(context.Context) error {
	return errors.New("LAN uses an explicit device invitation; no account login is needed")
}
func (b *lanBackend) Logout(context.Context) error {
	return errors.New("revoke LAN pairs explicitly, then stop soba")
}
func (b *lanBackend) DialIP(ctx context.Context, network string, ap netip.AddrPort) (net.Conn, error) {
	state, err := b.State(ctx)
	if err != nil {
		return nil, err
	}
	if !state.Snapshot.Running {
		return nil, errors.New("LAN pairing listener is not ready")
	}
	for _, peer := range b.PublicPeers() {
		if ap.Addr() == peer.Endpoint {
			conn, err := b.DialPeer(ctx, peer.Key, network, ap.Port())
			if err != nil {
				return nil, err
			}
			id := peer.Key
			return &exactPeerConn{Conn: conn, id: id, valid: func() bool {
				for _, current := range b.PublicPeers() {
					if current.Key == id {
						return true
					}
				}
				return false
			}}, nil
		}
	}
	return nil, errors.New("destination is not a current paired LAN server endpoint")
}
func (b *lanBackend) inboundPort(network, address, expected string) (uint16, error) {
	if network != expected {
		return 0, errors.New("unsupported LAN listener network")
	}
	ap, err := netip.ParseAddrPort(address)
	if err != nil || ap.Port() == 0 || ap.Addr() != b.OverlayAddr() {
		return 0, errors.New("LAN listener must use this node's exact overlay address")
	}
	state, err := b.State(b.ctx)
	if err != nil || !state.Snapshot.Running {
		return 0, errors.New("LAN pairing listener is not ready")
	}
	return ap.Port(), nil
}
func (b *lanBackend) Listen(network, address string) (net.Listener, error) {
	port, err := b.inboundPort(network, address, "tcp")
	if err != nil {
		return nil, err
	}
	return b.ListenPeer(b.ctx, network, port)
}
func (b *lanBackend) ListenPacket(network, address string) (net.PacketConn, error) {
	port, err := b.inboundPort(network, address, "udp")
	if err != nil {
		return nil, err
	}
	return b.lanEngine.ListenPacket(b.ctx, port)
}
func (b *lanBackend) WhoIs(ctx context.Context, remote netip.AddrPort) (string, error) {
	state, err := b.State(ctx)
	if err != nil || !state.Snapshot.Running {
		return "", errors.New("LAN pairing listener is not ready")
	}
	if !remote.IsValid() || remote.Port() == 0 {
		return "", errors.New("current LAN caller unavailable")
	}
	if key, ok := b.PeerKey(net.TCPAddrFromAddrPort(remote)); ok {
		return key, nil
	}
	return "", errors.New("current authenticated LAN caller unavailable")
}
func (b *lanBackend) Close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed, b.ready = true, false
	relay := b.relay
	b.mu.Unlock()
	err := b.lanEngine.Close()
	if relay != nil {
		err = errors.Join(err, relay.Close())
	}
	return err
}

func (c *Core) lanStatus() map[string]any {
	status := map[string]any{"resources": c.relayResourcesView(), "configured": false, "pairingReady": false, "listenerReady": false, "relayReady": false, "path": "unknown"}
	host := false
	if saved := c.lanStoreCopy(); saved != nil {
		state := saved.copy()
		status["publicKey"] = state.Identity.PublicKey()
		status["policy"] = c.lanPolicyView(state.DestinationPolicy)
		status["routeRecoveryRequired"] = saved.routesNeedRecovery()
		status["configured"] = state.Selection != nil
		if state.Selection != nil {
			status["relay"] = *state.Selection
			status["routeCandidates"] = state.RouteCandidates
			host = state.Selection.Kind == "host"
			if host {
				status["certificate"] = localRelayCertificateStatus(state.RelayIdentity, time.Now())
			}
		}
	}
	if _, ok := c.nodeCopy().(lanNetworkBackend); ok {
		state, err := c.current(c.ctx)
		ready := err == nil && state.Snapshot.Running
		status["pairingReady"], status["listenerReady"] = ready, ready
		status["relayReady"] = host && ready
	}
	return status
}

func (c *Core) lanCommand(ctx context.Context, name string, raw json.RawMessage) (any, error) {
	if name == "lan.inspect" {
		var input struct {
			Invitation string `json:"invitation"`
		}
		if err := decodePayload(raw, &input); err != nil {
			return nil, err
		}
		var invitation lanlink.Invitation
		if len(input.Invitation) > maxLANInvitation || strictLANJSON([]byte(input.Invitation), &invitation) != nil {
			return nil, &lanCommandError{"lan_invitation_invalid", "invitation contents are invalid or expired; request a new invitation"}
		}
		store := c.lanStoreCopy()
		if store == nil {
			return nil, &lanCommandError{"lan_identity_required", "create this device's public identity before reviewing an invitation"}
		}
		publicKey := store.copy().Identity.PublicKey()
		if invitation.RecipientKey != publicKey {
			return nil, &lanCommandError{"lan_invitation_recipient_mismatch", "this invitation is for another public identity; ask the host for an invitation to this device"}
		}
		if err := invitation.ValidateFor(publicKey, time.Now()); err != nil {
			return nil, &lanCommandError{"lan_invitation_invalid", "invitation contents are invalid or expired; request a new invitation"}
		}
		return map[string]any{
			"recipientPublicKey": invitation.RecipientKey, "recipientMatches": true,
			"hostPublicKey": invitation.Host.Peer.Key, "hostName": invitation.Host.Peer.Name, "expires": invitation.Expires,
			"relay": LANSelection{Kind: "relay", Address: invitation.Relay.Address.String(), CertificateSHA256: invitation.Relay.CertificateSHA256},
		}, nil
	}
	if name == "lan.addresses" {
		var input struct{}
		if err := decodePayload(raw, &input); err != nil {
			return nil, err
		}
		read := c.lanAddresses
		if read == nil {
			read = localLANAddresses
		}
		choices, err := read()
		if err != nil {
			return nil, err
		}
		return map[string]any{"addresses": choices}, nil
	}
	if name == "lan.identity" {
		var input struct{}
		if err := decodePayload(raw, &input); err != nil {
			return nil, err
		}
		store, err := c.ensureLANIdentity()
		if err != nil {
			return nil, err
		}
		return map[string]string{"publicKey": store.copy().Identity.PublicKey()}, nil
	}
	if name == "lan.revoke" {
		var input struct {
			PeerID string `json:"peerId"`
		}
		if err := decodePayload(raw, &input); err != nil {
			return nil, err
		}
		if !validLANPublicKey(input.PeerID) {
			return nil, errors.New("valid paired public identity required")
		}
		active := c.nodeCopy()
		if node, ok := active.(lanNetworkBackend); ok {
			return nil, c.revokeLANPeer(node, input.PeerID)
		}
		if active != nil {
			return nil, &lanCommandError{"network_restart_required", "stop soba and start with --offline before changing saved LAN pairs"}
		}
		if store := c.lanStoreCopy(); store != nil {
			return nil, c.revokeLANPeer(offlineLANRevoker{store}, input.PeerID)
		}
		return nil, errors.New("there is no saved LAN identity to revoke")
	}
	node, ok := c.nodeCopy().(lanNetworkBackend)
	if !ok {
		return nil, errors.New("configure and start the selected LAN relay first")
	}
	switch name {
	case "lan.invite":
		var input struct {
			RecipientPublicKey string `json:"recipientPublicKey"`
			Name               string `json:"name"`
			TTLSeconds         int    `json:"ttlSeconds"`
		}
		if err := decodePayload(raw, &input); err != nil {
			return nil, err
		}
		if input.TTLSeconds < 1 || input.TTLSeconds > 600 {
			return nil, errors.New("invitation lifetime must be 1..600 seconds")
		}
		invitation, err := node.IssueInvitation(ctx, lanlink.Peer{Key: input.RecipientPublicKey, Name: input.Name}, c.profileCopy().Settings.Hostname, time.Duration(input.TTLSeconds)*time.Second)
		if err != nil {
			return nil, err
		}
		encoded, err := json.Marshal(invitation)
		if err != nil || len(encoded) > maxLANInvitation {
			node.CancelInvitation(invitation.Token)
			return nil, errors.New("invitation exceeds the exchange limit")
		}
		// This explicit local command is the only public response with a private
		// pairing capability. Status, errors and logs never include this payload.
		return map[string]any{"invitation": string(encoded), "expires": invitation.Expires, "recipientPublicKey": invitation.RecipientKey}, nil
	case "lan.join", "lan.cancel":
		var input struct {
			Invitation string `json:"invitation"`
		}
		if err := decodePayload(raw, &input); err != nil {
			return nil, err
		}
		var invitation lanlink.Invitation
		if len(input.Invitation) > maxLANInvitation || strictLANJSON([]byte(input.Invitation), &invitation) != nil {
			return nil, errors.New("invalid invitation payload")
		}
		if name == "lan.cancel" {
			if invitation.Host.Peer.Key != node.PublicKey() {
				return nil, errors.New("this invitation belongs to another device")
			}
			node.CancelInvitation(invitation.Token)
			return map[string]bool{"cancelled": true}, nil
		}
		pairCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if err := node.PairInvitation(pairCtx, invitation); err != nil {
			if errors.Is(err, config.ErrAtomicRecovery) && !errors.Is(err, lanlink.ErrRemotePairedLocalSave) {
				return nil, privateAtomicError(errors.New("LAN pairing is paused because private state needs recovery; stop this device and inspect its saved approval before restarting or reopening pairing"), err)
			}
			var capacityError *lanlink.PeerCapacityError
			if errors.As(err, &capacityError) && !errors.Is(err, lanlink.ErrRemotePairedLocalSave) {
				return nil, capacityError
			}
			if errors.Is(err, lanlink.ErrRelayMismatch) {
				return nil, codedLANError(err)
			}
			if errors.Is(err, lanlink.ErrCancelInviteFirst) {
				return nil, &lanCommandError{"lan_cancel_invite_first", "cancel the invitation you issued before joining this peer's invitation"}
			}
			if errors.Is(err, lanlink.ErrPairReplyUncertain) {
				return nil, &lanCommandError{"lan_pair_reply_uncertain", "pairing reply was not received; check and revoke any completed pair on the other device before creating a new invitation"}
			}
			if errors.Is(err, lanlink.ErrRemotePairedLocalSave) {
				message := "the other device paired, but the local save was not written; inspect local saved approvals and revoke that pair on the other device before retrying"
				if errors.Is(err, config.ErrAtomicCommitted) {
					message = "the other device paired and local state was replaced, but durability could not be confirmed; pairing is paused; stop this device, inspect its saved approval before restarting, and reconcile or revoke the pair on both devices before retrying"
				}
				return nil, privateAtomicError(&lanCommandError{"lan_remote_paired_local_save", message}, err)
			}
			return nil, errors.New("pairing did not complete; check the invitation, selected relay and both devices")
		}
		return map[string]any{"peerId": invitation.Host.Peer.Key, "paired": true, "trusted": false}, nil
	default:
		return nil, fmt.Errorf("unknown LAN command %q", name)
	}
}

type lanRevoker interface {
	Revoke(string) error
	Close() error
}

func validLANPublicKey(key string) bool {
	if len(key) != 64 || key != strings.ToLower(key) || key == strings.Repeat("0", 64) {
		return false
	}
	_, err := hex.DecodeString(key)
	return err == nil
}

type offlineLANRevoker struct{ store *lanStore }

func (r offlineLANRevoker) Close() error { return nil }
func (r offlineLANRevoker) Revoke(id string) error {
	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	next := cloneLANState(r.store.state)
	peers := next.Trust.Peers[:0]
	for _, peer := range next.Trust.Peers {
		if peer.Key != id {
			peers = append(peers, peer)
		}
	}
	next.Trust.Peers = peers
	remotes := next.Remotes[:0]
	for _, remote := range next.Remotes {
		if remote.Peer.Key != id {
			remotes = append(remotes, remote)
		}
	}
	next.Remotes = remotes
	return r.store.saveLocked(next)
}

func (c *Core) revokeLANPeer(node lanRevoker, id string) error {
	revocationErr := errors.Join(c.retireMixedTransport("lan", id), c.revokeStartupPeer(id))
	// Even a pre-publication journal failure must not retain inbound authority.
	p := c.profileCopy()
	peers := p.Peers[:0]
	removedAppTrust := false
	for _, peer := range p.Peers {
		if peer.ID != id || peer.Network != "lan" {
			peers = append(peers, peer)
		} else {
			removedAppTrust = true
		}
	}
	p.Peers = peers
	// Close authorization before stopping flows: another request cannot reopen
	// an app flow while revocation waits for persistence or transport teardown.
	c.mu.Lock()
	c.profile = p
	delete(c.confirmed, id)
	delete(c.discovered, id)
	c.mu.Unlock()
	if removedAppTrust || revocationErr != nil {
		c.revokePeer(id)
	} else {
		c.stopPeerServices(id)
	}
	profileErr := c.saveProfile(p)
	transportErr := node.Revoke(id)
	if revocationErr != nil || profileErr != nil || transportErr != nil {
		_ = node.Close()
		c.networkReady.Store(false)
		c.stopAllServices()
		c.mu.Lock()
		c.networkState = "error"
		c.networkError = "LAN stopped because durable revocation could not be confirmed; repair private state before restarting"
		c.networkFatal = c.networkError
		c.networkErrorCode = "lan_revoke_not_persisted"
		c.mu.Unlock()
		outcome := &lanCommandError{"lan_revoke_not_persisted", "LAN stopped; durable revocation could not be confirmed. Repair private state before restarting"}
		return privateAtomicError(outcome, errors.Join(revocationErr, profileErr, transportErr))
	}
	return nil
}

// A successfully removed transport pair cannot regain old app trust after a
// partial profile-save failure and later restart/re-pair. This also keeps a
// restored transport snapshot from silently enabling stale autosave approval.
func (c *Core) reconcileLANTrust() error {
	paired := map[string]bool{}
	if saved := c.lanStoreCopy(); saved != nil {
		for _, peer := range saved.copy().Trust.Peers {
			paired[peer.Key] = true
		}
	}
	p := c.profileCopy()
	peers := p.Peers[:0]
	for _, peer := range p.Peers {
		if peer.Network != "lan" || paired[peer.ID] {
			peers = append(peers, peer)
		}
	}
	if len(peers) == len(p.Peers) {
		return nil
	}
	p.Peers = peers
	saveErr := c.saveProfile(p)
	if !atomicPublished(saveErr) {
		return errors.New("could not remove stale LAN application trust; repair private state before restarting")
	}
	c.mu.Lock()
	c.profile = p
	c.mu.Unlock()
	return saveErr
}

var _ NetworkBackend = (*lanBackend)(nil)
