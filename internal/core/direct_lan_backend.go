package core

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sync"

	"github.com/webkaz-labs/sobalink/internal/directlan"
	"github.com/webkaz-labs/sobalink/internal/identity"
	"github.com/webkaz-labs/sobalink/internal/policy"
)

// directLANBackend deliberately does not implement lanNetworkBackend: relay
// invitations and relay identities are incompatible with direct LAN pairing.
type directLANBackend struct {
	*directlan.Node
	mu            sync.Mutex
	ctx           context.Context
	store         *directLANStore
	ready, closed bool
	startAbsent   bool
	resources     directLANRuntimeResources
}

// Applications use the validated userspace WireGuard/gVisor data plane.
// Pairing TLS is a separate, bounded control plane.
const directLANNetstackReady = true

func (c *Core) newDirectLANBackend(s *directLANStore) (NetworkBackend, error) {
	if !directLANNetstackReady {
		return nil, &lanCommandError{"direct_lan_backend_pending", "direct LAN activation is unavailable until the userspace netstack backend is validated"}
	}
	if s.needsRecovery() {
		return nil, codedDirectLANError(directlan.ErrRecovery)
	}
	cfg, err := directLANConfig(s.copy())
	if err != nil {
		return nil, codedDirectLANError(err)
	}
	cfg.Persist = s.persist
	resources := directRuntimeResources(c.capacityPolicy())
	cfg.FlowLimit = resources.Flows
	cfg.ListenerLimit = resources.Listeners
	cfg.InvitationLimit = resources.Invitations
	cfg.PacketQueueLimit = resources.PacketQueue
	cfg.PeerLimitCurrent = s.transportPeerLimit
	n, err := directlan.NewNode(cfg)
	if err != nil {
		return nil, codedDirectLANError(err)
	}
	return &directLANBackend{Node: n, ctx: c.ctx, store: s, resources: resources}, nil
}
func (b *directLANBackend) Start() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return net.ErrClosed
	}
	if b.ready {
		return nil
	}
	if err := b.Node.Start(b.ctx); err != nil {
		if err == directlan.ErrLocalAddressUnavailable {
			b.startAbsent = true
			return &mixedError{"direct_lan_address_unavailable", "The selected direct LAN address is absent; reconnect or reconfigure while stopped, then restart", err}
		}
		if b.ctx.Err() != nil {
			return b.ctx.Err()
		}
		return &lanCommandError{"direct_lan_start_failed", "direct LAN could not bind the selected endpoint; check that the local address is assigned and its port is free"}
	}
	b.startAbsent = false
	b.ready = true
	return nil
}
func (b *directLANBackend) State(ctx context.Context) (identity.State, error) {
	if err := ctx.Err(); err != nil {
		return identity.State{}, err
	}
	b.mu.Lock()
	ready, closed, absent := b.ready, b.closed, b.startAbsent
	b.mu.Unlock()
	if closed || b.ctx.Err() != nil {
		return identity.State{}, net.ErrClosed
	}
	if b.store.needsRecovery() {
		return identity.State{}, codedDirectLANError(directlan.ErrRecovery)
	}
	if ready {
		if err := b.Node.Ready(); errors.Is(err, directlan.ErrLocalAddressUnavailable) {
			ready = false
			absent = true
		} else if err != nil {
			return identity.State{}, codedDirectLANError(err)
		}
	}
	st := identity.State{SelfID: b.PublicKey(), Backend: "starting", IPs: []netip.Addr{b.OverlayAddr()}, ReservedPorts: []uint16{b.Endpoint().Port()}, Snapshot: policy.Snapshot{Running: ready}}
	if ready {
		st.Backend = "ready"
	} else if absent {
		st.Backend = "unavailable"
	}
	for _, peer := range b.Peers() {
		ip, err := directlan.OverlayAddress(peer.Key)
		if err != nil {
			return identity.State{}, err
		}
		st.Snapshot.Peers = append(st.Snapshot.Peers, policy.Peer{ID: peer.Key, DNSName: peer.Key + ".direct-lan.sobalink", IPs: []netip.Addr{ip}})
	}
	return st, nil
}
func (b *directLANBackend) Login(context.Context) error {
	return errors.New("direct LAN uses explicit device invitations; no account login is needed")
}
func (b *directLANBackend) Logout(context.Context) error {
	return errors.New("revoke direct LAN pairs explicitly, then stop soba")
}
func (b *directLANBackend) DialIP(ctx context.Context, network string, ap netip.AddrPort) (net.Conn, error) {
	st, err := b.State(ctx)
	if err != nil {
		return nil, err
	}
	if !st.Snapshot.Running {
		if st.Backend == "unavailable" {
			return nil, directlan.ErrLocalAddressUnavailable
		}
		return nil, directlan.ErrUnavailable
	}
	for _, peer := range st.Snapshot.Peers {
		if len(peer.IPs) == 1 && peer.IPs[0] == ap.Addr() {
			return b.DialPeer(ctx, peer.ID, network, ap.Port())
		}
	}
	return nil, directlan.ErrUntrusted
}
func (b *directLANBackend) inboundPort(network, address, expected string) (uint16, error) {
	ap, err := netip.ParseAddrPort(address)
	if network != expected || err != nil || ap.Addr() != b.OverlayAddr() || ap.Port() == 0 {
		return 0, errors.New("direct LAN listeners require this node's exact virtual address")
	}
	st, err := b.State(b.ctx)
	if err != nil {
		return 0, err
	}
	if !st.Snapshot.Running {
		return 0, directlan.ErrUnavailable
	}
	return ap.Port(), nil
}
func (b *directLANBackend) Listen(network, address string) (net.Listener, error) {
	p, err := b.inboundPort(network, address, "tcp")
	if err != nil {
		return nil, err
	}
	return b.ListenPeer(b.ctx, network, p)
}
func (b *directLANBackend) ListenPacket(network, address string) (net.PacketConn, error) {
	p, err := b.inboundPort(network, address, "udp")
	if err != nil {
		return nil, err
	}
	return b.Node.ListenPacket(b.ctx, p)
}
func (b *directLANBackend) WhoIs(ctx context.Context, remote netip.AddrPort) (string, error) {
	st, err := b.State(ctx)
	if err != nil {
		return "", err
	}
	if !st.Snapshot.Running || !remote.IsValid() || remote.Port() == 0 {
		return "", directlan.ErrUntrusted
	}
	if key, ok := b.PeerKey(net.TCPAddrFromAddrPort(remote)); ok {
		return key, nil
	}
	return "", directlan.ErrUntrusted
}
func (b *directLANBackend) Close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.ready = false
	b.closed = true
	b.mu.Unlock()
	return b.Node.Close()
}

var _ NetworkBackend = (*directLANBackend)(nil)

func (b *directLANBackend) restartRequired() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.startAbsent
}
