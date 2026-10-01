package config

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

func fixture(t *testing.T) Config {
	t.Helper()
	c, e := New("server.example.ts.net", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if e != nil {
		t.Fatal(e)
	}
	return c
}
func TestValidate(t *testing.T) {
	c := fixture(t)
	for _, mut := range []func(*Config){func(c *Config) { c.Mode = "any" }, func(c *Config) { c.IDHost = "127.0.0.1" }, func(c *Config) { c.IDHost = "8.8.8.8" }, func(c *Config) { c.IDHost = "100.100.100.100" }, func(c *Config) { c.PublicKey = "bad" }, func(c *Config) { c.LocalRelayPort = c.LocalIDPort }, func(c *Config) { c.LocalIDPort = 1024 }, func(c *Config) { c.Hostname = "private user name" }, func(c *Config) { c.IDHost = "https://server.ts.net" }} {
		v := c
		mut(&v)
		if v.Validate() == nil {
			t.Fatalf("accepted invalid profile %+v", v)
		}
	}
}
func TestHost(t *testing.T) {
	for _, h := range []string{"server", "server.example.ts.net.", "100.64.0.1", "fd7a:115c:a1e0::1"} {
		if !ValidHost(h) {
			t.Errorf("reject %s", h)
		}
	}
	for _, h := range []string{"", "localhost:1234", "::1", "192.168.1.1", "-bad", "100.100.100.100", "fd7a:115c:a1e0::1%zone"} {
		if ValidHost(h) {
			t.Errorf("accept %s", h)
		}
	}
}
func TestPersistPrivateAndLock(t *testing.T) {
	d := t.TempDir()
	c := fixture(t)
	if e := Save(d, c); e != nil {
		t.Fatal(e)
	}
	got, e := Load(d)
	if e != nil || !reflect.DeepEqual(got, c) {
		t.Fatal(got, e)
	}
	if runtime.GOOS != "windows" {
		s, _ := os.Stat(filepath.Join(d, "profile.json"))
		if s.Mode().Perm() != 0600 {
			t.Fatal(s.Mode())
		}
	}
	l, e := AcquireLock(d)
	if e != nil {
		t.Fatal(e)
	}
	if l2, e := AcquireLock(d); e == nil {
		l2.Close()
		t.Fatal("double lock")
	}
	l.Close()
	l, e = AcquireLock(d)
	if e != nil {
		t.Fatal(e)
	}
	l.Close()
}
func TestStrictJSON(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "test.json")
	for _, s := range []string{`{"unexpected":true}`, `{} {}`, `[]`} {
		if e := os.WriteFile(p, []byte(s), 0600); e != nil {
			t.Fatal(e)
		}
		var c Config
		if ReadJSON(p, &c) == nil {
			t.Errorf("accepted %s", s)
		}
	}
}
func TestRefuseSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink privilege varies; Windows ACL tested separately")
	}
	d := t.TempDir()
	p := filepath.Join(d, "real")
	if e := os.WriteFile(p, []byte(`{}`), 0600); e != nil {
		t.Fatal(e)
	}
	link := filepath.Join(d, "link")
	if e := os.Symlink(p, link); e != nil {
		t.Fatal(e)
	}
	var c Config
	if ReadJSON(link, &c) == nil {
		t.Fatal("read symlink")
	}
	if AtomicWrite(link, []byte("x")) == nil {
		t.Fatal("write symlink")
	}
}
func TestCredentialsRandom(t *testing.T) {
	a, e := NewCredentials()
	if e != nil {
		t.Fatal(e)
	}
	b, e := NewCredentials()
	if e != nil || a == b || len(a.Password) < 24 || len(a.Password) > 255 {
		t.Fatal("bad credentials")
	}
}
