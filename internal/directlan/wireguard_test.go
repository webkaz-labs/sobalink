//go:build directlan_integration

package directlan

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/tailscale/wireguard-go/device"
)

func TestNativeWireGuardNetstackProof(t *testing.T) {
	cfgA, cfgB := testConfig(91), testConfig(92)
	var reservations [2]*net.UDPConn
	releaseReservation := func(index int) error {
		u := reservations[index]
		if u == nil {
			return nil
		}
		reservations[index] = nil
		return u.Close()
	}
	for index, cfg := range []*Config{&cfgA, &cfgB} {
		u, e := net.ListenUDP("udp4", net.UDPAddrFromAddrPort(netip.MustParseAddrPort("127.0.0.1:0")))
		if e != nil {
			t.Fatal(e)
		}
		reservations[index] = u
		t.Cleanup(func() {
			if err := releaseReservation(index); err != nil {
				t.Errorf("release WireGuard loopback reservation: %v", err)
			}
		})
		cfg.Listen = u.LocalAddr().(*net.UDPAddr).AddrPort()
	}
	if cfgA.Listen == cfgB.Listen {
		t.Fatal("simultaneously reserved UDP endpoints must be distinct")
	}
	addrA, _ := OverlayAddress(cfgA.Identity.PublicKey())
	addrB, _ := OverlayAddress(cfgB.Identity.PublicKey())
	ta, e := newUserspaceTunnel(addrA, func(src, dst netip.Addr) bool { return src == addrB && dst == addrA })
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { ta.Close() })
	tb, e := newUserspaceTunnel(addrB, func(src, dst netip.Addr) bool { return src == addrA && dst == addrB })
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { tb.Close() })
	ba, bb := &lanBind{cfg: cfgA}, &lanBind{cfg: cfgB}
	ba.policy.Store(&bindPolicy{endpoints: map[netip.AddrPort]bool{cfgB.Listen: true}})
	bb.policy.Store(&bindPolicy{endpoints: map[netip.AddrPort]bool{cfgA.Listen: true}})
	da := device.NewDevice(ta, ba, device.NewLogger(device.LogLevelSilent, ""))
	defer da.Close()
	db := device.NewDevice(tb, bb, device.NewLogger(device.LogLevelSilent, ""))
	defer db.Close()
	ka, _ := ecdh.X25519().NewPrivateKey(bytes.Repeat([]byte{91}, 32))
	kb, _ := ecdh.X25519().NewPrivateKey(bytes.Repeat([]byte{92}, 32))
	hexKey := func(b []byte) string { return hex.EncodeToString(b) }
	if e = da.IpcSet(fmt.Sprintf("private_key=%s\nlisten_port=%d\npublic_key=%s\nendpoint=%s\nallowed_ip=%s/128\n", hexKey(ka.Bytes()), cfgA.Listen.Port(), hexKey(kb.PublicKey().Bytes()), cfgB.Listen, addrB)); e != nil {
		t.Fatal(e)
	}
	if e = db.IpcSet(fmt.Sprintf("private_key=%s\nlisten_port=%d\npublic_key=%s\nendpoint=%s\nallowed_ip=%s/128\n", hexKey(kb.Bytes()), cfgB.Listen.Port(), hexKey(ka.PublicKey().Bytes()), cfgA.Listen, addrA)); e != nil {
		t.Fatal(e)
	}
	// IpcSet's BindUpdate does not open sockets while the device is down.
	// Keep both reservations until each corresponding Up call binds UDP.
	if e = releaseReservation(0); e != nil {
		t.Fatal(e)
	}
	if e = da.Up(); e != nil {
		t.Fatal(e)
	}
	if e = releaseReservation(1); e != nil {
		t.Fatal(e)
	}
	if e = db.Up(); e != nil {
		t.Fatal(e)
	}
	ln, e := ta.listenTCP(netip.AddrPortFrom(addrA, 45000))
	if e != nil {
		t.Fatal(e)
	}
	defer ln.Close()
	done := make(chan error, 1)
	go func() {
		c, e := ln.Accept()
		if e != nil {
			done <- e
			return
		}
		defer c.Close()
		_, e = io.Copy(c, c)
		done <- e
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, e := tb.dialTCP(ctx, netip.AddrPortFrom(addrA, 45000))
	if e != nil {
		t.Fatal(e)
	}
	c.SetDeadline(time.Now().Add(3 * time.Second))
	if _, e = c.Write([]byte("actual netstack")); e != nil {
		t.Fatal(e)
	}
	got := make([]byte, 15)
	if _, e = io.ReadFull(c, got); e != nil {
		t.Fatal(e)
	}
	if string(got) != "actual netstack" {
		t.Fatal(string(got))
	}
	c.Close()
	if e = <-done; e != nil {
		t.Fatal(e)
	}
}
