package core

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"reflect"
	"slices"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/lanpolicy"
	"github.com/webkaz-labs/sobalink/internal/routecat"
)

// WANCandidateConfig is an explicit local choice, never imported from a peer or
// a relay capability. The private state stores it separately from relay pins.
type WANCandidateConfig struct {
	STUNEndpoints []string `json:"stunEndpoints"`
	AdvertiseIPv6 bool     `json:"advertiseIPv6"`
	ProbeBudget   int      `json:"probeBudget"`
}

// Canonical validates without opening sockets or checking network reachability.
// It deliberately works in builds without UDP so saved state remains readable.
func (w WANCandidateConfig) Canonical() (WANCandidateConfig, error) {
	invalid := func() (WANCandidateConfig, error) {
		return WANCandidateConfig{}, &lanCommandError{"wan_candidates_invalid", "choose distinct canonical numeric STUN IP:port endpoints or explicitly enable IPv6 candidates, with a finite positive probe budget"}
	}
	if len(w.STUNEndpoints) > routecat.STUNRegionNamespaceSize || w.ProbeBudget < 0 || w.ProbeBudget > routecat.STUNRegionNamespaceSize || len(w.STUNEndpoints) == 0 && !w.AdvertiseIPv6 {
		return invalid()
	}
	if w.ProbeBudget == 0 {
		w.ProbeBudget = routecat.DefaultWANProbeBudget
	}
	w.STUNEndpoints = append([]string{}, w.STUNEndpoints...)
	for _, raw := range w.STUNEndpoints {
		ep, err := netip.ParseAddrPort(raw)
		ip := ep.Addr()
		if err != nil || ep.String() != raw || ep.Port() == 0 || ip.Is4In6() || ip.Zone() != "" || (!ip.IsGlobalUnicast() && !ip.IsLoopback()) {
			return invalid()
		}
	}
	slices.Sort(w.STUNEndpoints)
	for i := 1; i < len(w.STUNEndpoints); i++ {
		if w.STUNEndpoints[i] == w.STUNEndpoints[i-1] {
			return invalid()
		}
	}
	return w, nil
}

func cloneWANCandidates(w *WANCandidateConfig) *WANCandidateConfig {
	if w == nil {
		return nil
	}
	return &WANCandidateConfig{STUNEndpoints: slices.Clone(w.STUNEndpoints), AdvertiseIPv6: w.AdvertiseIPv6, ProbeBudget: w.ProbeBudget}
}

func wanTransportConfig(w *WANCandidateConfig) (*routecat.WANConfig, error) {
	if w == nil {
		return nil, nil
	}
	canonical, err := w.Canonical()
	if err != nil {
		return nil, err
	}
	result := &routecat.WANConfig{AdvertiseIPv6: canonical.AdvertiseIPv6, ProbeBudget: canonical.ProbeBudget}
	for _, raw := range canonical.STUNEndpoints {
		ep, _ := netip.ParseAddrPort(raw)
		result.STUNEndpoints = append(result.STUNEndpoints, ep)
	}
	return result, nil
}

func validateWANCandidateState(state lanState) error {
	if state.WANCandidates == nil {
		return nil
	}
	if state.Version < 5 {
		return errors.New("WAN candidate settings require private state version 5")
	}
	if state.Selection == nil {
		return &lanCommandError{"wan_candidates_setup_required", "select a pinned relay before configuring WAN candidates"}
	}
	policy, err := state.DestinationPolicy.Canonical()
	if err != nil {
		return err
	}
	if policy.Mode != lanpolicy.TrustedRelay {
		return &lanCommandError{"wan_candidates_restricted", "disable WAN candidates before choosing allowed LAN destinations; WAN candidates require trusted-relay mode"}
	}
	_, err = state.WANCandidates.Canonical()
	return err
}

func (c *Core) wanCandidatesView(w *WANCandidateConfig) map[string]any {
	endpoints := []string{}
	ipv6 := false
	budget := routecat.DefaultWANProbeBudget
	if w != nil {
		endpoints = append(endpoints, w.STUNEndpoints...)
		ipv6 = w.AdvertiseIPv6
		if w.ProbeBudget > 0 {
			budget = w.ProbeBudget
		}
	}
	return map[string]any{"enabled": w != nil, "stunEndpoints": endpoints, "advertiseIPv6": ipv6, "probeBudget": budget, "editable": c.nodeCopy() == nil, "restartRequired": true}
}

func (c *Core) wanCandidatesCommand(ctx context.Context, name string, raw json.RawMessage) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if name == "wan.candidates.get" {
		var input struct{}
		if err := decodePayload(raw, &input); err != nil {
			return nil, err
		}
		if store := c.lanStoreCopy(); store != nil {
			return c.wanCandidatesView(store.copy().WANCandidates), nil
		}
		return c.wanCandidatesView(nil), nil
	}
	if name != "wan.candidates.set" {
		return nil, errors.New("unknown WAN candidate command")
	}
	var input struct {
		Enabled       *bool    `json:"enabled"`
		STUNEndpoints []string `json:"stunEndpoints"`
		AdvertiseIPv6 bool     `json:"advertiseIPv6"`
		ProbeBudget   *int     `json:"probeBudget"`
	}
	if err := decodePayload(raw, &input); err != nil {
		return nil, err
	}
	if input.Enabled == nil {
		return nil, &lanCommandError{"wan_candidates_invalid", "explicitly enable or disable WAN candidates"}
	}
	budget := routecat.DefaultWANProbeBudget
	if input.ProbeBudget != nil {
		budget = *input.ProbeBudget
		if budget < 1 || budget > routecat.STUNRegionNamespaceSize {
			return nil, &lanCommandError{"wan_candidates_invalid", "probeBudget must be a finite positive integer within the discovery-ID namespace"}
		}
	}
	var requested *WANCandidateConfig
	if *input.Enabled {
		canonical, err := (WANCandidateConfig{STUNEndpoints: input.STUNEndpoints, AdvertiseIPv6: input.AdvertiseIPv6, ProbeBudget: budget}).Canonical()
		if err != nil {
			return nil, err
		}
		requested = &canonical
	} else if len(input.STUNEndpoints) != 0 || input.AdvertiseIPv6 || input.ProbeBudget != nil {
		return nil, &lanCommandError{"wan_candidates_invalid", "disabled WAN candidates must not include discovery options"}
	}
	if c.nodeCopy() != nil {
		return nil, &lanCommandError{"network_restart_required", "stop soba and start with --offline before changing WAN candidates"}
	}
	store := c.lanStoreCopy()
	if store == nil {
		return nil, &lanCommandError{"wan_candidates_setup_required", "select a pinned relay before configuring WAN candidates"}
	}
	store.mu.Lock()
	if store.routeRecovery {
		store.mu.Unlock()
		return nil, config.ErrAtomicRecovery
	}
	next := cloneLANState(store.state)
	if reflect.DeepEqual(next.WANCandidates, requested) {
		store.mu.Unlock()
		return c.wanCandidatesView(requested), nil
	}
	next.Version = 5
	next.WANCandidates = requested
	if err := validateWANCandidateState(next); err != nil {
		store.mu.Unlock()
		return nil, err
	}
	err := store.saveLocked(next)
	if err != nil && networkErrorCode(err) != "lan_state_capacity" {
		store.routeRecovery = true
	}
	store.mu.Unlock()
	if err != nil {
		if networkErrorCode(err) == "lan_state_capacity" {
			return nil, err
		}
		return nil, errors.Join(err, config.ErrAtomicRecovery)
	}
	return c.wanCandidatesView(requested), nil
}
