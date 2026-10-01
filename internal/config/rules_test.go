package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func ruleFixture() Rule {
	return Rule{Name: "web", Purpose: "web", Direction: "forward", Network: "tcp", ListenPort: 8080, TargetHost: "service.example.ts.net", TargetPort: 80, PeerID: "n123"}
}
func shareFixture() Rule {
	return Rule{Name: "api", Purpose: "custom", Direction: "share", Network: "tcp", ListenPort: 80, TargetHost: "127.0.0.1", TargetPort: 8080, AllowedPeers: []PeerRef{{ID: "n123", Host: "client.example.ts.net"}}}
}
func TestV2RuleValidation(t *testing.T) {
	for _, r := range []Rule{ruleFixture(), shareFixture()} {
		if err := r.Validate(); err != nil {
			t.Fatal(err)
		}
	}
	for name, mut := range map[string]func(*Rule){
		"name-empty": func(r *Rule) { r.Name = "" }, "name-long": func(r *Rule) { r.Name = strings.Repeat("a", 65) }, "name-control": func(r *Rule) { r.Name = "x\ny" },
		"bad-purpose": func(r *Rule) { r.Purpose = "unknown" }, "bad-direction": func(r *Rule) { r.Direction = "exit" }, "bad-network": func(r *Rule) { r.Network = "icmp" },
		"privileged-forward": func(r *Rule) { r.ListenPort = 80 }, "zero-target": func(r *Rule) { r.TargetPort = 0 }, "large-target": func(r *Rule) { r.TargetPort = 65536 }, "large-listen": func(r *Rule) { r.ListenPort = 65536 },
		"public-target": func(r *Rule) { r.TargetHost = "8.8.8.8" }, "url-target": func(r *Rule) { r.TargetHost = "https://host" }, "no-pin": func(r *Rule) { r.PeerID = "" }, "long-pin": func(r *Rule) { r.PeerID = strings.Repeat("a", 129) }, "forbidden-scope": func(r *Rule) { r.AllowedPeers = []PeerRef{{ID: "n", Host: "peer"}} },
	} {
		t.Run(name, func(t *testing.T) {
			r := ruleFixture()
			mut(&r)
			if err := r.Validate(); err == nil {
				t.Fatal("invalid rule accepted")
			}
		})
	}
	for _, host := range []string{"localhost", "127.0.0.2", "0.0.0.0", "::", "::ffff:127.0.0.1", "192.168.1.1", "100.64.0.1", "[::1]", "::1%lo"} {
		r := shareFixture()
		r.TargetHost = host
		if err := r.Validate(); err == nil {
			t.Errorf("share accepted %q", host)
		}
	}
	for _, host := range []string{"127.0.0.1", "::1"} {
		r := shareFixture()
		r.TargetHost = host
		if err := r.Validate(); err != nil {
			t.Fatal(err)
		}
	}
	for name, mut := range map[string]func(*Rule){"no-peers": func(r *Rule) { r.AllowedPeers = nil }, "duplicate-peers": func(r *Rule) { r.AllowedPeers = append(r.AllowedPeers, r.AllowedPeers[0]) }, "too-many-peers": func(r *Rule) { r.AllowedPeers = make([]PeerRef, 33) }, "missing-pin": func(r *Rule) { r.AllowedPeers[0].ID = "" }, "public-peer": func(r *Rule) { r.AllowedPeers[0].Host = "8.8.8.8" }, "wrong-pin": func(r *Rule) { r.PeerID = "n1" }} {
		t.Run(name, func(t *testing.T) {
			r := shareFixture()
			mut(&r)
			if err := r.Validate(); err == nil {
				t.Fatal("invalid share accepted")
			}
		})
	}
}
func TestV2ProfileValidationAndGroups(t *testing.T) {
	c, e := NewRules()
	if e != nil {
		t.Fatal(e)
	}
	if e = c.Validate(); e != nil {
		t.Fatal(e)
	}
	c.Rules = []Rule{ruleFixture()}
	c.Groups = []Group{{Name: "dev", Rules: []string{"web"}}}
	if e = c.Validate(); e != nil {
		t.Fatal(e)
	}
	for name, mut := range map[string]func(*Config){"duplicate-rule": func(c *Config) { c.Rules = append(c.Rules, c.Rules[0]) }, "too-many-rules": func(c *Config) { c.Rules = make([]Rule, 65) }, "too-many-groups": func(c *Config) { c.Groups = make([]Group, 33) }, "duplicate-group": func(c *Config) { c.Groups = append(c.Groups, c.Groups[0]) }, "missing-reference": func(c *Config) { c.Groups[0].Rules = []string{"missing"} }, "repeat-reference": func(c *Config) { c.Groups[0].Rules = []string{"web", "web"} }, "empty-group": func(c *Config) { c.Groups[0].Rules = nil }, "wrong-mode": func(c *Config) { c.Mode = "forward" }} {
		t.Run(name, func(t *testing.T) {
			v := c.Disabled()
			mut(&v)
			if e := v.Validate(); e == nil {
				t.Fatal("invalid profile accepted")
			}
		})
	}
	// Saved alternative rules may intentionally use the same listener tuple.
	r := c.Rules[0]
	r.Name = "alternate"
	c.Rules = append(c.Rules, r)
	if e = c.Validate(); e != nil {
		t.Fatal(e)
	}
}
func TestV2SaveAlwaysDisabledAndDoesNotMutate(t *testing.T) {
	c, _ := NewRules()
	c.Rules = []Rule{ruleFixture(), shareFixture()}
	for i := range c.Rules {
		c.Rules[i].Enabled = true
	}
	d := t.TempDir()
	if e := Save(d, c); e != nil {
		t.Fatal(e)
	}
	got, e := Load(d)
	if e != nil {
		t.Fatal(e)
	}
	for _, r := range got.Rules {
		if r.Enabled {
			t.Fatal("saved active rule")
		}
	}
	if !c.Rules[0].Enabled || !c.Rules[1].Enabled {
		t.Fatal("save mutated caller")
	}
	disabled := c.Disabled()
	disabled.Rules[1].AllowedPeers[0].Host = "different"
	if c.Rules[1].AllowedPeers[0].Host == "different" {
		t.Fatal("shallow copied scope")
	}
	b, _ := os.ReadFile(filepath.Join(d, "profile.json"))
	for _, field := range []string{"public_key", "id_port", "password", "username"} {
		if bytes.Contains(b, []byte(field)) {
			t.Fatalf("unexpected %s", field)
		}
	}
}
func TestV1MigrationRetainsOriginalAndDisabledFixedPorts(t *testing.T) {
	c := fixture(t)
	d := t.TempDir()
	if e := Save(d, c); e != nil {
		t.Fatal(e)
	}
	original, _ := os.ReadFile(filepath.Join(d, "profile.json"))
	next, e := MigrateV1(c, "n1", "")
	if e != nil {
		t.Fatal(e)
	}
	if len(next.Rules) != 4 || next.Version != 2 {
		t.Fatal(next)
	}
	for _, r := range next.Rules {
		if r.Enabled || r.PeerID != "n1" {
			t.Fatal(r)
		}
	}
	backup, e := SaveMigration(d, next)
	if e != nil {
		t.Fatal(e)
	}
	got, _ := os.ReadFile(backup)
	if !bytes.Equal(got, original) {
		t.Fatal("original changed")
	}
	loaded, e := Load(d)
	if e != nil || !reflect.DeepEqual(loaded, next) {
		t.Fatal(loaded, e)
	}
	if _, e = SaveMigration(d, next); e == nil {
		t.Fatal("repeated migration accepted")
	}
	next.Rules[3].ListenPort++
	if e = next.Validate(); e == nil {
		t.Fatal("migrated relay constraint lost")
	}
	c.Mode = "socks"
	if _, e = MigrateV1(c, "n1", ""); e == nil {
		t.Fatal("lossy socks migration accepted")
	}
}
func TestMigrationDoesNotOverwriteBackup(t *testing.T) {
	c := fixture(t)
	d := t.TempDir()
	Save(d, c)
	path := filepath.Join(d, "profile.v1.backup.json")
	os.WriteFile(path, []byte("keep"), 0600)
	next, _ := MigrateV1(c, "n1", "")
	if _, e := SaveMigration(d, next); e == nil {
		t.Fatal("overwrote backup")
	}
	b, _ := os.ReadFile(path)
	if string(b) != "keep" {
		t.Fatal(string(b))
	}
	got, _ := Load(d)
	if got.Version != 1 {
		t.Fatal("migration changed source on backup conflict")
	}
}
func TestJSONBounds(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "oversized.json")
	os.WriteFile(p, []byte(`{}`+strings.Repeat(" ", 64<<10)), 0600)
	var value any
	if e := ReadJSON(p, &value); e == nil {
		t.Fatal("oversized JSON accepted")
	}
	c, _ := NewRules()
	b, _ := json.Marshal(c)
	if len(b) > 64<<10 {
		t.Fatal("empty profile oversized")
	}
}

func TestOversizedSaveLeavesOriginal(t *testing.T) {
	d := t.TempDir()
	c, _ := NewRules()
	if e := Save(d, c); e != nil {
		t.Fatal(e)
	}
	original, _ := os.ReadFile(filepath.Join(d, "profile.json"))
	for i := 0; i < MaxRules; i++ {
		r := shareFixture()
		r.Name = fmt.Sprintf("service-%d", i)
		r.AllowedPeers = nil
		for j := 0; j < MaxAllowedPeers; j++ {
			r.AllowedPeers = append(r.AllowedPeers, PeerRef{ID: fmt.Sprintf("node-%d-%s", j, strings.Repeat("x", 100)), Host: strings.Repeat("a", 60) + ".example.ts.net"})
		}
		c.Rules = append(c.Rules, r)
	}
	if e := c.Validate(); e != nil {
		t.Fatal(e)
	}
	if e := Save(d, c); e == nil {
		t.Fatal("oversized profile saved")
	}
	got, _ := os.ReadFile(filepath.Join(d, "profile.json"))
	if !bytes.Equal(got, original) {
		t.Fatal("failed save changed original")
	}
}
func TestPrivateWritePreservesParentPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes; ACL private files covered separately")
	}
	d := t.TempDir()
	if e := os.Chmod(d, 0755); e != nil {
		t.Fatal(e)
	}
	p := filepath.Join(d, "file.json")
	if e := AtomicWritePrivate(p, []byte("{}")); e != nil {
		t.Fatal(e)
	}
	info, _ := os.Stat(d)
	if info.Mode().Perm() != 0755 {
		t.Fatal("changed parent permissions", info.Mode())
	}
	info, _ = os.Stat(p)
	if info.Mode().Perm() != 0600 {
		t.Fatal(info.Mode())
	}
}

func TestJapaneseRuleAndGroupNamesPreserved(t *testing.T) {
	c, e := NewRules()
	if e != nil {
		t.Fatal(e)
	}
	r := ruleFixture()
	r.Name = "開発用API"
	c.Rules = []Rule{r}
	c.Groups = []Group{{Name: "開発環境", Rules: []string{r.Name}}}
	dir := t.TempDir()
	if e = Save(dir, c); e != nil {
		t.Fatal(e)
	}
	got, e := Load(dir)
	if e != nil || got.Rules[0].Name != r.Name || got.Groups[0].Name != "開発環境" {
		t.Fatal(got, e)
	}
	for _, bad := range []string{"../設定", "名前 空白", "接続\n注入", strings.Repeat("界", 65)} {
		if ValidName(bad) {
			t.Fatal("invalid name accepted", bad)
		}
	}
}
