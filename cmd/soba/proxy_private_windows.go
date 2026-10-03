package main

import (
	"errors"
	"os"

	"github.com/webkaz-labs/sobalink/internal/config"
	"golang.org/x/sys/windows"
)

func checkProxyPrivateFile(f *os.File) error {
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("input is not a regular file")
	}
	descriptor, err := windows.GetSecurityInfo(windows.Handle(f.Fd()), windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	expected, err := config.SecurityDescriptor(false)
	if err != nil {
		return err
	}
	if descriptor.String() != expected {
		return errors.New("input ACL is not private to the current user")
	}
	return nil
}
