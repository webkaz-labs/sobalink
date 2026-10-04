//go:build lanlink_integration

package routecat

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"sync"
	"testing"
	"time"

	"tailscale.com/derp/derpserver"
	"tailscale.com/feature/buildfeatures"
	"tailscale.com/tailcfg"
	"tailscale.com/types/key"
	"tailscale.com/types/logger"
)

// This test is prepared for opt-in native CI only. Its only OS listeners are
// loopback TLS relays; UDP underlay MUST be compiled out. It proves userspace
// application success and stable identity across fresh clients, not existing
// TCP continuity, direct path migration, or packet-capture egress acceptance.
func TestMultipleRelayPresenceAndFreshClientRecovery(t *testing.T) {
	if os.Getenv("SOBALINK_RUN_LAN_INTEGRATION") != "1" {
		t.Skip("requires explicitly enabled isolated native CI")
	}
	if buildfeatures.HasUDPTransport {
		t.Fatal("isolated integration requires ts_omit_udptransport")
	}
	if err := validateRuntime(true); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	r1, close1 := newIntegrationRelay(t)
	r2, close2 := newIntegrationRelay(t)
	defer close1()
	defer close2()
	serverKey, role := key.NewNode(), key.NewNode()
	s := &Server{Key: serverKey, PresharedKey: NewPresharedKey(), Regions: []*tailcfg.DERPRegion{r2, r1}, PrivateOnly: true, Logf: logger.Discard, AllowClient: func(k key.NodePublic) bool { return k == role.Public() }}
	ln, err := s.Listen(ctx, "tcp", ":54546")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var handlers sync.WaitGroup
	accepted := make(chan error, 4)
	acceptDone := make(chan struct{})
	go func() {
		defer close(acceptDone)
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			handlers.Add(1)
			go func() {
				defer handlers.Done()
				defer c.Close()
				k, ok := s.PeerKey(c.RemoteAddr())
				if !ok || k != role.Public() {
					accepted <- errUnexpectedPeer{}
					return
				}
				_ = c.SetDeadline(time.Now().Add(10 * time.Second))
				_, err := io.Copy(c, c)
				accepted <- err
			}()
		}
	}()
	defer func() { ln.Close(); <-acceptDone; handlers.Wait() }()
	// A non-home connection becomes stale after 60s and cleanup can take 15s.
	// Leave both regions idle beyond that bound before attempting any clients.
	select {
	case <-time.After(80 * time.Second):
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	connect := func(r *tailcfg.DERPRegion) *Client {
		t.Helper()
		ci := &ConnInfo{ServerPublic: NodePublic{serverKey.Public()}, ServerDiscoPublic: DiscoPublicForNode(serverKey), PresharedKey: s.PresharedKey, Region: []*tailcfg.DERPRegion{r}}
		client := &Client{Key: role, Server: ci.Addr(), PrivateOnly: true, Logf: logger.Discard}
		bounded, stop := context.WithTimeout(ctx, 15*time.Second)
		defer stop()
		c, err := client.DialTCP(bounded, netip.AddrPortFrom(s.Addr(), 54546))
		if err != nil {
			client.Close()
			t.Fatal(err)
		}
		_ = c.SetDeadline(time.Now().Add(10 * time.Second))
		want := []byte("routecat isolated fixture")
		if _, err := c.Write(want); err != nil {
			c.Close()
			client.Close()
			t.Fatal(err)
		}
		got := make([]byte, len(want))
		if _, err := io.ReadFull(c, got); err != nil || string(got) != string(want) {
			c.Close()
			client.Close()
			t.Fatal("authenticated application echo failed")
		}
		// Complete the userspace TCP shutdown before stopping the engine.
		// Closing the engine immediately can discard the FIN/ACK in flight.
		cw, ok := c.(interface{ CloseWrite() error })
		if !ok {
			c.Close()
			client.Close()
			t.Fatal("TCP connection has no half-close")
		}
		if err := cw.CloseWrite(); err != nil {
			c.Close()
			client.Close()
			t.Fatal(err)
		}
		if _, err := io.Copy(io.Discard, c); err != nil {
			c.Close()
			client.Close()
			t.Fatal(err)
		}
		c.Close()
		if err := client.DrainTCP(bounded); err != nil {
			client.Close()
			t.Fatal(err)
		}
		if client.PublicKey() != role.Public() {
			client.Close()
			t.Fatal("role key changed")
		}
		return client
	}
	c1 := connect(r1)
	c1.Close()
	// Stop the first relay, then use a fresh client with the same durable role
	// key through the independent second relay. No application bytes are replayed.
	close1()
	c2 := connect(r2)
	defer c2.Close()
	if s.lb.priv.Public() != serverKey.Public() {
		t.Fatal("server identity changed")
	}
	for range 2 {
		select {
		case err := <-accepted:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	// The learned peer route must identify the second configured relay locally.
	s.lb.mu.Lock()
	peer := s.lb.clients[role.Public()]
	route := s.lb.dm.Regions[peer.HomeDERP]
	s.lb.mu.Unlock()
	if route.Nodes[0].DERPPort != r2.Nodes[0].DERPPort {
		t.Fatal("peer kept stale relay home")
	}
}

type errUnexpectedPeer struct{}

func (errUnexpectedPeer) Error() string { return "unexpected authenticated peer identity" }

func newIntegrationRelay(t *testing.T) (*tailcfg.DERPRegion, func()) {
	t.Helper()
	d := derpserver.New(key.NewNode(), logger.Discard)
	mux := http.NewServeMux()
	mux.Handle("/derp", derpserver.Handler(d))
	mux.HandleFunc("/derp/latency-check", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	s := httptest.NewUnstartedServer(mux)
	s.StartTLS()
	ap := s.Listener.Addr().(*net.TCPAddr).AddrPort()
	hash := sha256.Sum256(s.TLS.Certificates[0].Certificate[0])
	r := testRegion(ap.Addr().String(), int(ap.Port()))
	r.Nodes[0].CertName = "sha256-raw:" + hex.EncodeToString(hash[:])
	var once sync.Once
	return r, func() { once.Do(func() { d.Close(); s.Close() }) }
}
