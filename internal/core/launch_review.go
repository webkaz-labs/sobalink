package core

import (
	"errors"
	"os"
	"path/filepath"
)

// LaunchReview contains only credential-free effects of an online launch.
// Its metadata revisions bind private approval/credential rotation and durable
// revocation without making a password hash available to a review consumer.
type LaunchReview struct {
	Startup         map[string]any `json:"startup"`
	SavedProxies    map[string]any `json:"savedProxies"`
	ServicesRestart bool           `json:"servicesRestart"`
	ProxiesRestart  bool           `json:"proxiesRestart"`
}

// ReadLaunchReview reads metadata for OS-registration review. It does not start
// an engine, write files, change permissions, or return stored credentials.
func ReadLaunchReview(dir string) (LaunchReview, error) {
	var result LaunchReview
	limits, err := readCapacityPolicy(dir)
	if err != nil {
		return result, err
	}
	profile := Profile{Settings: Settings{Network: "none"}}
	if err := readBoundedPrivateJSON(filepath.Join(dir, "sobalink.json"), limits.Number("resources", "profileBytes"), &profile); err != nil && !errors.Is(err, os.ErrNotExist) {
		return result, err
	}
	c := &Core{dir: dir, profile: profile, capacity: limits}
	if err := c.loadStartupSettings(true, false); err != nil {
		return result, err
	}
	result.Startup, result.SavedProxies = c.startupView(), c.savedProxyView()
	delete(result.Startup, "suppressed")
	delete(result.SavedProxies, "suppressed")
	if profile.Settings.Network != "tailnet" && profile.Settings.Network != "lan" && profile.Settings.Network != "direct-lan" && profile.Settings.Network != "mixed" {
		return result, nil
	}
	for _, entry := range c.startup.Entries {
		if entry.Enabled && startupEntryValid(profile, entry) && samePeerEpochs(entry.PeerEpochs, c.startup.Revocations) {
			result.ServicesRestart = true
		}
	}
	for _, entry := range c.savedProxies.Entries {
		if entry.StartOnLaunch && entry.Hostname == profile.Settings.Hostname && entry.Scope.Backend == profile.Settings.Network && samePeerEpochs(entry.PeerEpochs, c.startup.Revocations) {
			result.ProxiesRestart = true
		}
	}
	return result, nil
}
