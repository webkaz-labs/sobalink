package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/resource"
	"github.com/webkaz-labs/sobalink/internal/webui"
)

const resourceHelpEN = `Local transfer settings resource (source-build preview)

  soba resource list [--json]
  soba resource inspect --id RESOURCE_ID [--json]
  soba resource preview --id RESOURCE_ID --concurrent-files default|N --concurrent-per-peer default|N [--json]

Start the local agent first. Use the opaque ID from resource list.
Both choices are required for preview: default or a positive finite integer.
Output is stable JSON in either language. Preview changes no settings.
Only list, inspect and preview are available; apply and operation status are not implemented.
There is no remote management endpoint or grant. --offline is unsupported.
Global --dry-run validates inputs and prints the request without contacting the agent;
it does not validate the current resource or produce an authoritative review.`

const resourceHelpJA = `ローカル転送設定リソース（ソースビルドの確認機能）

  soba resource list [--json]
  soba resource inspect --id RESOURCE_ID [--json]
  soba resource preview --id RESOURCE_ID --concurrent-files default|N --concurrent-per-peer default|N [--json]

先にローカルの本体を起動し、resource list が返す不透明な ID を使ってください。
preview には両方の項目が必要です。default または正の有限整数を指定します。
出力は言語によらず安定した JSON です。preview は設定を変更しません。
一覧・内容確認・変更案の確認のみ対応し、適用と操作結果の照会は未実装です。
遠隔管理の接続口や許可はありません。--offline は使えません。
共通オプション --dry-run は本体に接続せず、入力検査と要求の表示だけを行います。
現在のリソースを検査したり、正式な変更確認を作成したりはしません。`

func resourceCLI(ctx context.Context, args []string, dir string, ja, dryRun bool, out io.Writer, client controlCaller) error {
	if len(args) == 0 || len(args) == 1 && (args[0] == "help" || args[0] == "--help" || args[0] == "-h") {
		_, err := fmt.Fprintln(out, text(ja, resourceHelpEN, resourceHelpJA))
		return err
	}
	action := args[0]
	if action != "list" && action != "inspect" && action != "preview" {
		return usageError(ja, "resource list|inspect|preview --help")
	}
	if len(args) == 2 && (args[1] == "--help" || args[1] == "-h") {
		_, err := fmt.Fprintln(out, text(ja, resourceHelpEN, resourceHelpJA))
		return err
	}
	f := commandFlags("resource "+action, ja, out)
	f.Bool("json", false, text(ja, "stable JSON output (also the default)", "機械処理用の安定した JSON 出力（既定）"))
	var id, files, peers string
	if action != "list" {
		f.StringVar(&id, "id", "", text(ja, "exact resource ID from resource list", "resource list が返した正確なリソース ID"))
	}
	if action == "preview" {
		f.StringVar(&files, "concurrent-files", "", text(ja, "concurrent files: default or positive integer", "同時転送ファイル数: default または正の整数"))
		f.StringVar(&peers, "concurrent-per-peer", "", text(ja, "concurrent files per peer: default or positive integer", "相手ごとの同時転送数: default または正の整数"))
	}
	if err := parseFlags(f, args[1:], ja); err != nil {
		return err
	}
	var payload any = struct{}{}
	if action != "list" {
		if !resource.ValidID(id) {
			return errors.New(text(ja, "Use the exact 32-character lowercase resource ID from resource list", "resource list が返した小文字32文字の正確なリソース ID を指定してください"))
		}
		target := resource.Target{SchemaVersion: resource.SchemaVersion, ResourceID: id}
		if action == "inspect" {
			payload = target
		} else {
			fc, err := resourceCLIChoice(files, ja)
			if err != nil {
				return err
			}
			pc, err := resourceCLIChoice(peers, ja)
			if err != nil {
				return err
			}
			payload = resource.PreviewRequest{Target: target, Settings: resource.Settings{TransferConcurrentFiles: fc, TransferConcurrentPerPeer: pc}}
		}
	}
	write := func(v any) error { e := json.NewEncoder(out); e.SetIndent("", "  "); return e.Encode(v) }
	name := "resource." + action
	if dryRun {
		return write(map[string]any{"applied": false, "command": name, "payload": payload, "validation": "local-input-only"})
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	request, err := json.Marshal(webui.Command{RequestID: newCLIRequestID("cli-resource"), Name: name, Payload: raw})
	if err != nil {
		return err
	}
	var result json.RawMessage
	if err := client(ctx, dir, string(request), &result); err != nil {
		return resourceControlError(err)
	}
	return write(result)
}

func resourceCLIChoice(value string, ja bool) (capacity.Choice, error) {
	if value == "default" {
		return capacity.Default(), nil
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || n < 1 || n > capacity.MaxJSONInteger {
		return capacity.Choice{}, errors.New(text(ja, "Both transfer choices must be default or a positive finite integer", "転送の両項目に default または正の有限整数を指定してください"))
	}
	return capacity.Limited(n), nil
}

// Preserve the server error code through Unwrap; machine errors remain unchanged.
func localizeResourceError(ja bool, err error) error {
	if !ja {
		return err
	}
	var coded interface{ ErrorCode() string }
	if !errors.As(err, &coded) {
		return err
	}
	messages := map[string]string{
		"resource_control_unavailable": "ローカルリソースの要求を完了できません。本体の起動状態を確認して再試行してください",
		"resource_unavailable":         "ローカルリソースを利用できません。非公開の保存状態を確認して本体を再起動してください。ID を復元する目的で保存ファイルを削除しないでください",
		"resource_invalid":             "リソースの対象・形式・設定が正しくありません。resource list で ID を確認し、変更案には転送の両項目を指定してください",
		"resource_not_found":           "指定したローカルリソースが見つかりません。resource list で現在の ID を確認してください",
	}
	if message := messages[coded.ErrorCode()]; message != "" {
		return &localizedDiskSpaceError{err, message}
	}
	return err
}

// Local control failures can contain private filenames or socket addresses.
// Project only stable resource codes and fixed text, even in machine output.
// Unwrap retains failure identity for callers; it is never serialized here.
type resourceCLIError struct {
	code, message string
	cause         error
}

func (e *resourceCLIError) Error() string     { return e.message }
func (e *resourceCLIError) ErrorCode() string { return e.code }
func (e *resourceCLIError) Unwrap() error     { return e.cause }
func resourceControlError(err error) error {
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	code := "resource_control_unavailable"
	message := "local resource request could not be completed; check that the agent is running and retry"
	var coded interface{ ErrorCode() string }
	if errors.As(err, &coded) {
		switch coded.ErrorCode() {
		case "resource_unavailable":
			code, message = coded.ErrorCode(), "local resource identity is unavailable; check private state and restart its owning agent; do not delete state to restore an identity"
		case "resource_invalid":
			code, message = coded.ErrorCode(), "invalid resource target, schema or settings; inspect the local resource and provide both transfer choices"
		case "resource_not_found":
			code, message = coded.ErrorCode(), "local resource was not found; list the current resources"
		}
	}
	return &resourceCLIError{code: code, message: message, cause: err}
}
