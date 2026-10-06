//go:build directlan_integration

package core

import (
	"bytes"
	"context"
	"encoding/json"

	qrcode "github.com/skip2/go-qrcode"
	"github.com/webkaz-labs/sobalink/internal/testfixture"
	"github.com/webkaz-labs/sobalink/internal/webui"
	"io"
	"net"
	"net/http"
	"net/netip"
	"reflect"
	"testing"
	"time"
)

// This is a two-instance native loopback application test, not evidence of
// firewall traversal, real-device operation, WAN paths or OS lifecycle behavior.
func TestDirectLANCoreNativePairMessageAndScopedTCP(t *testing.T) { runDirectLANCoreNative(t, false) }
func TestDirectLANCoreNativeFirstHTTPRequest(t *testing.T)        { runDirectLANCoreNative(t, true) }
func runDirectLANCoreNative(t *testing.T, diagnoseFirstHTTP bool) {
	if !directLANNetstackReady {
		t.Skip("userspace netstack activation remains gated")
	}
	host, guest := openLANTestCore(t), openLANTestCore(t)
	// Native first-handshake latency can exceed the mock helper's five-second
	// caller deadline. Core retains its own ordinary operation-specific limits.
	must := func(t *testing.T, c *Core, name string, payload any) any {
		t.Helper()
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		value, err := c.Command(ctx, webui.Command{RequestID: randomID(), Name: name, Payload: raw})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return value
	}
	choose := func() *testfixture.PortReservation {
		t.Helper()
		reserve, err := testfixture.ReserveLoopbackTCPUDP(netip.MustParseAddr("127.0.0.1"), 54543, 54544, 54545)
		if err != nil {
			t.Fatalf("could not allocate a paired loopback TCP/UDP endpoint: %v", err)
		}
		t.Cleanup(func() {
			if err := reserve.Close(); err != nil {
				t.Errorf("release direct LAN fixture reservation: %v", err)
			}
		})
		return reserve
	}
	for _, item := range []struct {
		c    *Core
		name string
	}{{host, "native-host"}, {guest, "native-guest"}} {
		reserve := choose()
		listen := reserve.Endpoint().String()
		// Hold both protocols until immediately before the real configuration
		// starts the node. Product startup errors still fail this test.
		if err := reserve.Close(); err != nil {
			t.Fatal(err)
		}
		must(t, item.c, "network.configure", map[string]any{"mode": "direct-lan", "hostname": item.name, "directLAN": DirectLANSelection{Listen: listen, Prefixes: []string{"127.0.0.0/8"}}})
	}
	hostKey := host.directLANStoreCopy().copy().Identity.PublicKey()
	guestKey := guest.directLANStoreCopy().copy().Identity.PublicKey()
	issued := must(t, host, "direct-lan.invite", map[string]any{"recipientPublicKey": guestKey, "name": "native-guest", "ttlSeconds": 60, "qr": true}).(map[string]any)
	code, e := qrcode.New(issued["invitation"].(string), qrcode.Medium)
	if e != nil || !reflect.DeepEqual(issued["qr"], code.Bitmap()) {
		t.Fatal("private QR did not encode exact invitation", e)
	}
	result := must(t, guest, "direct-lan.join", map[string]any{"invitation": issued["invitation"]}).(map[string]any)
	if result["paired"] != true || result["trusted"] != false || result["peerId"] != hostKey {
		t.Fatal(result)
	}
	for _, pair := range []struct {
		c   *Core
		key string
	}{{host, guestKey}, {guest, hostKey}} {
		if _, ok := pair.c.trust(pair.key); ok {
			t.Fatal("pairing granted app trust")
		}
		state, err := pair.c.current(context.Background())
		if err != nil || len(state.Snapshot.Peers) != 1 {
			t.Fatal(state, err)
		}
		if _, err := pair.c.nodeCopy().WhoIs(context.Background(), netip.AddrPortFrom(state.Snapshot.Peers[0].IPs[0], 32000)); err == nil {
			t.Fatal("fabricated source authenticated")
		}
	}
	if _, err := command(guest, randomID(), "message.send", map[string]string{"peerId": hostKey, "text": "not-approved"}); err == nil {
		t.Fatal("unapproved message allowed")
	}
	for _, pair := range []struct {
		c   *Core
		key string
	}{{host, guestKey}, {guest, hostKey}} {
		must(t, pair.c, "peer.trust", map[string]any{"peerId": pair.key, "trusted": true})
	}
	until := time.Now().Add(10 * time.Second)
	for {
		host.mu.RLock()
		hr := host.peerServer != nil
		host.mu.RUnlock()
		guest.mu.RLock()
		gr := guest.peerServer != nil
		guest.mu.RUnlock()
		if hr && gr && host.networkReady.Load() && guest.networkReady.Load() {
			break
		}
		if time.Now().After(until) {
			t.Fatal("application listeners did not become ready")
		}
		time.Sleep(20 * time.Millisecond)
	}

	if diagnoseFirstHTTP {
		probeCtx, probeCancel := context.WithTimeout(context.Background(), 8*time.Second)
		probeTransport := &http.Transport{Proxy: nil, DisableKeepAlives: true, ResponseHeaderTimeout: 5 * time.Second, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			return guest.dial(ctx, hostKey, "tcp", PeerPort)
		}}
		defer probeTransport.CloseIdleConnections()
		probeRequest, err := http.NewRequestWithContext(probeCtx, "POST", "http://peer.invalid/v1/messages", bytes.NewBufferString(`{"id":"native-first","text":"first-request"}`))
		if err != nil {
			t.Fatal(err)
		}
		probeRequest.Header.Set("Content-Type", "application/json")
		started := time.Now()
		response, probeErr := (&http.Client{Transport: probeTransport}).Do(probeRequest)
		if probeErr != nil {
			probeCancel()
			t.Fatalf("first native application HTTP request after %s: %v", time.Since(started), probeErr)
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		probeCancel()
		t.Logf("first native application HTTP response after %s", time.Since(started))
		if response.StatusCode != 200 {
			t.Fatalf("native HTTP response status %d", response.StatusCode)
		}

	}

	message := must(t, guest, "message.send", map[string]string{"peerId": hostKey, "text": "native hello / こんにちは"}).(Message)
	if message.Status != "sent" {
		t.Fatalf("application message status %s", message.Status)
	}
	host.mu.RLock()
	received := len(host.messages) > 0 && host.messages[len(host.messages)-1].Text == message.Text
	host.mu.RUnlock()
	if !received {
		t.Fatal("host did not receive authenticated message")
	}
	target, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	accepted := make(chan net.Conn, 1)
	appDone := make(chan struct{})
	go func() {
		defer close(appDone)
		c, err := target.Accept()
		if err == nil {
			accepted <- c
			_, _ = io.Copy(c, c)
			_ = c.Close()
		}
	}()
	port := int(target.Addr().(*net.TCPAddr).Port)
	must(t, host, "service.share", map[string]any{"name": "native-echo", "network": "tcp", "ports": "32101", "localPort": port, "peerIds": []string{guestKey}, "ttlSeconds": 60})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := guest.dial(ctx, hostKey, "tcp", 32101)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Write([]byte("echo")); err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 4)
	if _, err := io.ReadFull(c, b); err != nil || string(b) != "echo" {
		t.Fatal("scoped app TCP failed", err)
	}
	var app net.Conn
	select {
	case app = <-accepted:
		defer app.Close()
	case <-ctx.Done():
		t.Fatal("target not reached")
	}

	udpTarget, e := net.ListenUDP("udp4", net.UDPAddrFromAddrPort(netip.MustParseAddrPort("127.0.0.1:0")))
	if e != nil {
		t.Fatal(e)
	}
	defer udpTarget.Close()
	udpDone := make(chan error, 1)
	go func() {
		buffer := make([]byte, 512)
		udpTarget.SetReadDeadline(time.Now().Add(10 * time.Second))
		n, from, e := udpTarget.ReadFromUDPAddrPort(buffer)
		if e == nil {
			_, e = udpTarget.WriteToUDPAddrPort(buffer[:n], from)
		}
		udpDone <- e
	}()
	udpPort := int(udpTarget.LocalAddr().(*net.UDPAddr).Port)
	must(t, host, "service.share", map[string]any{"name": "native-udp-echo", "network": "udp", "ports": "32102", "localPort": udpPort, "peerIds": []string{guestKey}, "ttlSeconds": 60})
	packet, e := guest.dial(context.Background(), hostKey, "udp", 32102)
	if e != nil {
		t.Fatal(e)
	}
	defer packet.Close()
	packet.SetDeadline(time.Now().Add(10 * time.Second))
	if _, e = packet.Write([]byte("native UDP")); e != nil {
		t.Fatal(e)
	}
	udpReply := make([]byte, 128)
	n, e := packet.Read(udpReply)
	if e != nil || string(udpReply[:n]) != "native UDP" {
		t.Fatal("scoped native app UDP failed", e)
	}
	if e = <-udpDone; e != nil {
		t.Fatal(e)
	}
	must(t, host, "direct-lan.revoke", map[string]string{"peerId": guestKey})
	if _, ok := host.trust(guestKey); ok || len(host.directLANStoreCopy().copy().Peers) != 0 {
		t.Fatal("revocation retained approval")
	}
	_ = c.SetReadDeadline(time.Now().Add(time.Second))
	_, err = c.Read(make([]byte, 1))
	if err == nil {
		t.Fatal("revoked connection remained open")
	}
	select {
	case <-appDone:
	case <-time.After(3 * time.Second):
		t.Fatal("revocation did not close the provider's application flow")
	}
}
