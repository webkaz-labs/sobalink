package core

import (
	"fmt"
	"math"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/lanlink"
)

var relayResourceKeys = []string{"relayPresenceConnections", "relayCandidateAttempts", "relayTLSConnections", "relayAdmissionConnections"}

func init() {
	for _, key := range relayResourceKeys {
		supportedCapacityResources[key] = true
	}
}
func selectedRelayResources(p capacity.Policy) (lanlink.RelayResources, error) {
	if err := p.Validate(); err != nil {
		return lanlink.RelayResources{}, err
	}
	values := make([]int, 0, len(relayResourceKeys))
	for _, key := range relayResourceKeys {
		n := p.Number("resources", key)
		if n < 1 || n > int64(math.MaxInt) {
			return lanlink.RelayResources{}, &localCommandError{"policy_invalid", fmt.Sprintf("%s must be a positive integer that fits this platform's resource counter", key)}
		}
		values = append(values, int(n))
	}
	r, err := (lanlink.RelayResources{PresenceConnections: values[0], CandidateAttempts: values[1], TLSConnections: values[2], AdmissionConnections: values[3]}).WithDefaults()
	if err != nil {
		return r, &localCommandError{"policy_invalid", err.Error()}
	}
	return r, nil
}
func relayResourcesChanged(a, b capacity.Policy) bool {
	if a.Version == 0 {
		a = capacity.Defaults()
	}
	for _, key := range relayResourceKeys {
		if a.Number("resources", key) != b.Number("resources", key) {
			return true
		}
	}
	return false
}
func relayResourceRestartError() error {
	return &localCommandError{"network_restart_required", "stop soba and start --offline before changing relay resource budgets; new budgets take effect on the next network start"}
}
func (c *Core) relayResourcesView() map[string]any {
	p := c.capacityPolicy()
	effective := map[string]int64{}
	for _, key := range relayResourceKeys {
		effective[key] = p.Number("resources", key)
	}
	offline := c.nodeCopy() == nil
	return map[string]any{"effective": effective, "editable": offline, "restartRequired": offline}
}
