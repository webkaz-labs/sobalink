package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/core"
)

func TestAtomicCommittedRevealPreservesPrivateFileAndLocalizedOutcome(t *testing.T) {
	for _, ja := range []bool{false, true} {
		t.Run(fmt.Sprint("ja=", ja), func(t *testing.T) {
			dir := t.TempDir()
			target := filepath.Join(dir, "credentials.json")
			credentials := core.SavedProxyCredentials{Username: "fixture-private-user", Password: "fixture-private-password"}
			query := func(_ string, _ any, out any) error {
				data, _ := json.Marshal(credentials)
				return json.Unmarshal(data, out)
			}
			writes := 0
			var out bytes.Buffer
			handled, err := savedProxyCLIWithWriter(context.Background(), []string{"reveal", "example", "--review", "revision", "--private-file", target}, dir, ja, false, &out, strings.NewReader(""), query, nil, func(path string, data []byte) error {
				writes++
				if err := config.AtomicWritePrivate(path, data); err != nil {
					return err
				}
				return fmt.Errorf("%w: private detail %s/.sobalink-atomic-v1 fixture-private-password", config.ErrAtomicCommitted, dir)
			})
			if !handled || !errors.Is(err, config.ErrAtomicCommitted) || writes != 1 || out.Len() != 0 {
				t.Fatal("committed reveal outcome lost/retried/printed success", writes, out.String(), err)
			}
			for _, secret := range []string{dir, ".sobalink-atomic-v1", credentials.Username, credentials.Password} {
				if strings.Contains(err.Error(), secret) {
					t.Fatal("private detail leaked", err)
				}
			}
			if ja && !strings.Contains(err.Error(), "再実行する前") || !ja && !strings.Contains(err.Error(), "before retrying") {
				t.Fatal("localized reconciliation instruction missing", err)
			}
			data, readErr := os.ReadFile(target)
			var saved core.SavedProxyCredentials
			if readErr != nil || json.Unmarshal(data, &saved) != nil || saved != credentials {
				t.Fatal("committed reveal output hidden/removed", readErr)
			}
		})
	}
}

func assertOnlyOwnedProfileMetadata(t *testing.T, dir string, entries []os.DirEntry) {
	t.Helper()
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	if strings.Join(names, ",") != ".sobalink-atomic-v1,process.lock,sobalink.json" {
		t.Fatal("unexpected initialized state", names)
	}
	owned := filepath.Join(dir, ".sobalink-atomic-v1")
	info, err := os.Lstat(owned)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("unsafe persistence namespace", err)
	}
	children, err := os.ReadDir(owned)
	if err != nil || len(children) != 1 || children[0].Name() != "owner.lock" || !children[0].Type().IsRegular() {
		t.Fatal("unexpected owned metadata or retained snapshot", children, err)
	}
	marker := filepath.Join(owned, "owner.lock")
	data, err := os.ReadFile(marker)
	if err != nil || string(data) != "sobalink atomic persistence v1\n" {
		t.Fatal("invalid owner marker", err)
	}
	file, err := os.Open(marker)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := checkProxyPrivateFile(file); err != nil {
		t.Fatal("owner marker not private", err)
	}
}
