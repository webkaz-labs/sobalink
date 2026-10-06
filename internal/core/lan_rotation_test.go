package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/lanlink"
)

func TestLANHostRotationRequiresExplicitAcknowledgement(t *testing.T) {
	c := openLANTestCore(t)
	host := &LANSelection{Kind: "host", Address: "192.168.50.10:48443"}
	if err := c.configureLANWithOptions(host, true); networkErrorCode(err) != "lan_certificate_rotation_invalid" || c.lanStoreCopy() != nil {
		t.Fatal("new host accepted replacement", err)
	}
	if err := c.configureLAN(host); err != nil {
		t.Fatal(err)
	}
	store := c.lanStoreCopy()
	initial := store.copy()
	portOnly := &LANSelection{Kind: "host", Address: "192.168.50.10:48444"}
	if err := c.configureLAN(portOnly); err != nil {
		t.Fatal(err)
	}
	afterPort := store.copy()
	if afterPort.Selection.CertificateSHA256 != initial.Selection.CertificateSHA256 || afterPort.RelayIdentity.Key.Public() != initial.RelayIdentity.Key.Public() {
		t.Fatal("port-only change rotated certificate or relay key")
	}
	changed := &LANSelection{Kind: "host", Address: "192.168.50.11:48444"}
	before, _ := os.ReadFile(store.path)
	if err := c.configureLAN(changed); networkErrorCode(err) != "lan_certificate_rotation_required" {
		t.Fatal("IP change silently rotated", err)
	}
	after, _ := os.ReadFile(store.path)
	if !bytes.Equal(before, after) {
		t.Fatal("refused rotation changed saved state")
	}
	if err := c.configureLANWithOptions(changed, true); err != nil {
		t.Fatal(err)
	}
	rotated := store.copy()
	if rotated.Selection.CertificateSHA256 == initial.Selection.CertificateSHA256 || rotated.Identity.PublicKey() != initial.Identity.PublicKey() {
		t.Fatal("rotation must change only relay identity, preserving device identity")
	}
	if _, err := rotated.RelayIdentity.Endpoint(netip.MustParseAddrPort(changed.Address)); err != nil {
		t.Fatal(err)
	}
	if err := c.configureLAN(changed); err != nil {
		t.Fatal(err)
	}
	if store.copy().Selection.CertificateSHA256 != rotated.Selection.CertificateSHA256 {
		t.Fatal("ordinary repeat rotated again")
	}
}

func TestLANHostRotationKeepsPairsAndRunningState(t *testing.T) {
	for _, mode := range []string{"pairs", "active", "save-failure"} {
		t.Run(mode, func(t *testing.T) {
			c := openLANTestCore(t)
			host := &LANSelection{Kind: "host", Address: "192.168.50.10:48443"}
			if err := c.configureLAN(host); err != nil {
				t.Fatal(err)
			}
			store := c.lanStoreCopy()
			expected := ""
			switch mode {
			case "pairs":
				store.mu.Lock()
				store.state.Remotes = []lanlink.RemotePeer{{Peer: lanlink.Peer{Key: strings.Repeat("a", 64)}}}
				store.mu.Unlock()
				expected = "lan_relay_pairs_present"
			case "active":
				b, _ := testLANBackend()
				c.lanFactory = func(*lanStore) (lanNetworkBackend, error) { return b, nil }
				mustCommand(t, c, "network.configure", map[string]any{"mode": "lan", "lan": host})
				expected = "network_restart_required"
			case "save-failure":
				store.write = func(string, []byte) error { return errors.New("synthetic save failure") }
			}
			before, _ := os.ReadFile(store.path)
			pin := store.copy().Selection.CertificateSHA256
			_, err := command(c, randomID(), "network.configure", map[string]any{"mode": "lan", "lan": host, "rotateCertificate": true})
			if err == nil || networkErrorCode(err) != expected {
				t.Fatalf("rotation restriction = %v, %q", err, networkErrorCode(err))
			}
			after, _ := os.ReadFile(store.path)
			if !bytes.Equal(before, after) || store.copy().Selection.CertificateSHA256 != pin {
				t.Fatal("blocked rotation changed saved identity")
			}
			if mode == "pairs" && len(store.copy().Remotes) != 1 {
				t.Fatal("rotation removed saved pairs")
			}
		})
	}
}

func TestLANCertificateStatusHasPublicDatesWithoutMutation(t *testing.T) {
	c := openLANTestCore(t)
	if err := c.configureLAN(&LANSelection{Kind: "host", Address: "192.168.50.10:48443"}); err != nil {
		t.Fatal(err)
	}
	store := c.lanStoreCopy()
	saved := store.copy()
	before, _ := os.ReadFile(store.path)
	public := localRelayCertificateStatus(saved.RelayIdentity, time.Now())
	expires := public["notAfter"].(time.Time)
	begins := public["notBefore"].(time.Time)
	for _, test := range []struct {
		now   time.Time
		state string
	}{{time.Now(), "valid"}, {expires.Add(-30 * 24 * time.Hour), "expiring"}, {expires, "expired"}, {begins.Add(-time.Second), "not-yet-valid"}} {
		if got := localRelayCertificateStatus(saved.RelayIdentity, test.now)["state"]; got != test.state {
			t.Fatalf("certificate state = %v, want %s", got, test.state)
		}
	}
	status, err := c.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(status["lan"])
	for _, private := range []string{"PRIVATE KEY", "certificate_pem", "private_key", "preshared"} {
		if strings.Contains(string(raw), private) {
			t.Fatal("certificate status exposed private material")
		}
	}
	if status["lan"].(map[string]any)["certificate"].(map[string]any)["state"] != "valid" {
		t.Fatal("certificate status missing")
	}
	after, _ := os.ReadFile(store.path)
	if !bytes.Equal(before, after) || c.nodeCopy() != nil {
		t.Fatal("certificate status changed state or started network")
	}
}
