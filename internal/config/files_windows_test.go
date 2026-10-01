package config

import (
	"golang.org/x/sys/windows"
	"path/filepath"
	"strings"
	"testing"
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
	sid, e := UserSID()
	if e != nil {
		t.Fatal(e)
	}
	for _, p := range []string{d, p} {
		sd, e := windows.GetNamedSecurityInfo(p, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
		if e != nil {
			t.Fatal(e)
		}
		s := sd.String()
		if !strings.Contains(s, sid) || !strings.Contains(s, "D:P") || strings.Contains(s, ";;;WD)") || strings.Contains(s, ";;;BU)") {
			t.Fatalf("unexpected DACL %s", s)
		}
	}
}
