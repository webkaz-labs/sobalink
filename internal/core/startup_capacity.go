package core

import (
	"errors"
	"os"
	"path/filepath"
)

// profileBytes is a per-file limit. Report the largest retained private file,
// plus individual byte counts, without serializing credential-bearing records.
func (c *Core) privateSettingsUsage() (map[string]int64, error) {
	usage := map[string]int64{}
	for name, key := range map[string]string{"startup.json": "startupBytes", "saved-proxies.json": "savedProxyBytes", "startup-revocations.json": "startupRevocationBytes"} {
		info, err := os.Lstat(filepath.Join(c.dir, name))
		if errors.Is(err, os.ErrNotExist) {
			usage[key] = 0
			continue
		}
		if err != nil || !info.Mode().IsRegular() {
			return nil, &localCommandError{"private_settings_unavailable", "private settings storage could not be inspected; repair the state directory"}
		}
		usage[key] = info.Size()
	}
	return usage, nil
}
func (c *Core) validatePrivateSettingsCapacity(limit int64) error {
	usage, err := c.privateSettingsUsage()
	if err != nil {
		return err
	}
	for _, size := range usage {
		if size > limit {
			return &localCommandError{"policy_in_use", "profileBytes cannot be below retained private startup, proxy or revocation storage; review and remove saved settings first"}
		}
	}
	return nil
}
