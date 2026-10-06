package routecat

import (
	"context"
	"errors"
	"tailscale.com/tailcfg"
	"testing"
)

func TestRelayMetadataBeyondFourAndPresenceBudget(t *testing.T) {
	var regions []*tailcfg.DERPRegion
	for i := 0; i < 7; i++ {
		regions = append(regions, testRegion("127.0.0.1", 54446+i))
	}
	canonical, err := validateRegions(regions, false)
	if err != nil || len(canonical) != 7 {
		t.Fatal("metadata retained a four-candidate ceiling", err)
	}
	for _, budget := range []int{0, 4, 6} {
		var exhausted *RelayPresenceBudgetError
		if !errors.As(ValidateRelayPresenceBudget(len(regions), budget), &exhausted) {
			t.Fatal("active presence budget not enforced", budget)
		}
	}
	if err := ValidateRelayPresenceBudget(7, 7); err != nil {
		t.Fatal("explicit larger budget rejected", err)
	}
	for _, budget := range []int{-1, RelayRegionNamespace + 1} {
		if err := ValidateRelayPresenceBudget(1, budget); err == nil {
			t.Fatal("invalid resource representation accepted")
		}
	}
}
func TestRelayPresenceBudgetStopsBeforeRuntime(t *testing.T) {
	// The budget is rejected before any runtime construction. Use a cancelled
	// caller to ensure no sockets can be opened even if constructor order changes.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	server := &Server{Regions: []*tailcfg.DERPRegion{testRegion("127.0.0.1", 54446)}, RelayPresenceConnections: -1}
	if err := server.startLocked(ctx); err == nil {
		t.Fatal("invalid active relay resource budget accepted")
	}
	if server.lb != nil {
		t.Fatal("budget failure constructed an engine")
	}
}
