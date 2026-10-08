//go:build !windows

package core

func resourceOpenHandleRenameDenied(error) bool { return false }
