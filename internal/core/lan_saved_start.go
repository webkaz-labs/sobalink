package core

import (
	"slices"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/lanpolicy"
)

// LANStartReview is a read-only view of an existing local host. The revision is
// an opaque compare-and-start guard, not a new grant or a portable capability.
type LANStartReview struct {
	Revision           string              `json:"revision"`
	PublicKey          string              `json:"publicKey"`
	Hostname           string              `json:"hostname"`
	Relay              LANSelection        `json:"relay"`
	Policy             lanpolicy.Config    `json:"policy"`
	PairedDevices      int                 `json:"pairedDevices"`
	TrustedDevices     int                 `json:"trustedDevices"`
	AutomaticReceivers int                 `json:"automaticReceivers"`
	PreparedRelays     []LANSelection      `json:"preparedRelays"`
	WANCandidates      *WANCandidateConfig `json:"wanCandidates,omitempty"`
	PendingStartup     []string            `json:"pendingStartup"`
}

func (c *Core) savedLANStartReview(store *lanStore, state lanState) *LANStartReview {
	store.mu.Lock()
	storeRevision, uncertain := store.reviewRevision, store.routeRecovery || store.startUncertain
	store.mu.Unlock()
	if uncertain || c.lanStartUncertain.Load() || state.Selection == nil || state.Selection.Kind != "host" {
		return nil
	}
	if _, err := savedLANRelay(state); err != nil {
		return nil
	}
	policy, err := state.DestinationPolicy.Canonical()
	if err != nil {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	p := c.profile
	if c.node != nil || c.closing || c.ctx.Err() != nil ||
		(p.Settings.Network != "none" && p.Settings.Network != "lan") ||
		(c.attemptedNetwork != "" && (c.attemptedNetwork != "lan" || c.attemptedHostname != p.Settings.Hostname)) {
		return nil
	}
	view := &LANStartReview{
		PublicKey: state.Identity.PublicKey(), Hostname: p.Settings.Hostname, Relay: *state.Selection, Policy: policy,
		PairedDevices: len(state.Remotes), PreparedRelays: []LANSelection{},
		WANCandidates: cloneWANCandidates(state.WANCandidates), PendingStartup: []string{},
	}
	for _, peer := range p.Peers {
		if peer.Network == "lan" {
			view.TrustedDevices++
			if peer.Autosave && !peer.Paused {
				view.AutomaticReceivers++
			}
		}
	}
	for _, candidate := range state.RouteCandidates {
		view.PreparedRelays = append(view.PreparedRelays, LANSelection{Kind: "relay", Address: candidate.Relay.Address.String(), CertificateSHA256: candidate.Relay.CertificateSHA256})
	}
	for name := range c.startupPending {
		view.PendingStartup = append(view.PendingStartup, name)
	}
	for name := range c.savedProxyPending {
		view.PendingStartup = append(view.PendingStartup, name)
	}
	slices.Sort(view.PendingStartup)
	// Include all persisted LAN material (identity, pin, routes, pair roles and
	// generations, destination and WAN policy) and application/startup authority.
	// None of the private values are returned. Offline state cannot be changed
	// by a transport callback; every configuration mutation shares c.op with the
	// guarded network.configure comparison and start.
	view.Revision = privateRevision(map[string]any{
		"purpose": "saved-lan-host-start-v1", "process": c.lanStartNonce, "profileDirectory": c.dir,
		"lan": state, "profile": p, "capacity": c.capacity,
		"startup": c.startup, "proxies": c.savedProxies,
		"pendingStartup": c.startupPending, "pendingProxies": c.savedProxyPending,
		"attemptedNetwork": c.attemptedNetwork, "attemptedHostname": c.attemptedHostname,
		"storeRevision": storeRevision, "writeRevision": c.lanStartWriteRevision.Load(),
	})
	return view
}

// Called only by network.configure under c.op, before any write or networking.
// This must not be replaced by a UI refresh/check: another command can change
// the saved policy between a browser review and its eventual execution.
func (c *Core) checkSavedLANStart(revision string) error {
	store := c.lanStoreCopy()
	if c.lanStartUncertain.Load() || store != nil && store.routesNeedRecovery() {
		return config.ErrAtomicRecovery
	}
	if store != nil {
		view := c.savedLANStartReview(store, store.copy())
		if view != nil && revision != "" && view.Revision == revision {
			return nil
		}
	}
	return &lanCommandError{"lan_saved_start_changed", "saved LAN host settings or current authority changed; refresh and review the saved host again before starting"}
}
