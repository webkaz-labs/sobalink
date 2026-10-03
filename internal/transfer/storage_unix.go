//go:build !windows

package transfer

// Mkdir's 0700 already protects new batch directories on Unix. Avoid a
// path-based chmod, which could follow a concurrently substituted symlink.
func protectDirectory(string) error { return nil }
