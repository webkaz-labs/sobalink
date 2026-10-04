package core

import (
	"errors"

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
	if c.atomicWrite != nil {
		return c.atomicWrite(path, data)
	}
	return config.AtomicWrite(path, data)
}
