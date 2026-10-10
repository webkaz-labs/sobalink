//go:build !resource_group_catalog_native

package resourceacceptance

// Normal builds have no recorder state, registration, callbacks or I/O.
func Record(any, Kind, string, string, string) {}
