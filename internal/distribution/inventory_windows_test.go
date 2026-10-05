package distribution

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func makeJunction(t *testing.T, link, target string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "cmd", "/c", "mklink", "/J", link, target).CombinedOutput()
	if err != nil {
		t.Fatalf("create fixture junction: %v: %s", err, output)
	}
	t.Cleanup(func() {
		if err := os.Remove(link); err != nil && !os.IsNotExist(err) {
			t.Errorf("remove fixture junction: %v", err)
		}
	})
}

// setup-go's cacheWindowsDir creates its GOROOT with fs.symlinkSync(...,
// "junction"). Go 1.23+ does not resolve that final junction in EvalSymlinks.
func TestNoticeCollectionWindowsRootJunction(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "actual toolchain")
	writeFixture(t, filepath.Join(target, "LICENSE"), "Go license")
	writeFixture(t, filepath.Join(target, "src", "vendor", "example", "NOTICE"), "vendored notice")
	writeFixture(t, filepath.Join(target, "src", "cmd", "vendor", "example", "LICENSE"), "excluded tool notice")
	writeFixture(t, filepath.Join(target, "src", "math", "log1p.go"), "embedded notice")
	outside := t.TempDir()
	writeFixture(t, filepath.Join(outside, "NOTICE"), "must not follow nested junction")
	link := filepath.Join(root, "x64")
	makeJunction(t, link, target)

	if info, err := os.Lstat(link); err != nil {
		t.Fatal(err)
	} else {
		t.Logf("fixture junction Lstat mode: %s", info.Mode())
	}
	resolved, err := filepath.EvalSymlinks(link)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("EvalSymlinks keeps final junction: %t", resolved == link)

	out := t.TempDir()
	inventory, err := collectInventory(nil, link, out)
	if err != nil {
		t.Fatal(err)
	}
	if len(inventory.Go.Notices) != 3 {
		t.Fatalf("Go notices = %+v", inventory.Go.Notices)
	}
	for _, want := range []string{"LICENSE", "src/vendor/example/NOTICE", "src/math/log1p.go"} {
		if _, err := os.Stat(filepath.Join(out, "go", filepath.FromSlash(want))); err != nil {
			t.Fatalf("missing required notice %s: %v", want, err)
		}
	}
	makeJunction(t, filepath.Join(target, "nested"), outside)
	if _, err := collectInventory(nil, link, t.TempDir()); err == nil {
		t.Fatal("accepted a nested junction that could hide notices")
	}
	if _, err := os.Stat(filepath.Join(out, "go", "nested", "NOTICE")); !os.IsNotExist(err) {
		t.Fatal("followed nested junction")
	}
	if _, err := os.Stat(filepath.Join(out, "go", "src", "cmd")); !os.IsNotExist(err) {
		t.Fatal("included excluded tool sources")
	}
}

func TestSourceInventoryWindowsRejectsJunctions(t *testing.T) {
	for _, rel := range []string{"internal", routecatPath} {
		t.Run(rel, func(t *testing.T) {
			root, packages := sourceFixture(t)
			original := filepath.Join(root, filepath.FromSlash(rel))
			target := filepath.Join(t.TempDir(), "original")
			if err := os.Rename(original, target); err != nil {
				t.Fatal(err)
			}
			makeJunction(t, original, target)
			if _, err := sourceInventory(root, packages, t.TempDir()); err == nil {
				t.Fatal("accepted a source input directory junction")
			}
		})
	}
}
