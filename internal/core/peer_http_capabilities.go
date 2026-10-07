package core

import (
	"context"
	"io"
	"net"
	"sync"
	"time"
)

// peerHTTPConn never embeds a net.Conn. Library Close is a signal to the sole
// close worker. That worker retains the physical reservation until Close and
// every admitted borrow have returned. Detachment leaves only an inert wrapper.
type peerHTTPConn struct {
	mu        sync.Mutex
	changed   *sync.Cond
	raw       net.Conn
	authority *peerHTTPAuthority
	closing   bool
	borrows   int
	closeOnce sync.Once
	stop      chan struct{}
	done      chan struct{}
	local     net.Addr
	remote    net.Addr
}

func ownPeerHTTPConn(authority *peerHTTPAuthority, raw net.Conn, requireTerminal bool, release func()) *peerHTTPConn {
	c := &peerHTTPConn{raw: raw, authority: authority, stop: make(chan struct{}), done: make(chan struct{}), local: raw.LocalAddr(), remote: raw.RemoteAddr()}
	c.changed = sync.NewCond(&c.mu)
	go func() {
		<-c.stop
		// No application/owner mutex is held across a close or terminal wait.
		_ = raw.Close()
		terminal, hasTerminal := raw.(interface{ WaitClosed(context.Context) error })
		if requireTerminal && !hasTerminal {
			return // retain the owner and charge; Close alone is not completion
		}
		if hasTerminal {
			if terminal.WaitClosed(context.Background()) != nil {
				// A failed terminal observation cannot free the physical lease.
				// Retain this owner and its references as incomplete cleanup.
				return
			}
		}
		c.mu.Lock()
		for c.borrows != 0 {
			c.changed.Wait()
		}
		c.raw, c.authority, c.local, c.remote = nil, nil, nil, nil
		c.mu.Unlock()
		release()
		close(c.done)
	}()
	return c
}

func (c *peerHTTPConn) borrow() (net.Conn, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closing || c.raw == nil || !c.authority.open() {
		return nil, net.ErrClosed
	}
	c.borrows++
	return c.raw, nil
}

func (c *peerHTTPConn) returned() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	valid := !c.closing && c.authority.open()
	c.borrows--
	if c.borrows == 0 {
		c.changed.Broadcast()
	}
	return valid
}

func (c *peerHTTPConn) Read(p []byte) (int, error) {
	raw, err := c.borrow()
	if err != nil {
		return 0, err
	}
	n, err := raw.Read(p)
	if !c.returned() {
		clear(p[:n])
		return 0, net.ErrClosed
	}
	return n, err
}

func (c *peerHTTPConn) Write(p []byte) (int, error) {
	raw, err := c.borrow()
	if err != nil {
		return 0, err
	}
	n, err := raw.Write(p)
	if !c.returned() {
		return 0, net.ErrClosed
	}
	return n, err
}

func (c *peerHTTPConn) Close() error {
	c.closeOnce.Do(func() {
		c.mu.Lock()
		c.closing = true
		c.mu.Unlock()
		close(c.stop)
	})
	return nil
}

func (c *peerHTTPConn) LocalAddr() net.Addr {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.local
}
func (c *peerHTTPConn) RemoteAddr() net.Addr {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.remote
}
func (c *peerHTTPConn) SetDeadline(t time.Time) error      { return c.deadline(t, 0) }
func (c *peerHTTPConn) SetReadDeadline(t time.Time) error  { return c.deadline(t, 1) }
func (c *peerHTTPConn) SetWriteDeadline(t time.Time) error { return c.deadline(t, 2) }
func (c *peerHTTPConn) deadline(t time.Time, which int) error {
	raw, err := c.borrow()
	if err != nil {
		return err
	}
	switch which {
	case 0:
		err = raw.SetDeadline(t)
	case 1:
		err = raw.SetReadDeadline(t)
	case 2:
		err = raw.SetWriteDeadline(t)
	}
	if !c.returned() {
		return net.ErrClosed
	}
	return err
}

// peerHTTPBody owns exactly one raw source/response body. EOF is not completion;
// Close seals, closes once, joins outstanding reads and detaches before release.
type peerHTTPBody struct {
	mu        sync.Mutex
	changed   *sync.Cond
	raw       io.ReadCloser
	authority *peerHTTPAuthority
	reads     int
	closing   bool
	done      chan struct{}
	release   func()
	err       error
}

func ownPeerHTTPBody(authority *peerHTTPAuthority, raw io.ReadCloser, release func()) *peerHTTPBody {
	b := &peerHTTPBody{raw: raw, authority: authority, done: make(chan struct{}), release: release}
	b.changed = sync.NewCond(&b.mu)
	return b
}

func (b *peerHTTPBody) Read(p []byte) (int, error) {
	b.mu.Lock()
	if b.closing || b.raw == nil || !b.authority.open() {
		b.mu.Unlock()
		return 0, net.ErrClosed
	}
	b.reads++
	raw := b.raw
	b.mu.Unlock()
	n, err := raw.Read(p)
	b.mu.Lock()
	valid := !b.closing && b.authority.open()
	b.reads--
	if b.reads == 0 {
		b.changed.Broadcast()
	}
	b.mu.Unlock()
	if !valid {
		clear(p[:n])
		return 0, net.ErrClosed
	}
	return n, err
}

func (b *peerHTTPBody) Close() error {
	b.mu.Lock()
	if b.closing {
		b.mu.Unlock()
		<-b.done
		return b.err
	}
	b.closing = true
	raw := b.raw
	b.mu.Unlock()
	err := raw.Close()
	b.mu.Lock()
	for b.reads != 0 {
		b.changed.Wait()
	}
	b.raw, b.authority = nil, nil
	release := b.release
	b.release = nil
	b.err = err
	b.mu.Unlock()
	release()
	close(b.done)
	return err
}
