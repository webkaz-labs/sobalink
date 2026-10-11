package core

import (
	"context"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/resourcecatalog"
	"github.com/webkaz-labs/sobalink/internal/transportorigin"
)

func TestResourceCatalogDiscoveryHistoricalUntilProvenance(t *testing.T) {
	s, l, budget := catalogLocalFixture(t, resourcecatalog.RemoteService)
	s.PeerKey = "synthetic-peer"
	checked := time.UnixMilli(1700000000000)
	services := []RemoteService{{ID: "synthetic-activation", Purpose: "web", Network: "tcp", Ports: "8080", Lifetime: "finite", ExpiresAt: checked.Add(time.Minute), Application: "unverified"}}
	got := resourceCatalogProjectDiscovered(context.Background(), s, services, checked, l, budget)
	if got.State != "stale" || got.CheckedAt != checked.UnixMilli() || len(got.Rows) != 1 || got.Rows[0].Identity.Lifetime != resourcecatalog.Activation {
		t.Fatal("cached publication was promoted to current authority")
	}
	view := resourcecatalog.SourceView{Selection: s, State: got.State, CheckedAt: got.CheckedAt, Revision: got.Revision, Complete: true, Total: got.Total, Rows: got.Rows}
	for _, now := range []int64{checked.UnixMilli(), checked.Add(15 * time.Second).UnixMilli(), checked.Add(15*time.Second + time.Millisecond).UnixMilli(), checked.Add(-5 * time.Second).UnixMilli(), checked.Add(-5*time.Second - time.Millisecond).UnixMilli()} {
		if len(resourcecatalog.Workflows(view, got.Rows[0], now, l)) != 0 {
			t.Fatal("unproven publication became actionable at clock boundary")
		}
	}
	empty := resourceCatalogProjectDiscovered(context.Background(), s, nil, checked, l, &resourceCatalogBudget{rows: 1, bytes: l.MaxBytes})
	if empty.State != "stale" || empty.Total == nil || *empty.Total != 0 || empty.Rows == nil {
		t.Fatal("confirmed historical empty became current or unconfirmed")
	}
}

func TestResourceCatalogDiscoveryLimitsAndUnsupportedContext(t *testing.T) {
	s, l, _ := catalogLocalFixture(t, resourcecatalog.RemoteService)
	s.PeerKey = "synthetic-peer"
	checked := time.UnixMilli(1700000000000)
	services := []RemoteService{{ID: "synthetic-activation", Purpose: "web", Network: "tcp", Ports: "8080", Lifetime: "until-revoked", Application: "unverified"}}
	got := resourceCatalogProjectDiscovered(context.Background(), s, services, checked, l, &resourceCatalogBudget{rows: 1, bytes: 1200})
	if got.State != "limited" || len(got.Rows) != 0 {
		t.Fatal("review expansion not pre-bounded")
	}
	c := &Core{ctx: context.Background()}
	got, origin := c.resourceCatalogDiscoveredServices(context.Background(), s, l, &resourceCatalogBudget{rows: 1, bytes: l.MaxBytes})
	if got.State != "unavailable" || origin != nil || len(got.Rows) != 0 {
		t.Fatal("missing identity treated as empty/unsupported")
	}
}

func TestResourceCatalogDiscoveryOriginalScalarFence(t *testing.T) {
	checked := time.UnixMilli(1700000000000)
	observation := DiscoveryObservation{State: "confirmed", CheckedAt: checked, Services: 0}
	c := &Core{ctx: context.Background(), confirmed: map[string]time.Time{"synthetic-peer": checked}, discoveryObservations: map[string]DiscoveryObservation{"synthetic-peer": observation}, discovered: map[string][]RemoteService{"synthetic-peer": {}}}
	o := &resourceCatalogDiscoveryOrigin{peer: "synthetic-peer", confirmed: checked, observation: observation, services: []RemoteService{}}
	if !c.resourceCatalogDiscoveryScalarsCurrent(o) {
		t.Fatal("unchanged scalar capture rejected")
	}
	c.lanStartWriteRevision.Add(1)
	if c.resourceCatalogDiscoveryScalarsCurrent(o) {
		t.Fatal("write generation change accepted")
	}
	c.lanStartWriteRevision.Store(0)
	c.managedDenied = map[string]bool{"synthetic-peer": true}
	if c.resourceCatalogDiscoveryScalarsCurrent(o) {
		t.Fatal("denied peer retained")
	}
	c.managedDenied = nil
	c.discoveryObservations["synthetic-peer"] = DiscoveryObservation{State: "unconfirmed", CheckedAt: checked.Add(time.Second)}
	if c.resourceCatalogDiscoveryScalarsCurrent(o) {
		t.Fatal("failed refresh retained prior rows")
	}
}

// This fake tests cache attribution only; it never admits transport work.
type discoveryCacheTestOrigin struct {
	token   *transportorigin.Token
	stopped chan struct{}
}

func (o *discoveryCacheTestOrigin) Identity() *transportorigin.Token { return o.token }
func (o *discoveryCacheTestOrigin) StopRequested() <-chan struct{}   { return o.stopped }
func (o *discoveryCacheTestOrigin) Acquire(context.Context) (transportorigin.Lease, error) {
	return nil, transportorigin.ErrStopped
}
func (o *discoveryCacheTestOrigin) AcquirePublication(context.Context) (transportorigin.Lease, error) {
	return nil, transportorigin.ErrStopped
}
func TestResourceCatalogDiscoveryRetainsPublicationOrigin(t *testing.T) {
	original := &discoveryCacheTestOrigin{transportorigin.NewToken(), make(chan struct{})}
	replacement := &discoveryCacheTestOrigin{transportorigin.NewToken(), make(chan struct{})}
	a := &peerHTTPApplication{origins: []peerHTTPApplicationOrigin{{origin: original}}}
	retained := a.discoveryOrigin()
	if retained.Identity() != original.Identity() {
		t.Fatal("publication origin lost")
	}
	c := &Core{ctx: context.Background(), discoveredOrigins: map[string]transportorigin.Origin{"peer": retained}}
	o := &resourceCatalogDiscoveryOrigin{peer: "peer", transport: retained}
	if !c.resourceCatalogDiscoveryScalarsCurrent(o) {
		t.Fatal("original attribution rejected")
	}
	c.discoveredOrigins["peer"] = replacement
	if c.resourceCatalogDiscoveryScalarsCurrent(o) {
		t.Fatal("replacement generation accepted old publication")
	}
	c.discoveredOrigins["peer"] = original
	close(original.stopped)
	if c.resourceCatalogDiscoveryScalarsCurrent(o) {
		t.Fatal("stopped publication accepted")
	}
	delete(c.discoveredOrigins, "peer")
	if c.resourceCatalogDiscoveryScalarsCurrent(o) {
		t.Fatal("removed publication accepted")
	}
}

func TestResourceCatalogDiscoveryEmptyOutcomesKeepScalarFence(t *testing.T) {
	for _, state := range []string{"unsupported", "limited"} {
		t.Run(state, func(t *testing.T) {
			checked := time.UnixMilli(1700000000000)
			observation := DiscoveryObservation{State: state, CheckedAt: checked}
			c := &Core{ctx: context.Background(), discoveryObservations: map[string]DiscoveryObservation{"peer": observation}}
			// Failed discovery clears its rows/origin, but retains its authenticated
			// historical empty outcome until the next observation or trust change.
			o := &resourceCatalogDiscoveryOrigin{peer: "peer", observation: observation, services: []RemoteService{}}
			if !c.resourceCatalogDiscoveryScalarsCurrent(o) {
				t.Fatal("empty historical outcome requires successful publication origin")
			}
			c.trustGeneration++
			if c.resourceCatalogDiscoveryScalarsCurrent(o) {
				t.Fatal("re-pair retained historical empty outcome")
			}
		})
	}
}
