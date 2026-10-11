//go:build directlan_integration

package core

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/resourcegrant"
	"github.com/webkaz-labs/sobalink/internal/testfixture"
	"github.com/webkaz-labs/sobalink/internal/webui"
)

// Three genuine Core/DirectLAN instances use native loopback transports. This
// is not separate-process, browser, real-device or OS lifecycle acceptance.
func TestCatalogViewNativeListAndConnect(t *testing.T) {
	if !directLANNetstackReady {
		t.Skip("userspace netstack activation remains gated")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	invoke := func(c *Core, name string, input any) (any, error) {
		raw, err := json.Marshal(input)
		if err != nil {
			return nil, err
		}
		return c.Command(ctx, webui.Command{RequestID: randomID(), Name: name, Payload: raw})
	}
	must := func(c *Core, name string, input any) any {
		t.Helper()
		v, err := invoke(c, name, input)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return v
	}
	cores := make([]*Core, 3)
	locks := make([]*config.Lock, 3)
	for i := range cores {
		dir := t.TempDir()
		lock, err := config.AcquireLock(dir)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = lock.Close() })
		locks[i] = lock
		c, err := Open(ctx, Options{Directory: dir, Version: "synthetic-catalog-native", SkipNetworkStart: true, LifecycleLock: lock, EnableResourceInspection: true, EnableCatalogView: true})
		if err != nil {
			t.Fatal(err)
		}
		cores[i] = c
		t.Cleanup(func() {
			if err := c.Close(); err != nil {
				t.Error(err)
			}
		})
		r, err := testfixture.ReserveLoopbackTCPUDP(netip.MustParseAddr("127.0.0.1"), 54543, 54544, 54545)
		if err != nil {
			t.Fatal(err)
		}
		listen := r.Endpoint().String()
		if err := r.Close(); err != nil {
			t.Fatal(err)
		}
		must(c, "network.configure", map[string]any{"mode": "direct-lan", "hostname": "synthetic-catalog-" + strconv.Itoa(i), "directLAN": DirectLANSelection{Listen: listen, Prefixes: []string{"127.0.0.0/8"}}})
	}
	host, guest, other := cores[0], cores[1], cores[2]
	key := func(c *Core) string { return c.directLANStoreCopy().copy().Identity.PublicKey() }
	hostKey, guestKey, otherKey := key(host), key(guest), key(other)
	for _, peer := range []*Core{guest, other} {
		issued := must(host, "direct-lan.invite", map[string]any{"recipientPublicKey": key(peer), "name": "synthetic-catalog-peer", "ttlSeconds": 120}).(map[string]any)
		must(peer, "direct-lan.join", map[string]any{"invitation": issued["invitation"]})
	}
	// Ordinary pairing creates legacy relationships. Catalog permission requires
	// the production managed pair context, so close and reopen the SAME owned
	// profiles offline, migrate by reviewed commands, then run both controllers.
	for i, c := range cores {
		dir := c.dir
		if err := c.Close(); err != nil {
			t.Fatal("legacy Core close failed", err)
		}
		fresh, err := Open(ctx, Options{Directory: dir, Version: "synthetic-catalog-native", SkipNetworkStart: true, LifecycleLock: locks[i], EnableResourceInspection: true, EnableCatalogView: true})
		if err != nil {
			t.Fatal("owned offline reopen failed", err)
		}
		cores[i] = fresh
		t.Cleanup(func() {
			if err := fresh.Close(); err != nil {
				t.Error(err)
			}
		})
		review := must(fresh, "direct-lan.migration.review", struct{}{}).(DirectLANMigrationReview)
		must(fresh, "direct-lan.migration.apply", map[string]any{"expectedRevision": review.Revision})
		if fresh.resourceIdentity == "" || fresh.resourceLock != locks[i] || fresh.catalogViews == nil || fresh.catalogViews.frozen {
			t.Fatal("owned catalog initialization failed")
		}
	}
	host, guest, other = cores[0], cores[1], cores[2]
	deadline := time.Now().Add(40 * time.Second).UTC().Format(time.RFC3339Nano)
	for _, pair := range []struct {
		c    *Core
		peer string
	}{{host, guestKey}, {guest, hostKey}} {
		in := UpgradeIntent{PeerID: pair.peer, Deadline: deadline}
		review := must(pair.c, "direct-lan.upgrade.review", in).(UpgradeReview)
		if review.RestartRequired {
			t.Fatal("offline managed activation unexpectedly needs restart")
		}
		in.ExpectedRevision = review.Revision
		must(pair.c, "direct-lan.upgrade.run", in)
	}
	for _, c := range []*Core{host, guest} {
		for {
			progress := must(c, "direct-lan.upgrade.status", UpgradeIntent{}).(UpgradeProgress)
			if progress.State == "network-started" {
				break
			}
			if progress.State == "failed" || progress.State == "cancelled" {
				t.Fatalf("managed pair activation failed: %s", progress.ErrorCode)
			}
			select {
			case <-ctx.Done():
				t.Fatal("managed pair activation timed out")
			case <-time.After(20 * time.Millisecond):
			}
		}
		c.op.Lock()
		_, err := c.resourceGrantRelationship(map[*Core]string{host: guestKey, guest: hostKey}[c])
		c.op.Unlock()
		if err != nil {
			t.Fatal("actual managed relationship unavailable", err)
		}
	}
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "synthetic-catalog-ok") }))
	defer target.Close()
	u, _ := url.Parse(target.URL)
	targetPort, _ := strconv.Atoi(u.Port())
	share := func(name, allowed string) (string, string) {
		must(host, "service.share", map[string]any{"name": name, "network": "tcp", "ports": "32101", "localPort": targetPort, "purpose": "web", "peerIds": []string{allowed}, "lifetime": "until-revoked", "discoverable": true})
		host.mu.RLock()
		defer host.mu.RUnlock()
		for id, active := range host.active {
			if active.spec.Name == name {
				return id, active.discoveryID
			}
		}
		t.Fatal("actual shared service activation missing")
		return "", ""
	}
	approve := func(serviceID string) resourcegrant.CatalogViewReview {
		review := must(host, "resource.catalog.view.preview", resourcegrant.CatalogViewInputs{Target: resourceTarget(host), PeerKey: guestKey, ServiceIDs: []string{serviceID}, ExpiresAt: time.Now().Add(time.Minute).Unix()}).(resourcegrant.CatalogViewReview)
		must(host, "resource.catalog.view.confirm", resourcegrant.CatalogViewConfirmation{Review: review, Confirm: true})
		return review
	}
	list := func(review resourcegrant.CatalogViewReview) catalogViewObservation {
		return must(guest, "resource.catalog.view.list", resourcegrant.RemoteCatalogViewInput{PeerKey: hostKey, Request: review.Grant.Selector()}).(catalogViewObservation)
	}
	connect := func(observation catalogViewObservation, name string, localPort int) (any, error) {
		row := observation.Services[0]
		return invoke(guest, "service.connect", map[string]any{"name": name, "peerId": hostKey, "network": row.Service.Network, "ports": row.Service.Ports, "purpose": row.Service.Purpose, "localPort": localPort, "lifetime": "until-stopped", "catalogView": catalogViewConnection{ReviewRevision: observation.ReviewRevision, ServiceID: row.Service.ID}})
	}
	viewOnlyID, viewOnlyGeneration := share("synthetic-view-only", otherKey)
	review := approve(viewOnlyGeneration)
	observation := list(review)
	if len(observation.Services) != 1 || observation.Services[0].ConnectAllowed || observation.Services[0].Service.ID != viewOnlyGeneration {
		t.Fatal("view-only catalog did not preserve exact selected activation")
	}
	before := len(guest.profileCopy().Services)
	if _, err := connect(observation, "synthetic-denied", availableServicePort(t)); err == nil || len(guest.profileCopy().Services) != before {
		t.Fatal("view-only approval allowed connection/save", err)
	}
	must(host, "resource.catalog.view.revoke", resourcegrant.GrantRevoke{Target: review.Grant.Target, GrantID: review.Grant.ID, GrantRevision: review.Grant.Revision, Confirm: true})
	if _, err := invoke(guest, "resource.catalog.view.list", resourcegrant.RemoteCatalogViewInput{PeerKey: hostKey, Request: review.Grant.Selector()}); err == nil {
		t.Fatal("revoked grant listed services")
	}
	must(host, "service.stop", map[string]any{"id": viewOnlyID})
	_, allowedGeneration := share("synthetic-allowed", guestKey)
	if allowedGeneration == viewOnlyGeneration {
		t.Fatal("share restart reused activation")
	}
	allowedReview := approve(allowedGeneration)
	allowed := list(allowedReview)
	if len(allowed.Services) != 1 || !allowed.Services[0].ConnectAllowed {
		t.Fatal("existing connection permission missing")
	}
	occupied, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal("occupied loopback listener setup failed", err)
	}
	t.Cleanup(func() { _ = occupied.Close() })
	occupiedPort := occupied.Addr().(*net.TCPAddr).Port
	before = len(guest.profileCopy().Services)
	if _, err := connect(allowed, "synthetic-saved-failure", occupiedPort); err == nil {
		t.Fatal("occupied listener allowed service start")
	}
	profile := guest.profileCopy()
	if len(profile.Services) != before+1 {
		t.Fatal("failed listener start did not retain exactly one saved definition")
	}
	failedID := ""
	for _, saved := range profile.Services {
		if saved.Name == "synthetic-saved-failure" && saved.LocalPort == occupiedPort && saved.PeerID == hostKey {
			failedID = saved.ID
		}
	}
	guest.mu.RLock()
	failedActive, failedState := guest.active[failedID], guest.serviceStates[failedID]
	guest.mu.RUnlock()
	if failedID == "" || failedActive != nil || failedState != "failed" {
		t.Fatal("saved-but-not-started service retained an active listener or lost failure state")
	}
	if _, err := connect(allowed, "synthetic-failed-repeat", occupiedPort); err == nil || len(guest.profileCopy().Services) != before+1 {
		t.Fatal("failed-start receipt was reused or created another saved definition", err)
	}
	if err := occupied.Close(); err != nil {
		t.Fatal("occupied listener release failed", err)
	}
	refreshed := list(allowedReview)
	if refreshed.ReviewRevision == allowed.ReviewRevision {
		t.Fatal("explicit refresh reused consumed receipt")
	}
	allowed = refreshed
	local := availableServicePort(t)
	if _, err := connect(allowed, "synthetic-forward", local); err != nil {
		t.Fatal("reviewed connect failed", err)
	}
	client := &http.Client{Timeout: 8 * time.Second}
	response, err := client.Get("http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(local)))
	if err != nil {
		t.Fatal("forward application request failed", err)
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK || string(body) != "synthetic-catalog-ok" {
		t.Fatal("forward did not reach actual application", err)
	}
	if _, err := connect(allowed, "synthetic-repeat", availableServicePort(t)); err == nil {
		t.Fatal("consumed catalog receipt was replayed")
	}
}
