package directlan

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"
)

// controlStream owns an exact numeric control socket. OS socket Close is safe
// with concurrent calls and wakes them; every such call remains generation work
// until it returns. TLS and cancellation callbacks receive only this wrapper.
type controlStream struct {
	g             *runtimeGeneration
	raw           net.Conn
	local, remote net.Addr
	once          sync.Once
	closeErr      error
}

func newControlStream(g *runtimeGeneration, raw net.Conn) *controlStream {
	return &controlStream{g: g, raw: raw, local: raw.LocalAddr(), remote: raw.RemoteAddr()}
}

// closeUnadmittedControl retains cleanup ownership for a raw dial/accept result
// that never transferred to a wire. The caller retains its work or accept-loop
// reservation until this close returns, including a nonnil result with an error.
func closeUnadmittedControl(g *runtimeGeneration, raw net.Conn, cause error) error {
	if raw == nil {
		return cause
	}
	owned, ok := raw.(*controlStream)
	if !ok {
		owned = newControlStream(g, raw)
	}
	return errors.Join(cause, owned.Close())
}

func (c *controlStream) Read(b []byte) (int, error) {
	w, e := c.g.acquireWork(nil, false)
	if e != nil {
		return 0, net.ErrClosed
	}
	defer w.finish()
	n, e := c.raw.Read(b)
	if !c.g.open() {
		clear(b[:n])
		return 0, net.ErrClosed
	}
	return n, e
}
func (c *controlStream) Write(b []byte) (int, error) {
	w, e := c.g.acquireWork(nil, false)
	if e != nil {
		return 0, net.ErrClosed
	}
	defer w.finish()
	return c.raw.Write(b)
}
func (c *controlStream) Close() error {
	c.once.Do(func() {
		c.closeErr = c.raw.Close()
		if c.closeErr != nil && (c.g.n.contextControl || len(c.g.cfg.PairContexts) > 0) {
			c.g.mu.Lock()
			c.g.failedControl[c] = c.closeErr
			c.g.controlCount++ // retained failure remains charged after work returns
			c.g.mu.Unlock()
			// Sealing may enter the WG owner; never hold the generation lock.
			c.g.requestStop(errors.Join(ErrRecovery, c.closeErr))
		}
	})
	return c.closeErr
}
func (c *controlStream) LocalAddr() net.Addr  { return c.local }
func (c *controlStream) RemoteAddr() net.Addr { return c.remote }
func (c *controlStream) SetDeadline(t time.Time) error {
	w, e := c.g.acquireWork(nil, false)
	if e != nil {
		return net.ErrClosed
	}
	defer w.finish()
	return c.raw.SetDeadline(t)
}
func (c *controlStream) SetReadDeadline(t time.Time) error {
	w, e := c.g.acquireWork(nil, false)
	if e != nil {
		return net.ErrClosed
	}
	defer w.finish()
	return c.raw.SetReadDeadline(t)
}
func (c *controlStream) SetWriteDeadline(t time.Time) error {
	w, e := c.g.acquireWork(nil, false)
	if e != nil {
		return net.ErrClosed
	}
	defer w.finish()
	return c.raw.SetWriteDeadline(t)
}

// watchConnection returns a joined stop function. A false AfterFunc stop result
// means the callback may still be running; it is not cleanup completion.
func watchConnection(ctx context.Context, c net.Conn) func() {
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { _ = c.Close(); close(done) })
	return func() {
		if !stop() {
			<-done
		}
	}
}
