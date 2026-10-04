package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/webkaz-labs/sobalink/internal/capacity"
)

// applyLANCapacityLocked is called with Core.mu held. Pair persistence only
// takes lanStore.mu and never re-enters Core. Holding both across publication
// prevents a pairing save from exceeding a concurrently lowered storage budget.
func (c *Core) applyLANCapacityLocked(policy capacity.Policy, publish func() error) error {
	store := c.lan
	if store == nil {
		return publish()
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	limits := selectedLANLimits(policy)
	encoded, err := json.MarshalIndent(store.state, "", "  ")
	if err != nil {
		return err
	}
	bytes := int64(len(encoded)) + 1
	if info, err := os.Lstat(store.path); err == nil {
		bytes = max(bytes, info.Size())
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if bytes > limits.bytes {
		return &localCommandError{"policy_in_use", fmt.Sprintf("lanStateBytes must hold the current %d-byte private LAN state; revoke unused pairs explicitly before reducing its storage budget", bytes)}
	}
	saveErr := publish()
	if atomicPublished(saveErr) {
		store.limits.Store(limits)
	}
	return saveErr
}
