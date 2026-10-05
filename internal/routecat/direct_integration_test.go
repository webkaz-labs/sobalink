//go:build lanlink_integration

package routecat

import (
	"context"
	"io"
	"net"
	"net/netip"
	"os"
	"sync"
	"testing"
	"time"

	"tailscale.com/feature/buildfeatures"
	"tailscale.com/tailcfg"
	"tailscale.com/types/key"
	"tailscale.com/types/logger"
)

// This separately opted-in normal-build fixture allows ordinary direct UDP.
// All configured relays are pinned numeric loopback endpoints. It proves fresh
// same-key clients can recover application TCP and UDP through a second relay;
// it is not a strict egress, existing-TCP continuity, or real-device test.
func TestNormalBuildMultipleRelayTCPUDPRecoveryIntegration(t *testing.T) {
	if os.Getenv("SOBALINK_RUN_DIRECT_INTEGRATION") != "1" {
		t.Skip("requires explicitly enabled normal-build native CI")
	}
	if !buildfeatures.HasUDPTransport {
		t.Fatal("normal-build integration requires direct UDP transport")
	}
	if err := validateRuntime(false); err != nil {
		t.Fatal("unsafe native fixture configuration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	r1, close1 := newIntegrationRelay(t)
	r2, close2 := newIntegrationRelay(t)
	defer close1()
	defer close2()
	serverKey, role := key.NewNode(), key.NewNode()
	s := &Server{Key: serverKey, PresharedKey: NewPresharedKey(), Regions: []*tailcfg.DERPRegion{r1, r2}, Logf: logger.Discard, AllowClient: func(k key.NodePublic) bool { return k == role.Public() }}
	var accepts, handlers sync.WaitGroup
	var listenerClosers []net.Listener
	handlerErrors := make(chan struct{}, 8)
	for _, network := range []string{"tcp", "udp"} {
		ln, err := s.Listen(ctx, network, ":54546")
		if err != nil {
			s.Close()
			t.Fatal("application listener startup failed")
		}
		listenerClosers = append(listenerClosers, ln)
		accepts.Add(1)
		go func(network string, ln net.Listener) {
			defer accepts.Done()
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				handlers.Add(1)
				go func() {
					defer handlers.Done()
					defer c.Close()
					peer, ok := s.PeerKey(c.RemoteAddr())
					if !ok || peer != role.Public() {
						select {
						case handlerErrors <- struct{}{}:
						default:
						}
						return
					}
					_ = c.SetDeadline(time.Now().Add(15 * time.Second))
					if network == "tcp" {
						_, _ = io.Copy(c, c)
						return
					}
					buf := make([]byte, 1024)
					for {
						n, err := c.Read(buf)
						if err != nil {
							return
						}
						if _, err := c.Write(buf[:n]); err != nil {
							return
						}
					}
				}()
			}
		}(network, ln)
	}
	defer func() {
		for _, ln := range listenerClosers {
			ln.Close()
		}
		s.Close()
		accepts.Wait()
		handlers.Wait()
	}()
	connect := func(stage string, relay *tailcfg.DERPRegion) *Client {
		t.Helper()
		ci := &ConnInfo{ServerPublic: NodePublic{serverKey.Public()}, ServerDiscoPublic: DiscoPublicForNode(serverKey), PresharedKey: s.PresharedKey, Region: []*tailcfg.DERPRegion{relay}}
		c := &Client{Key: role, Server: ci.Addr(), Logf: logger.Discard}
		t.Cleanup(func() { c.Close() })
		bounded, stop := context.WithTimeout(ctx, 25*time.Second)
		defer stop()
		if _, err := c.Ping(bounded); err != nil {
			t.Fatal(stage, "relay registration failed")
		}
		s.lb.mu.Lock()
		peer := s.lb.clients[role.Public()]
		matches := peer != nil && s.lb.dm.Regions[peer.HomeDERP].Nodes[0].DERPPort == relay.Nodes[0].DERPPort
		s.lb.mu.Unlock()
		if !matches {
			t.Fatal(stage, "authenticated server relay route did not update")
		}
		for _, network := range []string{"tcp", "udp"} {
			var conn net.Conn
			var err error
			address := netip.AddrPortFrom(s.Addr(), 54546)
			if network == "tcp" {
				conn, err = c.DialTCP(bounded, address)
			} else {
				conn, err = c.DialUDP(bounded, address)
			}
			if err != nil {
				t.Fatal(stage, network, "application dial failed")
			}
			_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
			payload := []byte("normal-build synthetic application roundtrip")
			if _, err := conn.Write(payload); err != nil {
				conn.Close()
				t.Fatal(stage, network, "application write failed")
			}
			got := make([]byte, len(payload))
			if _, err := io.ReadFull(conn, got); err != nil || string(got) != string(payload) {
				conn.Close()
				t.Fatal(stage, network, "application echo failed")
			}
			if network == "tcp" {
				cw, ok := conn.(interface{ CloseWrite() error })
				if !ok || cw.CloseWrite() != nil {
					conn.Close()
					t.Fatal(stage, "TCP half-close failed")
				}
				if _, err := io.Copy(io.Discard, conn); err != nil {
					conn.Close()
					t.Fatal(stage, "TCP shutdown failed")
				}
			}
			conn.Close()
		}
		if err := c.DrainTCP(bounded); err != nil {
			t.Fatal(stage, "TCP drain failed")
		}
		proofCtx, finish := context.WithTimeout(bounded, 5*time.Second)
		proof, err := c.DiscoPing(proofCtx)
		finish()
		if err != nil || proof == nil {
			t.Fatal(stage, "fresh transport evidence failed")
		}
		t.Logf("%s: candidate acknowledged; TCP/UDP roundtrips passed; direct observed=%t; relay observed=%t", stage, proof.Endpoint != "", proof.DERPRegionID != 0)
		if c.PublicKey() != role.Public() {
			t.Fatal("client identity changed")
		}
		select {
		case <-handlerErrors:
			t.Fatal("unexpected application peer identity")
		default:
		}
		return c
	}
	c1 := connect("first candidate", r1)
	c1.Close()
	close1()
	c2 := connect("second candidate after first relay closed", r2)
	defer c2.Close()
	if s.lb.priv.Public() != serverKey.Public() {
		t.Fatal("server identity changed")
	}
}
