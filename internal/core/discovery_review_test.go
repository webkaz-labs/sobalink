package core

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/webkaz-labs/sobalink/internal/capacity"
	"strings"
	"testing"
	"time"
)

func TestDiscoveryRefreshReportsPeerFramingCapacityAsPartial(t *testing.T) {
	p := newCorePair(t)
	mustCommand(t, p.b, "service.share", map[string]any{"name": "bounded", "network": "tcp", "ports": "8080", "peerIds": []string{"peer-a"}, "lifetime": "until-revoked", "discoverable": true})
	p.b.mu.Lock()
	p.b.capacity.Resources["pageBytes"] = capacity.Limited(1)
	p.b.mu.Unlock()
	result := mustCommand(t, p.a, "discovery.refresh", map[string]string{"peerId": "peer-b"}).(map[string]any)
	observations := result["observations"].([]map[string]any)
	if result["partial"] != true || len(observations) != 1 || observations[0]["state"] != "limited" || observations[0]["code"] != "discovery_capacity" {
		t.Fatal("capacity failure was indistinguishable from reachability failure", result)
	}
	if len(result["services"].([]map[string]any)) != 0 {
		t.Fatal("partial failure retained selectable services")
	}
}

func TestDiscoveryObservationFailureClearsSelectableCacheWithoutOfflineClaim(t *testing.T) {
	now := time.Now()
	c := &Core{confirmed: map[string]time.Time{"peer": now}, discovered: map[string][]RemoteService{"peer": {{ID: "sample"}}}}
	if got := c.discoveryObservation("peer"); got.State != "pending" {
		t.Fatal(got)
	}
	c.recordDiscoveryObservation("peer", nil)
	if got := c.discoveryObservation("peer"); got.State != "confirmed" || got.Services != 1 {
		t.Fatal(got)
	}
	c.recordDiscoveryObservation("peer", errors.New("unavailable"))
	if got := c.discoveryObservation("peer"); got.State != "unconfirmed" || got.Services != 0 {
		t.Fatal(got)
	}
	if len(c.discoveredViews()) != 0 || !c.confirmed["peer"].IsZero() {
		t.Fatal("failed query retained selectable confirmation")
	}
	c.recordDiscoveryObservation("peer", &localCommandError{"discovery_unsupported", "unsupported"})
	if got := c.discoveryObservation("peer"); got.State != "unsupported" {
		t.Fatal(got)
	}
	c.recordDiscoveryObservation("peer", &localCommandError{"discovery_capacity", "limited"})
	if got := c.discoveryObservation("peer"); got.State != "limited" {
		t.Fatal(got)
	}
	old := c.discoveryObservations["peer"]
	old.CheckedAt = now.Add(-16 * time.Second)
	c.discoveryObservations["peer"] = old
	if got := c.discoveryObservation("peer"); got.State != "stale" {
		t.Fatal(got)
	}
}

func TestDiscoveryGrantRotatesOnlyOnNewActivationAndClipsTaskLease(t *testing.T) {
	p := newCorePair(t)
	view := mustCommand(t, p.b, "service.share", map[string]any{"name": "leased", "network": "tcp", "ports": "8080", "peerIds": []string{"peer-a"}, "purpose": "web", "lifetime": "until-revoked", "discoverable": true, "owner": "job", "leaseSeconds": 30}).(map[string]any)
	id := view["id"].(string)
	first := p.b.permittedServices("peer-a")
	if len(first) != 1 || first[0].ID == id || first[0].Purpose != "web" || first[0].Lifetime != "finite" || first[0].ExpiresAt.IsZero() {
		t.Fatalf("incorrect scoped grant: %+v", first)
	}
	lease := p.b.active[id].leaseExpires.Load()
	if !first[0].ExpiresAt.Equal(*lease) {
		t.Fatal("advertised beyond task lease")
	}
	if got := p.b.permittedServices("other-peer"); len(got) != 0 {
		t.Fatal("advertised outside allowed peers")
	}
	if err := p.a.probePeer(context.Background(), "peer-b"); err != nil {
		t.Fatal(err)
	}
	rows := p.a.discoveredViews()
	if len(rows) != 1 || rows[0]["purpose"] != "web" || rows[0]["checkedAt"] == nil || rows[0]["revision"] == "" {
		t.Fatal("missing reviewed discovery fields", rows)
	}
	encoded, _ := json.Marshal(first)
	for _, private := range []string{"owner", "localPort", "loopbackHost", "peerIds", "leased", "job"} {
		if strings.Contains(string(encoded), private) {
			t.Fatal("private metadata leaked", private)
		}
	}
	withServiceOperation(p.b, func() { p.b.suspendServices(); p.b.revalidateServices(p.nb.state) })
	recovered := p.b.permittedServices("peer-a")
	if len(recovered) != 1 || recovered[0].ID != first[0].ID {
		t.Fatal("transport recovery changed grant identity")
	}
	mustCommand(t, p.b, "services.stop", map[string]any{"ids": []string{id}, "owner": "job"})
	spec := savedService(t, p.b, id).Configuration
	withServiceOperation(p.b, func() {
		if _, err := p.b.startServiceCommand(context.Background(), "service.share", savedStartPayload(spec, "job", 30)); err != nil {
			t.Fatal(err)
		}
	})
	restarted := p.b.permittedServices("peer-a")
	if len(restarted) != 1 || restarted[0].ID == first[0].ID {
		t.Fatal("fresh activation reused revoked grant identity")
	}
}

func TestDiscoveryReviewRejectsScopeSubstitutionAndShortenedLifetime(t *testing.T) {
	now := time.Now()
	base := RemoteService{ID: strings.Repeat("a", 32), Purpose: "web", Network: "tcp", Ports: "8080", Lifetime: "finite", ExpiresAt: now.Add(time.Hour), Application: "unverified"}
	review, err := decodeDiscoveryReview(encodeDiscoveryReview("peer-b", base, now))
	if err != nil {
		t.Fatal(err)
	}
	spec := ServiceSpec{Direction: "forward", PeerID: "peer-b", ServiceID: base.ID, Purpose: "web", Network: "tcp"}
	if !review.matchesRequest(spec, "8080") || !sameDiscoveredGrant(base, base) {
		t.Fatal("same reviewed service refused")
	}
	later := base
	later.ExpiresAt = base.ExpiresAt.Add(time.Hour)
	if !sameDiscoveredGrant(base, later) {
		t.Fatal("same grant renewal refused")
	}
	for _, alter := range []func(*RemoteService){
		func(s *RemoteService) { s.ID = strings.Repeat("b", 32) }, func(s *RemoteService) { s.Purpose = "ssh" },
		func(s *RemoteService) { s.Network = "udp" }, func(s *RemoteService) { s.Ports = "8081" },
		func(s *RemoteService) { s.ExpiresAt = s.ExpiresAt.Add(-time.Second) },
		func(s *RemoteService) { s.Lifetime = "until-revoked"; s.ExpiresAt = time.Time{} },
	} {
		changed := base
		alter(&changed)
		if sameDiscoveredGrant(base, changed) {
			t.Fatal("changed grant accepted", changed)
		}
	}
	spec.PeerID = "peer-c"
	if review.matchesRequest(spec, "8080") {
		t.Fatal("peer substitution accepted")
	}
	for _, checked := range []time.Time{time.Time{}, now.Add(-16 * time.Second), now.Add(6 * time.Second)} {
		if freshDiscoveryCheck(checked, now) {
			t.Fatal("invalid freshness accepted", checked)
		}
	}
}

func TestDiscoveryReviewedStartRejectsChangedGrantBeforeOpeningListener(t *testing.T) {
	p := newCorePair(t)
	started := mustCommand(t, p.b, "service.share", map[string]any{"name": "web", "network": "tcp", "ports": "8080", "purpose": "web", "peerIds": []string{"peer-a"}, "lifetime": "finite", "ttlSeconds": 3600, "discoverable": true}).(map[string]any)
	if err := p.a.probePeer(context.Background(), "peer-b"); err != nil {
		t.Fatal(err)
	}
	row := p.a.discoveredViews()[0]
	input := map[string]any{"name": "selected", "network": "tcp", "ports": "8080", "purpose": "web", "peerId": "peer-b", "lifetime": "until-stopped", "serviceId": row["id"], "serviceRevision": row["revision"]}
	withServiceOperation(p.b, func() { p.b.mu.Lock(); p.b.active[started["id"].(string)].discoveryID = randomID(); p.b.mu.Unlock() })
	_, err := command(p.a, randomID(), "service.connect", input)
	if networkErrorCode(err) != "discovery_review_changed" {
		t.Fatal("replaced grant was not rejected before listener", err)
	}
	if len(p.a.profileCopy().Services) != 0 {
		t.Fatal("failed selection created saved configuration")
	}
	delete(input, "serviceRevision")
	if _, err = command(p.a, randomID(), "service.connect", input); networkErrorCode(err) != "discovery_review_changed" {
		t.Fatal("missing review accepted", err)
	}
}
