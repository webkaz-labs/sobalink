package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/webkaz-labs/sobalink/internal/config"
)

type Initialization struct {
	State    string `json:"state"`
	Hostname string `json:"hostname"`
	Network  string `json:"network"`
}

// InitializeProfile creates only inert local metadata. It never constructs a
// network engine, transfer store, credential, service grant or listener.
func InitializeProfile(ctx context.Context, opts Options, hostname string) (Initialization, error) {
	return initializeProfile(ctx, opts, hostname, nil)
}

func initializeProfile(ctx context.Context, opts Options, hostname string, write func(string, []byte) error) (_ Initialization, err error) {
	var result Initialization
	if opts.Directory == "" {
		return result, errors.New("private state directory required")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	p := Profile{Version: 1, Settings: Settings{Locale: "auto", Theme: "system", Network: "none", Hostname: hostname}, Peers: []Trust{}, Services: []ServiceSpec{}}
	if hostname == "" {
		p.Settings.Hostname = "sobalink-" + randomID()[:8]
	}
	if err := validateProfile(p); err != nil {
		return result, err
	}
	lock, err := config.AcquireLock(opts.Directory)
	if err != nil {
		return result, &localCommandError{"offline_profile_unavailable", "could not lock the profile; stop the running soba agent before initializing metadata"}
	}
	defer func() { err = errors.Join(err, lock.Close()) }()
	limits, err := readCapacityPolicy(opts.Directory)
	if err != nil {
		return result, err
	}
	var existing Profile
	err = readBoundedPrivateJSON(filepath.Join(opts.Directory, "sobalink.json"), limits.Number("resources", "profileBytes"), &existing)
	if err == nil {
		if err := validateProfile(existing); err != nil {
			return result, err
		}
		if hostname != "" && hostname != existing.Settings.Hostname {
			return result, &localCommandError{"profile_already_initialized", "profile already has another node name; review an explicit offline network configuration change"}
		}
		return Initialization{State: "exists", Hostname: existing.Settings.Hostname, Network: existing.Settings.Network}, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	c := &Core{dir: opts.Directory, profile: p, capacity: limits, atomicWrite: write}
	saveErr := c.writeProfile(p)
	if !atomicPublished(saveErr) {
		return result, saveErr
	}
	return Initialization{State: "initialized", Hostname: p.Settings.Hostname, Network: "none"}, saveErr
}
