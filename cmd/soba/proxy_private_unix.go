//go:build !windows

package main

import (
	"errors"
	"os"
	"syscall"
)

func checkProxyPrivateFile(f *os.File) error {
	info, err := f.Stat()
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || stat.Uid != uint32(os.Getuid()) {
		return errors.New("input is not private")
	}
	return nil
}
