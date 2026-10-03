package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/tailscale/tailcat"
	"github.com/webkaz-labs/sobalink/internal/lanlink"
	"tailscale.com/tailcfg"
)

type localTestAddress string

func (a localTestAddress) String() string { return string(a) }
func (localTestAddress) Network() string  { return "ip" }

func TestLANAddressChoicesArePrivateUpBoundedAndReadOnly(t *testing.T) {
	interfaces := func() ([]net.Interface, error) {
		return []net.Interface{{Name: "fixture1", Flags: net.FlagUp}, {Name: "down"}, {Name: "loop", Flags: net.FlagUp | net.FlagLoopback}, {Name: "fixture0", Flags: net.FlagUp}}, nil
	}
	addrs := func(i net.Interface) ([]net.Addr, error) {
		if i.Name == "down" || i.Name == "loop" {
			t.Fatal("queried an ineligible interface")
		}
		if i.Name == "fixture0" {
			return []net.Addr{localTestAddress("fd00::10/64")}, nil
		}
		return []net.Addr{localTestAddress("192.168.50.10/24"), localTestAddress("192.168.50.10/24"), localTestAddress("::ffff:192.168.50.10/120"), localTestAddress("127.0.0.1/8"), localTestAddress("169.254.1.2/16"), localTestAddress("fe80::10/64"), localTestAddress("192.0.2.10/24"), localTestAddress("100.64.0.1/10"), localTestAddress("0.0.0.0/0"), localTestAddress("224.0.0.1/4"), localTestAddress("invalid")}, nil
	}
	choices, err := readLANAddresses(interfaces, addrs)
	want := []LANLocalAddress{{"fixture0", "fd00::10"}, {"fixture1", "192.168.50.10"}}
	if err != nil || !reflect.DeepEqual(choices, want) {
		t.Fatalf("address choices = %v, %v", choices, err)
	}
	c := openLANTestCore(t)
	c.lanAddresses = func() ([]LANLocalAddress, error) { return choices, nil }
	before, _ := os.ReadFile(filepath.Join(c.dir, "sobalink.json"))
	result := mustCommand(t, c, "lan.addresses", map[string]any{}).(map[string]any)
	if !reflect.DeepEqual(result["addresses"], want) || c.nodeCopy() != nil || c.lanStoreCopy() != nil {
		t.Fatal("reading choices changed identity or runtime")
	}
	after, _ := os.ReadFile(filepath.Join(c.dir, "sobalink.json"))
	if !bytes.Equal(before, after) {
		t.Fatal("reading choices changed the saved configuration")
	}
	if _, err := os.Stat(filepath.Join(c.dir, "lan.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("reading choices created private identity state")
	}
	for _, fail := range []bool{false, true} {
		_, err := readLANAddresses(interfaces, func(net.Interface) ([]net.Addr, error) {
			if fail {
				return nil, errors.New("synthetic enumeration failure")
			}
			var many []net.Addr
			for i := 0; i <= maxLANAddressChoices; i++ {
				many = append(many, localTestAddress(fmt.Sprintf("10.0.0.%d/24", i)))
			}
			return many, nil
		})
		if err == nil {
			t.Fatal("unbounded or partially failed enumeration accepted")
		}
	}
}

func testInspectableInvitation(recipient string) lanlink.Invitation {
	id := lanlink.GenerateIdentity()
	address, pin := "127.0.0.1", strings.Repeat("a", 64)
	region := &tailcfg.DERPRegion{RegionID: 1, RegionCode: "1", Nodes: []*tailcfg.DERPNode{{Name: address, RegionID: 1, HostName: address, CertName: "sha256-raw:" + pin, IPv4: address, IPv6: "none", DERPPort: 48443, STUNPort: -1}}}
	capability := (&tailcat.ConnInfo{ServerPublic: tailcat.NodePublic{NodePublic: id.Key.Public()}, ServerDiscoPublic: tailcat.DiscoPublicForNode(id.Key), PresharedKey: id.PSK, Region: []*tailcfg.DERPRegion{region}}).Addr()
	return lanlink.Invitation{Version: 1, Host: lanlink.PeerOffer{Peer: lanlink.Peer{Key: id.PublicKey(), Name: "Fixture host"}, Address: capability}, RecipientKey: recipient, Relay: lanlink.TrustedRelay{Address: netip.MustParseAddrPort("127.0.0.1:48443"), CertificateSHA256: pin}, Expires: time.Now().Add(time.Minute), Token: strings.Repeat("A", 43)}
}

func TestLANInspectionIsPublicAndHasNoSetupSideEffects(t *testing.T) {
	c := openLANTestCore(t)
	inspect := func(invitation string) (any, error) {
		return command(c, randomID(), "lan.inspect", map[string]any{"invitation": invitation})
	}
	inv := testInspectableInvitation(strings.Repeat("b", 64))
	raw, _ := json.Marshal(inv)
	if _, err := inspect(string(raw)); networkErrorCode(err) != "lan_identity_required" || c.lanStoreCopy() != nil {
		t.Fatal("inspection generated an identity before explicit setup")
	}
	key := mustCommand(t, c, "lan.identity", map[string]any{}).(map[string]string)["publicKey"]
	inv = testInspectableInvitation(key)
	raw, _ = json.Marshal(inv)
	before, _ := os.ReadFile(filepath.Join(c.dir, "lan.json"))
	value, err := inspect(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	result := value.(map[string]any)
	if result["hostPublicKey"] != inv.Host.Peer.Key || result["recipientPublicKey"] != key || result["recipientMatches"] != true || result["relay"].(LANSelection).Address != inv.Relay.Address.String() {
		t.Fatal("inspection omitted reviewed public identity or relay")
	}
	public, _ := json.Marshal(value)
	for _, secret := range []string{string(inv.Host.Address), inv.Token, "preshared", "client_private"} {
		if strings.Contains(string(public), secret) {
			t.Fatal("inspection exposed a private pairing field")
		}
	}
	for name, mutate := range map[string]func(*lanlink.Invitation){
		"recipient":  func(i *lanlink.Invitation) { i.RecipientKey = strings.Repeat("b", 64) },
		"expiry":     func(i *lanlink.Invitation) { i.Expires = time.Now().Add(-time.Second) },
		"relay":      func(i *lanlink.Invitation) { i.Relay.CertificateSHA256 = strings.Repeat("b", 64) },
		"capability": func(i *lanlink.Invitation) { i.Host.Address = "invalid" },
	} {
		t.Run(name, func(t *testing.T) {
			bad := inv
			mutate(&bad)
			raw, _ := json.Marshal(bad)
			_, err := inspect(string(raw))
			code := "lan_invitation_invalid"
			if name == "recipient" {
				code = "lan_invitation_recipient_mismatch"
			}
			if networkErrorCode(err) != code {
				t.Fatalf("inspection error code = %q", networkErrorCode(err))
			}
		})
	}
	for _, malformed := range []string{"{", string(raw) + "{}", strings.Repeat("x", maxLANInvitation+1)} {
		if _, err := inspect(malformed); networkErrorCode(err) != "lan_invitation_invalid" {
			t.Fatal("malformed invitation was accepted")
		}
	}
	after, _ := os.ReadFile(filepath.Join(c.dir, "lan.json"))
	if !bytes.Equal(before, after) || c.nodeCopy() != nil || c.profileCopy().Settings.Network != "none" || c.lanStoreCopy().copy().Selection != nil {
		t.Fatal("inspection changed saved identity, relay selection or runtime")
	}
}

type trackedRelayClose struct{ closed bool }

func (r *trackedRelayClose) Close() error { r.closed = true; return nil }

func TestLANHostReadinessAndApplicationStopUseWholeCoreLifecycle(t *testing.T) {
	c := openLANTestCore(t)
	b, engine := testLANBackend()
	relay := &trackedRelayClose{}
	b.start = func() (io.Closer, error) { return relay, nil }
	c.lanFactory = func(*lanStore) (lanNetworkBackend, error) { return b, nil }
	selection := &LANSelection{Kind: "host", Address: "192.168.50.10:48443"}
	if err := c.configureLAN(selection); err != nil {
		t.Fatal(err)
	}
	status := c.lanStatus()
	if status["configured"] != true || status["listenerReady"] != false || status["relayReady"] != false {
		t.Fatal("saved host setup was presented as a running listener")
	}
	mustCommand(t, c, "network.configure", map[string]any{"mode": "lan", "lan": selection})
	status = c.lanStatus()
	if status["listenerReady"] != true || status["relayReady"] != true || status["path"] != "unknown" {
		t.Fatal("ready host listener lost its distinction from peer reachability")
	}
	snapshot, err := c.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, peer := range snapshot["peers"].([]map[string]any) {
		if peer["online"] != false {
			t.Fatal("host listener readiness claimed remote peer reachability")
		}
	}
	mustCommand(t, c, "service.share", map[string]any{"name": "fixture-share", "network": "tcp", "ports": "8080", "peerIds": []string{engine.peers[0].Key}, "ttlSeconds": 60})
	if _, err := command(c, randomID(), "application.stop", map[string]any{"peerId": engine.peers[0].Key}); err == nil || c.ctx.Err() != nil {
		t.Fatal("remote-target stop payload stopped the local app")
	}
	before, _ := os.ReadFile(filepath.Join(c.dir, "lan.json"))
	value := mustCommand(t, c, "application.stop", map[string]any{}).(map[string]string)
	if value["state"] != "stopping" || c.ctx.Err() == nil || c.lanStatus()["relayReady"] != false {
		t.Fatal("stop did not report shutdown and close admission immediately")
	}
	if _, err := command(c, randomID(), "network.configure", map[string]any{"mode": "tailnet"}); err == nil {
		t.Fatal("stopped process accepted a different backend")
	}
	// The executable observes Done and calls Close; test that same owner path.
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if !relay.closed || !engine.closed || len(c.active) != 0 {
		t.Fatal("whole-Core shutdown retained relay, transport or active shares")
	}
	after, _ := os.ReadFile(filepath.Join(c.dir, "lan.json"))
	if !bytes.Equal(before, after) {
		t.Fatal("stopping the app removed its saved identity or configuration")
	}
}

func TestLANFailedHostStartKeepsConfiguredStateWithoutReadiness(t *testing.T) {
	c := openLANTestCore(t)
	b, _ := testLANBackend()
	b.start = func() (io.Closer, error) { return nil, errors.New("synthetic listener failure") }
	c.lanFactory = func(*lanStore) (lanNetworkBackend, error) { return b, nil }
	_, err := command(c, randomID(), "network.configure", map[string]any{"mode": "lan", "lan": LANSelection{Kind: "host", Address: "192.168.50.10:48443"}})
	if err == nil {
		t.Fatal("failed listener start reported success")
	}
	state, err := c.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	lan := state["lan"].(map[string]any)
	if lan["configured"] != true || lan["listenerReady"] != false || lan["relayReady"] != false || state["self"].(map[string]any)["status"] != "error" || c.attemptedNetwork != "lan" {
		t.Fatal("failed startup conflated persisted configuration with readiness or lost the process latch")
	}
}
