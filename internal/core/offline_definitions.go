package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/webui"
)

// OfflineDefinitionCommand edits only inert profile metadata under the same
// exclusive lock used by the running agent. It does not load private transport
// identities, initialize transfer state, start maintenance, or open listeners.
func OfflineDefinitionCommand(ctx context.Context, opts Options, command webui.Command) (_ any, err error) {
	if !opts.SkipNetworkStart {
		return nil, errors.New("offline definition commands require SkipNetworkStart")
	}
	favorites := false
	switch command.Name {
	case "favorites.list", "favorites.add", "favorites.remove":
		favorites = true
	case "rustdesk.preview", "rustdesk.save", "rustdesk.settings", "client.settings", "service.save", "service.config", "service.delete", "service.selection", "group.list", "group.save", "profile.export", "profile.import.preview", "profile.import":
	default:
		return nil, &localCommandError{"offline_command_unsupported", "offline mode supports saved service, group and profile definitions only"}
	}
	if opts.Directory == "" {
		return nil, errors.New("private state directory required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	lock, err := config.AcquireLock(opts.Directory)
	if err != nil {
		return nil, &localCommandError{"offline_profile_unavailable", "could not lock the profile; use the running agent without --offline or stop it before editing offline"}
	}
	defer func() { err = errors.Join(err, lock.Close()) }()
	limits, err := readCapacityPolicy(opts.Directory)
	if err != nil {
		return nil, err
	}
	p := Profile{Version: 1, Settings: Settings{Locale: "auto", Theme: "system", Network: "none", Hostname: "sobalink-" + randomID()[:8]}, Peers: []Trust{}, Services: []ServiceSpec{}}
	loadErr := readBoundedPrivateJSON(filepath.Join(opts.Directory, "sobalink.json"), limits.Number("resources", "profileBytes"), &p)
	if loadErr != nil && !errors.Is(loadErr, os.ErrNotExist) {
		return nil, loadErr
	}
	if err := validateProfile(p); err != nil {
		return nil, err
	}
	lifetime, cancel := context.WithCancel(ctx)
	defer cancel()
	c := &Core{dir: opts.Directory, version: opts.Version, profile: p, capacity: limits, ctx: lifetime, cancel: cancel, active: map[string]*activeService{}, serviceStates: map[string]string{}, requests: map[string]requestResult{}}
	if !favorites && errors.Is(loadErr, os.ErrNotExist) {
		// Favorites never materialize or rewrite the profile.
		// First-use public profile metadata is durable so a separate preview and
		// apply see the same revision. No transport identity is materialized.
		if err := c.writeProfile(p); err != nil {
			return nil, err
		}
	}
	return c.Command(ctx, command)
}
