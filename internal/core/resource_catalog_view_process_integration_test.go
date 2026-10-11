//go:build lanlink_integration && directlan_integration

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
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/resource"
	"github.com/webkaz-labs/sobalink/internal/resourcegrant"
	"github.com/webkaz-labs/sobalink/internal/testfixture"
)

type catalogProcessLocal struct {
	Target   resource.Target `json:"target"`
	Services []struct {
		ID        string `json:"id"`
		ServiceID string `json:"serviceId"`
	} `json:"services"`
}

func catalogProcessDecode[T any](t *testing.T, raw json.RawMessage) T {
	t.Helper()
	var value T
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal("private process reply invalid")
	}
	return value
}

// Real independent child Core processes and real DirectLAN peer HTTP. This
// establishes local process isolation, not browser or physical-device behavior.
func TestCatalogViewThreeProcessListAndConnect(t *testing.T) {
	runCatalogViewThreeProcess(t, false)
}

func TestCatalogViewThreeProcessBrowser(t *testing.T) {
	if runtime.GOOS != "linux" || os.Getenv("SOBALINK_RUN_CATALOG_BROWSER_NATIVE") != "1" {
		t.Skip("unperformed: explicit Linux process/browser acceptance required")
	}
	runCatalogViewThreeProcess(t, true)
}

func runCatalogViewThreeProcess(t *testing.T, browser bool) {
	if os.Getenv("SOBALINK_RUN_DIRECT_INTEGRATION") != "1" {
		t.Skip("explicit native loopback execution required")
	}
	if !directLANNetstackReady {
		t.Skip("userspace netstack activation remains gated")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	dirs := [3]string{t.TempDir(), t.TempDir(), t.TempDir()}
	var peers [3]*routeCoreProcess
	var keys [3]string
	start := func(i int) {
		peers[i] = startRouteCoreProcessWithEnvironment(t, ctx, dirs[i], true, []string{"SOBALINK_ROUTE_PROCESS_CATALOG=1"})
	}
	for i := range peers {
		start(i)
		r, err := testfixture.ReserveLoopbackTCPUDP(netip.MustParseAddr("127.0.0.1"), 54543, 54544, 54545)
		if err != nil {
			t.Fatal("loopback pair allocation failed", err)
		}
		listen := r.Endpoint().String()
		if err := r.Close(); err != nil {
			t.Fatal(err)
		}
		peers[i].invoke(t, "network.configure", map[string]any{"mode": "direct-lan", "hostname": "synthetic-process-" + strconv.Itoa(i), "directLAN": DirectLANSelection{Listen: listen, Prefixes: []string{"127.0.0.0/8"}}})
		identity := catalogProcessDecode[map[string]string](t, peers[i].invoke(t, "direct-lan.identity", struct{}{}))
		keys[i] = identity["publicKey"]
	}
	for i := 1; i < len(peers); i++ {
		invite := catalogProcessDecode[map[string]any](t, peers[0].invoke(t, "direct-lan.invite", map[string]any{"recipientPublicKey": keys[i], "name": "synthetic-process-peer", "ttlSeconds": 120}))
		peers[i].invoke(t, "direct-lan.join", map[string]any{"invitation": invite["invitation"]})
	}
	for i, p := range peers {
		p.stop(t)
		start(i)
		review := catalogProcessDecode[DirectLANMigrationReview](t, peers[i].invoke(t, "direct-lan.migration.review", struct{}{}))
		peers[i].invoke(t, "direct-lan.migration.apply", map[string]string{"expectedRevision": review.Revision})
	}
	deadline := time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano)
	for i := 0; i < 2; i++ {
		in := UpgradeIntent{PeerID: keys[1-i], Deadline: deadline}
		review := catalogProcessDecode[UpgradeReview](t, peers[i].invoke(t, "direct-lan.upgrade.review", in))
		if review.RestartRequired {
			t.Fatal("offline child unexpectedly requires restart")
		}
		in.ExpectedRevision = review.Revision
		peers[i].invoke(t, "direct-lan.upgrade.run", in)
	}
	for i := 0; i < 2; i++ {
		for {
			progress := catalogProcessDecode[UpgradeProgress](t, peers[i].invoke(t, "direct-lan.upgrade.status", UpgradeIntent{}))
			if progress.State == "network-started" {
				break
			}
			if progress.State == "failed" || progress.State == "cancelled" {
				t.Fatal("managed child activation failed", progress.ErrorCode)
			}
			select {
			case <-ctx.Done():
				t.Fatal("managed child activation timed out")
			case <-time.After(20 * time.Millisecond):
			}
		}
	}
	host, guest := peers[0], peers[1]
	// Controller completion is separate from peer application readiness. Reuse
	// the real transport probe before spending the ordinary list's eight seconds.
	for i := 0; i < 2; i++ {
		readyUntil := time.Now().Add(30 * time.Second)
		for {
			probe := catalogProcessDecode[map[string]any](t, peers[i].invoke(t, "fixture.probe", map[string]any{"Peer": keys[1-i], "Port": PeerPort}))
			if probe["connected"] == true && probe["networkReady"] == true {
				break
			}
			if time.Now().After(readyUntil) {
				t.Fatal("actual peer application transport did not become ready")
			}
			select {
			case <-ctx.Done():
				t.Fatal("peer application readiness timed out")
			case <-time.After(20 * time.Millisecond):
			}
		}
	}
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "synthetic-process-ok") }))
	defer target.Close()
	u, _ := url.Parse(target.URL)
	targetPort, _ := strconv.Atoi(u.Port())
	share := func(name, allowed string) catalogProcessLocal {
		host.invoke(t, "service.share", map[string]any{"name": name, "network": "tcp", "ports": "32101", "localPort": targetPort, "purpose": "web", "peerIds": []string{allowed}, "lifetime": "until-revoked", "discoverable": true})
		local := catalogProcessDecode[catalogProcessLocal](t, host.invoke(t, "fixture.catalog-local", struct{}{}))
		if len(local.Services) != 1 {
			t.Fatal("exact live child share missing")
		}
		return local
	}
	approve := func(local catalogProcessLocal) resourcegrant.CatalogViewReview {
		r := catalogProcessDecode[resourcegrant.CatalogViewReview](t, host.invoke(t, "resource.catalog.view.preview", resourcegrant.CatalogViewInputs{Target: local.Target, PeerKey: keys[1], ServiceIDs: []string{local.Services[0].ServiceID}, ExpiresAt: time.Now().Add(time.Minute).Unix()}))
		host.invoke(t, "resource.catalog.view.confirm", resourcegrant.CatalogViewConfirmation{Review: r, Confirm: true})
		return r
	}
	list := func(r resourcegrant.CatalogViewReview) catalogViewObservation {
		return catalogProcessDecode[catalogViewObservation](t, guest.invoke(t, "resource.catalog.view.list", resourcegrant.RemoteCatalogViewInput{PeerKey: keys[0], Request: r.Grant.Selector()}))
	}
	connect := func(o catalogViewObservation, name string, port int) processReply {
		row := o.Services[0].Service
		return guest.invokeResult(t, "service.connect", map[string]any{"name": name, "peerId": keys[0], "network": row.Network, "ports": row.Ports, "purpose": row.Purpose, "localPort": port, "lifetime": "until-stopped", "catalogView": catalogViewConnection{ReviewRevision: o.ReviewRevision, ServiceID: row.ID}})
	}
	local := share("synthetic-view-only", keys[2])
	r := approve(local)
	view := list(r)
	if len(view.Services) != 1 || view.Services[0].ConnectAllowed {
		t.Fatal("view-only child scope changed")
	}
	if connect(view, "synthetic-denied", availableServicePort(t)).Error == "" {
		t.Fatal("view-only child connection allowed")
	}
	guestLocal := catalogProcessDecode[localPage](t, guest.invoke(t, "service.list", struct{}{}))
	if guestLocal.Total != 0 || len(guestLocal.Items) != 0 {
		t.Fatal("view-only denial saved service")
	}
	host.invoke(t, "resource.catalog.view.revoke", resourcegrant.GrantRevoke{Target: r.Grant.Target, GrantID: r.Grant.ID, GrantRevision: r.Grant.Revision, Confirm: true})
	if guest.invokeResult(t, "resource.catalog.view.list", resourcegrant.RemoteCatalogViewInput{PeerKey: keys[0], Request: r.Grant.Selector()}).Error == "" {
		t.Fatal("revoked child grant disclosed catalog")
	}
	host.invoke(t, "service.stop", map[string]string{"id": local.Services[0].ID})
	oldGeneration := local.Services[0].ServiceID
	local = share("synthetic-allowed", keys[1])
	if local.Services[0].ServiceID == oldGeneration {
		t.Fatal("child share reused old generation")
	}
	r = approve(local)
	view = list(r)
	if len(view.Services) != 1 || !view.Services[0].ConnectAllowed {
		t.Fatal("existing child connection permission absent")
	}
	occupied, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	port := occupied.Addr().(*net.TCPAddr).Port
	if connect(view, "synthetic-saved-failure", port).Error == "" {
		t.Fatal("occupied child listener started")
	}
	saved := catalogProcessDecode[localPage](t, guest.invoke(t, "service.list", struct{}{}))
	if saved.Total != 1 || len(saved.Items) != 1 || saved.Items[0]["name"] != "synthetic-saved-failure" || saved.Items[0]["status"] != "failed" {
		t.Fatal("child lost saved-but-not-started state")
	}
	if connect(view, "synthetic-repeat", port).Error == "" {
		t.Fatal("failed child start reused receipt")
	}
	if err := occupied.Close(); err != nil {
		t.Fatal(err)
	}
	fresh := list(r)
	if fresh.ReviewRevision == view.ReviewRevision {
		t.Fatal("explicit child refresh reused receipt")
	}
	port = availableServicePort(t)
	if result := connect(fresh, "synthetic-forward", port); result.Error != "" {
		t.Fatal("reviewed child connection failed", result.Error)
	}
	client := &http.Client{Timeout: 8 * time.Second}
	response, err := client.Get("http://127.0.0.1:" + strconv.Itoa(port))
	if err != nil {
		t.Fatal("child forward application request failed", err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || response.StatusCode != 200 || string(body) != "synthetic-process-ok" {
		t.Fatal("child forward missed actual application")
	}
	if browser {
		// Reuse the same independent owners; permission is created through
		// genuine provider commands, not claimed as a browser approval flow.
		runCatalogViewProcessBrowser(t, ctx, guest, r, local.Services[0].ServiceID, availableServicePort(t), keys[0])
	}
	for _, p := range peers {
		p.stop(t)
	}
}

func runCatalogViewProcessBrowser(t *testing.T, ctx context.Context, guest *routeCoreProcess, review resourcegrant.CatalogViewReview, serviceID string, localPort int, provider string) {
	t.Helper()
	private := t.TempDir()
	viewer := catalogProcessDecode[struct {
		Schema int    `json:"schema"`
		URL    string `json:"url"`
		Code   string `json:"code"`
	}](t, guest.invoke(t, "fixture.web", struct{}{}))
	snapshot := catalogProcessDecode[struct {
		Peers []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"peers"`
	}](t, guest.invoke(t, "fixture.snapshot", struct{}{}))
	peerName := ""
	for _, peer := range snapshot.Peers {
		if peer.ID == provider {
			if peerName != "" {
				t.Fatal("duplicate provider in actual viewer snapshot")
			}
			peerName = peer.Name
		}
	}
	if peerName == "" {
		t.Fatal("actual provider peer display name unavailable")
	}
	descriptor := map[string]any{"schema": 1, "viewer": viewer, "request": resourcegrant.RemoteCatalogViewInput{PeerKey: provider, Request: review.Grant.Selector()}, "serviceId": serviceID, "peerName": peerName, "localPort": localPort, "expectedContent": "synthetic-process-ok"}
	raw, err := json.Marshal(descriptor)
	if err != nil {
		t.Fatal("private browser descriptor encoding failed")
	}
	path := filepath.Join(private, "session.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal("private browser descriptor save failed")
	}
	working, err := os.Getwd()
	if err != nil {
		t.Fatal("browser source directory unavailable")
	}
	webDir := filepath.Clean(filepath.Join(working, "..", "..", "web"))
	node := os.Getenv("SOBA_CATALOG_BROWSER_NODE")
	if node == "" {
		node, err = exec.LookPath("node")
		if err != nil {
			t.Fatal("prepared Node runtime unavailable")
		}
	}
	call, cancel := context.WithTimeout(ctx, 75*time.Second)
	defer cancel()
	command := exec.CommandContext(call, node, filepath.Join(webDir, "node_modules", "@playwright", "test", "cli.js"), "test", "--config", filepath.Join(webDir, "playwright.catalog-native.config.mjs"), "--workers", "1", "--retries", "0")
	command.Dir = webDir
	// Let Playwright close its browser before the existing bounded child join.
	command.Cancel = func() error { return command.Process.Signal(os.Interrupt) }
	command.WaitDelay = 10 * time.Second
	command.Env = append(os.Environ(), "SOBA_CATALOG_BROWSER_SESSION="+path)
	// Framework errors can contain private selectors. Do not publish raw output.
	command.Stdout, command.Stderr = io.Discard, io.Discard
	if err := command.Run(); err != nil {
		t.Fatal("real process/browser acceptance failed; private selectors withheld")
	}
	result, err := os.ReadFile(filepath.Join(private, "catalog-browser-result.json"))
	if err != nil {
		t.Fatal("browser receipt unavailable")
	}
	receipt := catalogProcessDecode[struct {
		Schema   int  `json:"schema"`
		Accepted bool `json:"accepted"`
		Observed int  `json:"observed"`
		Passed   int  `json:"passed"`
		Errors   int  `json:"errors"`
	}](t, result)
	if receipt.Schema != 1 || !receipt.Accepted || receipt.Observed != 1 || receipt.Passed != 1 || receipt.Errors != 0 {
		t.Fatal("browser receipt did not establish acceptance")
	}
}
