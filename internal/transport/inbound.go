package transport

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"time"
)

// SourceAuthorizer must check a pinned current peer identity, not just membership
// of the tailnet. It must honor cancellation. Inbound listeners come exclusively
// from the embedded identity backend; the OS is used only for the exact local
// numeric service target below.
type SourceAuthorizer func(context.Context, netip.AddrPort) error

func loopbackTarget(target string) (string, error) {
	ap, e := netip.ParseAddrPort(target)
	if e != nil || ap.Port() == 0 || ap.Addr().Zone() != "" || (ap.Addr() != netip.MustParseAddr("127.0.0.1") && ap.Addr() != netip.IPv6Loopback()) {
		return "", errors.New("service target must be 127.0.0.1 or ::1 with port 1..65535")
	}
	if ap.Addr().Is4() {
		return "4", nil
	}
	return "6", nil
}
func sourceAddress(addr net.Addr) (netip.AddrPort, error) {
	if addr == nil {
		return netip.AddrPort{}, errors.New("source unavailable")
	}
	return netip.ParseAddrPort(addr.String())
}
func authorizeInbound(ctx context.Context, source netip.AddrPort, authorize SourceAuthorizer, guard func() error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if guard == nil || authorize == nil {
		return errors.New("source authorization and lifetime guard required")
	}
	if e := guard(); e != nil {
		return e
	}
	check, cancel := context.WithTimeout(ctx, defaultDialTimeout)
	defer cancel()
	if e := authorize(check, source); e != nil {
		return e
	}
	if e := check.Err(); e != nil {
		return e
	}
	return guard()
}

// StartInboundTCP accepts an already-created tailnet listener. It never creates
// an OS listener or dials a non-loopback target.
func StartInboundTCP(ctx context.Context, cfg TCPConfig, l net.Listener, authorize SourceAuthorizer, guard func() error) (*Server, error) {
	family, e := loopbackTarget(cfg.Target)
	if e != nil {
		return nil, e
	}
	if l == nil || authorize == nil || guard == nil {
		return nil, errors.New("listener and authorization required")
	}
	timeout, e := normalizeTimeout(cfg.DialTimeout)
	if e != nil {
		return nil, e
	}
	return startServer(ctx, l, l.Addr(), func(s *Server) {
		acceptConnections(s, l, func(client net.Conn) {
			source, e := sourceAddress(client.RemoteAddr())
			if e != nil || authorizeInbound(s.ctx, source, authorize, guard) != nil {
				return
			}
			dial := func(c context.Context, _, address string) (net.Conn, error) {
				if address != cfg.Target {
					return nil, errors.New("target changed")
				}
				if e := authorizeInbound(c, source, authorize, guard); e != nil {
					return nil, e
				}
				return (&net.Dialer{}).DialContext(c, "tcp"+family, cfg.Target)
			}
			dialCtx, cancel := context.WithTimeout(s.ctx, timeout)
			remote, e := dialTracked(s, dialCtx, dial, "tcp", cfg.Target)
			cancel()
			if e != nil {
				return
			}
			defer s.release(remote)
			flowCtx, stop := context.WithCancel(s.ctx)
			defer stop()
			validate := func() error { return authorizeInbound(flowCtx, source, authorize, guard) }
			if e := validate(); e != nil {
				return
			}
			watched := make(chan struct{})
			go func() {
				defer close(watched)
				ticker := time.NewTicker(time.Second)
				defer ticker.Stop()
				for {
					select {
					case <-flowCtx.Done():
						return
					case <-ticker.C:
						if validate() != nil {
							client.Close()
							remote.Close()
							return
						}
					}
				}
			}()
			bridge(&inboundConn{Conn: client, validate: validate}, remote)
			stop()
			<-watched
		}, cfg)
	}), nil
}

type inboundConn struct {
	net.Conn
	validate func() error
}

func (c *inboundConn) Read(b []byte) (int, error) {
	if e := c.validate(); e != nil {
		c.Close()
		return 0, e
	}
	n, e := c.Conn.Read(b)
	if x := c.validate(); x != nil {
		clear(b[:n])
		c.Close()
		return 0, x
	}
	return n, e
}
func (c *inboundConn) Write(b []byte) (int, error) {
	if e := c.validate(); e != nil {
		c.Close()
		return 0, e
	}
	return c.Conn.Write(b)
}
func (c *inboundConn) CloseWrite() error {
	if v, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return v.CloseWrite()
	}
	return errors.ErrUnsupported
}

// packetAdapter deliberately parses numeric source endpoints without DNS.
type packetAdapter struct{ net.PacketConn }

func (p packetAdapter) ReadFromUDPAddrPort(b []byte) (int, netip.AddrPort, error) {
	n, a, e := p.ReadFrom(b)
	if e != nil {
		return 0, netip.AddrPort{}, e
	}
	ap, e := sourceAddress(a)
	return n, ap, e
}
func (p packetAdapter) WriteToUDPAddrPort(b []byte, a netip.AddrPort) (int, error) {
	return p.WriteTo(b, net.UDPAddrFromAddrPort(a))
}

func StartInboundUDP(ctx context.Context, cfg UDPConfig, packet net.PacketConn, authorize SourceAuthorizer, guard func() error) (*Server, error) {
	family, e := loopbackTarget(cfg.Target)
	if e != nil {
		return nil, e
	}
	if packet == nil || authorize == nil || guard == nil {
		return nil, errors.New("packet listener and authorization required")
	}
	if e := normalizeUDPConfig(&cfg); e != nil {
		return nil, e
	}
	dial := func(c context.Context, network, address string) (net.Conn, error) {
		if network != "udp" || address != cfg.Target {
			return nil, errors.New("target changed")
		}
		if e := guard(); e != nil {
			return nil, e
		}
		return (&net.Dialer{}).DialContext(c, "udp"+family, cfg.Target)
	}
	return startServer(ctx, packet, packet.LocalAddr(), func(s *Server) {
		table := &udpTable{sessions: make(map[netip.AddrPort]*udpSession), server: s, local: packetAdapter{packet}, cfg: cfg, dial: dial, authorize: authorize, guard: guard}
		table.readLocal()
	}), nil
}
