//go:build soba_e2e

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/core"
	"github.com/webkaz-labs/sobalink/internal/transfer"
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
	for _, name := range []string{"studio", "offline", "receive-legacy", "receive-damaged"} {
		if !validScenario(name) {
			t.Fatalf("supported scenario rejected: %s", name)
		}
	}
	for _, name := range []string{"", "live", "lan", "../studio", "OFFLINE", "receive", "receive-ready"} {
		if validScenario(name) {
			t.Fatalf("unknown scenario accepted: %s", name)
		}
	}
}

func TestFixtureBlocksLiveActivationBeforeCore(t *testing.T) {
	for _, scenario := range []string{"studio", "offline", "receive-legacy", "receive-damaged"} {
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

func TestReceiveRecoveryFixturesKeepUploadsOnProductionPath(t *testing.T) {
	for _, scenario := range []string{"receive-legacy", "receive-damaged"} {
		backend := &recordingBackend{}
		fixture := &fixtureBackend{Backend: backend, scenario: scenario}
		request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/api/upload", strings.NewReader("fictional outgoing batch"))
		response := httptest.NewRecorder()
		fixture.Upload(response, request)
		if response.Code != http.StatusOK || backend.uploads != 1 || backend.uploadBody != "fictional outgoing batch" {
			t.Fatal("receive recovery scenario injected an unrelated outgoing upload failure")
		}
	}
}

func TestReceiveRecoveryFixturesUsePersistedCoreAccounting(t *testing.T) {
	for _, scenario := range []string{"receive-legacy", "receive-damaged"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			directory := filepath.Join(root, "notebook")
			receiveDirectory := filepath.Join(root, "received")
			if err := os.Mkdir(receiveDirectory, 0700); err != nil {
				t.Fatal(err)
			}
			savedFile := filepath.Join(receiveDirectory, "saved-note.txt")
			if err := os.WriteFile(savedFile, []byte("Fictional saved notes.\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := prepareReceiveRecoveryFixture(directory, receiveDirectory, scenario); err != nil {
				t.Fatal(err)
			}
			index := filepath.Join(directory, "receive-accounting.json")
			originalIndex, indexErr := os.ReadFile(index)
			wantCode := "legacy_review_required"
			if scenario == "receive-legacy" {
				if !errors.Is(indexErr, os.ErrNotExist) {
					t.Fatal("legacy fixture must begin with an absent accounting index")
				}
			} else {
				wantCode = "index_unavailable"
				if indexErr != nil || json.Valid(originalIndex) {
					t.Fatal("damaged fixture must begin with a malformed accounting index")
				}
				info, err := os.Stat(index)
				// Windows reports writable files as 0666; POSIX modes do not
				// describe its DACL-protected fixture directory.
				if err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0600) {
					t.Fatal("damaged accounting fixture was not private")
				}
			}
			netw := &network{listeners: map[netip.AddrPort]*listener{}}
			n := &node{network: netw, ip: netip.MustParseAddr("100.64.0.1"), remoteIP: netip.MustParseAddr("100.64.0.2"), id: "fixture-notebook", remoteID: "fixture-studio", remoteName: "Studio"}
			app, err := core.Open(context.Background(), core.Options{Directory: directory, SkipNetworkStart: true, NodeFactory: func(string, string) (core.NetworkBackend, error) { return n, nil }})
			if err != nil {
				t.Fatal(err)
			}
			defer app.Close()
			fixture := &fixtureBackend{Backend: app, scenario: scenario}
			if err := command(context.Background(), app, "network.configure", map[string]string{"mode": "tailnet", "hostname": "Notebook"}); err != nil {
				t.Fatal("receive gate prevented synthetic network activation", err)
			}
			if err := command(context.Background(), app, "peer.trust", map[string]any{"peerId": "fixture-studio", "trusted": true}); err != nil {
				t.Fatal("receive gate prevented existing peer approval", err)
			}
			state, err := fixture.Snapshot(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			beforeSettings := state["settings"].(core.Settings)
			peers := state["peers"].([]map[string]any)
			if beforeSettings.ReceiveDirectory != receiveDirectory || len(peers) != 1 || peers[0]["trusted"] != true || peers[0]["online"] != true {
				t.Fatal("recovery fixture lost its saved destination or synthetic peer")
			}
			beforeAutosave := peers[0]["autosave"].(map[string]any)
			if beforeAutosave["enabled"] != true || beforeAutosave["paused"] != false || beforeAutosave["directory"] != receiveDirectory {
				t.Fatal("recovery fixture did not retain saved autosave")
			}
			assertState := func(wantState, code string) {
				t.Helper()
				state, err := fixture.Snapshot(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				view := state["receiveRecovery"].(transfer.ReceiveRecoveryView)
				if view.State != wantState || view.Code != code || (wantState == "blocked" && view.ReservedBytes != nil) || (wantState == "ready" && (view.ReservedBytes == nil || *view.ReservedBytes != 0)) {
					t.Fatal("production receive recovery state did not match fixture contract")
				}
				if !reflect.DeepEqual(state["settings"], beforeSettings) || !reflect.DeepEqual(state["peers"].([]map[string]any)[0]["autosave"], beforeAutosave) {
					t.Fatal("receive review changed saved receive settings")
				}
				encoded, err := json.Marshal(state)
				if err != nil || strings.Contains(string(encoded), "fictional-receive-index") || strings.Contains(string(encoded), "receive-accounting.json") {
					t.Fatal("private accounting contents escaped through the fixture snapshot")
				}
			}
			assertState("blocked", wantCode)
			preview, err := fixture.Command(context.Background(), webui.Command{RequestID: "preview", Name: "receive.recovery.confirm", Payload: json.RawMessage(`{}`)})
			if err != nil || preview.(transfer.ReceiveRecoveryView).Applied {
				t.Fatal("unreviewed preview changed accounting")
			}
			assertState("blocked", wantCode)
			if data, err := os.ReadFile(index); (scenario == "receive-legacy" && !errors.Is(err, os.ErrNotExist)) || (scenario == "receive-damaged" && (err != nil || string(data) != string(originalIndex))) {
				t.Fatal("opening or previewing recovery changed its private accounting fixture")
			}
			if err := command(context.Background(), app, "service.share", map[string]any{"name": "fixture-share", "network": "tcp", "ports": "8080", "peerIds": []string{"fixture-studio"}, "ttlSeconds": 60}); err != nil {
				t.Fatal("receive gate blocked unrelated service lifecycle", err)
			}
			for attempt := 0; attempt < 2; attempt++ {
				value, err := fixture.Command(context.Background(), webui.Command{RequestID: fmt.Sprintf("confirm-%d", attempt), Name: "receive.recovery.confirm", Payload: json.RawMessage(`{"reviewed":true}`)})
				if scenario == "receive-legacy" {
					if err != nil || value.(transfer.ReceiveRecoveryView).Applied != (attempt == 0) {
						t.Fatal("review did not initialize legacy accounting exactly once")
					}
					assertState("ready", "")
				} else {
					if err == nil || strings.Contains(err.Error(), "fictional-receive-index") || strings.Contains(err.Error(), directory) {
						t.Fatal("damaged accounting review did not fail privately")
					}
					assertState("blocked", wantCode)
				}
			}
			state, err = fixture.Snapshot(context.Background())
			if err != nil || len(state["shares"].([]map[string]any)) != 1 || state["shares"].([]map[string]any)[0]["status"] != "active" {
				t.Fatal("receive review interrupted the unrelated active service")
			}
			if err := app.Close(); err != nil {
				t.Fatal(err)
			}
			app, err = core.Open(context.Background(), core.Options{Directory: directory, SkipNetworkStart: true})
			if err != nil {
				t.Fatal("reviewed fixture could not reopen", err)
			}
			defer app.Close()
			state, err = app.Snapshot(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			view := state["receiveRecovery"].(transfer.ReceiveRecoveryView)
			if (scenario == "receive-legacy" && view.State != "ready") || (scenario == "receive-damaged" && (view.Code != wantCode || view.ReservedBytes != nil)) {
				t.Fatal("receive recovery result did not survive Core restart")
			}
			var savedProfile core.Profile
			profileBytes, err := os.ReadFile(filepath.Join(directory, "sobalink.json"))
			if err != nil || json.Unmarshal(profileBytes, &savedProfile) != nil || savedProfile.Settings != beforeSettings || len(savedProfile.Peers) != 1 || !savedProfile.Peers[0].Autosave || savedProfile.Peers[0].Paused || savedProfile.Peers[0].Directory != receiveDirectory {
				t.Fatal("receive review or restart changed persisted receive settings")
			}
			if scenario == "receive-damaged" {
				if data, err := os.ReadFile(index); err != nil || string(data) != string(originalIndex) {
					t.Fatal("confirmation or shutdown discarded damaged accounting")
				}
			}
			if data, err := os.ReadFile(savedFile); err != nil || string(data) != "Fictional saved notes.\n" {
				t.Fatal("receive review changed previously saved fixture output")
			}
		})
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
