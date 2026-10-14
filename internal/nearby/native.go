package nearby

import (
	"errors"
	"net"
	"net/netip"
)

var ErrSocketUnavailable = errors.New("nearby: selected multicast socket is unavailable; use manual setup")

// currentBinding checks the selected address, its exact local prefix and the
// same interface instance metadata. It never enumerates remote devices.
func currentBinding(scope Scope, original *net.Interface) (net.Interface, error) {
	return currentBindingWithPolicy(scope, original, discoveryPolicy{})
}

func currentBindingWithPolicy(scope Scope, original *net.Interface, policy discoveryPolicy) (net.Interface, error) {
	ifi, err := net.InterfaceByIndex(scope.InterfaceIndex)
	if err != nil {
		return net.Interface{}, ErrScopeChanged
	}
	addrs, err := ifi.Addrs()
	if err != nil || !bindingMatchesWithPolicy(scope, *ifi, addrs, original, policy) {
		return net.Interface{}, ErrScopeChanged
	}
	return *ifi, nil
}
func bindingMatches(scope Scope, ifi net.Interface, addrs []net.Addr, original *net.Interface) bool {
	return bindingMatchesWithPolicy(scope, ifi, addrs, original, discoveryPolicy{})
}

func bindingMatchesWithPolicy(scope Scope, ifi net.Interface, addrs []net.Addr, original *net.Interface, policy discoveryPolicy) bool {
	if policy.validateScope(scope) != nil || ifi.Index != scope.InterfaceIndex || ifi.Flags&net.FlagUp == 0 {
		return false
	}
	if policy.isolatedLoopbackTest {
		if ifi.Flags&net.FlagLoopback == 0 {
			return false
		}
	} else if ifi.Flags&net.FlagMulticast == 0 || ifi.Flags&net.FlagLoopback != 0 {
		return false
	}
	if original != nil && (ifi.Name != original.Name || ifi.HardwareAddr.String() != original.HardwareAddr.String()) {
		return false
	}
	for _, addr := range addrs {
		prefix, err := netip.ParsePrefix(addr.String())
		if err == nil && prefix.Addr() == scope.LocalAddress && (prefix.Masked() == scope.Prefix || policy.isolatedLoopbackTest) {
			return true
		}
	}
	return false
}
