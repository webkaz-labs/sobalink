//go:build lanlink_integration

package lanlink

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"tailscale.com/derp/derphttp"
	"tailscale.com/feature/buildfeatures"
	"tailscale.com/net/netmon"
	"tailscale.com/net/tlsdial"
	"tailscale.com/types/key"
	"tailscale.com/types/logger"
)

// This opt-in CI test uses real stock Tailcat/WireGuard over a TLS-pinned local
// DERP. Direct underlay UDP must be compiled out so every OS listener remains
// loopback-only. It tests existing TCP across one real two-minute relay lease;
// it does not prove direct UDP, real LAN/WAN/NAT migration or cross-relay migration.
func TestTrustedRelayTwoPeerIntegration(t *testing.T) {
	if os.Getenv("SOBALINK_RUN_LAN_INTEGRATION") != "1" {
		t.Skip("requires an explicitly enabled isolated native CI environment")
	}
	if buildfeatures.HasUDPTransport {
		t.Fatal("isolated integration requires ts_omit_udptransport; never create wildcard UDP listeners here")
	}
	if e := ValidateBuild(); e != nil {
		t.Fatal("integration setup or pairing failed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	var closers []io.Closer
	var cleanupOnce sync.Once
	cleanup := func() {
		cleanupOnce.Do(func() {
			stopped := make(chan struct{})
			go func() {
				for i := len(closers) - 1; i >= 0; i-- {
					closers[i].Close()
				}
				close(stopped)
			}()
			select {
			case <-stopped:
			case <-time.After(10 * time.Second):
				t.Error("backend teardown did not complete")
			}
		})
	}
	t.Cleanup(cleanup)
	probe, e := net.Listen("tcp4", "127.0.0.1:0")
	if e != nil {
		t.Fatal("integration setup or pairing failed")
	}
	endpoint := probe.Addr().(*net.TCPAddr).AddrPort()
	probe.Close()
	relayIdentity, e := GenerateRelayIdentity(endpoint.Addr())
	if e != nil {
		t.Fatal("integration setup or pairing failed")
	}
	relayConfig, e := relayIdentity.Endpoint(endpoint)
	if e != nil {
		t.Fatal("integration setup or pairing failed")
	}
	var hostSaves, clientSaves atomic.Int64
	hostIdentity, clientIdentity := GenerateIdentity(), GenerateIdentity()
	host, e := NewNode(NodeConfig{Identity: hostIdentity, Relay: relayConfig, Trust: NewBook(), EmbeddedRelay: true, Persist: func(Snapshot, []RemotePeer) error { hostSaves.Add(1); return nil }})
	if e != nil {
		t.Fatal("integration setup or pairing failed")
	}
	closers = append(closers, host)
	var approvals atomic.Int64
	unknown := key.NewNode()
	denied := make(chan struct{}, 1)
	allow := func(s string) bool {
		ok := host.AllowRelayKey(s)
		if ok {
			approvals.Add(1)
		}
		if s == keyString(unknown.Public()) && !ok {
			select {
			case denied <- struct{}{}:
			default:
			}
		}
		return ok
	}
	var rejectedBootstrap atomic.Int64
	bootstrap := func(frame []byte) error {
		err := host.AuthorizeRelayBootstrap(frame)
		if err != nil {
			rejectedBootstrap.Add(1)
		}
		return err
	}
	relay, e := StartLocalRelay(ctx, endpoint, relayIdentity, allow, bootstrap)
	if e != nil {
		t.Fatal("integration setup or pairing failed")
	}
	closers = append(closers, relay)
	if _, e = host.ServePairing(ctx); e != nil {
		t.Fatal("integration setup or pairing failed")
	}
	client, e := NewNode(NodeConfig{Identity: clientIdentity, Relay: relayConfig, Trust: NewBook(), Persist: func(Snapshot, []RemotePeer) error { clientSaves.Add(1); return nil }})
	if e != nil {
		t.Fatal("integration setup or pairing failed")
	}
	closers = append(closers, client)
	invitation, e := host.IssueInvitation(ctx, Peer{Key: client.PublicKey(), Name: "client"}, "host", time.Minute)
	if e != nil {
		t.Fatal("integration setup or pairing failed")
	}
	if _, e = client.ServePairing(ctx); e != nil {
		t.Fatal("integration setup or pairing failed")
	}
	// A wrong token must fail at the real pinned HTTPS bootstrap before trust.
	bad := invitation
	bad.Token = strings.Repeat("A", 43)
	if bad.Token == invitation.Token {
		bad.Token = strings.Repeat("B", 43)
	}
	if client.PairInvitation(ctx, bad) == nil {
		t.Fatal("invalid invitation was accepted")
	}
	if rejectedBootstrap.Load() != 1 {
		t.Fatal("invalid token did not reach the real HTTPS bootstrap handler")
	}
	if len(host.PublicPeers()) != 0 || len(client.PublicPeers()) != 0 {
		t.Fatal("invalid invitation changed trust")
	}
	// Exercise DERP's real admission controller with an uninvited key.
	mon := netmon.NewStatic()
	closers = append(closers, mon)
	stranger, err := derphttp.NewClient(unknown, "https://"+endpoint.String()+"/derp", logger.Discard, mon)
	if err != nil {
		t.Fatal("construct denied-key DERP client")
	}
	closers = append(closers, stranger)
	stranger.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS13, ServerName: endpoint.Addr().String()}
	tlsdial.SetConfigExpectedCertHash(stranger.TLSConfig, relayConfig.CertificateSHA256)
	attempt, stopAttempt := context.WithTimeout(ctx, 10*time.Second)
	stopSocket := context.AfterFunc(attempt, func() { stranger.Close() })
	// Connect can finish before asynchronous DERP admission; Recv must reject.
	err = stranger.Connect(attempt)
	if err == nil {
		_, err = stranger.Recv()
	}
	timedOut := attempt.Err() != nil
	stopSocket()
	stopAttempt()
	stranger.Close()
	if err == nil || timedOut {
		t.Fatal("uninvited DERP key was not promptly rejected")
	}
	select {
	case <-denied:
	default:
		t.Fatal("uninvited key never reached denying admission controller")
	}
	if e = client.PairInvitation(ctx, invitation); e != nil {
		t.Fatal("integration setup or pairing failed")
	}
	if hostSaves.Load() != 1 || clientSaves.Load() != 1 {
		t.Fatal("successful pairing did not persist exactly once on both peers")
	}
	type stream struct {
		conn     net.Conn
		accepted net.Conn
		listener net.Listener
		done     chan struct{}
	}
	var streams []stream
	for _, pair := range [][2]*Node{{host, client}, {client, host}} {
		provider, consumer := pair[0], pair[1]
		ln, err := provider.ListenPeer(ctx, "tcp", 32101)
		if err != nil {
			t.Fatal("listen TCP")
		}
		closers = append(closers, ln)
		accepted := make(chan net.Conn, 1)
		done := make(chan struct{})
		go func() {
			defer close(done)
			c, err := ln.Accept()
			if err != nil {
				accepted <- nil
				return
			}
			defer c.Close()
			accepted <- c
			io.Copy(c, c)
		}()
		conn, err := consumer.DialPeer(ctx, provider.PublicKey(), "tcp", 32101)
		if err != nil {
			t.Fatal("dial TCP")
		}
		closers = append(closers, conn)
		var incoming net.Conn
		select {
		case incoming = <-accepted:
		case <-ctx.Done():
			t.Fatal("TCP accept timeout")
		}
		if incoming == nil {
			t.Fatal("TCP accept failed")
		}
		if id, ok := provider.PeerKey(incoming.RemoteAddr()); !ok || id != consumer.PublicKey() {
			t.Fatal("TCP canonical identity mismatch")
		}
		streams = append(streams, stream{conn, incoming, ln, done})
	}
	// Keep these exact connections: no redial/retry can hide loss of TCP state.
	baseline := approvals.Load()
	started := time.Now()
	frames := 0
	for time.Since(started) < 130*time.Second {
		for _, s := range streams {
			s.conn.SetDeadline(time.Now().Add(20 * time.Second))
			payload := fmt.Sprintf("ordered-frame-%06d\n", frames)
			if _, err := io.WriteString(s.conn, payload); err != nil {
				t.Fatal("existing TCP write failed during lease rollover")
			}
			got := make([]byte, len(payload))
			if _, err := io.ReadFull(s.conn, got); err != nil || string(got) != payload {
				t.Fatal("existing TCP ordered echo failed during lease rollover")
			}
		}
		frames++
		select {
		case <-time.After(time.Second):
		case <-ctx.Done():
			t.Fatal("lease continuity timeout")
		}
	}
	// A final successful frame must occur after the elapsed lease boundary,
	// even if a CI runner was paused while the periodic loop was sleeping.
	for _, s := range streams {
		s.conn.SetDeadline(time.Now().Add(20 * time.Second))
		if _, err := io.WriteString(s.conn, "after-lease"); err != nil {
			t.Fatal("post-lease TCP write failed")
		}
		got := make([]byte, len("after-lease"))
		if _, err := io.ReadFull(s.conn, got); err != nil || string(got) != "after-lease" {
			t.Fatal("post-lease TCP echo failed")
		}
	}
	if approvals.Load() <= baseline {
		t.Fatal("no real DERP re-admission observed across lease")
	}
	// Application UDP uses the overlay; OS underlay UDP remains compiled out.
	var hostPackets net.PacketConn
	var clientSource net.Addr
	for _, pair := range [][2]*Node{{host, client}, {client, host}} {
		provider, consumer := pair[0], pair[1]
		packets, err := provider.ListenPacket(ctx, 32102)
		if err != nil {
			t.Fatal("listen UDP")
		}
		closers = append(closers, packets)
		udp, err := consumer.DialPacketPeer(ctx, provider.PublicKey(), 32102)
		if err != nil {
			t.Fatal("dial UDP")
		}
		closers = append(closers, udp)
		packets.SetDeadline(time.Now().Add(10 * time.Second))
		udp.SetDeadline(time.Now().Add(10 * time.Second))
		if _, err = udp.Write([]byte("request")); err != nil {
			t.Fatal("UDP request write failed")
		}
		buf := make([]byte, 64)
		n, source, err := packets.ReadFrom(buf)
		if err != nil || string(buf[:n]) != "request" {
			t.Fatal("UDP request mismatch")
		}
		if id, ok := provider.PeerKey(source); !ok || id != consumer.PublicKey() {
			t.Fatal("UDP canonical identity mismatch")
		}
		if _, err = packets.WriteTo([]byte("reply"), source); err != nil {
			t.Fatal("UDP reply write failed")
		}
		n, err = udp.Read(buf)
		if err != nil || string(buf[:n]) != "reply" {
			t.Fatal("UDP reply mismatch")
		}
		if provider == host {
			hostPackets, clientSource = packets, source
		}
	}
	if e = host.Revoke(client.PublicKey()); e != nil {
		t.Fatal("revoke failed")
	}
	if _, e = host.cfg.Trust.Epoch(client.PublicKey()); e == nil {
		t.Fatal("revoked peer remains trusted")
	}
	if _, ok := host.PeerKey(clientSource); ok {
		t.Fatal("revoked source remains authenticated")
	}
	if _, e = hostPackets.WriteTo([]byte("denied"), clientSource); e == nil {
		t.Fatal("revoked UDP session remained usable")
	}
	// The host-side half of each already-active connection must close immediately.
	for i, s := range streams {
		local := s.conn
		if i == 0 {
			local = s.accepted
		}
		local.SetWriteDeadline(time.Now().Add(2 * time.Second))
		if _, err := local.Write([]byte("denied")); err == nil {
			t.Fatal("revoked active TCP remained writable")
		}
		s.conn.Close()
		s.accepted.Close()
		s.listener.Close()
		select {
		case <-s.done:
		case <-time.After(5 * time.Second):
			t.Fatal("TCP echo goroutine did not stop")
		}
	}
	cleanup()
	if t.Failed() {
		return
	}
	// Only generic evidence is logged: never print errors that may contain peer capabilities.
	t.Logf("stock loopback DERP: bidirectional TCP/UDP, denied-key admission, active revocation, clean stop; %d ordered rounds across a real two-minute lease", frames)
}
