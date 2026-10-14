//go:build !darwin

package nearby

import "context"

func newNativeRecordProvider(ctx context.Context, cfg Config, policy discoveryPolicy) (recordProvider, error) {
	return newRawRecordProvider(ctx, cfg, policy, func(scope Scope) (Transport, error) { return nativeFactory(scope, policy) })
}
