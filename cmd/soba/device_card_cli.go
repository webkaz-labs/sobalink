package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/webkaz-labs/sobalink/internal/core"
	"github.com/webkaz-labs/sobalink/internal/devicecard"
	"github.com/webkaz-labs/sobalink/internal/webui"
	"golang.org/x/term"
)

type deviceCardCLIError struct{ code, message string }

func (e *deviceCardCLIError) Error() string     { return e.message }
func (e *deviceCardCLIError) ErrorCode() string { return e.code }

func cardCLIError(ja bool, code string) error {
	messages := map[string][2]string{
		"device_card_invalid_options":   {"Invalid card options; use soba card --help. Inspect accepts only --file or --stdin, never card text as an argument", "カードの指定を確認してください。soba card --help を参照できます。内容確認には --file または --stdin を使い、カードを引数へ貼り付けないでください"},
		"device_card_mode_required":     {"Choose --mode lan or --mode direct-lan explicitly", "--mode lan または --mode direct-lan を明示してください"},
		"device_card_invalid":           {"Invalid device card; use canonical card text and an explicit alias of 1–80 UTF-8 bytes without outer whitespace, control or directional characters", "カードが正しくありません。正規形式のカードと、前後の空白・制御文字・方向制御を含まない1〜80 UTF-8 byteの表示名を明示してください"},
		"device_card_mode_mismatch":     {"Device card is for a different network mode; select its mode explicitly", "カードの方式が異なります。カードの方式を明示して選び直してください"},
		"device_card_too_large":         {"Device card input must be at most 1,040 bytes, including outer whitespace", "カード入力は外側の空白を含めて1,040 byte以内にしてください"},
		"device_card_input_required":    {"Choose exactly one of --file PATH or --stdin (pipe only)", "--file PATH または --stdin（パイプ入力）のどちらか一つを選んでください"},
		"device_card_input_unavailable": {"Could not read the card input; use a readable regular file or a pipe", "カード入力を読み込めません。読取り可能な通常ファイルまたはパイプを使ってください"},
		"device_card_identity_required": {"Configure this network's identity explicitly before exporting its device card", "カードを書き出す前に、この方式の端末IDを明示的に設定してください"},
		"device_card_recovery_required": {"Saved network identity requires recovery; review protected state before exporting a device card", "保存済みIDの復旧が必要です。カードを書き出す前に非公開の保存状態を確認してください"},
		"device_card_hint_unavailable":  {"No saved endpoint hint is available; export without --include-endpoint or configure the endpoint explicitly", "保存済みアドレス情報がありません。--include-endpoint なしで書き出すか、端点を明示的に設定してください"},
		"device_card_qr_failed":         {"QR generation failed; retry without --qr to export card text", "QRを生成できませんでした。--qr なしでカード文字列を書き出してください"},
		"device_card_core_required":     {"This command needs the running local Core with the same --state-dir. Inspection also supports soba --offline card inspect; offline export is unavailable", "この操作には同じ --state-dir のローカル本体の起動が必要です。内容確認には soba --offline card inspect も使えます。停止中の書出しには対応していません"},
		"device_card_response_invalid":  {"Unexpected card response; update the CLI and local Core to matching versions", "カードの応答が正しくありません。CLIとローカル本体を同じ版に更新してください"},
	}
	message, ok := messages[code]
	if !ok {
		code = "device_card_core_required"
		message = messages[code]
	}
	return &deviceCardCLIError{code, text(ja, message[0], message[1])}
}

func cardCodecCLIError(ja bool, err error) error {
	switch {
	case errors.Is(err, devicecard.ErrMode):
		return cardCLIError(ja, "device_card_mode_required")
	case errors.Is(err, devicecard.ErrModeMismatch):
		return cardCLIError(ja, "device_card_mode_mismatch")
	case errors.Is(err, devicecard.ErrTooLarge):
		return cardCLIError(ja, "device_card_too_large")
	default:
		return cardCLIError(ja, "device_card_invalid")
	}
}

// This dispatch precedes DefaultDir and all profile handling. Help and explicit
// offline inspection use neither a local Core nor any saved identity/config.
func deviceCardCLI(ctx context.Context, args []string, dir string, ja, jsonErrors, dryRun, offline bool, out io.Writer, stdin io.Reader, client controlCaller) error {
	errorJA := ja && !jsonErrors
	if len(args) == 0 || (len(args) == 1 && (args[0] == "help" || args[0] == "--help" || args[0] == "-h")) {
		_, err := fmt.Fprintln(out, text(ja, deviceCardHelpEN, deviceCardHelpJA))
		return err
	}
	action := args[0]
	if action != "export" && action != "inspect" {
		return cardCLIError(errorJA, "device_card_invalid_options")
	}
	f := flag.NewFlagSet("card "+action, flag.ContinueOnError)
	// flag's errors can echo arbitrary supplied values; never copy them to an
	// output stream or error, even when a private invitation is pasted by mistake.
	f.SetOutput(io.Discard)
	f.Usage = func() {}
	mode := f.String("mode", "", "")
	machine := f.Bool("json", false, "")
	var name, path string
	var hint, qr, pipe bool
	if action == "export" {
		f.StringVar(&name, "name", "", "")
		f.BoolVar(&hint, "include-endpoint", false, "")
		f.BoolVar(&qr, "qr", false, "")
	} else {
		f.StringVar(&path, "file", "", "")
		f.BoolVar(&pipe, "stdin", false, "")
	}
	if err := f.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(out, text(ja, deviceCardHelpEN, deviceCardHelpJA))
			if writeErr != nil {
				return writeErr
			}
			return err
		}
		return cardCLIError(errorJA, "device_card_invalid_options")
	}
	if f.NArg() != 0 {
		return cardCLIError(errorJA, "device_card_invalid_options")
	}
	if !devicecard.ValidMode(*mode) {
		return cardCLIError(errorJA, "device_card_mode_required")
	}
	writeJSON := func(value any) error {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(value)
	}
	var payload any
	var inspection devicecard.Inspection
	if action == "export" {
		// Validate the alias with the same pure codec; this placeholder is never
		// exported, sent to Core or used as an identity.
		validation := devicecard.Card{Version: 1, Mode: *mode, Name: name, PublicKey: strings.Repeat("1", 64)}
		if err := validation.Validate(); err != nil {
			return cardCodecCLIError(errorJA, err)
		}
		if offline {
			return cardCLIError(errorJA, "device_card_core_required")
		}
		payload = struct {
			Mode                string `json:"mode"`
			Name                string `json:"name"`
			IncludeEndpointHint bool   `json:"includeEndpointHint,omitempty"`
			QR                  bool   `json:"qr,omitempty"`
		}{*mode, name, hint, qr}
	} else {
		input, err := readDeviceCardInput(ctx, path, pipe, stdin, errorJA)
		if err != nil {
			return err
		}
		inspection, err = devicecard.Inspect(input, *mode)
		if err != nil {
			return cardCodecCLIError(errorJA, err)
		}
		payload = struct {
			Card         string `json:"card"`
			ExpectedMode string `json:"expectedMode"`
		}{input, *mode}
	}
	if dryRun {
		return writeJSON(map[string]any{"applied": false, "command": "device-card." + action, "payload": payload, "validation": "local-input-only"})
	}
	if offline {
		if *machine {
			return writeJSON(inspection)
		}
		return writeDeviceCardHuman(out, inspection.Card, inspection.ContentDigest, "", ja)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if dir == "" {
		var err error
		dir, err = core.DefaultDir()
		if err != nil {
			return cardCLIError(errorJA, "device_card_core_required")
		}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return cardCLIError(errorJA, "device_card_invalid")
	}
	request, err := json.Marshal(webui.Command{RequestID: newCLIRequestID("cli-card"), Name: "device-card." + action, Payload: raw})
	if err != nil {
		return cardCLIError(errorJA, "device_card_invalid")
	}
	query := func(target any) error {
		if err := client(ctx, dir, string(request), target); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			var coded interface{ ErrorCode() string }
			if errors.As(err, &coded) {
				return cardCLIError(errorJA, coded.ErrorCode())
			}
			// IPC errors may contain private profile paths or arbitrary remote
			// text. Give a stable safe action instead of interpolating the cause.
			return cardCLIError(errorJA, "device_card_core_required")
		}
		return nil
	}
	if action == "inspect" {
		var result devicecard.Inspection
		if err := query(&result); err != nil {
			return err
		}
		want, _ := json.Marshal(inspection)
		got, _ := json.Marshal(result)
		if string(got) != string(want) {
			return cardCLIError(errorJA, "device_card_response_invalid")
		}
		if *machine {
			return writeJSON(result)
		}
		return writeDeviceCardHuman(out, result.Card, result.ContentDigest, "", ja)
	}
	var result core.DeviceCardExportView
	if err := query(&result); err != nil {
		return err
	}
	encoded, err := devicecard.Encode(result.Card)
	if err != nil || encoded != result.Text || result.Mode != *mode || result.Name != name || result.Verification != "unverified" || result.Freshness != "unknown" || (!hint && (result.Endpoint != "" || result.Relay != nil)) || (hint && result.Endpoint == "" && result.Relay == nil) || (!qr && len(result.QR) != 0) || (qr && !validDeviceCardBitmap(result.QR)) {
		return cardCLIError(errorJA, "device_card_response_invalid")
	}
	if *machine {
		return writeJSON(result)
	}
	if err := writeDeviceCardHuman(out, result.Card, "", result.Text, ja); err != nil {
		return err
	}
	if qr {
		return writeDeviceCardQR(out, result.QR, ja)
	}
	return nil
}

func readDeviceCardInput(ctx context.Context, path string, pipe bool, in io.Reader, ja bool) (string, error) {
	if (path == "") == !pipe {
		return "", cardCLIError(ja, "device_card_input_required")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if path != "" {
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			return "", cardCLIError(ja, "device_card_input_unavailable")
		}
		if info.Size() > devicecard.MaxInputBytes {
			return "", cardCLIError(ja, "device_card_too_large")
		}
		file, err := os.Open(path)
		if err != nil {
			return "", cardCLIError(ja, "device_card_input_unavailable")
		}
		defer file.Close()
		opened, err := file.Stat()
		if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
			return "", cardCLIError(ja, "device_card_input_unavailable")
		}
		in = file
	} else if file, ok := in.(*os.File); ok && term.IsTerminal(int(file.Fd())) {
		return "", cardCLIError(ja, "device_card_input_required")
	}
	if in == nil {
		return "", cardCLIError(ja, "device_card_input_unavailable")
	}
	stop := func() bool { return false }
	if closer, ok := in.(io.Closer); ok {
		stop = context.AfterFunc(ctx, func() { _ = closer.Close() })
	}
	defer stop()
	// Bound the read before parsing, including whitespace and malformed input.
	data, err := io.ReadAll(io.LimitReader(in, devicecard.MaxInputBytes+1))
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err != nil {
		return "", cardCLIError(ja, "device_card_input_unavailable")
	}
	if len(data) > devicecard.MaxInputBytes {
		return "", cardCLIError(ja, "device_card_too_large")
	}
	return string(data), nil
}

func writeDeviceCardHuman(out io.Writer, card devicecard.Card, digest, encoded string, ja bool) error {
	var b strings.Builder
	fmt.Fprintln(&b, text(ja, "Public device card — unverified; freshness unknown", "公開端末カード — 未検証・鮮度不明"))
	fmt.Fprintf(&b, text(ja, "Mode: %s\nAlias: %s\nPublic key: %s\n", "方式: %s\n表示名: %s\n公開鍵: %s\n"), card.Mode, card.Name, card.PublicKey)
	if card.Endpoint != "" {
		fmt.Fprintf(&b, text(ja, "Endpoint hint: %s\n", "端点の参考情報: %s\n"), card.Endpoint)
	}
	if card.Relay != nil {
		fmt.Fprintf(&b, text(ja, "Relay hint: %s\nCertificate SHA-256 hint: %s\n", "中継の参考情報: %s\n証明書SHA-256の参考情報: %s\n"), card.Relay.Address, card.Relay.CertificateSHA256)
	}
	if digest != "" {
		fmt.Fprintf(&b, text(ja, "Content digest: %s\n", "内容のdigest: %s\n"), digest)
	}
	fmt.Fprintln(&b, text(ja, "No identity, ownership, reachability or access is verified. Review privately before any separate invitation or trust action.", "本人・所有者・到達可否・利用許可を確認したものではありません。招待や信頼を別操作で行う前に、非公開の方法で内容を確認してください。"))
	if encoded != "" {
		fmt.Fprintln(&b, text(ja, "Card text (copy only the following line):", "カード文字列（次の1行だけをコピー）:"))
		fmt.Fprintln(&b, encoded)
	}
	n, err := io.WriteString(out, b.String())
	if err == nil && n != b.Len() {
		return io.ErrShortWrite
	}
	return err
}

const deviceCardHelpEN = `Read-only public device cards

  soba card export --mode lan|direct-lan --name ALIAS [--include-endpoint] [--qr] [--json]
  soba card inspect --mode lan|direct-lan --file PATH [--json]
  soba card inspect --mode lan|direct-lan --stdin [--json]
  soba --offline card inspect --mode lan|direct-lan --file PATH [--json]

Choose an explicit alias; the configured hostname is never copied. Address hints
are omitted unless --include-endpoint is given. Public keys can identify the same
device across copies; exchange cards through a suitable private channel.
Export and normal inspection require a running local Core with the same
--state-dir. Export reads an existing identity, never creates or repairs one.
--offline inspection uses only the supplied text, without reading local profiles,
contacting a Core or writing state. Offline export is unavailable.
Inspect accepts raw card text from one regular file or pipe, at most 1,040 bytes
including whitespace. Never pass card text or a private invitation as an argument.
--qr adds a local terminal QR (or a bitmap with --json); text remains available
when the terminal is narrow or cannot display QR. --json is stable in all locales.
--dry-run validates input and previews a read request without contacting Core.
Cards remain unverified, with unknown freshness. Inspection makes no connection,
changes no endpoint and grants no permission. Pairing and trust are separate.`

const deviceCardHelpJA = `読取り専用の公開端末カード

  soba card export --mode lan|direct-lan --name ALIAS [--include-endpoint] [--qr] [--json]
  soba card inspect --mode lan|direct-lan --file PATH [--json]
  soba card inspect --mode lan|direct-lan --stdin [--json]
  soba --offline card inspect --mode lan|direct-lan --file PATH [--json]

表示名を明示してください。設定済みの端末名は自動で使いません。アドレス情報は
--include-endpoint を指定した場合だけ含めます。公開鍵は同じ端末を識別する
手掛かりになるため、適切な非公開の方法でカードを交換してください。
書出しと通常の内容確認には、同じ --state-dir のローカル本体の起動が必要です。
書出しは既存IDを読むだけで、作成・修復は行いません。
--offline の内容確認は入力文字列だけを扱い、保存済みプロファイルの読取り・
本体への接続・状態の書込みを行いません。停止中の書出しには対応していません。
内容確認には通常ファイルまたはパイプからカード文字列を1つ入力します。
外側の空白を含めて1,040 byte以内です。カードや機密の招待を引数へ貼り付けないでください。
--qr は端末用QR（--json ではbitmap）をローカル生成します。幅や表示機能が
足りない端末でも文字列を使えます。--json は全言語で同じ項目です。
--dry-run は入力を検証し、本体へ接続せず読取り要求を表示します。
カードは未検証・鮮度不明です。内容確認で接続・端点更新・利用許可は行いません。
ペアリングと信頼は別操作です。`
