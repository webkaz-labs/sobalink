package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/control"
)

func TestLANJoinSaveOutcomeLocalesAndMachineEnvelope(t *testing.T) {
	for _, message := range []string{
		"the other device paired, but the local save was not written; inspect local saved approvals and revoke that pair on the other device before retrying",
		"the other device paired and local state was replaced, but durability could not be confirmed; pairing is paused; stop this device, inspect its saved approval before restarting, and reconcile or revoke the pair on both devices before retrying",
	} {
		for _, sentinel := range []error{nil, config.ErrAtomicCommitted, config.ErrAtomicBusy, config.ErrAtomicRecovery} {
			original := errors.Join(&control.RemoteError{Code: "lan_remote_paired_local_save", Message: message}, sentinel)
			envelope, err := json.Marshal(control.Response{Code: "lan_remote_paired_local_save", Error: original.Error()})
			if err != nil {
				t.Fatal(err)
			}
			for _, mode := range []string{"automatic-ja", "explicit-ja", "explicit-en"} {
				t.Run(mode+"/"+message[:28]+"/"+func() string {
					if sentinel == nil {
						return "ordinary"
					}
					return sentinel.Error()
				}(), func(t *testing.T) {
					t.Setenv("LC_ALL", "ja_JP.UTF-8")
					t.Setenv("TSNET_BRIDGE_LANG", "")
					args := []string{"lan"}
					if mode != "automatic-ja" {
						lang := "ja"
						if mode == "explicit-en" {
							lang = "en"
						}
						args = []string{"--lang", lang, "lan"}
					}
					_, _, translate, err := prepareLocale(args, nil)
					if err != nil {
						t.Fatal(err)
					}
					localized := translate(original)
					var remote *control.RemoteError
					if !errors.As(localized, &remote) || remote.ErrorCode() != "lan_remote_paired_local_save" || sentinel != nil && !errors.Is(localized, sentinel) {
						t.Fatal("localized type/code changed", localized)
					}
					if mode == "explicit-en" {
						if localized != original {
							t.Fatal("English changed", localized)
						}
					} else {
						if !hasJapanese(localized.Error()) || strings.Contains(localized.Error(), message) || !strings.Contains(localized.Error(), "相手端末") {
							t.Fatal("LAN outcome not translated", localized)
						}
						if strings.Contains(message, "local state was replaced") && (!strings.Contains(localized.Error(), "置換されました") || !strings.Contains(localized.Error(), "両端末") || !strings.Contains(localized.Error(), "再起動する前")) {
							t.Fatal("committed guidance mistranslated", localized)
						}
						if strings.Contains(message, "local save was not written") && (!strings.Contains(localized.Error(), "書き込まれていません") || !strings.Contains(localized.Error(), "保存済み承認")) {
							t.Fatal("unwritten guidance mistranslated", localized)
						}
					}
					var output bytes.Buffer
					_, writer, _, err := prepareLocale(append(args, "--json"), &output)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := writer.Write(envelope); err != nil || !bytes.Equal(output.Bytes(), envelope) {
						t.Fatal("machine envelope translated", output.String(), err)
					}
				})
			}
		}
	}
}

func TestLANPrecontactRecoveryLocalesAndMachineEnvelope(t *testing.T) {
	message := "LAN pairing is paused because private state needs recovery; stop this device and inspect its saved approval before restarting or reopening pairing"
	for _, mode := range []string{"automatic-ja", "explicit-ja", "explicit-en"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("TSNET_BRIDGE_LANG", "")
			t.Setenv("LC_ALL", "ja_JP.UTF-8")
			args := []string{"lan", "join"}
			if mode != "automatic-ja" {
				language := "ja"
				if mode == "explicit-en" {
					language = "en"
				}
				args = []string{"--lang", language, "lan", "join"}
			}
			_, _, translate, err := prepareLocale(args, nil)
			if err != nil {
				t.Fatal(err)
			}
			original := errors.Join(errors.New(message), config.ErrAtomicRecovery)
			localized := translate(original)
			if !errors.Is(localized, config.ErrAtomicRecovery) {
				t.Fatal("typed recovery outcome lost", localized)
			}
			if mode == "explicit-en" {
				if localized.Error() != original.Error() {
					t.Fatal("explicit English changed", localized)
				}
			} else if !hasJapanese(localized.Error()) || strings.Contains(localized.Error(), message) || !strings.Contains(localized.Error(), "保存済み承認") {
				t.Fatal("recovery guidance not localized", localized)
			}
			var output bytes.Buffer
			_, writer, _, err := prepareLocale(append(args, "--json"), &output)
			if err != nil {
				t.Fatal(err)
			}
			wire := []byte(`{"code":"","error":"` + message + `"}` + "\n")
			if _, err := writer.Write(wire); err != nil || !bytes.Equal(output.Bytes(), wire) {
				t.Fatal("machine JSON changed", output.String(), err)
			}
		})
	}
}
