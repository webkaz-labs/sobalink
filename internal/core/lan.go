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
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/identity"
	"github.com/webkaz-labs/sobalink/internal/lanlink"
	"github.com/webkaz-labs/sobalink/internal/policy"
)

const maxLANState = 2 << 20
const maxLANInvitation = 64 << 10

type lanCommandError struct{ code, message string }

func (e *lanCommandError) Error() string     { return e.message }
func (e *lanCommandError) ErrorCode() string { return e.code }

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
	Version       int                    `json:"version"`
	Identity      lanlink.Identity       `json:"identity"`
	Selection     *LANSelection          `json:"selection,omitempty"`
	RelayIdentity *lanlink.RelayIdentity `json:"relayIdentity,omitempty"`
	Trust         lanlink.Snapshot       `json:"trust"`
	Remotes       []lanlink.RemotePeer   `json:"remotes"`
}

type lanStore struct {
	mu    sync.Mutex
	path  string
	state lanState
}

func readLANStore(path string) (*lanStore, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > maxLANState {
		return nil, errors.New("private LAN state must be a regular file within the 2 MiB limit")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, errors.New("private LAN state changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxLANState+1))
	if err != nil || len(data) > maxLANState {
		return nil, errors.New("private LAN state exceeds the 2 MiB limit")
	}
	var state lanState
	if err := strictLANJSON(data, &state); err != nil {
		return nil, errors.New("invalid private LAN state")
	}
	if err := validateLANState(state); err != nil {
		return nil, err
	}
	return &lanStore{path: path, state: state}, nil
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

func validateLANState(s lanState) error {
	if s.Version != 1 || s.Identity.Validate() != nil || len(s.Remotes) > 128 {
		return errors.New("invalid private LAN state")
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
			return lanlink.TrustedRelay{}, errors.New("saved relay certificate is expired or not yet valid; revoke saved pairs and configure the local relay again")
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
			return lanlink.TrustedRelay{}, errors.New("saved relay certificate does not match the chosen address")
		}
		hash := sha256.Sum256(parsed.Raw)
		if hex.EncodeToString(hash[:]) != relay.CertificateSHA256 {
			return lanlink.TrustedRelay{}, errors.New("saved relay identity does not match the selected certificate")
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
	s.Remotes = append([]lanlink.RemotePeer(nil), s.Remotes...)
	return s
}

func (s *lanStore) saveLocked(next lanState) error {
	if err := validateLANState(next); err != nil {
		return err
	}
	data, err := json.MarshalIndent(next, "", "  ")
	if err != nil || len(data)+1 > maxLANState {
		return errors.New("private LAN state exceeds the 2 MiB limit")
	}
	if err := config.AtomicWrite(s.path, append(data, '\n')); err != nil {
		return errors.New("could not save private LAN state; check its directory permissions and available space")
	}
	s.state = cloneLANState(next)
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
	next.Trust, next.Remotes = trust, remotes
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
	s := &lanStore{path: filepath.Join(c.dir, "lan.json")}
	initial := lanState{Version: 1, Identity: lanlink.GenerateIdentity(), Trust: lanlink.Snapshot{Version: 1, Peers: []lanlink.Peer{}}, Remotes: []lanlink.RemotePeer{}}
	if err := s.save(initial); err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.lan = s
	c.mu.Unlock()
	return s, nil
}

func (c *Core) configureLAN(selection *LANSelection) error {
	saved := c.lanStoreCopy()
	if selection == nil {
		if saved == nil || saved.copy().Selection == nil {
			return errors.New("select an exact relay or a private local relay address")
		}
		return nil
	}
	if saved != nil {
		state := saved.copy()
		if state.Selection != nil && (*state.Selection == *selection || (selection.Kind == "host" && state.Selection.Kind == "host" && selection.Address == state.Selection.Address && selection.CertificateSHA256 == "")) {
			_, err := savedLANRelay(state)
			if err == nil {
				return nil
			}
			// An explicit repeat setup may rotate an unusable local certificate
			// once every old pair has been removed and no engine is active.
			if selection.Kind != "host" || selection.CertificateSHA256 != "" {
				return err
			}
		}
		if c.nodeCopy() != nil {
			return errors.New("restart soba before changing the selected relay")
		}
		if len(state.Remotes) != 0 {
			return errors.New("revoke current LAN pairs before changing the selected relay")
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
		id, err := lanlink.GenerateRelayIdentity(ap.Addr())
		if err != nil {
			return err
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
		saved = &lanStore{path: filepath.Join(c.dir, "lan.json"), state: lanState{Version: 1, Identity: lanlink.GenerateIdentity(), Trust: lanlink.Snapshot{Version: 1, Peers: []lanlink.Peer{}}, Remotes: []lanlink.RemotePeer{}}}
	}
	next := saved.copy()
	next.Selection, next.RelayIdentity = &choice, relayIdentity
	if err := saved.save(next); err != nil {
		return err
	}
	c.mu.Lock()
	c.lan = saved
	c.mu.Unlock()
	return nil
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
	mu       sync.Mutex
	ctx      context.Context
	start    func() (io.Closer, error)
	relay    io.Closer
	ready    bool
	closed   bool
	reserved []uint16
}

func (c *Core) newLANBackend(store *lanStore) (lanNetworkBackend, error) {
	state := store.copy()
	relay, err := savedLANRelay(state)
	if err != nil {
		return nil, err
	}
	book := lanlink.NewBook()
	if err := book.Restore(state.Trust); err != nil {
		return nil, err
	}
	node, err := lanlink.NewNode(lanlink.NodeConfig{Identity: state.Identity, Relay: relay, Trust: book, Remotes: state.Remotes, Persist: store.persist, EmbeddedRelay: state.Selection.Kind == "host"})
	if err != nil {
		return nil, err
	}
	b := &lanBackend{lanEngine: node, ctx: c.ctx, reserved: []uint16{lanlink.PairingPort}}
	if state.Selection.Kind == "host" {
		b.reserved = append(b.reserved, relay.Address.Port())
	}
	b.start = func() (io.Closer, error) {
		var local *lanlink.LocalRelay
		if state.Selection.Kind == "host" {
			local, err = lanlink.StartLocalRelay(c.ctx, relay.Address, *state.RelayIdentity, node.AllowRelayKey, node.AuthorizeRelayBootstrap)
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
			return b.DialPeer(ctx, peer.Key, network, ap.Port())
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
	status := map[string]any{"configured": false, "pairingReady": false, "path": "unknown"}
	if saved := c.lanStoreCopy(); saved != nil {
		state := saved.copy()
		status["publicKey"] = state.Identity.PublicKey()
		status["configured"] = state.Selection != nil
		if state.Selection != nil {
			status["relay"] = *state.Selection
		}
	}
	if node, ok := c.nodeCopy().(lanNetworkBackend); ok {
		state, err := node.State(c.ctx)
		status["pairingReady"] = err == nil && state.Snapshot.Running
	}
	return status
}

func (c *Core) lanCommand(ctx context.Context, name string, raw json.RawMessage) (any, error) {
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
			return nil, errors.New("stop soba and start with --offline before changing saved LAN pairs")
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
			if errors.Is(err, lanlink.ErrCancelInviteFirst) {
				return nil, &lanCommandError{"lan_cancel_invite_first", "cancel the invitation you issued before joining this peer's invitation"}
			}
			if errors.Is(err, lanlink.ErrPairReplyUncertain) {
				return nil, &lanCommandError{"lan_pair_reply_uncertain", "pairing reply was not received; check and revoke any completed pair on the other device before creating a new invitation"}
			}
			if errors.Is(err, lanlink.ErrRemotePairedLocalSave) {
				return nil, &lanCommandError{"lan_remote_paired_local_save", "the other device paired, but the local save failed; revoke that pair on the other device before retrying"}
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
	if removedAppTrust {
		c.revokePeer(id)
	} else {
		c.stopPeerServices(id)
	}
	profileErr := c.saveProfile(p)
	transportErr := node.Revoke(id)
	if profileErr != nil || transportErr != nil {
		_ = node.Close()
		c.networkReady.Store(false)
		c.stopAllServices()
		c.mu.Lock()
		c.networkState = "error"
		c.networkError = "LAN stopped because durable revocation could not be confirmed; repair private state before restarting"
		c.networkFatal = c.networkError
		c.mu.Unlock()
		return &lanCommandError{"lan_revoke_not_persisted", "LAN stopped; durable revocation could not be confirmed. Repair private state before restarting"}
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
	if err := c.saveProfile(p); err != nil {
		return errors.New("could not remove stale LAN application trust; repair private state before restarting")
	}
	c.mu.Lock()
	c.profile = p
	c.mu.Unlock()
	return nil
}

var _ NetworkBackend = (*lanBackend)(nil)
