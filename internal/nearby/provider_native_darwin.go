//go:build darwin

package nearby

import "context"

func newNativeRecordProvider(ctx context.Context, cfg Config, policy discoveryPolicy) (recordProvider, error) {
	return newDarwinRecordProvider(ctx, cfg, policy)
}
