//go:build !windows

package lanlink

import "syscall"

func relayPlatformAvailability(syscall.Errno) bool { return false }
