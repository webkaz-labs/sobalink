//go:build directlan_integration && directlan_lifecycle

package directlan

import (
	"bytes"
	"context"
	"encoding/hex"
	"io"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/tailscale/wireguard-go/device"
)

// These minute-scale tests are a separate explicit native gate. Do not include
// them in the short adapter repeat-count gate or shorten protocol timers.
func lifecyclePair(t *testing.T, seed byte) (*Node, *Node) {
	t.Helper()
	a, _ := nativeNode(t, seed)
	b, _ := nativeNode(t, seed+1)
	pairNative(t, a, b)
	for _, n := range []*Node{a, b} {
		ln, e := n.ListenPeer(context.Background(), "tcp", 42030)
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() { ln.Close() })
		go func() {
			for {
				c, e := ln.Accept()
				if e != nil {
					return
				}
				go func() { defer c.Close(); io.Copy(c, c) }()
			}
		}()
		udp, e := n.ListenPeer(context.Background(), "udp", 42031)
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() { udp.Close() })
		go func() {
			for {
				c, e := udp.Accept()
				if e != nil {
					return
				}
				go func() {
					defer c.Close()
					buf := make([]byte, MaxDatagram)
					for {
						size, e := c.Read(buf)
						if e != nil {
							return
						}
						if _, e = c.Write(buf[:size]); e != nil {
							return
						}
					}
				}()
			}
		}()
	}
	return a, b
}
func lifecycleDials(t *testing.T, a, b *Node) []net.Conn {
	t.Helper()
	type result struct {
		c net.Conn
		e error
	}
	done := make(chan result, 4)
	start := make(chan struct{})
	for _, network := range []string{"tcp", "udp"} {
		for i, n := range []*Node{a, b} {
			target := []*Node{b, a}[i]
			go func(n, target *Node, network string) {
				<-start
				ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
				defer cancel()
				port := uint16(42030)
				if network == "udp" {
					port = 42031
				}
				c, e := n.DialPeer(ctx, target.PublicKey(), network, port)
				done <- result{c, e}
			}(n, target, network)
		}
	}
	close(start)
	var out []net.Conn
	for range 4 {
		r := <-done
		if r.e != nil {
			for _, c := range out {
				c.Close()
			}
			t.Fatal(r.e)
		}
		out = append(out, r.c)
		t.Cleanup(func() { r.c.Close() })
	}
	return out
}
func lifecycleExchange(t *testing.T, cs []net.Conn) {
	t.Helper()
	done := make(chan error, len(cs))
	for _, c := range cs {
		go func(c net.Conn) {
			c.SetDeadline(time.Now().Add(8 * time.Second))
			_, e := c.Write([]byte{81})
			if e == nil {
				var data [1]byte
				_, e = io.ReadFull(c, data[:])
				if e == nil && data[0] != 81 {
					e = ErrIdentity
				}
			}
			done <- e
		}(c)
	}
	for range cs {
		if e := <-done; e != nil {
			t.Fatal(e)
		}
	}
}

// Only numeric timestamps survive this writer. Never retain or log raw IPC
// output, which also contains keys; the fixture has exactly one synthetic peer.
type handshakeTimeWriter struct {
	sec, nsec int64
	fields    int
}

func (w *handshakeTimeWriter) Write(p []byte) (int, error) {
	for _, line := range bytes.Split(p, []byte{'\n'}) {
		for _, field := range []string{"last_handshake_time_sec=", "last_handshake_time_nsec="} {
			if !bytes.HasPrefix(line, []byte(field)) {
				continue
			}
			value, e := strconv.ParseInt(string(line[len(field):]), 10, 64)
			if e != nil {
				return 0, ErrUnavailable
			}
			if field == "last_handshake_time_sec=" {
				w.sec = value
			} else {
				w.nsec = value
			}
			w.fields++
		}
	}
	return len(p), nil
}
func lastHandshake(t *testing.T, n *Node) time.Time {
	t.Helper()
	var w handshakeTimeWriter
	if n.engine.IpcGetOperation(&w) != nil || w.fields != 2 || w.sec == 0 {
		t.Fatal("no single completed fixture handshake")
	}
	return time.Unix(w.sec, w.nsec)
}
func TestNativeSessionLifecycle(t *testing.T) {
	t.Run("synthetic-expiry-rekey", func(t *testing.T) {
		t.Parallel()
		a, b := lifecyclePair(t, 72)
		cs := lifecycleDials(t, a, b)
		lifecycleExchange(t, cs)
		for i, n := range []*Node{a, b} {
			target := []*Node{b, a}[i]
			n.mu.Lock()
			p := n.peers[target.PublicKey()]
			p.enginePeer.ExpireCurrentKeypairs()
			n.mu.Unlock()
			if p.session.ready() {
				t.Fatal("expired key remained ready")
			}
		}
		fresh := lifecycleDials(t, a, b)
		lifecycleExchange(t, fresh)
		for i, n := range []*Node{a, b} {
			target := []*Node{b, a}[i]
			n.mu.Lock()
			p := n.peers[target.PublicKey()]
			p.enginePeer.ZeroAndFlushAll()
			n.mu.Unlock()
			if p.session.ready() {
				t.Fatal("flushed key remained ready")
			}
		}
		renewed := lifecycleDials(t, a, b)
		lifecycleExchange(t, renewed)
		if a.PublicKey() > b.PublicKey() {
			a, b = b, a
		}
		raw, _ := hex.DecodeString(b.cfg.Identity.TunnelKey())
		var key device.NoisePublicKey
		copy(key[:], raw)
		before := lastHandshake(t, a)
		a.engine.ScheduleHandshakeOnUserSend(key)
		until := time.Now().Add(device.RekeyTimeout + time.Second)
		for time.Now().Before(until) {
			lifecycleExchange(t, renewed)
			time.Sleep(250 * time.Millisecond)
		}
		if !lastHandshake(t, a).After(before) {
			t.Fatal("scheduled rekey did not complete")
		}
	})
	t.Run("active-through-natural-rekey", func(t *testing.T) {
		t.Parallel()
		a, b := lifecyclePair(t, 74)
		cs := lifecycleDials(t, a, b)
		lifecycleExchange(t, cs)
		beforeA, beforeB := lastHandshake(t, a), lastHandshake(t, b)
		start := time.Now()
		count := 0
		for time.Since(start) < device.RekeyAfterTime+5*time.Second {
			lifecycleExchange(t, cs)
			count++
			time.Sleep(500 * time.Millisecond)
		}
		lifecycleExchange(t, cs)
		if !lastHandshake(t, a).After(beforeA) || !lastHandshake(t, b).After(beforeB) {
			t.Fatal("natural rekey did not complete on both peers")
		}
		t.Logf("bidirectional TCP+UDP: %d rounds across %s; later handshake completed on both peers", count, time.Since(start))
	})
	t.Run("idle-through-natural-expiry-new-dial", func(t *testing.T) {
		t.Parallel()
		a, b := lifecyclePair(t, 76)
		cs := lifecycleDials(t, a, b)
		lifecycleExchange(t, cs)
		time.Sleep(device.RejectAfterTime + 5*time.Second)
		for i, n := range []*Node{a, b} {
			target := []*Node{b, a}[i]
			n.mu.Lock()
			p := n.peers[target.PublicKey()]
			n.mu.Unlock()
			if p.session.ready() {
				t.Fatal("idle expired session remained ready")
			}
		}
		fresh := lifecycleDials(t, a, b)
		lifecycleExchange(t, fresh)
	})
}
