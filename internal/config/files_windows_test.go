package config

import (
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestWindowsPrivateDACL(t *testing.T) {
	d := t.TempDir()
	if e := SecureDir(d); e != nil {
		t.Fatal(e)
	}
	p := filepath.Join(d, "secret.json")
	if e := WriteJSON(p, map[string]string{"example": "test-only"}); e != nil {
		t.Fatal(e)
	}
	sidText, e := UserSID()
	if e != nil {
		t.Fatal(e)
	}
	current, e := windows.StringToSid(sidText)
	if e != nil {
		t.Fatal(e)
	}
	for _, p := range []string{d, p} {
		sd, e := windows.GetNamedSecurityInfo(p, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
		if e != nil {
			t.Fatal(e)
		}
		flags, _, e := sd.Control()
		if e != nil || flags&windows.SE_DACL_PROTECTED == 0 {
			t.Fatal("DACL inheritance not protected", e)
		}
		acl, _, e := sd.DACL()
		if e != nil || acl == nil || acl.AceCount != 1 {
			t.Fatal("expected exactly one current-user access entry", e)
		}
		var ace *windows.ACCESS_ALLOWED_ACE
		if e = windows.GetAce(acl, 0, &ace); e != nil {
			t.Fatal(e)
		}
		// Windows can render a numeric SID as an SDDL alias (for example LA).
		// Compare the binary SID, not its textual presentation.
		actual := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || !actual.Equals(current) {
			t.Fatal("access entry does not belong exclusively to current user")
		}
		if ace.Mask&windows.GENERIC_ALL == 0 && ace.Mask&0x001f01ff != 0x001f01ff {
			t.Fatal("current user lacks full file access")
		}
	}
}
