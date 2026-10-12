//go:build windows

package transfer

// Every namespace and destination uses the identity-bound native directory
// barrier. Lack of filesystem acknowledgement fails closed at that operation.
func durableStorageSupported() bool { return true }
