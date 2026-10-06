package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/control"
)

func TestLANSetupRotationIsExplicitAndLocalized(t *testing.T) {
	for _, ja := range []bool{false, true} {
		for _, args := range [][]string{{"--rotate-certificate"}, {"--network", "none", "--rotate-certificate"}, {"--network", "lan", "--relay", "192.168.50.10:48443", "--certificate", strings.Repeat("a", 64), "--rotate-certificate"}} {
			if _, err := setupPayload(args, ja, io.Discard); err == nil || (ja && !strings.Contains(err.Error(), "必要")) {
				t.Fatal("invalid rotation accepted or not localized", err)
			}
		}
		args := []string{"--network", "lan", "--host", "192.168.50.10:48443"}
		plain, err := setupPayload(args, ja, io.Discard)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := plain["rotateCertificate"]; ok {
			t.Fatal("ordinary host setup rotates")
		}
		rotate, err := setupPayload(append(args, "--rotate-certificate"), ja, io.Discard)
		if err != nil || rotate["rotateCertificate"] != true {
			t.Fatal("explicit rotation missing", err)
		}
	}
}

func TestLANSetupRecoveryMessagesKeepMachineContract(t *testing.T) {
	for _, code := range []string{"lan_policy_invalid", "lan_policy_setup_required", "lan_policy_relay_outside", "lan_certificate_rotation_required", "lan_certificate_rotation_invalid", "lan_relay_pairs_present", "lan_certificate_expired", "network_restart_required"} {
		for _, locale := range []string{"ja", "en"} {
			for _, structured := range []bool{false, true} {
				original := &control.RemoteError{Code: code, Message: "Synthetic private-state recovery"}
				args := []string{"--locale", locale}
				if structured {
					args = append(args, "--json-errors")
				}
				args = append(args, "setup", "--network", "lan", "--host", "192.168.50.10:48443")
				err := runWith(context.Background(), args, io.Discard, strings.NewReader(""), func(context.Context, string, string, any) error { return original })
				var coded interface{ ErrorCode() string }
				if err == nil || !errors.As(err, &coded) || coded.ErrorCode() != code {
					t.Fatal("lost error code", err)
				}
				if locale == "ja" && !structured {
					if err.Error() == original.Message {
						t.Fatal("untranslated human error", code)
					}
				} else if !strings.Contains(err.Error(), original.Message) {
					t.Fatal("machine or English message changed", err)
				}
			}
		}
	}
}

func TestLANStatusReportsSavedReadyAndCertificateSeparately(t *testing.T) {
	raw := `{"self":{"name":"fixture","status":"idle"},"settings":{"network":"lan"},"lan":{"configured":true,"listenerReady":false,"relayReady":false,"relay":{"kind":"host","address":"192.168.50.10:48443"},"certificate":{"state":"expiring","notBefore":"2026-01-01T00:00:00Z","notAfter":"2027-01-01T00:00:00Z"}}}`
	for _, locale := range []string{"en", "ja"} {
		var out bytes.Buffer
		err := runWith(context.Background(), []string{"--locale", locale, "status"}, &out, strings.NewReader(""), func(_ context.Context, _ string, _ string, out any) error { return json.Unmarshal([]byte(raw), out) })
		if err != nil {
			t.Fatal(err)
		}
		for _, text := range []string{"192.168.50.10:48443", "2027-01-01T00:00:00Z", "30"} {
			if !strings.Contains(out.String(), text) {
				t.Fatal("status missing", text, out.String())
			}
		}
		if locale == "ja" && (!strings.Contains(out.String(), "未準備") || !strings.Contains(out.String(), "停止中")) {
			t.Fatal("Japanese status lost readiness distinction", out.String())
		}
	}
}
