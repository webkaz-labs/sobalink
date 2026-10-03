package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/config"
)

func TestAtomicOutcomeAtomicRecoveryMustLocalizeWrappedCause(t *testing.T) {
	cause := fmt.Errorf("%w: %w", config.ErrAtomicRecovery, errors.New("unknown persistence owner marker"))
	_, _, translate, err := prepareLocale([]string{"--lang", "ja", "export"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	localized := translate(cause)
	t.Logf("message=%q typed=%v unchangedEnglish=%v", localized.Error(), errors.Is(localized, config.ErrAtomicRecovery), localized.Error() == cause.Error())
	if !errors.Is(localized, config.ErrAtomicRecovery) || localized.Error() == cause.Error() || strings.Contains(localized.Error(), "unknown persistence owner marker") {
		t.Errorf("actual wrapped recovery outcome is not localized")
	}
}

func TestAtomicRecoveryProductionCausesLocalizeAutomaticAndExplicitJapanese(t *testing.T) {
	for _, mode := range []string{"automatic", "explicit-ja", "explicit-en"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("TSNET_BRIDGE_LANG", "")
			t.Setenv("LC_ALL", "ja_JP.UTF-8")
			args := []string{"export"}
			if mode != "automatic" {
				language := "ja"
				if mode == "explicit-en" {
					language = "en"
				}
				args = []string{"--lang", language, "export"}
			}
			_, _, translate, err := prepareLocale(args, nil)
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			if err := config.AtomicWritePrivate(filepath.Join(dir, "seed.json"), []byte(`{}`)); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, ".sobalink-atomic-v1", "owner.lock"), []byte("unknown"), 0600); err != nil {
				t.Fatal(err)
			}
			for _, tc := range []struct{ path, cause string }{
				{filepath.Join(dir, "export.json"), "unknown persistence owner marker"},
				{filepath.Join(dir, ".sobalink-atomic-v1", "user-value.json"), "reserved persistence namespace"},
			} {
				cause := config.AtomicWritePrivate(tc.path, []byte(`{}`))
				if !errors.Is(cause, config.ErrAtomicRecovery) {
					t.Fatal("not production recovery", cause)
				}
				localized := translate(cause)
				if !errors.Is(localized, config.ErrAtomicRecovery) || !errors.Is(localized, cause) {
					t.Fatal("cause identity lost", localized)
				}
				if mode == "explicit-en" {
					if localized != cause {
						t.Fatal("explicit English translated", localized)
					}
					continue
				}
				if !hasJapanese(localized.Error()) || localized.Error() == cause.Error() || tc.cause != "" && strings.Contains(localized.Error(), tc.cause) {
					t.Fatal("wrapped recovery not localized", localized)
				}
				var pathErr *os.PathError
				if errors.As(cause, &pathErr) {
					var retained *os.PathError
					if !errors.As(localized, &retained) || retained != pathErr || !strings.Contains(localized.Error(), pathErr.Path) {
						t.Fatal("captured path value changed", localized)
					}
				}
			}
			pathErr := &os.PathError{Op: "sync", Path: filepath.Join(dir, "User value 日本語 %s"), Err: os.ErrPermission}
			wrapped := fmt.Errorf("%w: %w", config.ErrAtomicRecovery, pathErr)
			localized := translate(wrapped)
			var retained *os.PathError
			if !errors.Is(localized, config.ErrAtomicRecovery) || !errors.Is(localized, os.ErrPermission) || !errors.As(localized, &retained) || retained != pathErr || !strings.Contains(localized.Error(), pathErr.Path) {
				t.Fatal("captured cause/path changed", localized)
			}
		})
	}
}

func TestAtomicRecoveryJapanesePresentationKeepsMachineJSONAndUserValues(t *testing.T) {
	t.Setenv("LC_ALL", "ja_JP.UTF-8")
	t.Setenv("TSNET_BRIDGE_LANG", "")
	original := []byte(`{"code":"atomic_recovery","error":"atomic persistence inventory requires recovery: unknown persistence owner marker","path":"User value 日本語 %s"}` + "\n")
	for _, args := range [][]string{{"export", "--json"}, {"--lang", "ja", "export", "--json"}} {
		var output bytes.Buffer
		_, writer, _, err := prepareLocale(args, &output)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(original); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(output.Bytes(), original) {
			t.Fatal("machine JSON translated", output.String())
		}
	}
}
