package core

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/lanpolicy"
)

// NetworkResumeReview describes an unchanged saved network, not new authority.
// Its opaque revision is valid only for this offline process and exact settings.
type NetworkResumeReview struct {
	TrustedDevices         int                     `json:"trustedDevices"`
	PairedDevices          int                     `json:"pairedDevices"`
	Revision               string                  `json:"revision"`
	Mode                   string                  `json:"mode"`
	Hostname               string                  `json:"hostname"`
	Backends               []string                `json:"backends"`
	ExternalControlTraffic bool                    `json:"externalControlTraffic"`
	LAN                    *NetworkResumeLAN       `json:"lan,omitempty"`
	DirectLAN              *DirectLANSelection     `json:"directLAN,omitempty"`
	AutomaticReceivers     []NetworkResumeReceiver `json:"automaticReceivers"`
	StartupSuppressed      bool                    `json:"startupSuppressed"`
	SavedProxiesSuppressed bool                    `json:"savedProxiesSuppressed"`
	PendingStartup         []string                `json:"pendingStartup"`
}
type NetworkResumeLAN struct {
	Relay          LANSelection        `json:"relay"`
	Policy         lanpolicy.Config    `json:"policy"`
	PreparedRelays []LANSelection      `json:"preparedRelays"`
	WANCandidates  *WANCandidateConfig `json:"wanCandidates,omitempty"`
}
type NetworkResumeReceiver struct {
	PeerID    string `json:"peerId"`
	Name      string `json:"name"`
	Directory string `json:"directory"`
}

func resumeUnavailable() error {
	return &localCommandError{"network_resume_unavailable", "resume requires an offline owner with an unstarted saved network; inspect current status or reopen local management offline"}
}

// Caller holds c.op. No network, persistence, initialization or expiry renewal
// is performed by this review. Apply rechecks it in the same command ordering.
// The offline/unattempted guard excludes transport callbacks; c.op orders every
// settings mutation. startNetwork retains native admission and expiry checks.
func (c *Core) networkResumeReview() (*NetworkResumeReview, error) {
	c.mu.RLock()
	if !c.startupSuppressed || c.node != nil || c.attemptedNetwork != "" || c.closing || c.ctx.Err() != nil {
		c.mu.RUnlock()
		return nil, resumeUnavailable()
	}
	p := c.profile
	view := &NetworkResumeReview{Mode: p.Settings.Network, Hostname: p.Settings.Hostname, Backends: []string{p.Settings.Network}, AutomaticReceivers: []NetworkResumeReceiver{}, StartupSuppressed: c.startupSuppressed, SavedProxiesSuppressed: c.startupSuppressed, PendingStartup: []string{}}
	basis := map[string]any{"purpose": "saved-network-resume-v1", "process": c.lanStartNonce, "directory": c.dir, "profile": p, "capacity": c.capacity, "startup": c.startup, "proxies": c.savedProxies, "pendingStartup": c.startupPending, "pendingProxies": c.savedProxyPending, "suppressed": c.startupSuppressed, "writeRevision": c.lanStartWriteRevision.Load()}
	for name := range c.startupPending {
		view.PendingStartup = append(view.PendingStartup, name)
	}
	for name := range c.savedProxyPending {
		view.PendingStartup = append(view.PendingStartup, name)
	}
	for _, peer := range p.Peers {
		if peer.Network == view.Mode {
			view.TrustedDevices++
		}
		if peer.Network == view.Mode && peer.Autosave && !peer.Paused {
			directory := peer.Directory
			if directory == "" {
				directory = p.Settings.ReceiveDirectory
			}
			view.AutomaticReceivers = append(view.AutomaticReceivers, NetworkResumeReceiver{peer.ID, peer.Name, directory})
		}
	}
	// Serialize while holding mu: maps in the basis must not escape unlocked.
	profileRevision := privateRevision(basis)
	c.mu.RUnlock()
	basis = map[string]any{"profile": profileRevision}
	if view.Mode != "tailnet" && view.Mode != "lan" && view.Mode != "direct-lan" && view.Mode != "mixed" {
		return nil, resumeUnavailable()
	}
	if err := validateCapacityBackend(view.Mode, c.capacityPolicy()); err != nil {
		return nil, err
	}
	if view.Mode == "mixed" {
		state, err := c.readMixedState()
		if err != nil {
			return nil, c.codedMixedError(err)
		}
		view.Backends = append([]string{}, state.Selection.Backends...)
		basis["mixed"] = state
	}
	for _, mode := range view.Backends {
		switch mode {
		case "tailnet":
			view.ExternalControlTraffic = true
		case "lan":
			store := c.lanStoreCopy()
			if store == nil {
				return nil, resumeUnavailable()
			}
			state := store.copy()
			view.PairedDevices += len(state.Remotes)
			store.mu.Lock()
			uncertain, revision := store.routeRecovery || store.startUncertain, store.reviewRevision
			store.mu.Unlock()
			if uncertain || c.lanStartUncertain.Load() {
				return nil, config.ErrAtomicRecovery
			}
			if state.Selection == nil {
				return nil, resumeUnavailable()
			}
			policy, err := state.DestinationPolicy.Canonical()
			if err != nil {
				return nil, err
			}
			view.LAN = &NetworkResumeLAN{Relay: *state.Selection, Policy: policy, PreparedRelays: []LANSelection{}, WANCandidates: cloneWANCandidates(state.WANCandidates)}
			for _, candidate := range state.RouteCandidates {
				view.LAN.PreparedRelays = append(view.LAN.PreparedRelays, LANSelection{Kind: "relay", Address: candidate.Relay.Address.String(), CertificateSHA256: candidate.Relay.CertificateSHA256})
			}
			basis["lan"], basis["lanRevision"] = state, revision
		case "direct-lan":
			store := c.directLANStoreCopy()
			if store == nil {
				return nil, resumeUnavailable()
			}
			store.mu.Lock()
			if store.recovery || store.endpointTransaction != nil {
				store.mu.Unlock()
				return nil, config.ErrAtomicRecovery
			}
			state := cloneDirectLANState(store.state)
			view.PairedDevices += len(state.Peers)
			basis["directLAN"], basis["directLANMetadata"], basis["directLANRevision"], basis["directLANFile"] = state, state.Metadata, store.reviewRevision, store.fileDigest
			store.mu.Unlock()
			selection := state.Selection
			view.DirectLAN = &selection
		}
	}
	slices.Sort(view.PendingStartup)
	view.Revision = privateRevision(basis)
	return view, nil
}
func (c *Core) networkResumeCommand(ctx context.Context, name string, raw json.RawMessage) (any, error) {
	var input struct {
		ExpectedRevision string `json:"expectedRevision"`
	}
	if name == "network.resume.preview" {
		if err := decodePayload(raw, &struct{}{}); err != nil {
			return nil, err
		}
		return c.networkResumeReview()
	}
	if err := decodePayload(raw, &input); err != nil {
		return nil, err
	}
	view, err := c.networkResumeReview()
	if err != nil {
		return nil, err
	}
	if input.ExpectedRevision == "" || input.ExpectedRevision != view.Revision {
		return nil, &localCommandError{"network_resume_changed", "saved network settings or permissions changed; refresh and review before resuming"}
	}
	// Cancel conflicting setup only after the exact review has been accepted.
	c.invalidateSetupAuthority("network.configure")
	if err := c.startNetwork(ctx); err != nil {
		c.mu.Lock()
		c.networkState, c.networkError, c.networkErrorCode = "error", err.Error(), networkErrorCode(err)
		c.mu.Unlock()
		return nil, err
	}
	return map[string]string{"state": "starting", "mode": view.Mode}, nil
}
