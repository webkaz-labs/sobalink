//go:build !windows

package routecat

import "syscall"

func relayPlatformAvailability(syscall.Errno) bool { return false }
