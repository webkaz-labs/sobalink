package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/control"
)

func TestDurableProfileErrorsLocalizeNormalCLIPaths(t *testing.T) {
	for _, fixture := range []struct {
		name, code string
		version    int
	}{
		{"unsupported-format", "profile_format_unsupported", 1},
		{"missing-state-guard", "outbox_recovery_required", 2},
		{"orphaned-store", "outbox_recovery_required", 0},
	} {
		for _, command := range [][]string{{"start", "--offline"}, {"init"}, {"--offline", "profile", "export"}} {
			t.Run(fixture.name+"/"+strings.Join(command, "-"), func(t *testing.T) {
				var machineResult string
				for _, locale := range []struct {
					flag, environment string
					ja                bool
				}{
					{"en", "ja_JP.UTF-8", false},
					{"ja", "en_US.UTF-8", true},
					{"auto", "ja_JP.UTF-8", true},
					{"auto", "zz_UNKNOWN", false},
				} {
					t.Setenv("LC_ALL", locale.environment)
					t.Setenv("LC_MESSAGES", "")
					t.Setenv("LANG", "")
					for _, machine := range []bool{false, true} {
						dir := t.TempDir()
						profilePath := filepath.Join(dir, "sobalink.json")
						marker := filepath.Join(dir, "durable-outbox-v1", "synthetic-retained-evidence")
						var original []byte
						if fixture.version != 0 {
							var err error
							original, err = json.Marshal(map[string]any{
								"version":  fixture.version,
								"settings": map[string]string{"network": "none", "locale": "auto", "theme": "system", "hostname": "synthetic-profile"},
								"peers":    []any{}, "services": []any{},
							})
							if err != nil {
								t.Fatal(err)
							}
							if err := os.WriteFile(profilePath, original, 0600); err != nil {
								t.Fatal(err)
							}
						} else {
							if err := os.Mkdir(filepath.Dir(marker), 0700); err != nil {
								t.Fatal(err)
							}
							if err := os.WriteFile(marker, []byte("synthetic evidence to preserve"), 0600); err != nil {
								t.Fatal(err)
							}
						}
						args := []string{"--locale", locale.flag, "--state-dir", dir}
						if machine {
							args = append(args, "--json-errors")
						}
						args = append(args, command...)
						ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
						var stdout bytes.Buffer
						err := runWith(ctx, args, &stdout, strings.NewReader(""), func(context.Context, string, string, any) error {
							t.Fatal("profile rejection attempted agent IPC")
							return nil
						})
						cancel()
						var coded interface{ ErrorCode() string }
						if err == nil || !errors.As(err, &coded) || coded.ErrorCode() != fixture.code || stdout.Len() != 0 {
							t.Fatalf("missing read-only coded rejection: %v, output %q", err, stdout.String())
						}
						if machine {
							var stderr bytes.Buffer
							writeCommandError(&stderr, err)
							var result struct {
								Code  string `json:"code"`
								Error string `json:"error"`
							}
							decoder := json.NewDecoder(&stderr)
							decoder.DisallowUnknownFields()
							if decoder.Decode(&result) != nil || decoder.Decode(new(any)) != io.EOF || result.Code != fixture.code {
								t.Fatal("machine error schema or code changed")
							}
							if machineResult != "" && result.Error != machineResult {
								t.Fatal("machine error was localized")
							}
							machineResult = result.Error
						} else {
							words := []string{text(locale.ja, "profile", "プロファイル"), text(locale.ja, "Keep", "保管")}
							if fixture.code == "profile_format_unsupported" {
								words = append(words, "--state-dir", text(locale.ja, "empty directory", "空のディレクトリ"))
							} else {
								words = append(words, text(locale.ja, "do not delete or replace", "削除や置換をせず"), text(locale.ja, "delivery records", "到着の記録"))
							}
							for _, word := range words {
								if !strings.Contains(err.Error(), word) {
									t.Fatalf("missing localized guidance %q: %v", word, err)
								}
							}
						}
						after, readErr := os.ReadFile(profilePath)
						if fixture.version == 0 {
							if !errors.Is(readErr, os.ErrNotExist) {
								t.Fatal("guard rejection created a profile")
							}
							if data, err := os.ReadFile(marker); err != nil || string(data) != "synthetic evidence to preserve" {
								t.Fatal("guard rejection changed retained evidence")
							}
						} else if readErr != nil || !bytes.Equal(original, after) {
							t.Fatal("format/guard rejection rewrote the profile")
						}
					}
				}
			})
		}
	}
}

func TestDurableProfileLocalizationIsBoundedAndPreservesCause(t *testing.T) {
	for _, ja := range []bool{false, true} {
		for _, code := range []string{"profile_format_unsupported", "outbox_recovery_required", "outbox_invalid", "different_recovery_required"} {
			original := &control.RemoteError{Code: code, Message: "original synthetic error"}
			wrapped := errors.Join(errors.New("outer context"), original)
			localized := localizeDurableProfileError(ja, wrapped)
			var coded interface{ ErrorCode() string }
			if !errors.Is(localized, original) || !errors.As(localized, &coded) || coded.ErrorCode() != code {
				t.Fatal("localization lost the original cause or stable code")
			}
			if code != "profile_format_unsupported" && code != "outbox_recovery_required" && localized != wrapped {
				t.Fatal("unrelated error was changed")
			}
		}
		uncoded := errors.New("uncoded synthetic error")
		if localizeDurableProfileError(ja, uncoded) != uncoded {
			t.Fatal("uncoded error was changed")
		}
	}
}
