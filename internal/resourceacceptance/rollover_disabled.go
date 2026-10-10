//go:build !resource_group_catalog_native && !resource_process_native

package resourceacceptance

// Ordinary builds retain no lifecycle aliases, counters, callbacks or I/O.
func CoreConstructed(any, any) {}
func NodeConstructed(any, any) {}
func MaintenancePassed(any)    {}
