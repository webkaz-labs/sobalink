//go:build !windows

package config

import "os"

func atomicSyncLeaseNamespace(_ string, owned *os.File) error {
	return atomicSyncDirectory(owned)
}
