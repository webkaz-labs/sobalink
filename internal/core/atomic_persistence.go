package core

import (
	"errors"
	"path/filepath"

	"github.com/webkaz-labs/sobalink/internal/config"
)

// A committed error publishes the new file but cannot certify its durability.
// Saved-state owners reconcile memory and return the error; operations that
// start new work must stop before activation when durability is uncertain.
func atomicPublished(err error) bool {
	return err == nil || errors.Is(err, config.ErrAtomicCommitted)
}

// Public private-state errors retain atomic outcomes, not path-bearing causes.
func privateAtomicError(outcome error, cause error) error {
	retained := []error{outcome}
	for _, sentinel := range []error{config.ErrAtomicCommitted, config.ErrAtomicBusy, config.ErrAtomicRecovery} {
		if errors.Is(cause, sentinel) {
			retained = append(retained, sentinel)
		}
	}
	return errors.Join(retained...)
}

func (c *Core) writeAtomic(path string, data []byte) error {
	// A saved-host review cannot survive an authority write and later revert.
	// This process-local counter changes no persisted format or grant lifetime.
	authority := false
	switch filepath.Base(path) {
	case "sobalink.json", "capacity.json", "startup.json", "startup-revocations.json", "saved-proxies.json":
		authority = true
		c.lanStartWriteRevision.Add(1)
	}
	write := c.atomicWrite
	if write == nil {
		write = config.AtomicWrite
	}
	err := write(path, data)
	if authority && errors.Is(err, config.ErrAtomicCommitted) {
		c.lanStartUncertain.Store(true)
	}
	return err
}
