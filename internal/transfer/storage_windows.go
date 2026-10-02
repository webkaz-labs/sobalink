package transfer

import "github.com/webkaz-labs/sobalink/internal/config"

// Files created within the private batch directory inherit its restricted ACL.
func protectDirectory(path string) error { return config.Protect(path, true) }
