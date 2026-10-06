package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/backendworker"
	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/identity"
	"github.com/webkaz-labs/sobalink/internal/routecat"
)

func workerCapacityLANFixture(t *testing.T, candidates int) string {
	t.Helper()
	// Match existing socket-free LAN fixtures: inherited runner proxy/control
	// variables are not the explicit synthetic product configuration.
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		upper := strings.ToUpper(name)
		if upper == "HTTP_PROXY" || upper == "HTTPS_PROXY" || upper == "ALL_PROXY" || upper == "TS_PROXY" || strings.HasPrefix(upper, "TS_") && upper != "TS_NO_LOGS_NO_SUPPORT" {
			t.Setenv(name, "")
		}
	}
	c := openLANTestCore(t)
	if e := c.configureLAN(testLANSelection()); e != nil {
		t.Fatal(e)
	}
	for i := 1; i < candidates; i++ {
		route := extraRouteFixture()
		route.Relay.Address = netip.MustParseAddrPort(fmt.Sprintf("192.0.2.%d:443", 20+i))
		if e := c.editLANRoute(route, ""); e != nil {
			t.Fatal(e)
		}
	}
	dir := c.dir
	if e := c.Close(); e != nil {
		t.Fatal(e)
	}
	return dir
}
func writeWorkerCapacity(t *testing.T, dir string, p capacity.Policy) {
	t.Helper()
	raw, e := json.Marshal(p)
	if e != nil {
		t.Fatal(e)
	}
	if e = config.AtomicWrite(filepath.Join(dir, capacityPolicyFile), raw); e != nil {
		t.Fatal(e)
	}
}

func TestNetworkWorkerSavedLANPresenceBudgetWithSelectedIPC(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		candidates, presence int
		allowed              bool
	}{
		{"raised-seven", 7, 7, true}, {"lowered-one", 2, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := workerCapacityLANFixture(t, tc.candidates)
			p := capacity.Defaults()
			p.Resources["relayPresenceConnections"] = capacity.Limited(int64(tc.presence))
			p.Resources["relayCandidateAttempts"] = capacity.Limited(6)
			p.Resources["relayTLSConnections"] = capacity.Limited(128)
			p.Resources["relayAdmissionConnections"] = capacity.Limited(32)
			p.Resources["workerRequests"] = capacity.Limited(24)
			writeWorkerCapacity(t, dir, p)
			selected := backendworker.Limits{FrameBytes: 256 << 10, Requests: 16, Handles: 12}
			node, limits, e := prepareNetworkWorker(context.Background(), dir, "lan", "fixture-worker", selected)
			if node != nil {
				defer node.Close()
			}
			if (e == nil) != tc.allowed {
				t.Fatalf("saved LAN presence budget not used: candidates=%d budget=%d error=%v", tc.candidates, tc.presence, e)
			}
			if !tc.allowed {
				var budget *routecat.RelayPresenceBudgetError
				if !errors.As(e, &budget) || budget.Candidates != tc.candidates || budget.Limit != tc.presence {
					t.Fatalf("unexpected constructor rejection: %v", e)
				}
			}
			if limits != selected {
				t.Fatalf("selected IPC allocation replaced by saved budget: %v", limits)
			}
			if tc.allowed {
				if _, ok := node.(*lanBackend); !ok {
					t.Fatal("wrong worker backend")
				}
			}
		})
	}
}

func TestNetworkWorkerCapacityCorruptionFailsBeforeStartupWithSelectedIPC(t *testing.T) {
	for _, mode := range []string{"lan", "tailnet"} {
		for _, raw := range []string{`{"version":`, `{"version":999}`} {
			t.Run(mode+raw, func(t *testing.T) {
				dir := t.TempDir()
				if e := config.AtomicWrite(filepath.Join(dir, capacityPolicyFile), []byte(raw)); e != nil {
					t.Fatal(e)
				}
				// The exported entrypoint must return before constructing/starting an engine
				// or accessing the deliberately absent owner pipes.
				if e := RunNetworkWorker(context.Background(), dir, mode, "fixture-worker", nil, nil, backendworker.DefaultLimits()); e == nil {
					t.Fatal("corrupt saved policy ignored")
				}
				if _, e := os.Stat(filepath.Join(dir, "identity")); !os.IsNotExist(e) {
					t.Fatal("identity was initialized before policy validation", e)
				}
			})
		}
	}
}

func TestNetworkWorkerLANStoreUsesSavedCapacitySnapshot(t *testing.T) {
	dir := workerCapacityLANFixture(t, 1)
	p := capacity.Defaults()
	p.Resources["lanStateBytes"] = capacity.Limited(1)
	writeWorkerCapacity(t, dir, p)
	node, _, e := prepareNetworkWorker(context.Background(), dir, "lan", "fixture-worker", backendworker.DefaultLimits())
	if node != nil {
		node.Close()
	}
	if networkErrorCode(e) != "lan_state_capacity" {
		t.Fatalf("saved LAN store byte budget ignored: %v", e)
	}
}

func TestNetworkWorkerCapacityDefaultsAndTailnetIPCSelection(t *testing.T) {
	dir := workerCapacityLANFixture(t, 1)
	node, limits, e := prepareNetworkWorker(context.Background(), dir, "lan", "fixture-worker")
	if e != nil {
		t.Fatal(e)
	}
	node.Close()
	catalogDefaults, e := selectedWorkerLimits(capacity.Defaults())
	if e != nil || limits != catalogDefaults {
		t.Fatal("legacy saved-policy default IPC budgets changed", limits, e)
	}
	tailDir := t.TempDir()
	p := capacity.Defaults()
	p.Resources["workerFrameBytes"] = capacity.Limited(256 << 10)
	p.Resources["workerRequests"] = capacity.Limited(24)
	p.Resources["workerHandles"] = capacity.Limited(12)
	writeWorkerCapacity(t, tailDir, p)
	expected, e := selectedWorkerLimits(p)
	if e != nil {
		t.Fatal(e)
	}
	for _, explicit := range []bool{false, true} {
		var selected []backendworker.Limits
		want := expected
		if explicit {
			want = backendworker.DefaultLimits()
			selected = []backendworker.Limits{want}
		}
		node, limits, e := prepareNetworkWorker(context.Background(), tailDir, "tailnet", "fixture-worker", selected...)
		if e != nil {
			t.Fatal(e)
		}
		if _, ok := node.(*identity.Node); !ok {
			t.Fatal("Tailnet constructor changed")
		}
		node.Close()
		if limits != want {
			t.Fatalf("Tailnet IPC allocation changed: %v want %v", limits, want)
		}
	}
}
