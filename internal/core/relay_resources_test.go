package core

import (
	"fmt"
	"net/netip"
	"os"
	"reflect"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/lanlink"
)

func TestRelayCapacityDefaultsOverridesAndValidation(t *testing.T) {
	defaults, err := selectedRelayResources(capacity.Defaults())
	if err != nil || defaults != (lanlink.RelayResources{PresenceConnections: 4, CandidateAttempts: 4, TLSConnections: 64, AdmissionConnections: 16}) {
		t.Fatal("old default choices changed", defaults, err)
	}
	for _, key := range relayResourceKeys {
		for _, choice := range []capacity.Choice{capacity.Unlimited(), capacity.Limited(0), capacity.Limited(-1)} {
			p := capacity.Defaults()
			p.Resources[key] = choice
			if err := validateSupportedCapacity(p); err == nil {
				t.Fatal("unbounded/invalid resource accepted", key)
			}
		}
	}
	for _, key := range []string{"relayPresenceConnections", "relayCandidateAttempts"} {
		p := capacity.Defaults()
		p.Resources[key] = capacity.Limited(65536)
		if err := validateSupportedCapacity(p); err == nil {
			t.Fatal("DERP identifier-space overflow accepted")
		}
	}
	p := capacity.Defaults()
	p.Resources["relayTLSConnections"] = capacity.Limited(128)
	p.Resources["relayAdmissionConnections"] = capacity.Limited(32)
	if r, err := selectedRelayResources(p); err != nil || r.TLSConnections != 128 || r.AdmissionConnections != 32 {
		t.Fatal("raised service budget ignored", r, err)
	}
}
func TestRelayMetadataBeyondFourAndCapacityEditsPreserveState(t *testing.T) {
	c := openLANTestCore(t)
	if err := c.configureLAN(testLANSelection()); err != nil {
		t.Fatal(err)
	}
	original := c.lanStoreCopy().copy()
	for i := 0; i < 6; i++ {
		candidate := extraRouteFixture()
		candidate.Relay.Address = netip.MustParseAddrPort(fmt.Sprintf("192.0.2.%d:443", 20+i))
		if err := c.editLANRoute(candidate, ""); err != nil {
			t.Fatal("stored candidate ceiling remains", err)
		}
	}
	store := c.lanStoreCopy()
	before, _ := os.ReadFile(store.path)
	p := capacity.Defaults()
	p.Resources["relayPresenceConnections"] = capacity.Limited(7)
	p.Resources["relayCandidateAttempts"] = capacity.Limited(6)
	p.Resources["relayTLSConnections"] = capacity.Limited(128)
	preview := mustCommand(t, c, "policy.preview", map[string]any{"policy": p}).(map[string]any)
	if preview["restartRequired"] != true {
		t.Fatal("next-start impact omitted")
	}
	mustCommand(t, c, "policy.apply", map[string]any{"policy": p, "expectedRevision": preview["revision"]})
	after, _ := os.ReadFile(store.path)
	if string(before) != string(after) || !reflect.DeepEqual(original.Trust, store.copy().Trust) || !reflect.DeepEqual(original.Identity, store.copy().Identity) || c.nodeCopy() != nil {
		t.Fatal("capacity edit changed pair/candidate data or started network")
	}
	loaded, err := readLANStore(store.path)
	if err != nil || len(loaded.copy().RouteCandidates) != 6 {
		t.Fatal("five-plus stored metadata failed reload", err)
	}
	if !c.relayResourcesView()["editable"].(bool) || !c.relayResourcesView()["restartRequired"].(bool) {
		t.Fatal("offline effective/restart state unclear")
	}
	b, _ := testLANBackend()
	c.mu.Lock()
	c.node = b
	c.mu.Unlock()
	changed := p.Clone()
	changed.Resources["relayTLSConnections"] = capacity.Limited(129)
	if _, err := command(c, randomID(), "policy.preview", map[string]any{"policy": changed}); networkErrorCode(err) != "network_restart_required" {
		t.Fatal("active budget edit was not refused", err)
	}
	if c.relayResourcesView()["editable"] != false || c.limit("resources", "relayTLSConnections") != 128 {
		t.Fatal("active budget was misreported or changed")
	}
}
func TestRelayResourceCatalogViewIsDetached(t *testing.T) {
	c := openLANTestCore(t)
	view := c.capacityView()
	keys := view["restartRequiredResources"].([]string)
	keys[0] = "synthetic-change"
	if c.capacityView()["restartRequiredResources"].([]string)[0] == "synthetic-change" {
		t.Fatal("public view aliases budget key validation")
	}
}
