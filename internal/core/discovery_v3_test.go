package core

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/capacity"
)

func TestDiscoveryV3RetainsExplicitPermanentLifetimeAndCacheExpiry(t *testing.T) {
	p := newCorePair(t)
	p.b.capacity.Resources["pageEntries"] = capacity.Limited(1)
	permanentLocalID := ""
	for _, rule := range []map[string]any{{"name": "permanent", "network": "tcp", "ports": "8080", "peerIds": []string{"peer-a"}, "lifetime": "until-revoked", "discoverable": true}, {"name": "week", "network": "tcp", "ports": "8081", "peerIds": []string{"peer-a"}, "lifetime": "finite", "ttlSeconds": 7 * 24 * 60 * 60, "discoverable": true}} {
		started := mustCommand(t, p.b, "service.share", rule).(map[string]any)
		if rule["name"] == "permanent" {
			permanentLocalID = started["id"].(string)
		}
	}
	if err := p.a.probePeer(context.Background(), "peer-b"); err != nil {
		t.Fatal(err)
	}
	if len(p.a.discoveredViews()) != 2 {
		t.Fatal("discovery pages or long lifetimes lost")
	}
	var permanent RemoteService
	for _, s := range p.a.discovered["peer-b"] {
		if s.Lifetime == "until-revoked" {
			permanent = s
		}
	}
	if permanent.ID == "" || !permanent.ExpiresAt.IsZero() {
		t.Fatal("permanent lifetime not explicit")
	}
	p.a.mu.Lock()
	p.a.confirmed["peer-b"] = time.Now().Add(-16 * time.Second)
	p.a.mu.Unlock()
	if len(p.a.discoveredViews()) != 0 {
		t.Fatal("permanent permission bypassed discovery freshness")
	}
	w := httptest.NewRecorder()
	p.b.serveServiceDiscovery(w, httptest.NewRequest("GET", "/.well-known/sobalink/services/v2", nil), "peer-a")
	if w.Code != 409 {
		t.Fatal("legacy discovery misrepresented permanent grant")
	}
	if permanent.ID == permanentLocalID {
		t.Fatal("discovery exposed a reusable saved-rule identity")
	}
	mustCommand(t, p.b, "service.stop", map[string]string{"id": permanentLocalID})
	if err := p.a.probePeer(context.Background(), "peer-b"); err != nil {
		t.Fatal(err)
	}
	if len(p.a.discoveredViews()) != 1 {
		t.Fatal("revocation did not remove permanent service")
	}
}

func TestLocalPagesHaveExplicitContinuationAndRejectStaleReview(t *testing.T) {
	c := &Core{capacity: capacity.Defaults()}
	c.capacity.Resources["pageEntries"] = capacity.Limited(1)
	rows := []map[string]any{{"id": "b", "name": "second"}, {"id": "a", "name": "first"}}
	first, err := c.pageRows(rows, "", "")
	if err != nil || len(first.Items) != 1 || first.NextCursor != "a" || first.Total != 2 {
		t.Fatal(first, err)
	}
	next, err := c.pageRows(rows, first.NextCursor, first.Revision)
	if err != nil || len(next.Items) != 1 || next.NextCursor != "" {
		t.Fatal(next, err)
	}
	rows[0]["name"] = "changed"
	if _, err := c.pageRows(rows, first.NextCursor, first.Revision); err == nil {
		t.Fatal("stale list continuation accepted")
	}
	if _, err := json.Marshal(first); err != nil {
		t.Fatal(err)
	}
}
