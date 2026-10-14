//go:build soba_e2e

package main

import (
	"testing"

	"github.com/webkaz-labs/sobalink/internal/config"
)

// This is the same real lifecycle lock used by the browser fixture executable,
// not substitute authority. A test reopening Core keeps and reuses this owner.
func e2eFixtureLifecycleOwner(t *testing.T, directory string) *config.Lock {
	t.Helper()
	owner, err := config.AcquireLock(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := owner.Close(); err != nil {
			t.Error(err)
		}
	})
	return owner
}
