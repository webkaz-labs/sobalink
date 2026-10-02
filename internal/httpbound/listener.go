// Package httpbound bounds accepted HTTP sockets before header parsing starts.
package httpbound

import (
	"net"
	"sync"
)

type Gate struct{ slots chan struct{} }

func New(limit int) *Gate {
	if limit < 1 {
		panic("positive connection limit required")
	}
	return &Gate{slots: make(chan struct{}, limit)}
}

type listener struct {
	net.Listener
	gate *Gate
}

func (g *Gate) Wrap(l net.Listener) net.Listener { return &listener{Listener: l, gate: g} }
func (l *listener) Accept() (net.Conn, error) {
	for {
		c, e := l.Listener.Accept()
		if e != nil {
			return nil, e
		}
		select {
		case l.gate.slots <- struct{}{}:
			return &conn{Conn: c, release: func() { <-l.gate.slots }}, nil
		default:
			_ = c.Close()
		}
	}
}

type conn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *conn) Close() error { e := c.Conn.Close(); c.once.Do(c.release); return e }
