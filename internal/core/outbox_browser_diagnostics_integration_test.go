//go:build lanlink_integration

package core

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type outboxBrowserFailure struct {
	Schema    int    `json:"schema"`
	Phase     string `json:"phase"`
	ErrorKind string `json:"errorKind"`
}

func readOutboxBrowserFailure(private string) (outboxBrowserFailure, bool) {
	var value outboxBrowserFailure
	path := filepath.Join(private, "outbox-browser-failure.json")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 512 {
		return value, false
	}
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) > 512 {
		return value, false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&value) != nil || decoder.Decode(new(any)) != io.EOF || value.Schema != 1 {
		return outboxBrowserFailure{}, false
	}
	switch value.Phase {
	case "launch", "authenticate", "open-english", "focus-english", "select-recipients", "prepare", "commit-lost", "recover-status", "reload-japanese", "open-japanese", "focus-japanese", "admission-bridge", "observe-receipts", "refresh-completed", "result":
	default:
		return outboxBrowserFailure{}, false
	}
	switch value.ErrorKind {
	case "timeout", "assertion", "selector_ambiguous", "browser_launch", "browser_closed", "runtime", "command_boundary", "checkpoint", "cleanup", "interrupted", "unexpected", "core_evidence", "evidence_unavailable", "locator_missing", "element_disabled", "element_hidden", "element_not_editable", "element_detached", "pointer_intercepted", "element_unstable":
		return value, true
	default:
		return outboxBrowserFailure{}, false
	}
}

func logOutboxBrowserFailure(t *testing.T, private string) {
	t.Helper()
	if !t.Failed() {
		return
	}
	if value, ok := readOutboxBrowserFailure(private); ok {
		t.Logf("outbox browser failure: phase=%s errorKind=%s", value.Phase, value.ErrorKind)
	}
}

func TestOutboxBrowserFailurePrivacy(t *testing.T) {
	valid := `{"schema":1,"phase":"prepare","errorKind":"assertion"}`
	for _, tc := range []struct {
		name, raw string
		mode      os.FileMode
		valid     bool
	}{
		{"fixed", valid, 0600, true},
		{"version", strings.Replace(valid, `"schema":1`, `"schema":2`, 1), 0600, false},
		{"phase", strings.Replace(valid, "prepare", "synthetic-private-selector", 1), 0600, false},
		{"kind", strings.Replace(valid, "assertion", "synthetic-private-body", 1), 0600, false},
		{"unknown", strings.TrimSuffix(valid, "}") + `,"message":"synthetic-private-token"}`, 0600, false},
		{"trailing", valid + `{}`, 0600, false},
		{"partial", `{"schema":1`, 0600, false},
		{"large", valid + strings.Repeat(" ", 513), 0600, false},
		{"public", valid, 0644, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "outbox-browser-failure.json"), []byte(tc.raw), tc.mode); err != nil {
				t.Fatal(err)
			}
			value, ok := readOutboxBrowserFailure(dir)
			if ok != tc.valid || ok && value != (outboxBrowserFailure{1, "prepare", "assertion"}) {
				t.Fatal("fixed failure classification validation differs")
			}
		})
	}
	for _, kind := range []string{"missing", "directory", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "outbox-browser-failure.json")
			switch kind {
			case "directory":
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				target := filepath.Join(dir, "private.json")
				if err := os.WriteFile(target, []byte(valid), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			}
			if _, ok := readOutboxBrowserFailure(dir); ok {
				t.Fatal("nonregular failure evidence accepted")
			}
		})
	}
}
