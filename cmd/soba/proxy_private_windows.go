package main

import (
	"errors"
	"os"
	"unsafe"

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
	sid, err := config.UserSID()
	if err != nil {
		return err
	}
	current, err := windows.StringToSid(sid)
	if err != nil {
		return err
	}
	if !proxyPrivateDACL(descriptor, current) {
		return errors.New("input ACL is not private to the current user")
	}
	return nil
}

func proxyPrivateDACL(descriptor *windows.SECURITY_DESCRIPTOR, current *windows.SID) bool {
	flags, _, err := descriptor.Control()
	if err != nil || flags&windows.SE_DACL_PROTECTED == 0 {
		return false
	}
	acl, _, err := descriptor.DACL()
	if err != nil || acl == nil || acl.AceCount != 1 {
		return false
	}
	var ace *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(acl, 0, &ace); err != nil {
		return false
	}
	if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceFlags != 0 {
		return false
	}
	// Compare the SID itself: Windows may render it as an SDDL alias, such
	// as LA, and may add descriptor control flags when storing the DACL.
	actual := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
	const fileAllAccess = 0x001f01ff
	return actual.Equals(current) && (ace.Mask&windows.GENERIC_ALL != 0 || ace.Mask&fileAllAccess == fileAllAccess)
}
