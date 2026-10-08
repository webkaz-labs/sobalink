package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Dedicated management presentation. All mutations still go through Core's
// exact process/file/proof review; a local preview is never authority itself.
func directLANEndpointCLI(ctx context.Context, args []string, ja bool, out io.Writer, stdin io.Reader, dryRun bool, query commandQuery, request actionRequest) (retErr error) {
	defer func() { retErr = localizeEndpointCLIError(ja, retErr) }()
	if len(args) == 0 || args[0] == "--help" || args[0] == "help" {
		_, err := fmt.Fprintln(out, text(ja, endpointHelpEN, endpointHelpJA))
		return err
	}
	op := args[0]
	commands := map[string][2]string{
		"move": {"move.preview", "move.apply"}, "export": {"export.preview", "export"}, "reexport": {"reexport.preview", "reexport"}, "delivery": {"delivery.preview", "delivery.apply"},
		"import": {"inspect", "accept"}, "reapprove": {"inspect", "reapprove-current"}, "follow": {"follow.preview", "follow.apply"}, "disable-follow": {"follow.preview", "follow.apply"}, "revoke": {"inspect", "revoke"}, "recover": {"recovery.inspect", "recovery.apply"}, "status": {"status", "status"},
	}
	names, ok := commands[op]
	if !ok {
		return usageError(ja, "direct-lan endpoint --help")
	}
	f := commandFlags("direct-lan endpoint "+op, ja, out)
	peer := f.String("peer", "", text(ja, "exact paired public identity", "ペアリング済みの正確な公開ID"))
	endpoint := f.String("endpoint", "", text(ja, "new exact numeric local endpoint", "新しい正確な数値IP接続先"))
	lifetime := f.String("lifetime", "", text(ja, "finite or until-revoked (explicit)", "finite または until-revoked（明示指定）"))
	expires := f.String("expires", "", text(ja, "exact RFC3339 expiry", "正確なRFC3339期限"))
	granted := f.String("granted", "", text(ja, "exact RFC3339 approval start; never renewed automatically", "正確なRFC3339承認開始日時（自動更新なし）"))
	operation := f.String("operation", "set", text(ja, "set or withdraw", "set または withdraw"))
	apply := f.Bool("apply", false, text(ja, "apply the exact reviewed choices", "確認済みの正確な選択を適用"))
	revision := f.String("review", "", text(ja, "revision from the matching preview", "一致する事前確認のリビジョン"))
	approve := f.Bool("approve", false, text(ja, "approve this exact imported endpoint", "取り込む正確な接続先を承認"))
	file := f.String("json-file", "", text(ja, "private signed-update response file", "非公開の署名付き更新応答ファイル"))
	useStdin := f.Bool("stdin", false, text(ja, "read private signed-update JSON from stdin", "標準入力から非公開の署名付き更新JSONを読む"))
	transaction := f.String("transaction", "", text(ja, "exact pending recovery transaction", "正確な保留中復旧トランザクション"))
	cancel := f.Bool("cancel", false, text(ja, "review cancellation where supported", "対応している場合に取り消しを確認"))
	structured := f.Bool("json", false, text(ja, "stable machine JSON", "安定した機械用JSON"))
	var deliveries policyPrefixes
	f.Var(&deliveries, "to", text(ja, "move: explicitly deliver to this peer, repeatable", "移動：この相手に明示的に配送（複数可）"))
	if err := parseFlags(f, args[1:], ja); err != nil {
		return err
	}
	allowed := map[string]string{"status": "json", "move": "endpoint to lifetime expires", "export": "peer operation lifetime expires", "reexport": "peer", "delivery": "peer", "import": "peer json-file stdin approve granted lifetime expires", "reapprove": "peer granted lifetime expires", "follow": "peer granted lifetime expires", "disable-follow": "peer", "revoke": "peer", "recover": "transaction cancel"}
	var unsupported bool
	f.Visit(func(flag *flag.Flag) {
		if !strings.Contains(" "+allowed[op]+" ", " "+flag.Name+" ") && !(op != "status" && strings.Contains(" apply review json ", " "+flag.Name+" ")) {
			unsupported = true
		}
	})
	if unsupported {
		return usageError(ja, "direct-lan endpoint "+op+" --help")
	}
	if *apply != (*revision != "") {
		return errors.New(text(ja, "Use --apply and --review together; preview first, then keep every choice unchanged", "先に確認し、すべての選択を維持して --apply と --review を一緒に指定してください"))
	}
	payload := map[string]any{}
	if op != "move" && op != "recover" && op != "status" {
		if *peer == "" {
			return usageError(ja, "direct-lan endpoint "+op+" --peer ID")
		}
		payload["peerId"] = *peer
	}
	lifetimeInput := op == "export" || op == "follow" || op == "reapprove" || op == "import" && *approve || op == "move" && len(deliveries) > 0
	if !lifetimeInput && (*lifetime != "" || *expires != "" || *granted != "") {
		return errors.New(text(ja, "Lifetime flags require an explicit export, delivery target or approval", "有効期間の引数には明示的な出力・配送相手・承認が必要です"))
	}
	if dryRun && (op == "follow" || op == "reapprove" || op == "import" && *approve) {
		return errors.New(text(ja, "This approval preview needs current read-only state; omit --dry-run. No change is applied without --apply --review", "この承認の事前確認には現在の状態の読み取りが必要です。--dry-run を外してください。--apply --review なしでは変更しません"))
	}
	if lifetimeInput {
		if *lifetime != "finite" && *lifetime != "until-revoked" {
			return errors.New(text(ja, "Choose --lifetime finite or until-revoked explicitly", "--lifetime finite または until-revoked を明示してください"))
		}
		if *lifetime == "finite" {
			deadline, err := time.Parse(time.RFC3339Nano, *expires)
			if err != nil || !deadline.After(time.Now()) {
				return errors.New(text(ja, "Choose a future exact --expires RFC3339 value", "将来の正確な --expires RFC3339日時を指定してください"))
			}
		} else if *expires != "" {
			return errors.New(text(ja, "until-revoked must not include --expires", "until-revoked に --expires は指定できません"))
		}
	}
	if op == "follow" || op == "reapprove" || op == "import" && *approve {
		if _, err := time.Parse(time.RFC3339Nano, *granted); err != nil {
			return errors.New(text(ja, "Supply the same exact --granted RFC3339 value in preview and apply", "確認と適用で同じ正確な --granted RFC3339日時を指定してください"))
		}
	}
	switch op {
	case "move":
		if *endpoint == "" {
			return usageError(ja, "direct-lan endpoint move --endpoint IP:PORT")
		}
		payload["endpoint"] = *endpoint
		targets := make([]map[string]string, 0, len(deliveries))
		seen := map[string]bool{}
		for _, id := range deliveries {
			if seen[id] {
				return errors.New(text(ja, "Duplicate delivery peer", "配送相手が重複しています"))
			}
			seen[id] = true
			targets = append(targets, map[string]string{"peerId": id, "lifetime": *lifetime, "expires": *expires})
		}
		payload["deliveries"] = targets
	case "export":
		payload["operation"], payload["lifetime"], payload["expires"] = *operation, *lifetime, *expires
	case "import":
		inputArgs := []string{"direct-lan.endpoint.inspect"}
		if *useStdin && *file == "" {
			inputArgs = append(inputArgs, "--stdin")
		} else if !*useStdin && *file != "" {
			inputArgs = append(inputArgs, "--json-file", *file)
		} else {
			return errors.New(text(ja, "Use one private --json-file or --stdin; never paste a signed proof as an argument", "非公開の --json-file または --stdin の一方を使い、署名を引数に貼り付けないでください"))
		}
		raw, err := commandPayload(ctx, inputArgs, stdin, ja)
		if err != nil {
			return err
		}
		var value struct {
			Update string `json:"update"`
		}
		if json.Unmarshal(raw, &value) != nil || value.Update == "" {
			return errors.New(text(ja, "Expected a signed-update JSON response with an update field", "update フィールドを持つ署名付き更新JSON応答が必要です"))
		}
		payload["update"], payload["action"] = value.Update, "receive"
		if *approve {
			var proof map[string]any
			if err := query("direct-lan.endpoint.inspect", payload, &proof); err != nil {
				return err
			}
			approval, err := endpointCLIApproval(proof, "endpoint", *granted, *lifetime, *expires)
			if err != nil {
				return err
			}
			payload["approval"] = approval
		}
	case "reapprove", "follow":
		var status struct {
			Peers []map[string]any `json:"peers"`
		}
		if err := query("direct-lan.endpoint.status", map[string]any{}, &status); err != nil {
			return err
		}
		var saved map[string]any
		for _, p := range status.Peers {
			if p["peerId"] == *peer {
				saved = p
				break
			}
		}
		if saved == nil {
			return errors.New(text(ja, "Refresh endpoint status and select an existing peer", "状態を更新し、存在する相手を選択してください"))
		}
		if op == "reapprove" {
			approval, err := endpointCLIApproval(saved, "signedEndpoint", *granted, *lifetime, *expires)
			if err != nil {
				return err
			}
			payload["action"], payload["approval"] = "reapprove", approval
		} else {
			scope, _ := saved["scopeDigest"].(string)
			rev, _ := saved["authorityRevision"].(string)
			n, err := strconv.ParseUint(rev, 10, 64)
			if err != nil || n == ^uint64(0) || scope == "" {
				return errors.New(text(ja, "Current follow scope/revision is unavailable", "現在の追従範囲・リビジョンを取得できません"))
			}
			payload["action"], payload["follow"] = "grant-follow", map[string]any{"scope_digest": scope, "revision": strconv.FormatUint(n+1, 10), "granted": *granted, "lifetime": *lifetime, "expires": *expires, "active": true}
		}
	case "disable-follow", "revoke":
		payload["action"] = op
	case "recover":
		if *transaction == "" {
			return usageError(ja, "direct-lan endpoint recover --transaction ID")
		}
		payload["transactionId"], payload["cancel"] = *transaction, *cancel
	}
	name := "direct-lan.endpoint." + names[0]
	if *apply {
		name = "direct-lan.endpoint." + names[1]
		payload["expectedRevision"] = *revision
	}
	if dryRun || *structured {
		return request(name, payload)
	}
	var result map[string]any
	if err := query(name, payload, &result); err != nil {
		return err
	}
	fmt.Fprintln(out, text(ja, "Endpoint review / result (saved, active and delivery are separate):", "接続先の確認・結果（保存・稼働・配送は別の状態です）："))
	writeEndpointCLIFields(out, ja, result, "")
	if op != "status" && !*apply {
		fmt.Fprintln(out, text(ja, "Review exact targets, scope and lifetime. To apply, repeat identical choices with --apply --review REVISION. Back/cancel: do not apply.", "対象・範囲・有効期間を確認してください。適用するには同じ選択に --apply --review REVISION を追加します。戻る・中止する場合は適用しないでください。"))
	}
	fmt.Fprintln(out, text(ja, "A move may interrupt existing TCP connections. An unconfirmed delivery is not success; refresh status before an explicit reviewed retry. No automatic scope/lifetime renewal or route fallback.", "移動時は既存のTCP接続が切れる場合があります。配送未確認は成功ではありません。状態を更新し、明示的に確認してから再送してください。範囲・期限の自動更新や別経路への切り替えは行いません。"))
	return nil
}
func endpointCLIApproval(source map[string]any, endpointKey, granted, lifetime, expires string) (map[string]string, error) {
	digest, _ := source["proofDigest"].(string)
	endpoint, _ := source[endpointKey].(string)
	if digest == "" || endpoint == "" {
		return nil, errors.New("endpoint proof must be inspected before exact approval")
	}
	return map[string]string{"kind": "exact", "proof_digest": digest, "endpoint": endpoint, "follow_revision": "", "granted": granted, "lifetime": lifetime, "expires": expires}, nil
}
func writeEndpointCLIFields(out io.Writer, ja bool, fields map[string]any, indent string) {
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := fields[key]
		switch v := value.(type) {
		case map[string]any:
			fmt.Fprintf(out, "%s%s:\n", indent, endpointCLIField(ja, key))
			writeEndpointCLIFields(out, ja, v, indent+"  ")
		case []any:
			fmt.Fprintf(out, "%s%s:\n", indent, endpointCLIField(ja, key))
			for _, item := range v {
				if m, ok := item.(map[string]any); ok {
					writeEndpointCLIFields(out, ja, m, indent+"  ")
				} else {
					fmt.Fprintf(out, "%s  %s\n", indent, displayText(fmt.Sprint(item)))
				}
			}
		default:
			fmt.Fprintf(out, "%s%s: %s\n", indent, endpointCLIField(ja, key), displayText(endpointCLIState(ja, fmt.Sprint(value))))
		}
	}
}

const endpointHelpEN = `Signed endpoint management (preview by default):
  soba direct-lan endpoint status [--json]
  soba direct-lan endpoint move --endpoint IP:PORT [--to PEER ... --lifetime finite --expires RFC3339]
  soba direct-lan endpoint export --peer ID --operation set|withdraw --lifetime finite --expires RFC3339
  soba direct-lan endpoint reexport|delivery --peer ID
  soba direct-lan endpoint import --peer ID --json-file PRIVATE_FILE [--approve --granted RFC3339 --lifetime finite --expires RFC3339]
  soba direct-lan endpoint reapprove|follow --peer ID --granted RFC3339 --lifetime finite --expires RFC3339
  soba direct-lan endpoint disable-follow|revoke --peer ID
  soba direct-lan endpoint recover --transaction ID [--cancel]
Use --stdin instead of --json-file. For no expiry explicitly use --lifetime until-revoked without --expires.
Apply only after reviewing: repeat identical flags plus --apply --review REVISION. --json keeps machine output stable.
Move without --to sends nothing. Delivery retries the identical existing proof without renewing its lifetime.
Recovery is offline only. Saved is not active; reconnecting is temporary interruption; unconfirmed is unknown delivery.`
const endpointHelpJA = `署名付き接続先の管理（通常は事前確認のみ）：
  soba direct-lan endpoint status [--json]
  soba direct-lan endpoint move --endpoint IP:PORT [--to PEER ... --lifetime finite --expires RFC3339]
  soba direct-lan endpoint export --peer ID --operation set|withdraw --lifetime finite --expires RFC3339
  soba direct-lan endpoint reexport|delivery --peer ID
  soba direct-lan endpoint import --peer ID --json-file PRIVATE_FILE [--approve --granted RFC3339 --lifetime finite --expires RFC3339]
  soba direct-lan endpoint reapprove|follow --peer ID --granted RFC3339 --lifetime finite --expires RFC3339
  soba direct-lan endpoint disable-follow|revoke --peer ID
  soba direct-lan endpoint recover --transaction ID [--cancel]
--json-file の代わりに --stdin を使用できます。期限なしは --expires を付けず --lifetime until-revoked を明示します。
確認後、同じ引数に --apply --review REVISION を追加して適用します。--json は安定した機械用出力です。
--to のない移動では送信しません。再送は元の署名を使い、期限を更新しません。
復旧はオフラインのみです。保存と稼働は別、再接続中は一時中断、配送未確認は結果不明です。`

func localizeEndpointCLIError(ja bool, err error) error {
	if !ja || err == nil {
		return err
	}
	var coded interface{ ErrorCode() string }
	if !errors.As(err, &coded) {
		return err
	}
	messages := map[string]string{
		"direct_lan_endpoint_review_changed":   "確認後に接続先の状態が変わりました。状態を更新し、正確な選択を再確認してください。",
		"direct_lan_endpoint_review_required":  "適用前に、この正確な署名と明示的な承認の有効期間を確認してください。",
		"direct_lan_endpoint_invalid":          "接続先の選択が無効です。相手・署名・範囲・正確な有効期間を確認してください。",
		"direct_lan_endpoint_context_required": "このペアには一致する接続先コンテキストの確認が必要です。先に署名付きペアのアップグレードを確認してください。",
		"direct_lan_endpoint_stale":            "この署名は保存済みの連番より古いものです。現在の署名付き更新を取得してください。",
		"direct_lan_endpoint_conflict":         "署名の連番が保存済みの状態と競合しています。状態を保持してペアを確認してください。",
		"direct_lan_endpoint_expired":          "署名または承認の期限が切れました。現在の署名を確認し、必要な場合に新しい承認を明示してください。",
		"direct_lan_endpoint_identity":         "署名付き更新が選択したペアのID・コンテキストと一致しません。",
	}
	if message := messages[coded.ErrorCode()]; message != "" {
		return &localizedDiskSpaceError{err, message}
	}
	return err
}

func endpointCLIField(ja bool, key string) string {
	labels := map[string][2]string{"revision": {"Review revision", "確認リビジョン"}, "peerId": {"Peer identity", "相手のID"}, "endpoint": {"Endpoint", "接続先"}, "signedEndpoint": {"Signed proposal endpoint", "署名された提案接続先"}, "previousEndpoint": {"Previous endpoint", "変更前の接続先"}, "destination": {"Delivery destination", "配送先"}, "destinations": {"Delivery destinations", "配送先一覧"}, "deliveries": {"Explicit deliveries", "明示的な配送"}, "scope": {"Unchanged scope", "変更しない許可範囲"}, "recipientScope": {"Recipient scope", "相手の許可範囲"}, "expires": {"Exact expiry", "正確な期限"}, "granted": {"Approval start", "承認開始日時"}, "lifetime": {"Lifetime", "有効期間"}, "approval": {"Exact endpoint approval", "接続先の承認"}, "follow": {"Following approval", "追従の承認"}, "active": {"Active", "稼働"}, "saved": {"Durably saved", "永続保存済み"}, "state": {"State", "状態"}, "outcome": {"Outcome", "結果"}, "proofDigest": {"Proof digest", "署名のダイジェスト"}, "recoveryRequired": {"Recovery required", "復旧が必要"}, "cleanupPending": {"Cleanup unresolved", "終了処理が未解決"}, "transactionId": {"Transaction", "トランザクション"}, "update": {"Private signed update", "非公開の署名付き更新"}}
	if label, ok := labels[key]; ok {
		return text(ja, label[0], label[1])
	}
	return key
}

func endpointCLIState(ja bool, value string) string {
	labels := map[string][2]string{"active": {"Active transport", "通信が稼働中"}, "reconnecting": {"Reconnecting: temporary interruption", "再接続中：通信は一時中断"}, "saved_unavailable": {"Saved but unavailable", "保存済み・利用不可"}, "recovery_required": {"Recovery required", "復旧が必要"}, "saved_only": {"Saved only; active transport unconfirmed", "保存のみ・通信の稼働は未確認"}, "unconfirmed": {"Delivery unconfirmed", "配送未確認"}, "review_required": {"Approval review required", "承認の確認が必要"}}
	if label, ok := labels[value]; ok {
		return text(ja, label[0], label[1])
	}
	return value
}
