package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	qrcode "github.com/skip2/go-qrcode"
	"github.com/webkaz-labs/sobalink/internal/core"
	"github.com/webkaz-labs/sobalink/internal/devicecard"
	"github.com/webkaz-labs/sobalink/internal/webui"
)

func fictionalDeviceCard(t *testing.T, mode string) (devicecard.Card, string) {
	t.Helper()
	card := devicecard.Card{Version: 1, Mode: mode, Name: "Fictional device", PublicKey: strings.Repeat("a1", 32)}
	encoded, err := devicecard.Encode(card)
	if err != nil {
		t.Fatal(err)
	}
	return card, encoded
}

func cardNoCalls(t *testing.T) controlCaller {
	t.Helper()
	return func(context.Context, string, string, any) error {
		t.Error("read-only local operation contacted Core")
		return errors.New("unexpected call")
	}
}

func cardFixtureClient(t *testing.T, calls *[]webui.Command) controlCaller {
	t.Helper()
	return func(_ context.Context, _ string, raw string, target any) error {
		var command webui.Command
		if err := json.Unmarshal([]byte(raw), &command); err != nil {
			t.Fatal(err)
		}
		*calls = append(*calls, command)
		var value any
		switch command.Name {
		case "device-card.export":
			var input struct {
				Mode                string `json:"mode"`
				Name                string `json:"name"`
				IncludeEndpointHint bool   `json:"includeEndpointHint"`
				QR                  bool   `json:"qr"`
			}
			if err := json.Unmarshal(command.Payload, &input); err != nil {
				t.Fatal(err)
			}
			card, _ := fictionalDeviceCard(t, input.Mode)
			card.Name = input.Name
			if input.IncludeEndpointHint {
				if input.Mode == "lan" {
					card.Relay = &devicecard.RelayHint{Address: "192.0.2.10:54546", CertificateSHA256: strings.Repeat("b2", 32)}
				} else {
					card.Endpoint = "192.168.50.10:54546"
				}
			}
			encoded, err := devicecard.Encode(card)
			if err != nil {
				t.Fatal(err)
			}
			view := core.DeviceCardExportView{Text: encoded, Card: card, Verification: "unverified", Freshness: "unknown"}
			if input.QR {
				code, err := qrcode.New(encoded, qrcode.Medium)
				if err != nil {
					t.Fatal(err)
				}
				view.QR = code.Bitmap()
			}
			value = view
		case "device-card.inspect":
			var input struct {
				Card         string `json:"card"`
				ExpectedMode string `json:"expectedMode"`
			}
			if err := json.Unmarshal(command.Payload, &input); err != nil {
				t.Fatal(err)
			}
			var err error
			value, err = devicecard.Inspect(input.Card, input.ExpectedMode)
			if err != nil {
				t.Fatal(err)
			}
		default:
			t.Fatalf("unexpected authority/state action: %s", command.Name)
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return json.Unmarshal(encoded, target)
	}
}

func TestDeviceCardCLIExportDefaultsAndExplicitOptions(t *testing.T) {
	for _, mode := range []string{"lan", "direct-lan"} {
		for _, include := range []bool{false, true} {
			for _, qr := range []bool{false, true} {
				var calls []webui.Command
				var outputs []string
				for _, locale := range []string{"en", "ja"} {
					args := []string{"--locale", locale, "card", "export", "--mode", mode, "--name", "架空の表示名", "--json"}
					if include {
						args = append(args, "--include-endpoint")
					}
					if qr {
						args = append(args, "--qr")
					}
					var out bytes.Buffer
					if err := runWith(context.Background(), args, &out, nil, cardFixtureClient(t, &calls)); err != nil {
						t.Fatal(err)
					}
					var view core.DeviceCardExportView
					if err := json.Unmarshal(out.Bytes(), &view); err != nil {
						t.Fatal(err)
					}
					if (view.Endpoint != "" || view.Relay != nil) != include || (len(view.QR) > 0) != qr || view.Name != "架空の表示名" || view.Verification != "unverified" || view.Freshness != "unknown" {
						t.Fatal("export lost its explicit public contract")
					}
					var payload map[string]any
					if err := json.Unmarshal(calls[len(calls)-1].Payload, &payload); err != nil {
						t.Fatal(err)
					}
					if !include && payload["includeEndpointHint"] != nil || !qr && payload["qr"] != nil {
						t.Fatal("optional data was requested by default")
					}
					outputs = append(outputs, out.String())
				}
				if len(calls) != 2 || outputs[0] != outputs[1] {
					t.Fatal("extra action or localized JSON")
				}
			}
		}
	}
}

func TestDeviceCardCLIInspectOnlineOfflineAndNoProfile(t *testing.T) {
	for _, mode := range []string{"lan", "direct-lan"} {
		_, encoded := fictionalDeviceCard(t, mode)
		input := " \t" + encoded + "\r\n"
		path := filepath.Join(t.TempDir(), "card.txt")
		if err := os.WriteFile(path, []byte(input), 0600); err != nil {
			t.Fatal(err)
		}
		var expected string
		for _, offline := range []bool{false, true} {
			for _, file := range []bool{false, true} {
				for _, locale := range []string{"en", "ja"} {
					dir := filepath.Join(t.TempDir(), "absent-profile")
					var calls []webui.Command
					client := cardFixtureClient(t, &calls)
					args := []string{"--locale", locale, "--state-dir", dir}
					if offline {
						args = append(args, "--offline")
						client = cardNoCalls(t)
					}
					args = append(args, "card", "inspect", "--mode", mode, "--json")
					if file {
						args = append(args, "--file", path)
					} else {
						args = append(args, "--stdin")
					}
					var out bytes.Buffer
					if err := runWith(context.Background(), args, &out, strings.NewReader(input), client); err != nil {
						t.Fatal(err)
					}
					if expected == "" {
						expected = out.String()
					}
					if out.String() != expected || (!offline && len(calls) != 1) {
						t.Fatal("inspection output or API changed")
					}
					if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
						t.Fatal("inspection touched local profile")
					}
				}
			}
		}
	}
	// Explicit offline inspection does not even need DefaultDir to work.
	t.Setenv("HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	_, encoded := fictionalDeviceCard(t, "lan")
	var out bytes.Buffer
	if err := runWith(context.Background(), []string{"--offline", "card", "inspect", "--mode", "lan", "--stdin"}, &out, strings.NewReader(encoded), cardNoCalls(t)); err != nil {
		t.Fatal(err)
	}
}

func TestDeviceCardCLIHelpAndLocale(t *testing.T) {
	for _, locale := range []string{"en", "ja"} {
		for _, args := range [][]string{{"card"}, {"card", "help"}, {"help", "card"}, {"card", "--help"}, {"card", "export", "--help"}, {"card", "inspect", "-h"}, {"--offline", "card", "--help"}} {
			var out bytes.Buffer
			if err := runWith(context.Background(), append([]string{"--locale", locale}, args...), &out, nil, cardNoCalls(t)); err != nil {
				t.Fatal(err)
			}
			want := "Read-only public device cards"
			if locale == "ja" {
				want = "読取り専用の公開端末カード"
			}
			if !strings.Contains(out.String(), want) || !strings.Contains(out.String(), "--include-endpoint") {
				t.Fatal("incomplete help")
			}
		}
	}
	for _, tc := range []struct{ env, override, want string }{{"ja_JP.UTF-8", "auto", "未検証・鮮度不明"}, {"ja_JP.UTF-8", "en", "unverified; freshness unknown"}, {"unknown_XX", "auto", "unverified; freshness unknown"}} {
		t.Setenv("LC_ALL", tc.env)
		var calls []webui.Command
		var out bytes.Buffer
		if err := runWith(context.Background(), []string{"--locale", tc.override, "card", "export", "--mode", "lan", "--name", "Alias"}, &out, nil, cardFixtureClient(t, &calls)); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), tc.want) || !strings.Contains(out.String(), "Alias") {
			t.Fatal("locale or explicit alias was lost")
		}
	}
}

func TestDeviceCardCLIInvalidArgumentsNeverLeakOrCall(t *testing.T) {
	const secret = "fictional-private-marker"
	cases := [][]string{
		{"card", secret}, {"card", "export"},
		{"card", "export", "--mode", "tailnet", "--name", secret},
		{"card", "export", "--mode", "lan"},
		{"card", "export", "--mode", "lan", "--name", " " + secret},
		{"card", "export", "--mode", "lan", "--name", secret + "\n"},
		{"card", "export", "--mode", "lan", "--name", secret + "\u202e"},
		{"card", "export", "--mode", "lan", "--name", strings.Repeat(secret, 10)},
		{"card", "export", "--mode", "lan", "--name", "Alias", "--qr=" + secret},
		{"card", "export", "--mode", "lan", "--name", "Alias", "--" + secret},
		{"card", "inspect", "--mode", "lan", secret},
		{"card", "inspect", "--mode", "lan", "--card", secret},
		{"card", "inspect", "--mode", "lan", "--json-file", secret},
		{"card", "inspect", "--mode", "lan"},
		{"card", "inspect", "--mode", "lan", "--file", secret, "--stdin"},
		{"card", "inspect", "--mode", "lan", "--file", secret},
		{"--offline", "card", "export", "--mode", "lan", "--name", "Alias"},
	}
	for _, args := range cases {
		for _, locale := range []string{"en", "ja"} {
			var out bytes.Buffer
			err := runWith(context.Background(), append([]string{"--locale", locale}, args...), &out, strings.NewReader(secret), cardNoCalls(t))
			if err == nil || out.Len() != 0 || strings.Contains(err.Error(), secret) {
				t.Fatal("invalid input accepted, echoed, or wrote human usage to stdout")
			}
		}
	}
}

func TestDeviceCardCLIRejectsMalformedInputBeforeCore(t *testing.T) {
	_, encoded := fictionalDeviceCard(t, "lan")
	for _, input := range []string{"", "fictional-private-marker", `{"invitation":"fictional-private-marker"}`, encoded + "junk", strings.Repeat(" ", devicecard.MaxInputBytes+1), encoded} {
		mode := "lan"
		if input == encoded {
			mode = "direct-lan"
		}
		var out bytes.Buffer
		err := runWith(context.Background(), []string{"card", "inspect", "--mode", mode, "--stdin"}, &out, strings.NewReader(input), cardNoCalls(t))
		if err == nil || strings.Contains(err.Error()+out.String(), "fictional-private-marker") {
			t.Fatal("malformed input accepted or leaked")
		}
	}
}

type countingCardReader struct{ count int }

func (r *countingCardReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = ' '
	}
	r.count += len(p)
	return len(p), nil
}

type failedCardReader struct{}

func (failedCardReader) Read([]byte) (int, error) { return 0, errors.New("fictional-private-marker") }

func TestDeviceCardCLIInputBoundsFilesAndCancellation(t *testing.T) {
	r := new(countingCardReader)
	if _, err := readDeviceCardInput(context.Background(), "", true, r, false); err == nil || r.count != devicecard.MaxInputBytes+1 {
		t.Fatal("unbounded read")
	}
	if _, err := readDeviceCardInput(context.Background(), "", true, failedCardReader{}, false); err == nil || strings.Contains(err.Error(), "fictional-private-marker") {
		t.Fatal("reader error leaked")
	}
	_, encoded := fictionalDeviceCard(t, "lan")
	dir := t.TempDir()
	path := filepath.Join(dir, "card.txt")
	if err := os.WriteFile(path, []byte(encoded+strings.Repeat(" ", devicecard.MaxInputBytes-len(encoded))), 0600); err != nil {
		t.Fatal(err)
	}
	input, err := readDeviceCardInput(context.Background(), path, false, nil, false)
	if err != nil || len(input) != devicecard.MaxInputBytes {
		t.Fatal("boundary input rejected")
	}
	if _, err := devicecard.Inspect(input, "lan"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Repeat(" ", devicecard.MaxInputBytes+1)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readDeviceCardInput(context.Background(), path, false, nil, false); err == nil {
		t.Fatal("oversized file accepted")
	}
	if _, err := readDeviceCardInput(context.Background(), dir, false, nil, false); err == nil {
		t.Fatal("directory accepted")
	}
	link := filepath.Join(dir, "symlink")
	if err := os.Symlink(path, link); err == nil {
		if _, err := readDeviceCardInput(context.Background(), link, false, nil, false); err == nil {
			t.Fatal("symlink accepted")
		}
	}
	reader, writer := io.Pipe()
	defer writer.Close()
	defer reader.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := readDeviceCardInput(ctx, "", true, reader, false); done <- err }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("cancellation lost")
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled read did not stop")
	}
}

func TestDeviceCardCLIDryRunHasNoProfileOrCoreAccess(t *testing.T) {
	_, encoded := fictionalDeviceCard(t, "lan")
	for _, args := range [][]string{{"card", "export", "--mode", "lan", "--name", "Alias", "--include-endpoint", "--qr"}, {"card", "inspect", "--mode", "lan", "--stdin"}} {
		dir := filepath.Join(t.TempDir(), "absent")
		var out bytes.Buffer
		if err := runWith(context.Background(), append([]string{"--state-dir", dir, "--dry-run"}, args...), &out, strings.NewReader(encoded), cardNoCalls(t)); err != nil {
			t.Fatal(err)
		}
		var preview struct {
			Applied    bool   `json:"applied"`
			Validation string `json:"validation"`
		}
		if err := json.Unmarshal(out.Bytes(), &preview); err != nil || preview.Applied || preview.Validation != "local-input-only" {
			t.Fatal("bad preview")
		}
		if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("dry run created profile")
		}
	}
}

func TestDeviceCardCLIRequestIDsAndPublicOnlyCalls(t *testing.T) {
	var calls []webui.Command
	client := cardFixtureClient(t, &calls)
	_, encoded := fictionalDeviceCard(t, "lan")
	for range 64 {
		for _, args := range [][]string{{"card", "export", "--mode", "lan", "--name", "Alias", "--json"}, {"card", "inspect", "--mode", "lan", "--stdin", "--json"}} {
			if err := runWith(context.Background(), args, io.Discard, strings.NewReader(encoded), client); err != nil {
				t.Fatal(err)
			}
		}
	}
	seen := map[string]bool{}
	for _, command := range calls {
		if !strings.HasPrefix(command.RequestID, "cli-card-") || len(strings.TrimPrefix(command.RequestID, "cli-card-")) < 26 || seen[command.RequestID] {
			t.Fatal("request ID was reused or lacks random suffix")
		}
		seen[command.RequestID] = true
	}
	if len(seen) != 128 {
		t.Fatal("unexpected calls")
	}
}

func TestDeviceCardCLIErrorSafetyLocalizationAndStableJSON(t *testing.T) {
	for _, code := range []string{"device_card_identity_required", "device_card_recovery_required", "device_card_hint_unavailable", "device_card_invalid", "unrecognized_error"} {
		var jsonOutputs []string
		for _, locale := range []string{"en", "ja"} {
			for _, machine := range []bool{false, true} {
				args := []string{"--locale", locale}
				if machine {
					args = append(args, "--json-errors")
				}
				args = append(args, "card", "export", "--mode", "lan", "--name", "Alias")
				var out bytes.Buffer
				client := func(context.Context, string, string, any) error {
					return &deviceCardCLIError{code, "fictional-private-marker"}
				}
				err := runWith(context.Background(), args, &out, nil, client)
				if err == nil {
					t.Fatal("error swallowed")
				}
				writeCommandError(&out, err)
				if strings.Contains(out.String(), "fictional-private-marker") {
					t.Fatal("remote error leaked")
				}
				if machine {
					jsonOutputs = append(jsonOutputs, out.String())
				}
			}
		}
		if jsonOutputs[0] != jsonOutputs[1] {
			t.Fatal("machine error was localized")
		}
	}
}

func TestDeviceCardCLIRejectsUnexpectedResponse(t *testing.T) {
	card, encoded := fictionalDeviceCard(t, "lan")
	valid := core.DeviceCardExportView{Text: encoded, Card: card, Verification: "unverified", Freshness: "unknown"}
	for _, mutate := range []func(*core.DeviceCardExportView){
		func(v *core.DeviceCardExportView) { v.Text = "fictional-private-marker" },
		func(v *core.DeviceCardExportView) { v.Name = "fictional-private-marker" },
		func(v *core.DeviceCardExportView) { v.Verification = "verified" },
		func(v *core.DeviceCardExportView) { v.Freshness = "fresh" },
		func(v *core.DeviceCardExportView) {
			v.Relay = &devicecard.RelayHint{Address: "192.0.2.10:54546", CertificateSHA256: strings.Repeat("b2", 32)}
			v.Text, _ = devicecard.Encode(v.Card)
		},
	} {
		view := valid
		mutate(&view)
		client := func(_ context.Context, _, _ string, target any) error {
			data, _ := json.Marshal(view)
			return json.Unmarshal(data, target)
		}
		var out bytes.Buffer
		err := runWith(context.Background(), []string{"card", "export", "--mode", "lan", "--name", card.Name, "--json"}, &out, nil, client)
		if err == nil || out.Len() != 0 || strings.Contains(err.Error(), "fictional-private-marker") {
			t.Fatal("unreviewed response escaped")
		}
	}
}

func TestDeviceCardCLIOfflineInspectionMatchesCodec(t *testing.T) {
	card, _ := fictionalDeviceCard(t, "direct-lan")
	card.Endpoint = "192.168.50.10:54546"
	encoded, err := devicecard.Encode(card)
	if err != nil {
		t.Fatal(err)
	}
	want, err := devicecard.Inspect(encoded, "direct-lan")
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runWith(context.Background(), []string{"--offline", "card", "inspect", "--mode", "direct-lan", "--stdin", "--json"}, &out, strings.NewReader(encoded), cardNoCalls(t)); err != nil {
		t.Fatal(err)
	}
	var got devicecard.Inspection
	if err := json.Unmarshal(out.Bytes(), &got); err != nil || !reflect.DeepEqual(want, got) {
		t.Fatal("codec output changed")
	}
}

type shortCardWriter struct{}

func (shortCardWriter) Write(p []byte) (int, error) { return len(p) - 1, nil }
func TestDeviceCardCLIHumanOutputErrors(t *testing.T) {
	card, encoded := fictionalDeviceCard(t, "lan")
	if err := writeDeviceCardHuman(shortCardWriter{}, card, "", encoded, false); !errors.Is(err, io.ErrShortWrite) {
		t.Fatal("short output was accepted")
	}
	if err := writeDeviceCardHuman(failedQRWriter{}, card, "", encoded, false); err == nil {
		t.Fatal("output error was ignored")
	}
}
