//go:build !windows

package config

func resourceOpenHandleRenameDenied(error) bool { return false }
