package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/config"
	"golang.org/x/sys/windows"
)

func TestWindowsProxyPrivateDACL(t *testing.T) {
	// A well-known SID makes the alias regression independent of the account
	// running the test. The actual file check always uses the process user SID.
	current, err := windows.StringToSid("S-1-5-18")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		sddl    string
		private bool
	}{
		{"numeric SID", "D:P(A;;FA;;;S-1-5-18)", true},
		{"SID alias", "D:P(A;;FA;;;SY)", true},
		{"generic full access", "D:P(A;;GA;;;SY)", true},
		{"stored control flags", "D:PAI(A;;FA;;;SY)", true},
		{"owner and primary group metadata", "O:SYG:BAD:P(A;;FA;;;SY)", true},
		{"missing DACL", "O:SY", false},
		{"null DACL", "D:PNO_ACCESS_CONTROL", false},
		{"empty DACL", "D:P", false},
		{"inheritance enabled", "D:(A;;FA;;;SY)", false},
		{"everyone only", "D:P(A;;FA;;;WD)", false},
		{"additional reader", "D:P(A;;FA;;;SY)(A;;FR;;;WD)", false},
		{"shared group", "D:P(A;;FA;;;BA)", false},
		{"denied access", "D:P(D;;FA;;;SY)", false},
		{"inherit only", "D:P(A;OIIO;FA;;;SY)", false},
		{"inherited entry", "D:P(A;ID;FA;;;SY)", false},
		{"read access only", "D:P(A;;FR;;;SY)", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			descriptor, err := windows.SecurityDescriptorFromString(tc.sddl)
			if err != nil {
				t.Fatal(err)
			}
			if got := proxyPrivateDACL(descriptor, current); got != tc.private {
				t.Fatalf("private DACL = %t, want %t", got, tc.private)
			}
		})
	}
}

func TestWindowsProxyPrivateFileWriters(t *testing.T) {
	for _, tc := range []struct {
		name  string
		write func(string, []byte) error
	}{
		{"protected file", func(path string, data []byte) error {
			if err := os.WriteFile(path, data, 0600); err != nil {
				return err
			}
			return config.Protect(path, false)
		}},
		{"private export", config.AtomicWritePrivate},
		{"private state", config.AtomicWrite},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "private.json")
			if err := tc.write(path, []byte(`{"password":"fixture-secret"}`)); err != nil {
				t.Fatal(err)
			}
			file, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			if err := checkProxyPrivateFile(file); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestWindowsProxyPrivateFileRejectsSharedDACL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shared.json")
	if err := config.AtomicWritePrivate(path, []byte(`{"password":"fixture-secret"}`)); err != nil {
		t.Fatal(err)
	}
	sid, err := config.UserSID()
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;" + sid + ")(A;;FR;;;WD)")
	if err != nil {
		t.Fatal(err)
	}
	acl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := checkProxyPrivateFile(file); err == nil {
		t.Fatal("shared credential file accepted")
	}
}
