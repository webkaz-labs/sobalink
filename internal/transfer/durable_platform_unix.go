//go:build !windows

package transfer

// Sync barriers are checked at every save. Native process-crash tests do not
// establish arbitrary filesystem or hardware power-loss guarantees.
func durableStorageSupported() bool { return true }
