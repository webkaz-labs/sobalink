package core

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/resourcegrant"
	"github.com/webkaz-labs/sobalink/internal/transportorigin"
)

func catalogConnectReceipt() (*catalogViewReceipt, *catalogViewConnectProof) {
	original := &discoveryCacheTestOrigin{transportorigin.NewToken(), make(chan struct{})}
	row := catalogViewService{Service: RemoteService{ID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Purpose: "web", Network: "tcp", Ports: "8080", Lifetime: "finite", ExpiresAt: time.Now().Add(time.Hour), Application: "unverified"}, ConnectAllowed: true}
	r := &catalogViewReceipt{observation: catalogViewObservation{catalogViewReply: catalogViewReply{Selector: resourcegrant.CatalogViewSelector{ProtocolVersion: resourcegrant.ProtocolVersion, GrantID: "cccccccccccccccccccccccccccccccc", GrantRevision: 1}, Services: []catalogViewService{row}}, PeerKey: "synthetic-peer", CheckedAt: time.Now(), ReviewRevision: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, original: original, process: "dddddddddddddddddddddddddddddddd"}
	return r, &catalogViewConnectProof{receipt: r, row: row}
}
func TestCatalogViewConnectExactRefetchRejectsChangedConditions(t *testing.T) {
	for _, kind := range []string{"selector", "missing", "connect", "id", "purpose", "network", "ports", "lifetime", "expiry", "application", "duplicate"} {
		t.Run(kind, func(t *testing.T) {
			r, p := catalogConnectReceipt()
			result := r.observation.catalogViewReply
			result.Services = append([]catalogViewService{}, result.Services...)
			switch kind {
			case "selector":
				result.Selector.GrantRevision++
			case "missing":
				result.Services = []catalogViewService{}
			case "connect":
				result.Services[0].ConnectAllowed = false
			case "id":
				result.Services[0].Service.ID = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
			case "purpose":
				result.Services[0].Service.Purpose = "ssh"
			case "network":
				result.Services[0].Service.Network = "udp"
			case "ports":
				result.Services[0].Service.Ports = "8081"
			case "lifetime":
				result.Services[0].Service.Lifetime = "until-revoked"
				result.Services[0].Service.ExpiresAt = time.Time{}
			case "expiry":
				result.Services[0].Service.ExpiresAt = result.Services[0].Service.ExpiresAt.Add(time.Second)
			case "application":
				result.Services[0].Service.Application = "verified"
			case "duplicate":
				result.Services = append(result.Services, result.Services[0])
			}
			if _, ok := catalogViewRefetchedRow(r, p.row, result); ok {
				t.Fatal("changed remote conditions accepted")
			}
		})
	}
	r, p := catalogConnectReceipt()
	if _, ok := catalogViewRefetchedRow(r, p.row, r.observation.catalogViewReply); !ok {
		t.Fatal("unchanged exact conditions rejected")
	}
}
func TestCatalogViewConnectReceiptConsumedAndProofBound(t *testing.T) {
	r, p := catalogConnectReceipt()
	in := &catalogViewConnection{ReviewRevision: r.observation.ReviewRevision, ServiceID: p.row.Service.ID}
	if _, ok := catalogViewSelectedRow(r, in); !ok {
		t.Fatal("selected row missing")
	}
	r.consumed = true
	if _, ok := catalogViewSelectedRow(r, in); ok {
		t.Fatal("consumed review replayed")
	}
	revision := encodeDiscoveryReview(r.observation.PeerKey, p.row.Service, r.observation.CheckedAt)
	if !p.matchesInput(r.observation.PeerKey, "tcp", "8080", "web", p.row.Service.ID, revision, 0) {
		t.Fatal("exact input rejected")
	}
	if p.matchesInput("other-peer", "tcp", "8080", "web", p.row.Service.ID, revision, 0) || p.matchesInput(r.observation.PeerKey, "tcp", "8081", "web", p.row.Service.ID, revision, 0) || p.matchesInput(r.observation.PeerKey, "tcp", "8080", "web", p.row.Service.ID, "changed", 0) {
		t.Fatal("substituted input accepted")
	}
	close(r.original.(*discoveryCacheTestOrigin).stopped)
	if p.current() {
		t.Fatal("stopped original origin accepted")
	}
}
func TestCatalogViewConnectViewingOnlyRejectsPublicCommandBeforeSave(t *testing.T) {
	pair := newCorePair(t)
	r, p := catalogConnectReceipt()
	r.observation.PeerKey = "peer-b"
	r.process = pair.a.resourceNonce
	r.observation.Services[0].ConnectAllowed = false
	p.row.ConnectAllowed = false
	pair.a.catalogViewReceived = r
	_, err := command(pair.a, randomID(), "service.connect", map[string]any{"name": "view-only", "peerId": "peer-b", "network": "tcp", "ports": "8080", "purpose": "web", "lifetime": "until-stopped", "catalogView": catalogViewConnection{ReviewRevision: r.observation.ReviewRevision, ServiceID: p.row.Service.ID}})
	if networkErrorCode(err) != "catalog_view_stale" || len(pair.a.profileCopy().Services) != 0 || len(pair.a.serviceViews()) != 0 {
		t.Fatal("catalog browsing created connection permission", err)
	}
}
func TestCatalogViewConnectReferenceCannotBypassPublicReview(t *testing.T) {
	for _, raw := range []string{`{"catalogView":null}`, `{"catalogView":{"reviewRevision":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","serviceId":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","extra":true}}`} {
		if _, err := catalogViewConnectInput(json.RawMessage(raw)); err == nil {
			t.Fatal("invalid optional reference accepted")
		}
	}
	pair := newCorePair(t)
	raw, _ := json.Marshal(map[string]any{"name": "forged", "peerId": "peer-b", "network": "tcp", "ports": "8080", "purpose": "web", "lifetime": "until-stopped", "catalogView": catalogViewConnection{ReviewRevision: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ServiceID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}})
	if _, err := pair.a.startServiceCommand(context.Background(), "service.connect", raw); networkErrorCode(err) != "catalog_view_stale" {
		t.Fatal("reference without private proof accepted", err)
	}
}

// Uses the existing net.Pipe peer fixture plus a loopback application and the
// actual forward listener. The private proof is injected; this test establishes
// transport reuse/guard behavior, not managed DirectLAN provenance acceptance.
func TestCatalogViewConnectProofUsesExistingTransportAndStopGuard(t *testing.T) {
	pair := newCorePair(t)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "synthetic-service-ok") }))
	defer target.Close()
	u, _ := url.Parse(target.URL)
	targetPort, _ := strconv.Atoi(u.Port())
	mustCommand(t, pair.b, "service.share", map[string]any{"name": "shared", "network": "tcp", "ports": "8080", "localPort": targetPort, "purpose": "web", "peerIds": []string{"peer-a"}, "lifetime": "until-revoked", "discoverable": true})
	// pipeNode.DialIP models explicit listeners only. Bridge its synthetic
	// service port to the actual registered compact share dispatcher, which
	// production backends select directly via their TCP fallback path.
	bridge, err := pair.nb.Listen("tcp", netip.AddrPortFrom(pair.nb.ip, 8080).String())
	if err != nil {
		t.Fatal(err)
	}
	bridgeDone := make(chan struct{})
	go func() {
		defer close(bridgeDone)
		for {
			conn, err := bridge.Accept()
			if err != nil {
				return
			}
			pair.nb.mu.Lock()
			dispatch := pair.nb.fallback
			pair.nb.mu.Unlock()
			if dispatch == nil {
				conn.Close()
				continue
			}
			handler, _ := dispatch(netip.AddrPortFrom(pair.na.ip, 40000), netip.AddrPortFrom(pair.nb.ip, 8080))
			if handler == nil {
				conn.Close()
				continue
			}
			go handler(conn)
		}
	}()
	defer func() { bridge.Close(); <-bridgeDone }()
	service := pair.b.permittedServices("peer-a")[0]
	r, p := catalogConnectReceipt()
	r.observation.PeerKey = "peer-b"
	p.row.Service = service
	r.observation.Services = []catalogViewService{p.row}
	local := availableServicePort(t)
	raw, _ := json.Marshal(map[string]any{"name": "reviewed", "peerId": "peer-b", "network": "tcp", "ports": "8080", "purpose": "web", "localPort": local, "lifetime": "until-stopped", "catalogView": catalogViewConnection{ReviewRevision: r.observation.ReviewRevision, ServiceID: service.ID}})
	pair.a.op.Lock()
	_, err = pair.a.startServiceCommand(context.WithValue(context.Background(), catalogViewConnectKey{}, p), "service.connect", raw)
	pair.a.op.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Get("http://127.0.0.1:" + strconv.Itoa(local))
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || string(body) != "synthetic-service-ok" {
		t.Fatal("existing transport did not reach application", err, string(body))
	}
	pair.a.mu.RLock()
	var stopped <-chan struct{}
	for _, a := range pair.a.active {
		if a.spec.Name == "reviewed" {
			stopped = a.servers[0].Done()
		}
	}
	pair.a.mu.RUnlock()
	close(r.original.(*discoveryCacheTestOrigin).stopped)
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("original stop did not close transport")
	}
	response, err = client.Get("http://127.0.0.1:" + strconv.Itoa(local))
	if err == nil {
		response.Body.Close()
		t.Fatal("stopped original generation remained usable")
	}
}

func TestCatalogViewConnectLocalActivationIDOnlyForCurrentAdvertisedShare(t *testing.T) {
	c, _, a := catalogCommandFixture(t)
	if got := c.serviceView(a)["activationId"]; got != a.discoveryID {
		t.Fatal("local approval lacks current publication identity", got)
	}
	a.ready.Store(false)
	if _, present := c.serviceView(a)["activationId"]; present {
		t.Fatal("inactive share exposes selectable activation")
	}
	a.ready.Store(true)
	a.spec.Discoverable = false
	if _, present := c.serviceView(a)["activationId"]; present {
		t.Fatal("private share exposes selectable activation")
	}
	a.spec.Discoverable = true
	a.spec.Direction = "forward"
	if _, present := c.serviceView(a)["activationId"]; present {
		t.Fatal("outbound connection exposes share activation")
	}
}
