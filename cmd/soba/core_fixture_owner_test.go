package main

import (
	"testing"

	"github.com/webkaz-labs/sobalink/internal/config"
)

// Every fixture uses a real, directory-bound lifecycle owner. Core must close
// before cleanup releases this lock; a Core reopen reuses the same live owner.
func cliFixtureLifecycleOwner(t *testing.T, directory string) *config.Lock {
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
