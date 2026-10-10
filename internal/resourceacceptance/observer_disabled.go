//go:build !resource_group_catalog_native && !resource_process_native

package resourceacceptance

// Normal builds have no recorder state, registration, callbacks or I/O.
func Record(any, Kind, string, string, string) {}
