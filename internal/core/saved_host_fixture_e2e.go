//go:build soba_e2e

package core

import (
	"errors"
	"os"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/lanpolicy"
)

// PrepareSavedHostBrowserFixture seeds only a fresh private synthetic profile.
// It uses production host configuration validation and persistence, without a
// transport constructor, pairing, application trust or a network listener. This
// helper is excluded from ordinary builds and distribution.
func PrepareSavedHostBrowserFixture(directory string) error {
	if err := config.SecureDir(directory); err != nil {
		return err
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 0 {
		return errors.New("saved-host fixture requires an empty private directory")
	}
	profile := Profile{Version: 1, Settings: Settings{Locale: "auto", Theme: "system", Network: "lan", Hostname: "saved-notebook"}, Peers: []Trust{}, Services: []ServiceSpec{}}
	if err := validateProfile(profile); err != nil {
		return err
	}
	c := &Core{dir: directory, profile: profile, capacity: capacity.Defaults()}
	if err := c.configureLANWithPolicy(&LANSelection{Kind: "host", Address: "192.168.50.10:48443"}, false, &lanpolicy.Config{Mode: lanpolicy.AllowedLANDestinations, Prefixes: []string{"192.168.50.0/24"}}); err != nil {
		return err
	}
	return c.writeProfile(profile)
}
