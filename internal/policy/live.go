package policy

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/webkaz-labs/sobalink/internal/transportorigin"
)

const liveValidationTimeout = 10 * time.Second

// liveConn pins a socket to the identity and numeric endpoint selected at dial
// time. Hostname reuse and numeric-address reassignment cannot retarget it.
type liveConn struct {
	net.Conn
	policy    *Policy
	network   string
	endpoint  netip.AddrPort
	peerID    string
	ctx       context.Context
	cancel    context.CancelFunc
	once      sync.Once
	closeInit sync.Once
	closeDone chan struct{}
	closeErr  error
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
	if err := c.authorized(snapshot); err != nil {
		if snapshot.Running && c.policy.OnRevoked != nil {
			c.policy.OnRevoked()
		}
		return err
	}
	return nil
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
	if c.policy.Guard != nil {
		if err := c.policy.Guard(); err != nil {
			_ = c.Close()
			return 0, err
		}
	}
	n, err := c.Conn.Read(b)
	if c.policy.Guard != nil {
		if guardErr := c.policy.Guard(); guardErr != nil {
			clear(b[:n])
			_ = c.Close()
			return 0, guardErr
		}
	}
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
	if c.policy.Guard != nil {
		if err := c.policy.Guard(); err != nil {
			_ = c.Close()
			return 0, err
		}
	}
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

func (c *liveConn) closeCompletion() chan struct{} {
	c.closeInit.Do(func() { c.closeDone = make(chan struct{}) })
	return c.closeDone
}
func (c *liveConn) Close() error {
	done := c.closeCompletion()
	c.once.Do(func() {
		c.cancel()
		c.closeErr = c.Conn.Close()
		c.policy.mu.Lock()
		delete(c.policy.active, c)
		c.policy.mu.Unlock()
		close(done)
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
			if snapshot.Running && p.OnRevoked != nil {
				p.OnRevoked()
			}
			_ = c.Close()
			result = err
		}
	}
	return result
}

func (c *liveConn) TransportOrigin() transportorigin.Origin {
	if carrier, ok := c.Conn.(transportorigin.Carrier); ok {
		return carrier.TransportOrigin()
	}
	return nil
}
func (c *liveConn) WaitClosed(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.closeCompletion():
	}
	if terminal, ok := c.Conn.(interface{ WaitClosed(context.Context) error }); ok {
		return terminal.WaitClosed(ctx)
	}
	if c.TransportOrigin() != nil {
		return transportorigin.ErrMissingOrigin
	}
	if c.closeErr != nil && !errors.Is(c.closeErr, net.ErrClosed) {
		return c.closeErr
	}
	return nil
}
