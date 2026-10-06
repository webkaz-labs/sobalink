package main

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/webkaz-labs/sobalink/internal/core"
)

type servicePortCLIError struct{ code, message string }

func (e *servicePortCLIError) Error() string     { return e.message }
func (e *servicePortCLIError) ErrorCode() string { return e.code }

func validServiceRevision(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32
}

func servicePortsCommand(args []string, dir string, ja, dryRun bool, out io.Writer, query commandQuery, request actionRequest) error {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		_, err := fmt.Fprintln(out, text(ja, servicePortsHelpEN, servicePortsHelpJA))
		return err
	}
	if len(args) == 0 {
		return usageError(ja, "service ports NAME_OR_ID [--from-port PORT] [--count N] [--attempts N] [--json]")
	}
	f := commandFlags("service ports NAME_OR_ID", ja, out)
	from := f.Int("from-port", 49152, text(ja, "first high port to check", "確認する高位ポートの開始位置"))
	count := f.Int("count", 0, text(ja, "requested proposals; zero follows the finite policy budget", "候補数（0 は有限の容量設定に従う）"))
	attempts := f.Int("attempts", 0, text(ja, "candidate windows to check; zero follows the finite policy budget", "確認する候補範囲の回数（0 は有限の容量設定に従う）"))
	structured := f.Bool("json", false, text(ja, "stable machine JSON", "言語共通の機械向け JSON"))
	if err := parseFlags(f, args[1:], ja); err != nil {
		return err
	}
	if *from < 1024 || *from > 65535 {
		return &servicePortCLIError{"listener_mapping_invalid", "choose --from-port within 1024..65535"}
	}
	if *count < 0 || *attempts < 0 {
		return &servicePortCLIError{"listener_probe_invalid", "proposal count and attempt count must be nonnegative"}
	}
	resolved, err := resolveServiceReferences([]string{args[0]}, ja, query)
	if err != nil {
		return err
	}
	var saved core.SavedServiceConfiguration
	if err := query("service.config", map[string]string{"id": resolved.IDs[0]}, &saved); err != nil {
		return err
	}
	if err := checkResolvedName(resolved, saved.Configuration.ID, saved.Configuration.Name, ja); err != nil {
		return err
	}
	if saved.Configuration.ID != resolved.IDs[0] || !validServiceRevision(saved.Revision) {
		return &servicePortCLIError{"service_configuration_invalid", "saved configuration is incomplete; refresh service show"}
	}
	payload := map[string]any{"id": saved.Configuration.ID, "expectedRevision": saved.Revision, "fromPort": *from, "count": *count, "attempts": *attempts}
	if dryRun {
		return request("service.ports", payload)
	}
	var result core.ServicePortProposals
	if err := query("service.ports", payload, &result); err != nil {
		return err
	}
	if *structured {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}
	fmt.Fprintln(out, text(ja, "Transient loopback bind checks only. No forwarding or saved setting changes. Proposals are not reservations; restart rechecks every bind.", "ループバックの一時的な待受確認だけを実施しました。転送・保存済み設定の変更はありません。候補は予約ではなく、再開始時にすべて再確認します。"))
	writeHumanService(out, ja, humanFromSpec(result.Configuration), nil)
	fmt.Fprintf(out, "%s: %d; %s: %s\n", text(ja, "Conflicting local port", "競合したローカルポート"), result.ConflictPort, text(ja, "Effective remote ports in ascending order", "昇順の有効なリモートポート"), displayText(result.EffectivePorts))
	fmt.Fprintf(out, "%s: %s\n", text(ja, "Reviewed revision", "確認した版"), result.Revision)
	for _, proposal := range result.Proposals {
		fmt.Fprintf(out, "%s: %s → %s\n", text(ja, "Local ports → ascending remote ports", "ローカルポート → 昇順のリモートポート"), proposal.String(), displayText(result.EffectivePorts))
		fmt.Fprintln(out, "  "+cliCommandExample(dir, "service", "restart", result.Configuration.ID, "--local-port", fmt.Sprint(proposal.LocalPort), "--expected-revision", result.Revision))
	}
	fmt.Fprintf(out, "%s: %d; %s: %d; %s: %s\n", text(ja, "Candidate windows checked", "確認した候補範囲"), result.Attempts, text(ja, "Bind checks", "待受確認回数"), result.BindChecks, text(ja, "Search stop reason", "検索の終了理由"), proposalStopReason(result.StopReason, ja))
	if len(result.Proposals) == 0 {
		fmt.Fprintln(out, result.NextSteps[map[bool]string{false: "en", true: "ja"}[ja]])
	}
	return nil
}

func localizeListenerError(ja bool, err error) error {
	if !ja {
		return err
	}
	var coded interface{ ErrorCode() string }
	if !errors.As(err, &coded) {
		return err
	}
	messages := map[string]string{
		"listener_conflict":             "ローカルポートが使用中です。競合している入口を明示的に停止するか、service ports NAME_OR_ID で別の候補を確認してください",
		"listener_capacity":             "入口の容量が不足しています。範囲を縮小するか未使用の動作を停止し、有限の入口数の設定を確認してください",
		"listener_permission_denied":    "OS がループバックの待受を拒否しました。ローカルのアクセス権を確認してください。権限昇格やセキュリティ設定の自動変更は行いません",
		"listener_address_unavailable":  "選んだループバックのアドレス系統が利用できません。IPv4 または IPv6 の明示的な選択を確認してください",
		"listener_unavailable":          "ローカル待受が原因を分類できない理由で失敗しました。入口の設定を確認してから再試行してください",
		"listener_mapping_invalid":      "保存済みのプロトコル・ループバック・ポート・除外を確認してください。ローカルポートの全範囲は1024〜65535に収めてください",
		"listener_probe_invalid":        "候補数と確認回数は0以上で指定してください",
		"service_revision_invalid":      "--expected-revision は restart で使い、保存済みの完全な版を指定してください",
		"service_configuration_invalid": "保存済み設定が不完全です。service show で再確認してください",
		"listener_probe_timeout":        "ポート確認の期限に達しました。保存済み設定は変更していません。確認の有限の容量を見直し、明示的に再試行してください",
		"listener_probe_canceled":       "ポート確認を取り消しました。保存済み設定は変更せず、一時的な待受はすべて閉じました",
		"listener_probe_capacity":       "確認の容量が不足しています。portProposalResults・portProposalAttempts・portProposalBinds の有限の設定か要求数を確認してください",
		"listener_no_conflict":          "保存済みのローカルポートは現在利用可能です。別のポートの選択や開始は行っていません",
		"listener_proposal_unsupported": "代替のローカル入口ポートは停止中の送信接続だけが対象です。共有やアプリ側のポートは変更しません",
		"service_revision_conflict":     "保存済み設定が変更されました。service ports/show で設定全体を再確認してください",
		"service_active":                "サービスは稼働中です。ポートの確認・編集・再開始の前に明示的に停止してください",
		"service_backend_mismatch":      "保存済みサービスのネットワークを選択してから再確認してください",
		"service_not_found":             "保存済みサービスが見つかりません。最新の一覧を確認してください",
		"network_unavailable":           "選択したネットワークの状態を確認してから予約ポートの確認を再試行してください",
	}
	if message := messages[coded.ErrorCode()]; message != "" {
		for cause := err; cause != nil; cause = errors.Unwrap(cause) {
			if strings.HasPrefix(cause.Error(), "saved but not started: ") {
				message = "設定は保存されましたが、開始できませんでした: " + message
				break
			}
		}
		return &localizedDiskSpaceError{err, message}
	}
	return err
}

const servicePortsHelpEN = `Check alternate local entry ports for a stopped saved connection

  soba service ports NAME_OR_ID [--from-port 49152] [--count N] [--attempts N] [--json]
  soba service restart NAME_OR_ID --local-port PORT --expected-revision REVISION

This explicit check temporarily binds only the saved TCP/UDP loopback family.
It confirms an address-in-use failure before checking alternatives. Every socket
is closed; no forwarder, permission or saved setting is created or changed.
Proposals preserve effective remote ports and exclusions, mapped in ascending
order to consecutive local ports. Reserved application/backend ports are skipped.
A proposal is not a reservation. Review the full service and lifetime before the
separate restart; actual binding and the saved revision are checked again.
--dry-run prints the planned check without binding. --json is locale independent.
Finite capacity settings portProposalResults, portProposalAttempts and
portProposalBinds default to 3 results, 32 windows and 4096 total bind checks.
portProposalSeconds defaults to a 5-second check deadline, independently adjustable.
--count/--attempts can request fewer, or more after raising the matching finite
capacity setting. The remaining listener budget bounds simultaneous sockets;
the high-port domain ends at 65535. No permission or lifetime is shortened.`
const servicePortsHelpJA = `停止中の保存済み接続で、代替のローカル入口ポートを確認

  soba service ports NAME_OR_ID [--from-port 49152] [--count N] [--attempts N] [--json]
  soba service restart NAME_OR_ID --local-port PORT --expected-revision REVISION

この明示的な操作は、保存済みの TCP/UDP とループバックのアドレス系統で
一時的に待受を試し、使用中のアドレスを確認してから候補を調べます。
すべて閉じ、転送・許可の開始や保存済み設定の変更は行いません。
有効なリモートポートと除外を維持し、昇順で連続するローカルポートに
対応させます。アプリとバックエンドの予約ポートは候補から除外します。
候補は予約ではありません。サービス全体と有効期間を確認してから、別の
restart 操作で明示的に選択してください。待受と保存済みの版は再確認します。
--dry-run は待受せず予定を表示します。--json は言語によらず共通です。
有限の容量設定 portProposalResults・portProposalAttempts・portProposalBinds の
既定は候補3個・候補範囲32回・待受確認の合計4096回です。
確認の期限 portProposalSeconds の既定は5秒で、独立して変更できます。
--count/--attempts で要求数を減らせます。増やす場合は対応する容量設定も
確認してください。同時ソケット数は入口の残容量内、高位ポートは65535までです。
許可や有効期間の制限を追加するものではありません。`

func proposalStopReason(reason string, ja bool) string {
	switch reason {
	case "requested_count":
		return text(ja, "requested number found", "要求した候補数に到達")
	case "attempt_budget":
		return text(ja, "candidate-window budget reached", "候補範囲の確認回数に到達")
	case "bind_budget":
		return text(ja, "bind-check budget reached", "待受確認の容量に到達")
	case "port_range":
		return text(ja, "end of high-port range", "高位ポートの範囲の末尾")
	default:
		return reason
	}
}
