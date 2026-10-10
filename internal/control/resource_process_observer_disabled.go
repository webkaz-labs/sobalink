//go:build !resource_process_native

package control

// Ordinary and N4 builds perform no extra parsing, recording or I/O.
func observeResourceProcessDialAttempt(string)      {}
func observeResourceProcessDialCompleted(string)    {}
func observeResourceProcessDispatch(string, string) {}
