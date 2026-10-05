//go:build directlan_integration

package directlan

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"
)

func nativeNode(t *testing.T, id byte) (*Node, *[]Peer) { return nativeNodeOnIP(t, id, "127.0.0.1") }
func nativeNodeOnIP(t *testing.T, id byte, ip string) (*Node, *[]Peer) {
	return nativeConfiguredNode(t, id, ip, nil)
}
func nativeConfiguredNode(t *testing.T, id byte, ip string, configure func(*Config)) (*Node, *[]Peer) {
	t.Helper()
	address := netip.MustParseAddr(ip)
	network := "tcp6"
	if address.Is4() {
		network = "tcp4"
	}
	reserve, e := net.Listen(network, netip.AddrPortFrom(address, 0).String())
	if e != nil {
		t.Fatalf("native loopback sockets unavailable; this integration gate is NOT passed: %v", e)
	}
	ap := reserve.Addr().(*net.TCPAddr).AddrPort()
	reserve.Close()
	c := testConfig(id)
	c.Listen = ap
	c.AllowedPrefixes = []netip.Prefix{netip.PrefixFrom(address, address.BitLen())}
	var saved []Peer
	c.Persist = func(p []Peer) error { saved = append([]Peer(nil), p...); return nil }
	if configure != nil {
		configure(&c)
	}
	n, e := NewNode(c)
	if e != nil {
		t.Fatal(e)
	}
	if e = n.Start(context.Background()); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { n.Close() })
	return n, &saved
}
func pairNative(t *testing.T, a, b *Node) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	inv, e := a.IssueInvitation(ctx, Peer{Key: b.PublicKey(), Name: "Synthetic peer", Endpoint: b.Endpoint()}, time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	encoded, e := inv.Encode()
	if e != nil {
		t.Fatal(e)
	}
	decoded, e := ParseInvitation(encoded)
	if e != nil {
		t.Fatal(e)
	}
	if e = b.PairInvitation(ctx, decoded); e != nil {
		t.Fatal(e)
	}
}
func TestNativeColdStartTCPUDPRevoke(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "::1"} {
		t.Run(ip, func(t *testing.T) {
			a, as := nativeNodeOnIP(t, 30, ip)
			b, bs := nativeNodeOnIP(t, 31, ip)
			pairNative(t, a, b)
			if len(*as) != 1 || len(*bs) != 1 {
				t.Fatal("missing durable pair")
			}
			ctx := context.Background()
			ln, e := a.ListenPeer(ctx, "tcp", 42001)
			if e != nil {
				t.Fatal(e)
			}
			defer ln.Close()
			tcpDone := make(chan error, 1)
			var caller net.Addr
			payload := bytes.Repeat([]byte("synthetic stream bytes"), 100000)
			go func() {
				c, e := ln.Accept()
				if e != nil {
					tcpDone <- e
					return
				}
				defer c.Close()
				caller = c.RemoteAddr()
				c.SetDeadline(time.Now().Add(20 * time.Second))
				if key, ok := a.PeerKey(c.RemoteAddr()); !ok || key != b.PublicKey() {
					tcpDone <- ErrIdentity
					return
				}
				got, e := io.ReadAll(c)
				if e != nil {
					tcpDone <- e
					return
				}
				sum := sha256.Sum256(got)
				_, e = c.Write(sum[:])
				c.Close()
				tcpDone <- e
			}()
			c, e := b.DialPeer(ctx, a.PublicKey(), "tcp", 42001)
			if e != nil {
				t.Fatal(e)
			}
			c.SetDeadline(time.Now().Add(20 * time.Second))
			if _, e = c.Write(payload); e != nil {
				t.Fatal(e)
			}
			if e = c.(interface{ CloseWrite() error }).CloseWrite(); e != nil {
				t.Fatal(e)
			}
			got, e := io.ReadAll(c)
			if e != nil {
				t.Fatal(e)
			}
			sum := sha256.Sum256(payload)
			if !bytes.Equal(got, sum[:]) {
				t.Fatal("stream/half-close integrity")
			}
			c.Close()
			if e = <-tcpDone; e != nil {
				t.Fatal(e)
			}
			if _, ok := a.PeerKey(caller); ok {
				t.Fatal("closed source retained identity")
			}
			pc, e := a.ListenPacket(ctx, 42002)
			if e != nil {
				t.Fatal(e)
			}
			defer pc.Close()
			u, e := b.DialPacketPeer(ctx, a.PublicKey(), 42002)
			if e != nil {
				t.Fatal(e)
			}
			defer u.Close()
			u.SetDeadline(time.Now().Add(5 * time.Second))
			pc.SetDeadline(time.Now().Add(20 * time.Second))
			udpDone := make(chan error, 1)
			packets := [][]byte{nil, []byte("first"), []byte("second"), bytes.Repeat([]byte{123}, MaxDatagram)}
			go func() {
				buf := make([]byte, MaxDatagram)
				for range packets {
					n, remote, e := pc.ReadFrom(buf)
					if e != nil {
						udpDone <- e
						return
					}
					if key, ok := a.PeerKey(remote); !ok || key != b.PublicKey() {
						udpDone <- ErrIdentity
						return
					}
					if _, e = pc.WriteTo(buf[:n], remote); e != nil {
						udpDone <- e
						return
					}
				}
				udpDone <- nil
			}()
			for _, p := range packets {
				if _, e = u.Write(p); e != nil {
					t.Fatal(e)
				}
				buf := make([]byte, MaxDatagram)
				count, e := u.Read(buf)
				if e != nil || !bytes.Equal(buf[:count], p) {
					t.Fatal("UDP boundary", count, e)
				}
			}
			if e = <-udpDone; e != nil {
				t.Fatal(e)
			}
			if _, e = u.WriteTo([]byte("denied"), net.UDPAddrFromAddrPort(netip.MustParseAddrPort("127.0.0.1:12345"))); !errors.Is(e, ErrUntrusted) {
				t.Fatal("arbitrary UDP destination permitted", e)
			}
			if e = a.Revoke(b.PublicKey()); e != nil {
				t.Fatal(e)
			}
			if key, ok := u.(interface{ PeerIdentity() (string, bool) }).PeerIdentity(); ok && key == b.PublicKey() {
				t.Fatal("wrong outgoing identity")
			}
			if len(*as) != 0 {
				t.Fatal("revocation not persisted")
			}
			u.SetReadDeadline(time.Now().Add(time.Second))
			if _, e = u.Read(make([]byte, 32)); e == nil {
				t.Fatal("revoke kept flow live")
			}
			if _, e = b.DialPeer(ctx, a.PublicKey(), "tcp", 42001); e == nil {
				t.Fatal("revoked peer reopened")
			}
		})
	}
}
func TestNativeScopedDispatcherAndUnknownPeer(t *testing.T) {
	a, _ := nativeNode(t, 32)
	b, _ := nativeNode(t, 33)
	unpaired, _ := nativeNode(t, 34)
	pairNative(t, a, b)
	undo, e := a.RegisterTCPFallback(func(src, dst netip.AddrPort) (func(net.Conn), bool) {
		key, ok := a.PeerKey(net.TCPAddrFromAddrPort(src))
		if !ok || key != b.PublicKey() || dst.Port() != 43000 {
			return nil, false
		}
		return func(c net.Conn) { c.Write([]byte("scoped")) }, true
	})
	if e != nil {
		t.Fatal(e)
	}
	defer undo()
	c, e := b.DialPeer(context.Background(), a.PublicKey(), "tcp", 43000)
	if e != nil {
		t.Fatal(e)
	}
	got, e := io.ReadAll(c)
	c.Close()
	if e != nil || string(got) != "scoped" {
		t.Fatal(string(got), e)
	}
	denied, e := b.DialPeer(context.Background(), a.PublicKey(), "tcp", 43001)
	if e == nil {
		denied.SetReadDeadline(time.Now().Add(time.Second))
		n, readErr := denied.Read(make([]byte, 16))
		denied.Close()
		if n != 0 || readErr == nil {
			t.Fatal("unapproved dispatcher returned application data")
		}
	}
	if _, e = unpaired.DialPeer(context.Background(), a.PublicKey(), "tcp", 43000); !errors.Is(e, ErrUntrusted) {
		t.Fatal("unpaired dial", e)
	}
	undo()
	if _, e = b.DialPeer(context.Background(), a.PublicKey(), "tcp", 43000); e == nil {
		t.Fatal("removed dispatcher", e)
	}
}
func TestNativeUDPDeadlinesTruncationAndListenerClose(t *testing.T) {
	a, _ := nativeNode(t, 35)
	b, _ := nativeNode(t, 36)
	pairNative(t, a, b)
	ln, e := a.ListenPeer(context.Background(), "udp", 44000)
	if e != nil {
		t.Fatal(e)
	}
	client, e := b.DialPacketPeer(context.Background(), a.PublicKey(), 44000)
	if e != nil {
		t.Fatal(e)
	}
	defer client.Close()
	if _, e = client.Write([]byte("initial")); e != nil {
		t.Fatal(e)
	}
	server, e := ln.Accept()
	if e != nil {
		t.Fatal(e)
	}
	initial := make([]byte, 16)
	server.SetReadDeadline(time.Now().Add(time.Second))
	if _, e = server.Read(initial); e != nil {
		t.Fatal(e)
	}
	client.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
	if _, e = client.Read(make([]byte, 32)); e == nil {
		t.Fatal("missing deadline")
	} else {
		var timeout net.Error
		if !errors.As(e, &timeout) || !timeout.Timeout() {
			t.Fatal("deadline", e)
		}
	}
	client.SetReadDeadline(time.Now().Add(time.Second))
	go func() { server.Write([]byte("long datagram")); server.Write([]byte("next")) }()
	small := make([]byte, 4)
	n, e := client.Read(small)
	if e != nil || string(small[:n]) != "long" {
		t.Fatal("truncate", n, e)
	}
	n, e = client.Read(small)
	if e != nil || string(small[:n]) != "next" {
		t.Fatal("packet remainder leaked", n, e)
	}
	remote := server.RemoteAddr()
	ln.Close()
	if _, ok := server.(interface{ PeerIdentity() (string, bool) }).PeerIdentity(); ok {
		t.Fatal("closed inbound stream retained identity")
	}
	if _, ok := a.PeerKey(remote); ok {
		t.Fatal("listener close retained identity")
	}
	if _, e = client.Read(small); e == nil {
		t.Fatal("listener close retained work")
	}
}
func TestNativePairRestartAndFailedRevoke(t *testing.T) {
	a, as := nativeNode(t, 37)
	b, bs := nativeNode(t, 38)
	pairNative(t, a, b)
	ac, bc := a.cfg, b.cfg
	ac.Peers = append([]Peer(nil), (*as)...)
	bc.Peers = append([]Peer(nil), (*bs)...)
	a.Close()
	b.Close()
	a, e := NewNode(ac)
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	b, e = NewNode(bc)
	if e != nil {
		t.Fatal(e)
	}
	defer b.Close()
	if e = a.Start(context.Background()); e != nil {
		t.Fatal(e)
	}
	if e = b.Start(context.Background()); e != nil {
		t.Fatal(e)
	}
	ln, e := a.ListenPeer(context.Background(), "tcp", 44001)
	if e != nil {
		t.Fatal(e)
	}
	defer ln.Close()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		c, e := ln.Accept()
		if e == nil {
			defer c.Close()
			io.Copy(io.Discard, c)
		}
	}()
	c, e := b.DialPeer(context.Background(), a.PublicKey(), "tcp", 44001)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	a.cfg.Persist = func([]Peer) error { return errors.New("synthetic persistence failure") }
	if e = a.Revoke(b.PublicKey()); !errors.Is(e, ErrRecovery) {
		t.Fatal(e)
	}
	c.SetReadDeadline(time.Now().Add(time.Second))
	if _, e = c.Read(make([]byte, 1)); e == nil {
		t.Fatal("failed revoke kept stream")
	}
	ln.Close()
	wg.Wait()
}

func TestNativeInvitationTunnelKeyTampering(t *testing.T) {
	a, _ := nativeNode(t, 64)
	b, _ := nativeNode(t, 65)
	inv, e := a.IssueInvitation(context.Background(), Peer{Key: b.PublicKey()}, time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	inv.Host.TunnelKey = testIdentity(66).TunnelKey()
	if e = b.PairInvitation(context.Background(), inv); !errors.Is(e, ErrRemotePairedLocalSave) {
		t.Fatal("tampered host tunnel key accepted or uncertainty hidden", e)
	}
	if len(b.Peers()) != 0 {
		t.Fatal("unproven host tunnel key activated")
	}
	if e = a.Revoke(b.PublicKey()); e != nil {
		t.Fatal(e)
	}
}
