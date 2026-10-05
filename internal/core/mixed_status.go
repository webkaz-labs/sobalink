package core

import (
	"context"
	"maps"
	"sort"
	"time"
)

func (n *mixedBackend) peerNetworks(peerID string) []string {
	out := []string{"mixed"}
	n.mu.Lock()
	states := maps.Clone(n.cached)
	n.mu.Unlock()
	for _, name := range n.order {
		for _, p := range states[name].Snapshot.Peers {
			if n.logical(name, p.ID) == peerID {
				out = append(out, name)
				break
			}
		}
	}
	return out
}
func (c *Core) addMixedRuntimeStatus(out map[string]any) {
	n, ok := c.nodeCopy().(*mixedBackend)
	if !ok {
		out["active"] = false
		return
	}
	out["active"] = true
	ctx, cancel := context.WithTimeout(c.ctx, 2*time.Second)
	defer cancel()
	routes, states, e := n.routeSnapshot(ctx)
	if e != nil {
		out["backendStatusAvailable"] = false
		return
	}
	out["backendStatusAvailable"] = true
	var backends []map[string]any
	for _, name := range n.order {
		s := states[name]
		backends = append(backends, map[string]any{"backend": name, "state": s.Backend, "running": s.Snapshot.Running, "selfId": s.SelfID})
	}
	out["backendStates"] = backends
	var peers []map[string]any
	for id, rs := range routes {
		for _, r := range rs {
			peers = append(peers, map[string]any{"peerId": id, "backend": r.backend, "transportId": r.id, "name": r.peer.DNSName, "backendReady": states[r.backend].Snapshot.Running, "expired": r.peer.Expired})
		}
	}
	sort.Slice(peers, func(i, j int) bool {
		a, b := peers[i], peers[j]
		if a["peerId"] == b["peerId"] {
			return a["backend"].(string) < b["backend"].(string)
		}
		return a["peerId"].(string) < b["peerId"].(string)
	})
	out["routes"] = peers
	current, e := selectedWorkerLimits(c.capacityPolicy())
	out["workerResources"] = n.workerLimits
	out["resourceRestartRequired"] = e != nil || current != n.workerLimits
	out["automaticSwitchTrigger"] = "backend-unavailable"
}
