package core

import (
	"errors"
	"github.com/webkaz-labs/sobalink/internal/config"
	"os"
)

type outboxOwnedScope struct{ profile, lock os.FileInfo }

// The coordinator mutex is held. Try admission occurs before the transition,
// and a retained scope permits nested bounded snapshot/cleanup operations.
func (o *outboxCoordinator) withOwnedLocked(fn func() error) error {
	if o.frozen != "" {
		return outboxError(o.frozen)
	}
	if o.owned != nil {
		return fn()
	}
	err := o.core.resourceLock.TryWithOwnershipInfo(o.core.dir, func(profile, lock os.FileInfo) error {
		o.owned = &outboxOwnedScope{profile, lock}
		defer func() { o.owned = nil }()
		return fn()
	})
	if errors.Is(err, config.ErrLifecycleBusy) {
		return outboxError("outbox_storage_busy")
	}
	return err
}
func (o *outboxCoordinator) withOwnedInfoLocked(fn func(os.FileInfo, os.FileInfo) error) error {
	return o.withOwnedLocked(func() error { return fn(o.owned.profile, o.owned.lock) })
}
func outboxOwnedCall[T any](o *outboxCoordinator, fn func() (T, error)) (T, error) {
	var result T
	err := o.withOwnedLocked(func() error { var err error; result, err = fn(); return err })
	return result, err
}
