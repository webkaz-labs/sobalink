package core

import (
	"path/filepath"
	"testing"
	"unsafe"

	"github.com/webkaz-labs/sobalink/internal/config"
	"golang.org/x/sys/windows"
)

func assertLANStatePrivate(t *testing.T, path string) {
	t.Helper()
	sidText, err := config.UserSID()
	if err != nil {
		t.Fatal(err)
	}
	current, err := windows.StringToSid(sidText)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{filepath.Dir(path), path} {
		sd, err := windows.GetNamedSecurityInfo(p, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
		if err != nil {
			t.Fatal(err)
		}
		flags, _, err := sd.Control()
		if err != nil || flags&windows.SE_DACL_PROTECTED == 0 {
			t.Fatal("LAN state DACL inheritance is not protected", err)
		}
		acl, _, err := sd.DACL()
		if err != nil || acl == nil || acl.AceCount != 1 {
			t.Fatal("LAN state must have exactly one current-user access entry", err)
		}
		var ace *windows.ACCESS_ALLOWED_ACE
		if err = windows.GetAce(acl, 0, &ace); err != nil {
			t.Fatal(err)
		}
		actual := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || !actual.Equals(current) {
			t.Fatal("LAN state access is not exclusive to the current user")
		}
		if ace.Mask&windows.GENERIC_ALL == 0 && ace.Mask&0x001f01ff != 0x001f01ff {
			t.Fatal("current user lacks full access to LAN state")
		}
	}
}
