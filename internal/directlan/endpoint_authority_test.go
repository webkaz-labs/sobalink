//go:build directlan_integration

package directlan

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"
	"time"

	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv6"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
)

func TestNativeApprovedSourceCannotChangePeerIdentity(t *testing.T) {
	receiver, _ := nativeNode(t, 111)
	a, _ := nativeNode(t, 112)
	b, _ := nativeNode(t, 113)
	pairNative(t, receiver, a)
	pairNative(t, receiver, b)
	pc, err := receiver.ListenPacket(context.Background(), 45221)
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	c, err := a.DialPacketPeer(context.Background(), receiver.PublicKey(), 45221)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	positive := func(payload string) {
		t.Helper()
		c.SetDeadline(time.Now().Add(5 * time.Second))
		pc.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := c.Write([]byte(payload)); err != nil {
			t.Fatal(err)
		}
		data := make([]byte, 100)
		n, remote, err := pc.ReadFrom(data)
		if err != nil || string(data[:n]) != payload {
			t.Fatalf("positive receive failed: %v", err)
		}
		if key, ok := receiver.PeerKey(remote); !ok || key != a.PublicKey() {
			t.Fatal("A key did not retain A identity")
		}
		if _, err := pc.WriteTo([]byte("reply"), remote); err != nil {
			t.Fatal(err)
		}
		n, err = c.Read(data)
		if err != nil || string(data[:n]) != "reply" {
			t.Fatalf("reply did not reach A's pinned endpoint: %v", err)
		}
	}
	positive("original endpoint")
	// Test only: send A's authenticated WG ciphertext from B's already approved
	// UDP socket. A's actual receiving socket and keys remain unchanged.
	a.bind.mu.Lock()
	original := a.bind.socket
	a.bind.socket = b.bind.socket
	a.bind.mu.Unlock()
	defer func() { a.bind.mu.Lock(); a.bind.socket = original; a.bind.mu.Unlock() }()
	positive("other approved source endpoint")

	// Test only: give A's userspace stack B's virtual source address. Encryption
	// still uses A's WG key. The receiver must reject B-source plaintext.
	fake := b.OverlayAddr()
	if e := a.tunnel.stack.AddProtocolAddress(1, tcpip.ProtocolAddress{Protocol: ipv6.ProtocolNumber, AddressWithPrefix: tcpip.AddrFromSlice(fake.AsSlice()).WithPrefix()}, stack.AddressProperties{}); e != nil {
		t.Fatal(e)
	}
	forged, err := dialNativeOwnedUDP(a.tunnel, netip.AddrPortFrom(fake, 0), netip.AddrPortFrom(receiver.OverlayAddr(), 45221))
	if err != nil {
		t.Fatal(err)
	}
	defer forged.Close()
	if _, err = forged.Write([]byte("must not inherit B grants")); err != nil {
		t.Fatal(err)
	}
	pc.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	_, _, err = pc.ReadFrom(make([]byte, 100))
	var timeout net.Error
	if !errors.As(err, &timeout) || !timeout.Timeout() {
		t.Fatalf("forged B overlay reached application: %v", err)
	}
	if _, ok := receiver.PeerKey(forged.LocalAddr()); ok {
		t.Fatal("forged B source acquired live identity")
	}
	positive("legitimate A after rejected forgery")
}

// dialNativeOwnedUDP permits a synthetic local address in this identity fixture
// while retaining creator cleanup and live endpoint ownership.
func dialNativeOwnedUDP(tunnel *userspaceTunnel, local, remote netip.AddrPort) (net.Conn, error) {
	creator, err := tunnel.owner.acquireCreator(context.Background(), false)
	if err != nil {
		return nil, err
	}
	defer creator.finishOutgoing()
	raw, ep, err := tunnel.dialOwned(creator, "udp", local, remote)
	if err != nil {
		cleanupCreated(ep)
		return nil, err
	}
	if !creator.TryHandoff(ep) {
		cleanupCreated(ep)
		return nil, net.ErrClosed
	}
	owner := creator.live
	owner.attach(raw)
	return owner, nil
}

func TestNativeIPv6DatagramSizes(t *testing.T) {
	a, _ := nativeNodeOnIP(t, 120, "::1")
	b, _ := nativeNodeOnIP(t, 121, "::1")
	pairNative(t, a, b)
	pc, e := a.ListenPacket(context.Background(), 45222)
	if e != nil {
		t.Fatal(e)
	}
	defer pc.Close()
	c, e := b.DialPacketPeer(context.Background(), a.PublicKey(), 45222)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	for _, payload := range [][]byte{nil, bytes.Repeat([]byte{7}, 1232), bytes.Repeat([]byte{8}, MaxDatagram)} {
		c.SetDeadline(time.Now().Add(5 * time.Second))
		pc.SetDeadline(time.Now().Add(5 * time.Second))
		if _, e = c.Write(payload); e != nil {
			t.Fatal(e)
		}
		data := make([]byte, MaxDatagram)
		n, remote, e := pc.ReadFrom(data)
		if e != nil || !bytes.Equal(data[:n], payload) {
			t.Fatalf("IPv6 native UDP receive failed: %v", e)
		}
		if _, e = pc.WriteTo(data[:n], remote); e != nil {
			t.Fatal(e)
		}
		n, e = c.Read(data)
		if e != nil || !bytes.Equal(data[:n], payload) {
			t.Fatalf("IPv6 native UDP echo failed: %v", e)
		}
	}
}
