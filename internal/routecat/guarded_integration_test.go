//go:build lanlink_integration

package routecat

import (
	"context"
	"io"
	"net"
	"net/netip"
	"os"
	"testing"
	"time"

	"tailscale.com/feature/buildfeatures"
	"tailscale.com/tailcfg"
	"tailscale.com/types/key"
	"tailscale.com/types/logger"
	"tailscale.com/types/views"
)

// Explicit synthetic static loopback endpoints make direct-path evidence
// independent of the hosted runner's NIC inventory. Every socket send still
// passes the production constructor-wired guard. This does not prove a physical
// NIC boundary, NAT traversal or VPN containment.
func TestGuardedDirectEncryptedTCPUDPIntegration(t *testing.T) {
	if os.Getenv("SOBALINK_RUN_GUARDED_INTEGRATION") != "1" {
		t.Skip("requires explicit native guarded integration")
	}
	if !buildfeatures.HasUDPTransport {
		t.Fatal("ordinary UDP-enabled build required")
	}
	for _, family := range []string{"127.0.0.1", "::1"} {
		t.Run(family, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			region, closeRelay := newIntegrationRelay(t)
			defer closeRelay()
			prefixes := []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32"), netip.MustParsePrefix("::1/128")}
			serverKey, role := key.NewNode(), key.NewNode()
			server := &Server{Key: serverKey, PresharedKey: NewPresharedKey(), Regions: []*tailcfg.DERPRegion{region}, DestinationPrefixes: prefixes, Logf: logger.Discard, AllowClient: func(k key.NodePublic) bool { return k == role.Public() }}
			defer server.Close()
			listeners := make(map[string]net.Listener)
			for _, network := range []string{"tcp", "udp"} {
				ln, err := server.Listen(ctx, network, ":54546")
				if err != nil {
					t.Fatal("guarded listener failed")
				}
				listeners[network] = ln
				defer ln.Close()
			}
			capability := (&ConnInfo{ServerPublic: NodePublic{serverKey.Public()}, ServerDiscoPublic: DiscoPublicForNode(serverKey), PresharedKey: server.PresharedKey, Region: []*tailcfg.DERPRegion{region}}).Addr()
			client := &Client{Key: role, Server: capability, DestinationPrefixes: prefixes, Logf: logger.Discard}
			defer client.Close()
			if _, err := client.Ping(ctx); err != nil {
				t.Fatal("guarded pinned relay handshake failed")
			}
			loopback := netip.MustParseAddr(family)
			for _, backend := range []*locoBackend{server.lb, client.lb} {
				sock := backend.sys.MagicSock.Get()
				if sock.LocalPort() == 0 {
					t.Fatal("guarded UDP socket absent")
				}
				sock.SetStaticEndpoints(views.SliceOf([]netip.AddrPort{netip.AddrPortFrom(loopback, sock.LocalPort())}))
			}
			deadline := time.Now().Add(20 * time.Second)
			direct := false
			for time.Now().Before(deadline) {
				bounded, stop := context.WithTimeout(ctx, 2*time.Second)
				proof, err := client.DiscoPing(bounded)
				stop()
				if err == nil && proof != nil && proof.Endpoint != "" {
					endpoint, err := netip.ParseAddrPort(proof.Endpoint)
					if err == nil && endpoint.Addr() == loopback {
						direct = true
						break
					}
				}
				time.Sleep(100 * time.Millisecond)
			}
			if !direct {
				t.Fatal("selected family did not establish direct encrypted path")
			}
			// Removing the relay after path establishment ensures subsequent application
			// bytes really use the direct path rather than only observing a probe.
			closeRelay()
			for _, network := range []string{"tcp", "udp"} {
				accepted := make(chan net.Conn, 1)
				go func(ln net.Listener) { c, _ := ln.Accept(); accepted <- c }(listeners[network])
				bounded, stop := context.WithTimeout(ctx, 10*time.Second)
				address := netip.AddrPortFrom(server.Addr(), 54546)
				var outgoing net.Conn
				var err error
				if network == "tcp" {
					outgoing, err = client.DialTCP(bounded, address)
				} else {
					outgoing, err = client.DialUDP(bounded, address)
				}
				stop()
				if err != nil {
					t.Fatal("direct application dial failed")
				}
				defer outgoing.Close()
				_ = outgoing.SetDeadline(time.Now().Add(10 * time.Second))
				payload := []byte("guarded direct synthetic payload")
				if _, err := outgoing.Write(payload); err != nil {
					t.Fatal("direct write failed")
				}
				var incoming net.Conn
				select {
				case incoming = <-accepted:
				case <-time.After(10 * time.Second):
					t.Fatal("direct accept timeout")
				}
				if incoming == nil {
					t.Fatal("direct accept failed")
				}
				defer incoming.Close()
				_ = incoming.SetDeadline(time.Now().Add(10 * time.Second))
				got := make([]byte, len(payload))
				if _, err := io.ReadFull(incoming, got); err != nil || string(got) != string(payload) {
					t.Fatal("direct payload mismatch")
				}
				if _, err := incoming.Write(got); err != nil {
					t.Fatal("direct reply failed")
				}
				if _, err := io.ReadFull(outgoing, got); err != nil || string(got) != string(payload) {
					t.Fatal("direct reply mismatch")
				}
			}
			closed := make(chan struct{})
			go func() { client.Close(); server.Close(); close(closed) }()
			select {
			case <-closed:
			case <-time.After(10 * time.Second):
				t.Fatal("guarded retirement blocked")
			}
		})
	}
}
