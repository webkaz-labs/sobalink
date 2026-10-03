package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitMetadataOnlyRepeatAndDryRun(t *testing.T) {
	for _, locale := range []string{"en", "ja"} {
		dir := filepath.Join(t.TempDir(), "profile")
		call := func(args ...string) (string, error) {
			var out bytes.Buffer
			err := runWith(context.Background(), append([]string{"--state-dir", dir, "--locale", locale}, args...), &out, panicReader{}, func(context.Context, string, string, any) error { t.Fatal("init contacted agent"); return nil })
			return out.String(), err
		}
		for _, args := range [][]string{{"init", "--help"}, {"--dry-run", "init", "--name", "example-node"}, {"init", "--name", "invalid_name"}} {
			_, _ = call(args...)
			if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("preview/invalid init created state", err)
			}
		}
		result, err := call("init", "--name", "example-node", "--json")
		if err != nil || !strings.Contains(result, `"state":"initialized"`) {
			t.Fatal(err, result)
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if entry.Name() != "sobalink.json" && entry.Name() != "process.lock" {
				t.Fatal("unexpected initialized state", entry.Name())
			}
		}
		before, _ := os.ReadFile(filepath.Join(dir, "sobalink.json"))
		result, err = call("init", "--json")
		if err != nil || !strings.Contains(result, `"state":"exists"`) {
			t.Fatal(err, result)
		}
		if _, err := call("init", "--name", "changed-node"); err == nil {
			t.Fatal("renamed existing profile")
		}
		after, _ := os.ReadFile(filepath.Join(dir, "sobalink.json"))
		if !bytes.Equal(before, after) {
			t.Fatal("repeat changed profile")
		}
	}
}
