package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/control"
	"github.com/webkaz-labs/sobalink/internal/resource"
	"github.com/webkaz-labs/sobalink/internal/webui"
)

const resourceTestID = "0123456789abcdef0123456789abcdef"

func TestResourceCLICommandsAndStableJSON(t *testing.T) {
	for _, locale := range []string{"en", "ja"} {
		for _, action := range []string{"list", "inspect", "preview"} {
			args := []string{"--locale", locale, "--state-dir", t.TempDir(), "resource", action, "--json"}
			if action != "list" {
				args = append(args, "--id", resourceTestID)
			}
			if action == "preview" {
				args = append(args, "--concurrent-files", "default", "--concurrent-per-peer", "2")
			}
			var out bytes.Buffer
			calls := 0
			client := func(_ context.Context, _ string, raw string, target any) error {
				calls++
				var cmd webui.Command
				if err := json.Unmarshal([]byte(raw), &cmd); err != nil {
					t.Fatal(err)
				}
				if cmd.Name != "resource."+action || cmd.RequestID == "" {
					t.Fatal(cmd)
				}
				var p map[string]json.RawMessage
				if err := json.Unmarshal(cmd.Payload, &p); err != nil {
					t.Fatal(err)
				}
				if action == "list" {
					if len(p) != 0 {
						t.Fatal(p)
					}
				} else {
					if string(p["schemaVersion"]) != "1" || string(p["resourceId"]) != `"`+resourceTestID+`"` {
						t.Fatal(p)
					}
					want := 2
					if action == "preview" {
						want = 3
						var settings resource.Settings
						if err := json.Unmarshal(p["settings"], &settings); err != nil {
							t.Fatal(err)
						}
						if settings.TransferConcurrentFiles.Mode != "default" || settings.TransferConcurrentPerPeer.Mode != "limited" || (settings.TransferConcurrentPerPeer.Value == nil || *settings.TransferConcurrentPerPeer.Value != 2) {
							t.Fatal(settings)
						}
					}
					if len(p) != want {
						t.Fatal(p)
					}
				}
				return json.Unmarshal([]byte(`{"schemaVersion":1,"resourceId":"`+resourceTestID+`"}`), target)
			}
			if err := runWith(t.Context(), args, &out, strings.NewReader(""), client); err != nil {
				t.Fatal(err)
			}
			if calls != 1 || !json.Valid(out.Bytes()) || !strings.Contains(out.String(), resourceTestID) {
				t.Fatal(calls, out.String())
			}
		}
	}
}

func TestResourceCLIRejectsInvalidAndOffline(t *testing.T) {
	noCall := func(context.Context, string, string, any) error { t.Fatal("invalid input contacted agent"); return nil }
	for _, ja := range []bool{false, true} {
		cases := [][]string{{"apply"}, {"status"}, {"operation.status"}, {"list", "extra"}, {"list", "--id", resourceTestID}, {"inspect"}, {"inspect", "--id", strings.ToUpper(resourceTestID)}, {"inspect", "--id", "bad"}, {"preview", "--id", resourceTestID}, {"preview", "--id", resourceTestID, "--concurrent-files", "1"}}
		for _, value := range []string{"0", "-1", "unlimited", "9007199254740992", "9223372036854775808", "1.5", ""} {
			cases = append(cases, []string{"preview", "--id", resourceTestID, "--concurrent-files", value, "--concurrent-per-peer", "2"})
		}
		for _, args := range cases {
			if err := resourceCLI(t.Context(), args, t.TempDir(), ja, false, io.Discard, noCall); err == nil {
				t.Fatal("accepted", args)
			}
		}
	}
	if err := runWith(t.Context(), []string{"--offline", "--state-dir", t.TempDir(), "resource", "list"}, io.Discard, strings.NewReader(""), noCall); err == nil {
		t.Fatal("offline resource accepted")
	}
}

func TestResourceCLIHelpDryRunAndError(t *testing.T) {
	noCall := func(context.Context, string, string, any) error {
		t.Fatal("read-only help/dry run contacted agent")
		return nil
	}
	for _, locale := range []string{"en", "ja"} {
		for _, suffix := range [][]string{{"help", "resource"}, {"resource", "--help"}, {"resource", "inspect", "--help"}} {
			var out bytes.Buffer
			if err := runWith(t.Context(), append([]string{"--locale", locale}, suffix...), &out, strings.NewReader(""), noCall); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), "resource preview") {
				t.Fatal(out.String())
			}
		}
		var out bytes.Buffer
		args := []string{"--locale", locale, "--state-dir", t.TempDir(), "--dry-run", "resource", "preview", "--id", resourceTestID, "--concurrent-files", "3", "--concurrent-per-peer", "default"}
		if err := runWith(t.Context(), args, &out, strings.NewReader(""), noCall); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), `"validation": "local-input-only"`) || !strings.Contains(out.String(), `"applied": false`) {
			t.Fatal(out.String())
		}
	}
	sentinel := errors.New("resource_unavailable")
	if err := resourceCLI(t.Context(), []string{"list"}, t.TempDir(), false, false, io.Discard, func(context.Context, string, string, any) error { return sentinel }); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
}

type resourceTestError string

func (e resourceTestError) Error() string     { return string(e) }
func (e resourceTestError) ErrorCode() string { return string(e) }

func TestResourceCLIErrorLocalizationAndMachineCodes(t *testing.T) {
	for _, code := range []string{"resource_unavailable", "resource_invalid", "resource_not_found"} {
		original := resourceTestError(code)
		for _, machine := range []bool{false, true} {
			args := []string{"--locale", "ja", "--state-dir", t.TempDir()}
			if machine {
				args = append(args, "--json-errors")
			}
			args = append(args, "resource", "list")
			err := runWith(t.Context(), args, io.Discard, strings.NewReader(""), func(context.Context, string, string, any) error { return original })
			var coded interface{ ErrorCode() string }
			if !errors.As(err, &coded) || coded.ErrorCode() != code {
				t.Fatal(err)
			}
			if machine {
				if err.Error() != resourceControlError(original).Error() {
					t.Fatal("machine message translated", err)
				}
			} else if err.Error() == resourceControlError(original).Error() {
				t.Fatal("human error untranslated", err)
			}
		}
	}
}

func TestResourceCLIAutomaticLocaleAndOverride(t *testing.T) {
	for _, env := range []string{"ja_JP.UTF-8", "en_US.UTF-8", "zz_ZZ.UTF-8"} {
		t.Setenv("LC_ALL", env)
		for _, locale := range []string{"auto", "en", "ja"} {
			var out bytes.Buffer
			if err := runWith(t.Context(), []string{"--locale", locale, "help", "resource"}, &out, strings.NewReader(""), func(context.Context, string, string, any) error { t.Fatal("help contacted agent"); return nil }); err != nil {
				t.Fatal(err)
			}
			wantJapanese := locale == "ja" || locale == "auto" && strings.HasPrefix(env, "ja")
			if strings.Contains(out.String(), "ローカル転送設定") != wantJapanese {
				t.Fatal(env, locale, out.String())
			}
		}
	}
}

func TestResourceCLIProjectsPrivateControlErrors(t *testing.T) {
	const privatePath = "/synthetic-private-state/control.sock"
	const privateAddress = "192.0.2.27:4321"
	failures := []error{
		&os.PathError{Op: "open", Path: privatePath, Err: os.ErrPermission},
		&net.OpError{Op: "dial", Net: "unix", Addr: &net.UnixAddr{Name: privatePath, Net: "unix"}, Err: errors.New(privateAddress)},
		errors.New("unexpected control failure " + privatePath + " " + privateAddress),
		&control.RemoteError{Code: "resource_invalid", Message: privatePath + " " + privateAddress},
		&control.RemoteError{Code: "unrecognized_code", Message: privatePath + " " + privateAddress},
		fmt.Errorf("%s: %w", privatePath, context.Canceled),
		fmt.Errorf("%s: %w", privateAddress, context.DeadlineExceeded),
	}
	for _, original := range failures {
		for _, locale := range []string{"en", "ja"} {
			for _, machine := range []bool{false, true} {
				args := []string{"--locale", locale, "--state-dir", t.TempDir()}
				if machine {
					args = append(args, "--json-errors")
				}
				args = append(args, "resource", "list")
				err := runWith(t.Context(), args, io.Discard, strings.NewReader(""), func(context.Context, string, string, any) error { return original })
				if err == nil {
					t.Fatal("failure became success")
				}
				if errors.Is(original, context.Canceled) && !errors.Is(err, context.Canceled) {
					t.Fatal("cancellation lost")
				}
				if errors.Is(original, context.DeadlineExceeded) && !errors.Is(err, context.DeadlineExceeded) {
					t.Fatal("deadline lost")
				}
				var rendered bytes.Buffer
				writeCommandError(&rendered, err)
				if strings.Contains(rendered.String(), privatePath) || strings.Contains(rendered.String(), privateAddress) {
					t.Fatal("private control details serialized", rendered.String())
				}
				if machine {
					var output struct {
						Code  string `json:"code"`
						Error string `json:"error"`
					}
					if e := json.Unmarshal(rendered.Bytes(), &output); e != nil {
						t.Fatal(e)
					}
					expected := "resource_control_unavailable"
					if errors.Is(original, context.Canceled) {
						expected = "canceled"
					} else if errors.Is(original, context.DeadlineExceeded) {
						expected = "deadline_exceeded"
					} else if remote, ok := original.(*control.RemoteError); ok && remote.Code == "resource_invalid" {
						expected = remote.Code
					}
					if output.Code != expected || output.Error == "" {
						t.Fatal("error semantics changed", output)
					}
				}
			}
		}
	}
}
