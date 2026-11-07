//go:build !resource_group_catalog_native && !resource_process_native

package core

import "testing"

// Only parents whose children remain sequential may use this helper. Waiting
// parents consume Go test parallel slots, so parallel children could deadlock
// while their parent retains a permit. Keep the two existing child-parallel
// matrices outside this group.
var endpointParentPermits = make(chan struct{}, 2)

func parallelEndpointParent(t *testing.T) {
	t.Helper()
	t.Parallel()
	endpointParentPermits <- struct{}{}
	// Call before creating fixtures: their cleanup must join owners and release
	// private files before this parent returns its persistence permit.
	t.Cleanup(func() { <-endpointParentPermits })
}
