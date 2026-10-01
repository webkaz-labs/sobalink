package policy

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sync"
	"time"
)

const liveValidationTimeout = 10 * time.Second

// liveConn pins a socket to the identity and numeric endpoint selected at dial
// time. Hostname reuse and numeric-address reassignment cannot retarget it.
type liveConn struct {
	net.Conn
	policy   *Policy
	network  string
	endpoint netip.AddrPort
	peerID   string
	ctx      context.Context
	cancel   context.CancelFunc
	once     sync.Once
	closeErr error
}

func (c *liveConn) authorized(snapshot Snapshot) error {
	endpoint, peerID, err := c.policy.resolveSnapshot(snapshot, c.network, c.endpoint.String())
	if err != nil || endpoint != c.endpoint || peerID != c.peerID {
		return errors.New("live destination is no longer permitted")
	}
	return nil
}

func (c *liveConn) validate(ctx context.Context) error {
	if c.ctx.Err() != nil {
		return net.ErrClosed
	}
	if c.policy.Source == nil {
		return errors.New("peer information unavailable")
	}
	snapshot, err := c.policy.Source(ctx)
	if err != nil || ctx.Err() != nil {
		return errors.New("peer information unavailable")
	}
	return c.authorized(snapshot)
}

func (c *liveConn) validateDatagram() error {
	ctx, cancel := context.WithTimeout(c.ctx, liveValidationTimeout)
	defer cancel()
	if err := c.validate(ctx); err != nil {
		_ = c.Close()
		return err
	}
	return nil
}

func (c *liveConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if c.network == "udp" && (n > 0 || err == nil) {
		if validateErr := c.validateDatagram(); validateErr != nil {
			// Never expose bytes from a now-revoked destination, including when
			// the underlying socket returned data and an error together.
			clear(b[:n])
			return 0, validateErr
		}
	}
	return n, err
}

func (c *liveConn) Write(b []byte) (int, error) {
	if c.network == "udp" {
		if err := c.validateDatagram(); err != nil {
			return 0, err
		}
	}
	return c.Conn.Write(b)
}

// Preserve half-close through the wrapper; TCP forwarding depends on it.
func (c *liveConn) CloseWrite() error {
	if conn, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return conn.CloseWrite()
	}
	return errors.ErrUnsupported
}

func (c *liveConn) CloseRead() error {
	if conn, ok := c.Conn.(interface{ CloseRead() error }); ok {
		return conn.CloseRead()
	}
	return errors.ErrUnsupported
}

func (c *liveConn) Close() error {
	c.once.Do(func() {
		c.cancel()
		c.closeErr = c.Conn.Close()
		c.policy.mu.Lock()
		delete(c.policy.active, c)
		c.policy.mu.Unlock()
	})
	return c.closeErr
}

// RevalidateActive checks one current peer snapshot against every live socket.
// Any stale identity is closed. If the snapshot cannot be read, all captured
// sockets are closed. TCP uses this health check rather than per-chunk queries;
// UDP additionally verifies every incoming and outgoing datagram.
func (p *Policy) RevalidateActive(ctx context.Context) error {
	p.mu.Lock()
	connections := make([]*liveConn, 0, len(p.active))
	for c := range p.active {
		connections = append(connections, c)
	}
	p.mu.Unlock()
	if len(connections) == 0 {
		return nil
	}
	var snapshot Snapshot
	var err error
	if p.Source == nil {
		err = errors.New("peer information unavailable")
	} else {
		snapshot, err = p.Source(ctx)
	}
	if err != nil || ctx.Err() != nil {
		for _, c := range connections {
			_ = c.Close()
		}
		return errors.New("peer information unavailable; live connections closed")
	}
	var result error
	for _, c := range connections {
		if err := c.authorized(snapshot); err != nil {
			_ = c.Close()
			result = err
		}
	}
	return result
}
