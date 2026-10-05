package core

import (
	"errors"
	"slices"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/lanpolicy"
)

func canonicalLANSetupPolicy(policy *lanpolicy.Config) (*lanpolicy.Config, error) {
	if policy == nil {
		return nil, nil
	}
	if policy.Mode == "" {
		return nil, &lanCommandError{"lan_policy_invalid", lanpolicy.ErrPolicy.Error()}
	}
	canonical, err := policy.Canonical()
	if err != nil {
		return nil, &lanCommandError{"lan_policy_invalid", err.Error()}
	}
	return &canonical, nil
}

func applyLANSetupPolicy(state *lanState, policy *lanpolicy.Config) error {
	if policy != nil {
		state.Version = 4
		state.DestinationPolicy = *policy
	}
	if err := validateLANDestinationPolicy(*state); err != nil {
		return &lanCommandError{"lan_policy_relay_outside", "include the selected and prepared relay addresses in the allowed prefixes, or remove those relay candidates first"}
	}
	return nil
}

func (c *Core) configureExistingLANPolicy(store *lanStore, state lanState, policy *lanpolicy.Config) error {
	if policy == nil {
		return nil
	}
	previous, err := state.DestinationPolicy.Canonical()
	if err != nil {
		return err
	}
	if previous.Mode == policy.Mode && slices.Equal(previous.Prefixes, policy.Prefixes) {
		return nil
	}
	if c.nodeCopy() != nil {
		return &lanCommandError{"network_restart_required", "stop soba and start with --offline before changing allowed LAN destinations"}
	}
	if err := applyLANSetupPolicy(&state, policy); err != nil {
		return err
	}
	if err := store.save(state); err != nil {
		store.requireRouteRecovery()
		return errors.Join(err, config.ErrAtomicRecovery)
	}
	return nil
}
