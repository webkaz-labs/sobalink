//go:build directlan_integration

package directlan

import (
	"context"
	"io"
	"net"
	"testing"
	"time"
)

func TestNativeSimultaneousColdSession(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "::1"} {
		t.Run(ip, func(t *testing.T) {
			a, _ := nativeNodeOnIP(t, 66, ip)
			b, _ := nativeNodeOnIP(t, 67, ip)
			pairNative(t, a, b)
			listeners := make([]net.Listener, 2)
			for i, n := range []*Node{a, b} {
				var e error
				listeners[i], e = n.ListenPeer(context.Background(), "tcp", 42020)
				if e != nil {
					t.Fatal(e)
				}
				defer listeners[i].Close()
			}
			accepted := make(chan error, 2)
			for _, ln := range listeners {
				go func(ln net.Listener) {
					c, e := ln.Accept()
					if e == nil {
						defer c.Close()
						c.SetDeadline(time.Now().Add(5 * time.Second))
						_, e = io.CopyN(c, c, 1)
					}
					accepted <- e
				}(ln)
			}
			start := make(chan struct{})
			done := make(chan error, 2)
			for i, n := range []*Node{a, b} {
				target := []*Node{b, a}[i]
				go func(n, target *Node) {
					<-start
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					c, e := n.DialPeer(ctx, target.PublicKey(), "tcp", 42020)
					if e == nil {
						defer c.Close()
						c.SetDeadline(time.Now().Add(5 * time.Second))
						_, e = c.Write([]byte{77})
						if e == nil {
							var data [1]byte
							_, e = io.ReadFull(c, data[:])
							if e == nil && data[0] != 77 {
								e = ErrIdentity
							}
						}
					}
					done <- e
				}(n, target)
			}
			close(start)
			for range 2 {
				if e := <-done; e != nil {
					t.Fatal(e)
				}
				if e := <-accepted; e != nil {
					t.Fatal(e)
				}
			}
		})
	}
}
func TestNativeSessionControlWithOneFlowSlot(t *testing.T) {
	a, _ := nativeConfiguredNode(t, 68, "127.0.0.1", func(c *Config) { c.FlowLimit = 1; c.ControlLimit = 1 })
	b, _ := nativeConfiguredNode(t, 69, "127.0.0.1", func(c *Config) { c.FlowLimit = 1; c.ControlLimit = 1 })
	pairNative(t, a, b)
	if a.PublicKey() > b.PublicKey() {
		a, b = b, a
	}
	ln, e := a.ListenPeer(context.Background(), "tcp", 42021)
	if e != nil {
		t.Fatal(e)
	}
	defer ln.Close()
	accepted := make(chan net.Conn, 1)
	go func() { c, _ := ln.Accept(); accepted <- c }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// The higher key needs the authenticated reverse-control request while its
	// sole application slot is reserved. It must not be counted twice.
	c, e := b.DialPeer(ctx, a.PublicKey(), "tcp", 42021)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	select {
	case remote := <-accepted:
		if remote == nil {
			t.Fatal("no accepted flow")
		}
		remote.Close()
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func TestNativeImmediateResponderRestart(t *testing.T) {
	a, _ := nativeNode(t, 70)
	b, _ := nativeNode(t, 71)
	pairNative(t, a, b)
	if a.PublicKey() > b.PublicKey() {
		a, b = b, a
	}
	ln, e := a.ListenPeer(context.Background(), "tcp", 42022)
	if e != nil {
		t.Fatal(e)
	}
	defer ln.Close()
	accepted := make(chan error, 2)
	go func() {
		for range 2 {
			c, e := ln.Accept()
			if e == nil {
				c.SetDeadline(time.Now().Add(8 * time.Second))
				_, e = io.CopyN(c, c, 1)
				c.Close()
			}
			accepted <- e
		}
	}()
	dial := func(n *Node) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		c, e := n.DialPeer(ctx, a.PublicKey(), "tcp", 42022)
		if e != nil {
			t.Fatal(e)
		}
		defer c.Close()
		c.SetDeadline(time.Now().Add(8 * time.Second))
		if _, e = c.Write([]byte{12}); e != nil {
			t.Fatal(e)
		}
		var buf [1]byte
		if _, e = io.ReadFull(c, buf[:]); e != nil || buf[0] != 12 {
			t.Fatal("echo", e)
		}
		if e = <-accepted; e != nil {
			t.Fatal(e)
		}
	}
	dial(b)
	cfg := b.cfg
	cfg.Peers = b.Peers()
	b.Close()
	reopened, e := NewNode(cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	if e = reopened.Start(context.Background()); e != nil {
		t.Fatal(e)
	}
	// Do not sleep through the engine rate limit: exercise an immediate restart
	// while the initiator still remembers the old established session.
	dial(reopened)
}

func TestNativeSessionControlRejectsWrongRoleAndUnpaired(t *testing.T) {
	a, _ := nativeNode(t, 78)
	b, _ := nativeNode(t, 79)
	pairNative(t, a, b)
	if a.PublicKey() > b.PublicKey() {
		a, b = b, a
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	a.mu.Lock()
	wrong := a.peers[b.PublicKey()]
	a.mu.Unlock()
	if e := a.requestSession(ctx, wrong); e == nil {
		t.Fatal("responder accepted initiator-only control")
	}
	outsider, _ := nativeNode(t, 80)
	target := Peer{Key: a.PublicKey(), Endpoint: a.Endpoint(), TunnelKey: a.cfg.Identity.TunnelKey()}
	c, w, e := outsider.connect(ctx, target, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer outsider.removeWire(w)
	if e = writeJSON(c, request{Version: 1, Operation: "session"}); e != nil {
		t.Fatal(e)
	}
	var r response
	if e = readJSON(c, &r); e == nil && r.OK {
		t.Fatal("unpaired authenticated identity woke session")
	}
	b.mu.Lock()
	paired := b.peers[a.PublicKey()]
	b.mu.Unlock()
	c2, w2, e := b.connect(ctx, paired.peer, paired)
	if e != nil {
		t.Fatal(e)
	}
	defer b.removeWire(w2)
	if e = writeJSON(c2, request{Version: 1, Operation: "session", Network: "tcp", Port: 42030}); e != nil {
		t.Fatal(e)
	}
	if e = readJSON(c2, &r); e == nil && r.OK {
		t.Fatal("application fields accepted on control")
	}
}
