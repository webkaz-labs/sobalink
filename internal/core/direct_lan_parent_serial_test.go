//go:build resource_group_catalog_native || resource_process_native

package core

import "testing"

// Acceptance instrumentation retains process-global observers. Preserve the
// original sequential parent scheduling when either observer mode is enabled.
func parallelEndpointParent(t *testing.T) {
	t.Helper()
}
