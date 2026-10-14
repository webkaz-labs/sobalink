//go:build darwin && !cgo

package nearby

import "context"

// A cross-compiled no-cgo binary cannot claim the system DNS-SD adapter. There
// is no raw-socket fallback, interface widening or platform service install.
func newDarwinRecordProvider(context.Context, Config, discoveryPolicy) (recordProvider, error) {
	return nil, ErrDNSServiceUnavailable
}
