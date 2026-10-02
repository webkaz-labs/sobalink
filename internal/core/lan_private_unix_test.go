//go:build !windows

package core

import (
	"os"
	"path/filepath"
	"testing"
)

func assertLANStatePrivate(t *testing.T, path string) {
	t.Helper()
	for _, p := range []string{filepath.Dir(path), path} {
		info, err := os.Stat(p)
		if err != nil || info.Mode().Perm()&0077 != 0 {
			t.Fatalf("LAN state access is not private: %v", err)
		}
	}
}
