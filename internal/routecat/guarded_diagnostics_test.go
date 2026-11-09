//go:build lanlink_integration

package routecat

import (
	"net/netip"
	"slices"
	"strings"
	"sync"
	"testing"

	"tailscale.com/ipn/ipnstate"
)

// Diagnostics retain only counts and endpoint equality, never formatted engine
// logs, host interfaces, addresses, keys or capabilities. They do not decide
// whether the guarded integration passes.
type guardedDiagnostics struct {
	mu                                                                 sync.Mutex
	portUpdates, portChanges                                           uint64
	lastPort                                                           uint16
	bindFailures, receiveErrors, discoSendErrors, endpointUpdateErrors uint64
}

func newGuardedDiagnostics() *guardedDiagnostics { return new(guardedDiagnostics) }

func (d *guardedDiagnostics) portUpdate(port uint16) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.portUpdates++
	if d.lastPort != 0 && d.lastPort != port {
		d.portChanges++
	}
	d.lastPort = port
}

func (d *guardedDiagnostics) logf(format string, _ ...any) {
	d.mu.Lock()
	defer d.mu.Unlock()
	switch {
	case strings.Contains(format, "magicsock: unable to bind"):
		d.bindFailures++
	case strings.Contains(format, "Receive func %s exiting with error"):
		d.receiveErrors++
	case strings.Contains(format, "magicsock: disco: failed to send"):
		d.discoSendErrors++
	case strings.Contains(format, "magicsock: endpoint update (%s) failed"):
		d.endpointUpdateErrors++
	}
}

func (d *guardedDiagnostics) report(t *testing.T, role string, backend *locoBackend, selected, peerSelected netip.AddrPort) {
	t.Helper()
	backend.mu.Lock()
	backendHasSelected := slices.Contains(backend.eps, selected)
	backendEndpointCount := len(backend.eps)
	nm := backend.nm
	backend.mu.Unlock()
	mc := backend.sys.MagicSock.Get()
	sb := ipnstate.StatusBuilder{WantPeers: true}
	mc.UpdateStatus(&sb)
	status := sb.Status()
	selfHasSelected := status.Self != nil && slices.Contains(status.Self.Addrs, selected.String())
	var currentSelected, currentOther, retainedNewCandidates, retainedRemovals, retainedDirectPongs int
	for _, peer := range status.Peer {
		if peer.CurAddr == peerSelected.String() {
			currentSelected++
		} else if peer.CurAddr != "" {
			currentOther++
		}
	}
	if nm != nil {
		for _, peer := range nm.Peers {
			changes, err := mc.GetEndpointChanges(peer)
			if err != nil {
				continue
			}
			for _, change := range changes {
				switch change.What {
				case "handleCallMeMaybe-new-endpoints":
					retainedNewCandidates++
				case "deleteEndpointLocked-handleCallMeMaybe":
					retainedRemovals++
				case "handlePongConnLocked-bestAddr-update", "handlePongConnLocked-bestAddr-latency":
					retainedDirectPongs++
				}
			}
		}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	t.Logf("guarded endpoint diagnostics: role=%s port_updates=%d port_changes=%d selected_port_matches_latest_event=%t backend_endpoints=%d backend_has_selected=%t magicsock_has_selected=%t current_selected=%d current_other=%d retained_new_candidates=%d retained_candidate_removals=%d retained_direct_path_events=%d bind_failures=%d receive_errors=%d disco_send_errors=%d endpoint_update_errors=%d",
		role, d.portUpdates, d.portChanges, d.lastPort == selected.Port(), backendEndpointCount, backendHasSelected, selfHasSelected, currentSelected, currentOther, retainedNewCandidates, retainedRemovals, retainedDirectPongs, d.bindFailures, d.receiveErrors, d.discoSendErrors, d.endpointUpdateErrors)
}

func TestGuardedDiagnosticsRetainOnlyCounts(t *testing.T) {
	d := newGuardedDiagnostics()
	d.portUpdate(42000)
	d.portUpdate(42000)
	d.portUpdate(42001)
	d.logf("ignored %s", "synthetic-private-value")
	d.logf("magicsock: unable to bind %v port %d: %v", "synthetic-private-value")
	d.logf("Receive func %s exiting with error: %T, %v", "synthetic-private-value")
	d.logf("magicsock: disco: failed to send %v to %v %s: %v", "synthetic-private-value")
	d.logf("magicsock: endpoint update (%s) failed: %v", "synthetic-private-value")
	if d.portUpdates != 3 || d.portChanges != 1 || d.lastPort != 42001 || d.bindFailures != 1 || d.receiveErrors != 1 || d.discoSendErrors != 1 || d.endpointUpdateErrors != 1 {
		t.Fatal("diagnostics did not retain the expected counts")
	}
}
