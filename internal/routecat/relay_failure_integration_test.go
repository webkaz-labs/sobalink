//go:build lanlink_integration

package routecat

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strings"
	"testing"
	"time"

	"tailscale.com/derp/derphttp"
	"tailscale.com/tailcfg"
	"tailscale.com/types/key"
	"tailscale.com/types/logger"
)

// The production engine must return its actual dial/TLS/admission failure, not
// the otherwise indistinguishable ten-second peer handshake timeout.
func TestGuardedDERPFailurePropagationIntegration(t *testing.T) {
	if os.Getenv("SOBALINK_RUN_GUARDED_INTEGRATION") != "1" {
		t.Skip("requires explicit native guarded integration")
	}
	for _, phase := range []string{"dial", "tls", "protocol"} {
		t.Run(phase, func(t *testing.T) {
			var region *tailcfg.DERPRegion
			switch phase {
			case "dial":
				listener, err := net.Listen("tcp4", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				ap := listener.Addr().(*net.TCPAddr).AddrPort()
				listener.Close()
				region = testRegion(ap.Addr().String(), int(ap.Port()))
			case "tls":
				var closeRelay func()
				region, closeRelay = newIntegrationRelay(t)
				defer closeRelay()
				region.Nodes[0].CertName = "sha256-raw:" + strings.Repeat("0", 64)
			case "protocol":
				relay := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusForbidden) }))
				defer relay.Close()
				ap := relay.Listener.Addr().(*net.TCPAddr).AddrPort()
				region = testRegion(ap.Addr().String(), int(ap.Port()))
				hash := sha256.Sum256(relay.TLS.Certificates[0].Certificate[0])
				region.Nodes[0].CertName = "sha256-raw:" + hex.EncodeToString(hash[:])
			}
			serverKey := key.NewNode()
			addr := (&ConnInfo{ServerPublic: NodePublic{serverKey.Public()}, ServerDiscoPublic: DiscoPublicForNode(serverKey), PresharedKey: NewPresharedKey(), Region: []*tailcfg.DERPRegion{region}}).Addr()
			client := &Client{Server: addr, DestinationPrefixes: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}, Logf: logger.Discard}
			defer client.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, err := client.Ping(ctx)
			var staged *derphttp.ConnectionError
			if !errors.As(err, &staged) || staged.DERPFailurePhase() != phase {
				t.Fatalf("actual %s failure was lost: %v", phase, err)
			}
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
				t.Fatal("relay failure arrived only as timeout/cancellation")
			}
			if got := relayDialAvailability(err, 0); got != (phase == "dial") {
				t.Fatalf("wrong availability for phase %s: %v", phase, err)
			}
		})
	}
}
