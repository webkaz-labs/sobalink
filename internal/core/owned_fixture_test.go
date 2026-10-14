package core

import (
	"context"
	"errors"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/config"
)

// Tests that exercise a writable Core own the same native profile lock as the
// installed agent. Reopens explicitly reuse their original owner's lock; this
// helper never recovers authority from a path or a process-global registry.
func openOwnedTestCore(t testing.TB, ctx context.Context, options Options) (*Core, error) {
	t.Helper()
	owner := options.LifecycleLock
	owned := owner == nil
	if owned {
		var err error
		owner, err = config.AcquireLock(options.Directory)
		if err != nil {
			return nil, err
		}
		options.LifecycleLock = owner
	}
	app, err := Open(ctx, options)
	if err != nil {
		if owned {
			err = errors.Join(err, owner.Close())
		}
		return nil, err
	}
	// Scenario tests retain their own explicit Close assertions, including
	// intentionally failed cleanup. This fallback only joins before temp cleanup.
	t.Cleanup(func() {
		_ = app.Close()
		if owned {
			_ = owner.Close()
		}
	})
	return app, nil
}
