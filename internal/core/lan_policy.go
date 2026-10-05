package core

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"reflect"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/lanpolicy"
)

func validateLANDestinationPolicy(state lanState) error {
	if err := validateWANCandidateState(state); err != nil {
		return err
	}
	policy, err := state.DestinationPolicy.Canonical()
	if err != nil {
		return err
	}
	if state.Version < 4 && (state.DestinationPolicy.Mode != "" || len(state.DestinationPolicy.Prefixes) != 0) {
		return errors.New("destination policy requires private state version 4")
	}
	if !policy.Strict() {
		return nil
	}
	if state.Selection == nil {
		return errors.New("select a relay before restricting LAN destinations")
	}
	relay, err := netip.ParseAddrPort(state.Selection.Address)
	if err != nil {
		return err
	}
	if err := policy.CheckRelay(relay); err != nil {
		return err
	}
	for _, candidate := range state.RouteCandidates {
		if err := policy.CheckRelay(candidate.Relay.Address); err != nil {
			return err
		}
	}
	return nil
}

func (c *Core) lanPolicyView(policy lanpolicy.Config) map[string]any {
	canonical, err := policy.Canonical()
	if err != nil {
		canonical = policy
	}
	prefixes := append([]string{}, canonical.Prefixes...)
	return map[string]any{"mode": canonical.Mode, "prefixes": prefixes, "editable": c.nodeCopy() == nil, "restartRequired": c.nodeCopy() == nil}
}

func (c *Core) lanPolicyCommand(ctx context.Context, name string, raw json.RawMessage) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if name == "lan.policy.get" {
		var input struct{}
		if err := decodePayload(raw, &input); err != nil {
			return nil, err
		}
		if store := c.lanStoreCopy(); store != nil {
			return c.lanPolicyView(store.copy().DestinationPolicy), nil
		}
		return c.lanPolicyView(lanpolicy.Config{}), nil
	}
	var input lanpolicy.Config
	if err := decodePayload(raw, &input); err != nil {
		return nil, err
	}
	// Empty mode is a compatibility representation, never permission to widen an
	// explicitly submitted policy. The caller must choose the requested mode.
	if input.Mode == "" {
		return nil, &lanCommandError{"lan_policy_invalid", lanpolicy.ErrPolicy.Error()}
	}
	canonical, err := input.Canonical()
	if err != nil {
		return nil, &lanCommandError{"lan_policy_invalid", err.Error()}
	}
	if c.nodeCopy() != nil {
		return nil, &lanCommandError{"network_restart_required", "stop soba and start with --offline before changing allowed LAN destinations"}
	}
	store := c.lanStoreCopy()
	if store == nil {
		return nil, &lanCommandError{"lan_policy_setup_required", "select the LAN relay before choosing allowed destinations"}
	}
	if store.routesNeedRecovery() {
		return nil, config.ErrAtomicRecovery
	}
	next := store.copy()
	previous, err := next.DestinationPolicy.Canonical()
	if err != nil {
		return nil, err
	}
	if reflect.DeepEqual(previous, canonical) {
		return c.lanPolicyView(previous), nil
	}
	next.Version = max(next.Version, 4)
	next.DestinationPolicy = canonical
	if err := validateWANCandidateState(next); err != nil {
		return nil, err
	}
	if err := validateLANDestinationPolicy(next); err != nil {
		return nil, &lanCommandError{"lan_policy_relay_outside", "include the selected and prepared relay addresses in the allowed prefixes, or remove those relay candidates first"}
	}
	if err := store.save(next); err != nil {
		// Do not restart under an in-memory policy after uncertain persistence. A
		// fresh process still reads the actual saved file; a failed request is not a
		// durable policy change and must never be reported as one.
		store.requireRouteRecovery()
		return nil, errors.Join(err, config.ErrAtomicRecovery)
	}
	return c.lanPolicyView(canonical), nil
}
