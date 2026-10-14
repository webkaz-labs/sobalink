//go:build !linux && !darwin && !windows

package nearby

func NativeFactory(scope Scope) (Transport, error) { return nativeFactory(scope, discoveryPolicy{}) }

func nativeFactory(scope Scope, policy discoveryPolicy) (Transport, error) {
	if err := policy.validateScope(scope); err != nil {
		return nil, err
	}
	return nil, ErrUnsupported
}
