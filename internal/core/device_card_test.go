package core

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	qrcode "github.com/skip2/go-qrcode"
	"github.com/webkaz-labs/sobalink/internal/devicecard"
	"github.com/webkaz-labs/sobalink/internal/directlan"
	"github.com/webkaz-labs/sobalink/internal/lanlink"
	tailcat "github.com/webkaz-labs/sobalink/internal/routecat"
	"github.com/webkaz-labs/sobalink/internal/webui"
)

// A nonnil backend with no implemented methods panics if a read ever attempts
// transport access. These tests do not call Close on this deliberately inert Core.
type deviceCardForbiddenBackend struct{ NetworkBackend }

func deviceCardTestCore(t *testing.T) *Core {
	t.Helper()
	forbiddenWrite := func(string, []byte) error {
		t.Error("device card attempted a write")
		return errors.New("forbidden write")
	}
	c := &Core{ctx: context.Background(), node: deviceCardForbiddenBackend{}, atomicWrite: forbiddenWrite}
	c.profile.Settings.Network = "mixed"
	c.profile.Settings.Hostname = "private-hostname-sentinel"
	c.factory = func(string, string) (NetworkBackend, error) {
		t.Fatal("device card attempted network initialization")
		return nil, nil
	}
	c.lanFactory = func(*lanStore) (lanNetworkBackend, error) {
		t.Fatal("device card attempted relay initialization")
		return nil, nil
	}
	c.lanAddresses = func() ([]LANLocalAddress, error) {
		t.Fatal("device card attempted interface inspection")
		return nil, nil
	}
	c.lan = &lanStore{write: forbiddenWrite, state: lanState{Version: 1, Identity: lanlink.GenerateIdentity(), Selection: &LANSelection{Kind: "relay", Address: "192.0.2.1:443", CertificateSHA256: strings.Repeat("b", 64)}, Remotes: []lanlink.RemotePeer{{Address: tailcat.Addr("private-PSK-sentinel")}}, RelayIdentity: &lanlink.RelayIdentity{PrivateKeyPEM: []byte("private-relay-sentinel")}}}
	c.directLAN = &directLANStore{write: forbiddenWrite, state: directLANState{Version: directLANStateVersion, Identity: directlan.Identity{Seed: strings.Repeat("c", 64)}, Selection: DirectLANSelection{Listen: "127.0.0.1:55446", Prefixes: []string{"private-prefix-sentinel"}}, Peers: []directlan.Peer{{Name: "private-peer-sentinel"}}}}
	return c
}

func cardCommand(t *testing.T, c *Core, id, name string, payload any) (any, error) {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return c.Command(context.Background(), webui.Command{RequestID: id, Name: name, Payload: raw})
}
func cardJSONKeys(t *testing.T, value any) []string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
func requireCardKeys(t *testing.T, value any, want ...string) {
	t.Helper()
	sort.Strings(want)
	if got := cardJSONKeys(t, value); !reflect.DeepEqual(got, want) {
		t.Fatalf("response keys %v; want %v", got, want)
	}
}

func TestDeviceCardPublicAllowlistAndNoSideEffects(t *testing.T) {
	for _, mode := range []string{"lan", "direct-lan"} {
		for _, hint := range []bool{false, true} {
			t.Run(mode+map[bool]string{false: "/private", true: "/hint"}[hint], func(t *testing.T) {
				c := deviceCardTestCore(t)
				beforeLAN, _ := json.Marshal(c.lan.state)
				beforeDirect, _ := json.Marshal(c.directLAN.state)
				beforeProfile, _ := json.Marshal(c.profile)
				value, err := cardCommand(t, c, "export", "device-card.export", map[string]any{"mode": mode, "name": "Public alias", "includeEndpointHint": hint})
				if err != nil {
					t.Fatal(err)
				}
				view := value.(DeviceCardExportView)
				wantKey := c.lan.state.Identity.PublicKey()
				if mode == "direct-lan" {
					wantKey = c.directLAN.state.Identity.PublicKey()
				}
				if view.PublicKey != wantKey || view.Mode != mode || view.Name != "Public alias" {
					t.Fatal("wrong component identity", view)
				}
				keys := []string{"card", "version", "mode", "publicKey", "name", "verification", "freshness"}
				if hint {
					if mode == "lan" {
						keys = append(keys, "relay")
						requireCardKeys(t, view.Relay, "address", "certificateSHA256")
					} else {
						keys = append(keys, "endpoint")
					}
				}
				requireCardKeys(t, value, keys...)
				if view.Verification != "unverified" || view.Freshness != "unknown" {
					t.Fatal("export treated as identity proof", view)
				}
				if !hint && (view.Endpoint != "" || view.Relay != nil) {
					t.Fatal("endpoint was not opt-in")
				}
				parsed, err := devicecard.Parse(view.Text)
				if err != nil || !reflect.DeepEqual(parsed, view.Card) {
					t.Fatal("card does not match exported fields", err)
				}
				inspected, err := cardCommand(t, c, "inspect", "device-card.inspect", map[string]any{"card": view.Text, "expectedMode": mode})
				if err != nil {
					t.Fatal(err)
				}
				keys = []string{"version", "mode", "publicKey", "name", "contentDigest", "verification", "freshness"}
				if hint {
					if mode == "lan" {
						keys = append(keys, "relay")
					} else {
						keys = append(keys, "endpoint")
					}
				}
				requireCardKeys(t, inspected, keys...)
				inspection := inspected.(devicecard.Inspection)
				if inspection.Verification != "unverified" || inspection.Freshness != "unknown" {
					t.Fatal("card treated as identity proof", inspection)
				}
				exportedJSON, _ := json.Marshal(value)
				inspectedJSON, _ := json.Marshal(inspected)
				privateKey, _ := json.Marshal(c.lan.state.Identity.Key)
				psk, _ := json.Marshal(c.lan.state.Identity.PSK)
				for _, secret := range []string{"private-hostname-sentinel", "private-PSK-sentinel", "private-relay-sentinel", "private-prefix-sentinel", "private-peer-sentinel", c.directLAN.state.Identity.Seed, string(privateKey), string(psk)} {
					if strings.Contains(string(exportedJSON), secret) || strings.Contains(string(inspectedJSON), secret) {
						t.Fatal("private state escaped public allowlist")
					}
				}
				afterLAN, _ := json.Marshal(c.lan.state)
				afterDirect, _ := json.Marshal(c.directLAN.state)
				afterProfile, _ := json.Marshal(c.profile)
				if string(beforeLAN) != string(afterLAN) || string(beforeDirect) != string(afterDirect) || string(beforeProfile) != string(afterProfile) {
					t.Fatal("read changed saved state")
				}
				if len(c.requests) != 0 || len(c.inflightRequests) != 0 {
					t.Fatal("card entered result retention")
				}
			})
		}
	}
}

func TestDeviceCardAbsentStateStaysAbsent(t *testing.T) {
	c := openLANTestCore(t)
	before, err := os.ReadDir(c.dir)
	if err != nil {
		t.Fatal(err)
	}
	c.atomicWrite = func(string, []byte) error { t.Fatal("read attempted persistence"); return nil }
	for _, mode := range []string{"lan", "direct-lan"} {
		if _, err := cardCommand(t, c, "absent", "device-card.export", map[string]any{"mode": mode, "name": "Public alias"}); networkErrorCode(err) != "device_card_identity_required" {
			t.Fatal(mode, err)
		}
		text, err := devicecard.Encode(devicecard.Card{Version: 1, Mode: mode, PublicKey: strings.Repeat("a", 64), Name: "Public alias"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = cardCommand(t, c, "inspect-absent", "device-card.inspect", map[string]any{"card": text, "expectedMode": mode}); err != nil {
			t.Fatal(err)
		}
	}
	if c.lanStoreCopy() != nil || c.directLANStoreCopy() != nil || c.nodeCopy() != nil || c.profileCopy().Settings.Network != "none" {
		t.Fatal("read initialized identity or network")
	}
	for _, name := range []string{"lan.json", "direct-lan.json"} {
		if _, err := os.Stat(filepath.Join(c.dir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("read created identity file", name, err)
		}
	}
	after, err := os.ReadDir(c.dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != len(after) {
		t.Fatal("read created other state")
	}
}

func TestDeviceCardRecoveryAndInvalidRequests(t *testing.T) {
	for _, mode := range []string{"lan", "direct-lan"} {
		for _, failure := range []string{"store-recovery", "invalid-identity", "global-recovery", "invalid-version"} {
			t.Run(mode+"/"+failure, func(t *testing.T) {
				c := deviceCardTestCore(t)
				switch failure {
				case "global-recovery":
					c.networkFatal = "private-recovery-sentinel"
				case "store-recovery":
					if mode == "lan" {
						c.lan.routeRecovery = true
					} else {
						c.directLAN.recovery = true
					}
				case "invalid-identity":
					if mode == "lan" {
						c.lan.state.Identity = lanlink.Identity{}
					} else {
						c.directLAN.state.Identity = directlan.Identity{}
					}
				case "invalid-version":
					if mode == "lan" {
						c.lan.state.Version = 999
					} else {
						c.directLAN.state.Version = 999
					}
				}
				if value, err := cardCommand(t, c, "recovery", "device-card.export", map[string]any{"mode": mode, "name": "Public alias"}); networkErrorCode(err) != "device_card_recovery_required" || value != nil {
					t.Fatal(value, err)
				}
			})
		}
	}
	c := deviceCardTestCore(t)
	for _, mode := range []string{"", "mixed", "tailnet"} {
		if _, err := cardCommand(t, c, "badmode", "device-card.export", map[string]any{"mode": mode, "name": "Public alias"}); networkErrorCode(err) != "device_card_mode_required" {
			t.Fatal(mode, err)
		}
	}
	for _, name := range []string{"", " bad", "bad\u202ename", strings.Repeat("a", 81)} {
		if _, err := cardCommand(t, c, "badname", "device-card.export", map[string]any{"mode": "lan", "name": name}); networkErrorCode(err) != "device_card_invalid" {
			t.Fatal(err)
		}
	}
	value, err := cardCommand(t, c, "valid", "device-card.export", map[string]any{"mode": "lan", "name": "Public alias"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = cardCommand(t, c, "wrongmode", "device-card.inspect", map[string]any{"card": value.(DeviceCardExportView).Text, "expectedMode": "direct-lan"}); networkErrorCode(err) != "device_card_mode_mismatch" {
		t.Fatal(err)
	}
	for _, payload := range []string{`{"mode":"lan","name":"Public alias","extra":true}`, `{"mode":"lan","name":"Public alias"}{}`, `{"mode":"lan","name":"Public alias","qr":"true"}`} {
		if _, err := c.Command(context.Background(), webui.Command{RequestID: "invalid", Name: "device-card.export", Payload: json.RawMessage(payload)}); err == nil {
			t.Fatal("invalid payload accepted")
		}
	}
}

func TestDeviceCardLANStateVersions(t *testing.T) {
	for _, version := range []int{1, 2, 3, 4, 5} {
		c := deviceCardTestCore(t)
		c.lan.state.Version = version
		if _, err := cardCommand(t, c, "version", "device-card.export", map[string]any{"mode": "lan", "name": "Public alias"}); err != nil {
			t.Fatalf("supported LAN version %d rejected: %v", version, err)
		}
	}
	for _, version := range []int{-1, 0, 6, 999} {
		c := deviceCardTestCore(t)
		c.lan.state.Version = version
		if _, err := cardCommand(t, c, "version", "device-card.export", map[string]any{"mode": "lan", "name": "Public alias"}); networkErrorCode(err) != "device_card_recovery_required" {
			t.Fatalf("unsupported LAN version %d accepted: %v", version, err)
		}
	}
}

func TestDeviceCardExplicitUnavailableHint(t *testing.T) {
	for _, mode := range []string{"lan", "direct-lan"} {
		c := deviceCardTestCore(t)
		c.lan.state.Selection = nil
		c.directLAN.state.Selection.Listen = ""
		if _, err := cardCommand(t, c, "no-hint", "device-card.export", map[string]any{"mode": mode, "name": "Public alias"}); err != nil {
			t.Fatal("identity-only export failed", mode, err)
		}
		if value, err := cardCommand(t, c, "hint", "device-card.export", map[string]any{"mode": mode, "name": "Public alias", "includeEndpointHint": true}); value != nil || networkErrorCode(err) != "device_card_hint_unavailable" {
			t.Fatal("missing requested hint was silently omitted", mode, value, err)
		}
	}
}

func TestDeviceCardInspectionIsIndependentOfRecovery(t *testing.T) {
	c := deviceCardTestCore(t)
	c.networkFatal = "private-recovery-sentinel"
	c.lan.routeRecovery = true
	c.directLAN.recovery = true
	for _, mode := range []string{"lan", "direct-lan"} {
		text, err := devicecard.Encode(devicecard.Card{Version: 1, Mode: mode, PublicKey: strings.Repeat("a", 64), Name: "Public alias"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := cardCommand(t, c, "recovery-inspect", "device-card.inspect", map[string]any{"card": text, "expectedMode": mode}); err != nil {
			t.Fatal("local recovery blocked public text inspection", mode, err)
		}
	}
}

func TestDeviceCardRetryReadsCurrentIdentityAndHint(t *testing.T) {
	for _, mode := range []string{"lan", "direct-lan"} {
		t.Run(mode, func(t *testing.T) {
			c := deviceCardTestCore(t)
			payload := map[string]any{"mode": mode, "name": "Public alias", "includeEndpointHint": true}
			first, err := cardCommand(t, c, "same-id", "device-card.export", payload)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "lan" {
				c.lan.mu.Lock()
				c.lan.state.Identity = lanlink.GenerateIdentity()
				c.lan.state.Selection.Address = "198.51.100.1:443"
				c.lan.mu.Unlock()
			} else {
				c.directLAN.mu.Lock()
				c.directLAN.state.Identity = directlan.Identity{Seed: strings.Repeat("d", 64)}
				c.directLAN.state.Selection.Listen = "127.0.0.1:55447"
				c.directLAN.mu.Unlock()
			}
			second, err := cardCommand(t, c, "same-id", "device-card.export", payload)
			if err != nil {
				t.Fatal(err)
			}
			a, b := first.(DeviceCardExportView), second.(DeviceCardExportView)
			if a.PublicKey == b.PublicKey || a.Text == b.Text {
				t.Fatal("retained stale identity")
			}
			if mode == "lan" && a.Relay.Address == b.Relay.Address || mode == "direct-lan" && a.Endpoint == b.Endpoint {
				t.Fatal("retained stale endpoint")
			}
			if _, err := cardCommand(t, c, "inspect-id", "device-card.inspect", map[string]any{"card": a.Text, "expectedMode": mode}); err != nil {
				t.Fatal(err)
			}
			if _, err := cardCommand(t, c, "inspect-id", "device-card.inspect", map[string]any{"card": b.Text, "expectedMode": mode}); err != nil {
				t.Fatal("inspection was retained", err)
			}
			if len(c.requests) != 0 {
				t.Fatal("retained bounded pure read")
			}
		})
	}
}

func TestDeviceCardQRIsExplicitAndBounded(t *testing.T) {
	c := deviceCardTestCore(t)
	value, err := cardCommand(t, c, "qr", "device-card.export", map[string]any{"mode": "lan", "name": "Public alias", "qr": true})
	if err != nil {
		t.Fatal(err)
	}
	view := value.(DeviceCardExportView)
	requireCardKeys(t, view, "card", "version", "mode", "publicKey", "name", "verification", "freshness", "qr")
	code, err := qrcode.New(view.Text, qrcode.Medium)
	if err != nil || !reflect.DeepEqual(view.QR, code.Bitmap()) {
		t.Fatal("QR is not exact card text", err)
	}
	// A byte-mode input of the maximum protocol text length fits Medium. Real
	// cards can never exceed this bound; no authentication URL wrapper applies.
	maxCode, err := qrcode.New(strings.Repeat("a", devicecard.MaxEncodedBytes), qrcode.Medium)
	if err != nil {
		t.Fatal("maximum card cannot fit QR", err)
	}
	if len(maxCode.Bitmap()) > 185 {
		t.Fatal("QR exceeds maximum QR dimension")
	}
	maxCard := devicecard.Card{Version: 1, Mode: "lan", PublicKey: strings.Repeat("a", 64), Name: strings.Repeat("<", 80), Relay: &devicecard.RelayHint{Address: "[2001:db8:aaaa:bbbb:cccc:dddd:eeee:ffff]:65535", CertificateSHA256: strings.Repeat("b", 64)}}
	text, err := devicecard.Encode(maxCard)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = qrcode.New(text, qrcode.Medium); err != nil {
		t.Fatal(err)
	}
}

func TestDeviceCardConcurrentStoreReads(t *testing.T) {
	c := deviceCardTestCore(t)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 30; j++ {
				if _, err := cardCommand(t, c, "concurrent", "device-card.export", map[string]any{"mode": "lan", "name": "Public alias", "includeEndpointHint": true}); err != nil {
					t.Error(err)
				}
				if _, err := cardCommand(t, c, "concurrent-direct", "device-card.export", map[string]any{"mode": "direct-lan", "name": "Public alias", "includeEndpointHint": true}); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for j := 0; j < 30; j++ {
			c.lan.mu.Lock()
			c.lan.state.Identity = lanlink.GenerateIdentity()
			c.lan.mu.Unlock()
			c.directLAN.mu.Lock()
			c.directLAN.state.Selection.Listen = "127.0.0.1:55447"
			c.directLAN.mu.Unlock()
		}
	}()
	wg.Wait()
}
