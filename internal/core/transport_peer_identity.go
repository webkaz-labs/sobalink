package core

import (
	"context"
	"github.com/webkaz-labs/sobalink/internal/connectionroute"
	"net"
	"net/netip"
)

func observedBackendPeer(ctx context.Context, node NetworkBackend, c net.Conn) (string, error) {
	if authenticated, ok := c.(interface {
		AuthenticatedPeer(context.Context) (string, error)
	}); ok {
		return authenticated.AuthenticatedPeer(ctx)
	}
	if pinned, ok := c.(interface{ PeerIdentity() (string, bool) }); ok {
		if id, valid := pinned.PeerIdentity(); valid && id != "" {
			return id, nil
		}
		return "", connectionroute.ErrDenied
	}
	ap, e := netip.ParseAddrPort(c.RemoteAddr().String())
	if e != nil {
		return "", connectionroute.ErrDenied
	}
	return node.WhoIs(ctx, ap)
}

// exactPeerConn is created only after an exact-key LAN DialPeer completes its
// encrypted tunnel handshake. It never derives identity from a display or IP.
type exactPeerConn struct {
	net.Conn
	id    string
	valid func() bool
}

func (c *exactPeerConn) PeerIdentity() (string, bool) { return c.id, c.valid != nil && c.valid() }
func (c *exactPeerConn) CloseWrite() error {
	if v, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return v.CloseWrite()
	}
	return connectionroute.ErrDenied
}
func (c *exactPeerConn) ReadFrom(p []byte) (int, net.Addr, error) {
	if v, ok := c.Conn.(net.PacketConn); ok {
		return v.ReadFrom(p)
	}
	return 0, nil, connectionroute.ErrDenied
}
func (c *exactPeerConn) WriteTo(p []byte, to net.Addr) (int, error) {
	if v, ok := c.Conn.(net.PacketConn); ok {
		return v.WriteTo(p, to)
	}
	return 0, connectionroute.ErrDenied
}
