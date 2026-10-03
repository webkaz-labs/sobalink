//go:build soba_e2e

package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/core"
	"github.com/webkaz-labs/sobalink/internal/webui"
)

type recordingBackend struct {
	commands   []webui.Command
	uploads    int
	uploadBody string
	state      map[string]any
}

func (b *recordingBackend) Snapshot(context.Context) (map[string]any, error) { return b.state, nil }
func (b *recordingBackend) Command(_ context.Context, cmd webui.Command) (any, error) {
	b.commands = append(b.commands, cmd)
	return nil, nil
}
func (b *recordingBackend) Upload(w http.ResponseWriter, r *http.Request) {
	b.uploads++
	data, _ := io.ReadAll(r.Body)
	b.uploadBody = string(data)
	w.WriteHeader(http.StatusOK)
}

func TestFixtureScenarioNamesAreBounded(t *testing.T) {
	for _, name := range []string{"studio", "offline"} {
		if !validScenario(name) {
			t.Fatalf("supported scenario rejected: %s", name)
		}
	}
	for _, name := range []string{"", "live", "lan", "../studio", "OFFLINE"} {
		if validScenario(name) {
			t.Fatalf("unknown scenario accepted: %s", name)
		}
	}
}

func TestFixtureBlocksLiveActivationBeforeCore(t *testing.T) {
	for _, scenario := range []string{"studio", "offline"} {
		backend := &recordingBackend{}
		fixture := &fixtureBackend{Backend: backend, scenario: scenario}
		for _, cmd := range []webui.Command{
			{Name: "network.login", Payload: json.RawMessage(`{}`)},
			{Name: "network.configure", Payload: json.RawMessage(`{"mode":"lan","lan":{"kind":"host","address":"127.0.0.1:54443"}}`)},
			{Name: "network.configure", Payload: json.RawMessage(`{"mode":"lan","lan":{"kind":"relay","address":"127.0.0.1:54443","certificateSHA256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}`)},
		} {
			if _, err := fixture.Command(context.Background(), cmd); err == nil {
				t.Fatal("live activation or login reached fixture backend")
			}
		}
		if scenario == "offline" {
			if _, err := fixture.Command(context.Background(), webui.Command{Name: "network.configure", Payload: json.RawMessage(`{"mode":"tailnet"}`)}); err == nil {
				t.Fatal("offline scenario activated a peer network")
			}
		}
		if len(backend.commands) != 0 {
			t.Fatal("refused action reached Core")
		}
		if _, err := fixture.Command(context.Background(), webui.Command{Name: "settings.update", Payload: json.RawMessage(`{"locale":"ja"}`)}); err != nil || len(backend.commands) != 1 {
			t.Fatal("ordinary settings no longer use the production backend")
		}
	}
}

func TestFirstUploadFailurePreservesExactRetryInput(t *testing.T) {
	backend := &recordingBackend{}
	fixture := &fixtureBackend{Backend: backend, scenario: "studio"}
	body := "same reviewed batch and request identifier"
	for attempt := 0; attempt < 3; attempt++ {
		request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/api/upload", strings.NewReader(body))
		response := httptest.NewRecorder()
		fixture.Upload(response, request)
		if attempt == 0 {
			var failure struct {
				Code string `json:"code"`
			}
			if response.Code != http.StatusServiceUnavailable || json.Unmarshal(response.Body.Bytes(), &failure) != nil || failure.Code != "upload_failed" || backend.uploads != 0 {
				t.Fatal("first failure staged data or missed its deterministic recovery response")
			}
		} else if response.Code != http.StatusOK || backend.uploads != attempt || backend.uploadBody != body {
			t.Fatal("retry input was changed or failed again before reaching Core")
		}
	}
}

func TestOfflineFixtureKeepsIdleEmptyState(t *testing.T) {
	dir := t.TempDir()
	var factoryCalls atomic.Int32
	app, err := core.Open(context.Background(), core.Options{Directory: dir, Version: "test", SkipNetworkStart: true, NodeFactory: func(string, string) (core.NetworkBackend, error) {
		factoryCalls.Add(1)
		return nil, errors.New("no network permitted")
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	fixture := &fixtureBackend{Backend: app, scenario: "offline"}
	_, _ = fixture.Command(context.Background(), webui.Command{RequestID: "denied", Name: "network.configure", Payload: json.RawMessage(`{"mode":"lan","lan":{"kind":"host","address":"127.0.0.1:54443"}}`)})
	state, err := fixture.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(state)
	var snapshot struct {
		Self struct {
			Status string `json:"status"`
		} `json:"self"`
		Settings struct {
			Network string `json:"network"`
		} `json:"settings"`
		Peers []any `json:"peers"`
	}
	if json.Unmarshal(encoded, &snapshot) != nil || snapshot.Self.Status != "idle" || snapshot.Settings.Network != "none" || len(snapshot.Peers) != 0 || factoryCalls.Load() != 0 {
		t.Fatal("offline fixture did not remain idle with no peers")
	}
	if _, err := os.Stat(filepath.Join(dir, "lan.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("refused activation generated private LAN state")
	}
}

func TestManagementPortStaysExcludedWithFixtureWrapper(t *testing.T) {
	port := new(atomic.Uint32)
	port.Store(43001)
	n := &node{managementPort: port}
	state, err := n.State(context.Background())
	if err != nil || len(state.ReservedPorts) != 1 || state.ReservedPorts[0] != 43001 {
		t.Fatal("synthetic backend omitted management port from service exclusions")
	}
	backend := &recordingBackend{state: map[string]any{"reservedPorts": []uint16{54543, 54544, 54545}}}
	fixture := &fixtureBackend{Backend: backend, scenario: "offline", managementPort: port}
	for i := 0; i < 2; i++ {
		state, err := fixture.Snapshot(context.Background())
		if err != nil || len(state["reservedPorts"].([]uint16)) != 4 {
			t.Fatal("offline snapshot omitted or duplicated management exclusion")
		}
	}
}

type reservedListener struct{ closed int }

func (*reservedListener) Accept() (net.Conn, error) { return nil, net.ErrClosed }
func (r *reservedListener) Close() error            { r.closed++; return nil }
func (*reservedListener) Addr() net.Addr {
	return net.TCPAddrFromAddrPort(netip.MustParseAddrPort("127.0.0.1:43002"))
}

func TestServiceReservationReleasedOnlyForMatchingConnect(t *testing.T) {
	reserved := &reservedListener{}
	backend := &recordingBackend{}
	fixture := &fixtureBackend{Backend: backend, scenario: "studio", servicePort: 43002, serviceReservation: reserved}
	for _, payload := range []string{`{"localPort":43003}`, `{}`} {
		_, _ = fixture.Command(context.Background(), webui.Command{Name: "service.connect", Payload: json.RawMessage(payload)})
		if reserved.closed != 0 {
			t.Fatal("unrelated request released the reserved fixture port")
		}
	}
	for i := 0; i < 2; i++ {
		_, _ = fixture.Command(context.Background(), webui.Command{Name: "service.connect", Payload: json.RawMessage(`{"localPort":43002}`)})
	}
	if reserved.closed != 1 || len(backend.commands) != 4 {
		t.Fatal("matching service action did not release once and preserve Core command handling")
	}
}

func TestSyntheticServiceShareLifecycleUsesCoreWithoutSockets(t *testing.T) {
	netw := &network{listeners: map[netip.AddrPort]*listener{}}
	n := &node{network: netw, ip: netip.MustParseAddr("100.64.0.1"), remoteIP: netip.MustParseAddr("100.64.0.2"), id: "fixture-notebook", remoteID: "fixture-studio", remoteName: "Studio"}
	app, err := core.Open(context.Background(), core.Options{Directory: t.TempDir(), Version: "test", NodeFactory: func(string, string) (core.NetworkBackend, error) { return n, nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	if err := command(context.Background(), app, "network.configure", map[string]any{"mode": "tailnet"}); err != nil {
		t.Fatal(err)
	}
	fixture := &fixtureBackend{Backend: app, scenario: "studio"}
	share := webui.Command{RequestID: "share", Name: "service.share", Payload: json.RawMessage(`{"name":"fixture-share","network":"tcp","ports":"8080","peerIds":["fixture-studio"],"ttlSeconds":60}`)}
	if _, err := fixture.Command(context.Background(), share); err != nil {
		t.Fatal(err)
	}
	state, err := fixture.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	shares := state["shares"].([]map[string]any)
	if len(shares) != 1 || shares[0]["status"] != "active" {
		t.Fatal("share did not enter production active state")
	}
	stop, _ := json.Marshal(map[string]any{"id": shares[0]["id"]})
	if _, err := fixture.Command(context.Background(), webui.Command{RequestID: "stop", Name: "service.stop", Payload: stop}); err != nil {
		t.Fatal(err)
	}
	state, err = fixture.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, share := range state["shares"].([]map[string]any) {
		if share["status"] == "active" {
			t.Fatal("stopped service remained active")
		}
	}
}

func TestLANHostReviewUsesOnlyFixedPrivateFixtureChoices(t *testing.T) {
	backend := &recordingBackend{}
	fixture := &fixtureBackend{Backend: backend, scenario: "offline"}
	value, err := fixture.Command(context.Background(), webui.Command{Name: "lan.addresses", Payload: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	choices := value.(map[string]any)["addresses"].([]core.LANLocalAddress)
	if len(choices) != 1 || choices[0].Interface != "fixture0" || choices[0].Address != "192.168.50.10" || len(backend.commands) != 0 {
		t.Fatal("host review exposed machine interfaces or reached production enumeration")
	}
	if _, err := fixture.Command(context.Background(), webui.Command{Name: "network.configure", Payload: json.RawMessage(`{"mode":"lan","lan":{"kind":"host","address":"192.168.50.10:48443"}}`)}); err == nil || len(backend.commands) != 0 {
		t.Fatal("review fixture activated a real relay")
	}
}

func TestApplicationStopSignalsFixtureOwnerAndPreservesIdleConfiguration(t *testing.T) {
	app, err := core.Open(context.Background(), core.Options{Directory: t.TempDir(), Version: "test", SkipNetworkStart: true, NodeFactory: func(string, string) (core.NetworkBackend, error) {
		t.Fatal("stop must not activate networking")
		return nil, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	fixture := &fixtureBackend{Backend: app, scenario: "offline"}
	value, err := fixture.Command(context.Background(), webui.Command{RequestID: "stop-app", Name: "application.stop", Payload: json.RawMessage(`{}`)})
	if err != nil || value.(map[string]string)["state"] != "stopping" {
		t.Fatal("stop response did not reach the fixture client")
	}
	select {
	case <-app.Done():
	default:
		t.Fatal("fixture owner was not notified to close its server")
	}
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
}
