package core

import (
	"errors"
	"net/netip"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/lanlink"
)

func extraRouteFixture() lanlink.RouteCandidate {
	return lanlink.RouteCandidate{Relay: lanlink.TrustedRelay{Address: netip.MustParseAddrPort("192.0.2.20:443"), CertificateSHA256: strings.Repeat("b", 64)}, Scope: "external"}
}
func TestLANRouteMigrationPreservesIdentityAndSingleton(t *testing.T) {
	c := openLANTestCore(t)
	if err := c.configureLAN(testLANSelection()); err != nil {
		t.Fatal(err)
	}
	store := c.lanStoreCopy()
	before := store.copy()
	if before.Version != 1 {
		t.Fatal("legacy migrated implicitly")
	}
	initial, err := ownLANCandidates(before)
	if err != nil || len(initial) != 1 {
		t.Fatal("legacy widened", err)
	}
	if err := c.editLANRoute(extraRouteFixture(), ""); err != nil {
		t.Fatal(err)
	}
	after := store.copy()
	if after.Version != 2 || len(after.RouteCandidates) != 1 || !reflect.DeepEqual(before.Identity, after.Identity) || !reflect.DeepEqual(before.Trust, after.Trust) || !reflect.DeepEqual(before.Remotes, after.Remotes) || !reflect.DeepEqual(before.Selection, after.Selection) {
		t.Fatal("migration changed existing pair/selection")
	}
	reloaded, err := readLANStore(store.path)
	if err != nil || !reflect.DeepEqual(reloaded.copy(), after) {
		t.Fatal("route state did not survive reload", err)
	}
	if err := c.editLANRoute(extraRouteFixture(), ""); err != nil || len(store.copy().RouteCandidates) != 1 {
		t.Fatal("repeat duplicated route", err)
	}
	if err := c.editLANRoute(lanlink.RouteCandidate{}, extraRouteFixture().ID()); err != nil {
		t.Fatal(err)
	}
	if len(store.copy().RouteCandidates) != 0 || store.copy().Version != 2 {
		t.Fatal("removal lost migration barrier")
	}
}
func TestLANRouteConfigurationDoesNotActivateAndIsBounded(t *testing.T) {
	c := openLANTestCore(t)
	if err := c.configureLAN(testLANSelection()); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		v := extraRouteFixture()
		v.Relay.Address = netip.AddrPortFrom(v.Relay.Address.Addr(), uint16(443+i))
		if err := c.editLANRoute(v, ""); err != nil {
			t.Fatal(err)
		}
	}
	v := extraRouteFixture()
	v.Relay.Address = netip.MustParseAddrPort("192.0.2.21:443")
	if err := c.editLANRoute(v, ""); err == nil {
		t.Fatal("candidate budget bypass")
	}
	if c.nodeCopy() != nil || c.profileCopy().Settings.Network != "none" {
		t.Fatal("route setup activated network")
	}
	copy := c.lanStoreCopy().copy()
	copy.RouteCandidates[0].Scope = "local"
	if err := validateLANState(copy); err == nil {
		t.Fatal("public endpoint accepted as local")
	}
	copy = c.lanStoreCopy().copy()
	copy.Version = 1
	if err := validateLANState(copy); err == nil {
		t.Fatal("downgraded route config accepted")
	}
}
func TestLANRouteSaveFailureAndUncertainDurability(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		t.Run(map[bool]string{false: "not-published", true: "published-uncertain"}[uncertain], func(t *testing.T) {
			c := openLANTestCore(t)
			if err := c.configureLAN(testLANSelection()); err != nil {
				t.Fatal(err)
			}
			store := c.lanStoreCopy()
			before, _ := os.ReadFile(store.path)
			store.write = func(path string, data []byte) error {
				if uncertain {
					if err := config.AtomicWrite(path, data); err != nil {
						return err
					}
					return config.ErrAtomicCommitted
				}
				return errors.New("synthetic write failure")
			}
			err := c.editLANRoute(extraRouteFixture(), "")
			if err == nil {
				t.Fatal("save failure hidden")
			}
			if uncertain {
				if !store.routesNeedRecovery() {
					t.Fatal("missing recovery latch")
				}
				if _, err := c.newLANBackend(store); !errors.Is(err, config.ErrAtomicRecovery) {
					t.Fatal("uncertain route activated", err)
				}
				if err := c.editLANRoute(lanlink.RouteCandidate{}, extraRouteFixture().ID()); !errors.Is(err, config.ErrAtomicRecovery) {
					t.Fatal("uncertain state overwritten", err)
				}
				reloaded, err := readLANStore(store.path)
				if err != nil || reloaded.routesNeedRecovery() || len(reloaded.copy().RouteCandidates) != 1 {
					t.Fatal("explicit reload failed", err)
				}
			} else {
				after, _ := os.ReadFile(store.path)
				if string(before) != string(after) || len(store.copy().RouteCandidates) != 0 || store.copy().Version != 1 {
					t.Fatal("failed save activated draft")
				}
			}
		})
	}
}
