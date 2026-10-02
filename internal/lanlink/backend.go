package lanlink

import (
	"context"
	"net"
)

// DataPlane provides application traffic through a userspace netstack.
// Implementations must never fall back to OS routing or DNS for application
// traffic. The key is an exact approved public key, not a mutable display name.
// Node implements explicit trusted-relay mode. It is not a strict LAN-only
// underlay sandbox; direct peer paths and selected-relay diagnostics are allowed.
type DataPlane interface {
	DialPeer(ctx context.Context, peerKey, network string, port uint16) (net.Conn, error)
	ListenPeer(ctx context.Context, network string, port uint16) (net.Listener, error)
	DialPacketPeer(ctx context.Context, peerKey string, port uint16) (ConnPacketConn, error)
	PeerKey(remote net.Addr) (string, bool)
	Close() error
}

// Unavailable prevents configuration or pairing success being misreported as a
// working LAN data plane. It performs no network I/O.
type Unavailable struct{}

func (Unavailable) DialPeer(ctx context.Context, peerKey, network string, port uint16) (net.Conn, error) {
	return nil, ErrBackendUnavailable
}
func (Unavailable) ListenPeer(ctx context.Context, network string, port uint16) (net.Listener, error) {
	return nil, ErrBackendUnavailable
}
func (Unavailable) Close() error { return nil }

// ConnPacketConn preserves datagram boundaries for connected UDP flows.
type ConnPacketConn interface {
	net.Conn
	net.PacketConn
}

func (Unavailable) DialPacketPeer(ctx context.Context, peerKey string, port uint16) (ConnPacketConn, error) {
	return nil, ErrBackendUnavailable
}
func (Unavailable) PeerKey(remote net.Addr) (string, bool) { return "", false }
