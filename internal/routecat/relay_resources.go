package routecat

import "fmt"

// RelayPresenceBudgetError rejects startup before any engine/socket is created.
// The saved candidate set and grants stay unchanged; the caller can explicitly
// choose a sufficient resource budget before activating all configured relays.
type RelayPresenceBudgetError struct{ Candidates, Limit int }

func (e *RelayPresenceBudgetError) Error() string {
	return fmt.Sprintf("%d configured relays require a relayPresenceConnections budget of at least %d; selected budget is %d; raise it before starting; saved candidates and permissions are unchanged", e.Candidates, e.Candidates, e.Limit)
}
func (*RelayPresenceBudgetError) ErrorCode() string { return "lan_relay_presence_capacity" }
func ValidateRelayPresenceBudget(candidates, limit int) error {
	if limit == 0 {
		limit = DefaultRelayPresenceConnections
	}
	if limit < 1 || limit > RelayRegionNamespace {
		return fmt.Errorf("relayPresenceConnections must fit the nonzero 16-bit DERP region identifier space")
	}
	if candidates > limit {
		return &RelayPresenceBudgetError{candidates, limit}
	}
	return nil
}
