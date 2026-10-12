package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/resource"
	"github.com/webkaz-labs/sobalink/internal/transfer"
	"github.com/webkaz-labs/sobalink/internal/webui"
)

const transferDispatchHelpEN = `Send files to an explicitly selected set of peers

  soba send PEER_ID PATH...                         Existing single-peer send
  soba send --peer PEER_A --peer PEER_B [--confirm] [--json] PATH...
  soba send --status PREPARATION_ID --process PROCESS_ID [--json]
  soba send --commit PREPARATION_ID --process PROCESS_ID --revision REVIEW --confirm [--json]
  soba send --discard PREPARATION_ID --process PROCESS_ID [--json]

Put options before paths; use -- before a path beginning with a dash.
Multiple-peer send stages one local copy, then shows exact peers and file contents for confirmation.
No child batches exist before commit. Each admitted peer receives its own batch after commit;
committed means admitted, not delivered. Existing receive approval still applies.
Without --confirm, normal output asks y/N; declining discards the prepared copy.
--json alone prepares and returns stable JSON only: it DOES NOT send or prompt.
--confirm explicitly approves sending the selected staged contents to all listed peers.
For a prepared JSON result, use its exact processId, preparationId and reviewRevision with --commit.
If a response is lost, query the original preparation ID with --status. Never prepare a replacement
or assume delivery failed. A committed receipt cannot be discarded; manage its child transfers.
Preparation and receipts belong to the current process, not a durable outbox or restart resume.
--dry-run performs local input validation only, with zero agent calls and no staging or sending.`

const transferDispatchHelpJA = `明示した複数の相手へファイルを送る

  soba send PEER_ID PATH...                         従来の単一相手への送信
  soba send --peer PEER_A --peer PEER_B [--confirm] [--json] PATH...
  soba send --status PREPARATION_ID --process PROCESS_ID [--json]
  soba send --commit PREPARATION_ID --process PROCESS_ID --revision REVIEW --confirm [--json]
  soba send --discard PREPARATION_ID --process PROCESS_ID [--json]

オプションはパスより前に置き、ハイフンで始まるパスの前には -- を置いてください。
複数相手への送信はローカルコピーを1つ準備し、正確な相手と内容を表示して確認します。
commit 前に子バッチはありません。確定すると相手ごとに別のバッチを受け付けます。
committed は受付済みで、配達完了ではありません。既存の受信承認も引き続き必要です。
--confirm がなければ通常表示で y/N を確認し、否認すると準備済みコピーを破棄します。
--json 単独は準備結果の安定した JSON だけを返し、送信も確認質問もしません。
--confirm は、選んだ準備内容を一覧の全相手へ送ることの明示承認です。
準備結果の processId・preparationId・reviewRevision を正確に使って --commit できます。
応答を失ったら元の準備 ID を --status で照会し、別の準備を自動作成しないでください。
受付済みを破棄することはできません。子バッチの状態を確認・操作してください。
準備・受付記録は現在の起動中だけ有効です。永続送信箱や再起動後の再開ではありません。
--dry-run は入力検査のみで、本体への接続・コピー準備・送信を一切行いません。`

type dispatchCLIStrings []string

func (s *dispatchCLIStrings) String() string         { return strings.Join(*s, ",") }
func (s *dispatchCLIStrings) Set(value string) error { *s = append(*s, value); return nil }

type dispatchCLIChild struct {
	PeerID     string `json:"peerId"`
	TransferID string `json:"transferId"`
}
type dispatchCLIReceipt struct {
	Version        int                `json:"version"`
	ProcessID      string             `json:"processId"`
	PreparationID  string             `json:"preparationId"`
	ReviewRevision string             `json:"reviewRevision,omitempty"`
	PeerIDs        []string           `json:"peerIds,omitempty"`
	Entries        []transfer.Entry   `json:"entries,omitempty"`
	TotalBytes     int64              `json:"totalBytes,omitempty"`
	DispatchID     string             `json:"dispatchId,omitempty"`
	Children       []dispatchCLIChild `json:"children,omitempty"`
	Status         string             `json:"status"`
}

type dispatchCLIError struct {
	code, message string
	cause         error
}

func (e *dispatchCLIError) Error() string     { return e.message }
func (e *dispatchCLIError) ErrorCode() string { return e.code }
func (e *dispatchCLIError) Unwrap() error     { return e.cause }
func dispatchCLIControlError(ja bool, cause error) error {
	code := "transfer_dispatch_control_unavailable"
	var coded interface{ ErrorCode() string }
	if errors.As(cause, &coded) {
		code = coded.ErrorCode()
	}
	message := text(ja, "The dispatch request was not confirmed. Check the original preparation status; do not create a replacement send.", "送信準備・受付の結果を確認できません。元の準備の状態を照会し、代わりの送信を作らないでください。")
	switch code {
	case "transfer_dispatch_invalid":
		message = text(ja, "The recipient selection or confirmation is invalid. Check the exact original request and preparation status.", "宛先の選択または承認が正しくありません。元の正確な要求と準備の状態を確認してください。")
	case "transfer_dispatch_stale":
		message = text(ja, "The preparation is unavailable or its process, trust, scope or lifetime changed. Inspect current transfers before deciding whether to prepare again.", "準備が取得不能か、起動世代・信頼・範囲・期限が変わりました。新たに準備するか決める前に、現在の転送一覧を確認してください。")
	case "transfer_dispatch_preparing":
		message = text(ja, "Local staging is still in progress. Query this same preparation later; do not create another send.", "ローカルコピーの準備中です。別の送信を作らず、後で同じ準備を照会してください。")
	case "transfer_dispatch_committed":
		message = text(ja, "The send is already admitted and cannot be discarded. Inspect the original receipt and its child transfers.", "送信は受付済みのため破棄できません。元の受付結果と子バッチの状態を確認してください。")
	}
	return &dispatchCLIError{code, message, cause}
}
func dispatchCLIInvalid(ja bool) error {
	return &dispatchCLIError{"transfer_dispatch_invalid_response", text(ja, "The dispatch response did not match the reviewed request. Check the original preparation status.", "応答が確認済みの要求と一致しません。元の準備の状態を照会してください。"), nil}
}
func dispatchCLIOpaque(s string) bool {
	return config.ValidPeerID(s)
}

// Only option-led sends enter the new workflow; positional single-peer sends
// retain their existing parser and transfer.send request unchanged.
func transferDispatchCLI(ctx context.Context, args []string, dir string, ja, dryRun bool, out io.Writer, in io.Reader, client controlCaller) error {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		_, err := fmt.Fprintln(out, text(ja, transferDispatchHelpEN, transferDispatchHelpJA))
		return err
	}
	f := commandFlags("send --peer PEER [--peer PEER] [OPTIONS] PATH...", ja, out)
	var peers dispatchCLIStrings
	f.Var(&peers, "peer", text(ja, "exact recipient ID; repeat explicitly", "正確な宛先 ID。相手ごとに明示"))
	machine := f.Bool("json", false, text(ja, "stable JSON; prepares only unless --confirm", "安定した JSON。--confirm がなければ準備のみ"))
	confirm := f.Bool("confirm", false, text(ja, "approve the exact selected recipients and staged contents", "選んだ正確な相手と準備内容への送信を承認"))
	process := f.String("process", "", text(ja, "original processId", "元の processId"))
	revision := f.String("revision", "", text(ja, "exact reviewed reviewRevision", "確認済みの正確な reviewRevision"))
	status := f.String("status", "", text(ja, "query original preparation ID", "元の準備 ID を照会"))
	commit := f.String("commit", "", text(ja, "commit original preparation ID", "元の準備 ID を確定"))
	discard := f.String("discard", "", text(ja, "discard uncommitted preparation ID", "未確定の準備 ID を破棄"))
	if err := f.Parse(args); err != nil {
		return err
	}
	action, id, modes := "prepare", "", 0
	for _, item := range []struct{ name, id string }{{"status", *status}, {"commit", *commit}, {"discard", *discard}} {
		if item.id != "" {
			action, id = item.name, item.id
			modes++
		}
	}
	invalid := func() error { return usageError(ja, "send --help") }
	if modes > 1 {
		return invalid()
	}
	// An explicitly empty selector is not silently treated as a new prepare.
	emptySelector := false
	f.Visit(func(flag *flag.Flag) {
		if (flag.Name == "status" || flag.Name == "commit" || flag.Name == "discard") && flag.Value.String() == "" {
			emptySelector = true
		}
	})
	if emptySelector {
		return invalid()
	}
	paths := []string{}
	if action == "prepare" {
		if len(peers) == 0 || f.NArg() == 0 || *process != "" || *revision != "" {
			return invalid()
		}
		seen := map[string]bool{}
		for _, peer := range peers {
			if !dispatchCLIOpaque(peer) || seen[peer] {
				return invalid()
			}
			seen[peer] = true
		}
		for _, path := range f.Args() {
			if path == "" {
				return invalid()
			}
			absolute, err := filepath.Abs(path)
			if err != nil {
				return err
			}
			paths = append(paths, absolute)
		}
	} else if len(peers) != 0 || f.NArg() != 0 || !resource.ValidDigest(*process) || !dispatchCLIOpaque(id) || action == "commit" && (!*confirm || !resource.ValidDigest(*revision)) || action != "commit" && (*confirm || *revision != "") {
		return invalid()
	}
	if dryRun {
		payload := map[string]any{}
		if action == "prepare" {
			payload["peerIds"], payload["paths"] = peers, paths
		} else {
			payload["processId"], payload["preparationId"] = *process, id
		}
		if action == "commit" {
			payload["reviewRevision"], payload["confirm"] = *revision, true
		}
		return json.NewEncoder(out).Encode(map[string]any{"applied": false, "command": "transfer.dispatch." + action, "payload": payload, "validation": "local-input-only", "requiresProcessState": action == "prepare"})
	}
	call := func(action, requestID string, payload any) (json.RawMessage, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		command, err := json.Marshal(webui.Command{RequestID: requestID, Name: "transfer.dispatch." + action, Payload: raw})
		if err != nil {
			return nil, err
		}
		var result json.RawMessage
		if err := client(ctx, dir, string(command), &result); err != nil {
			return nil, dispatchCLIControlError(ja, err)
		}
		return result, nil
	}
	emit := func(raw json.RawMessage, r dispatchCLIReceipt) error {
		if *machine {
			return json.NewEncoder(out).Encode(raw)
		}
		return dispatchCLIShow(out, dir, ja, r)
	}
	uncertain := func(phase string, err error) error {
		var outputErr error
		if *machine {
			outputErr = json.NewEncoder(out).Encode(map[string]any{"version": 1, "processId": *process, "preparationId": id, "status": "unconfirmed", "phase": phase, "nextAction": "status"})
		} else {
			_, outputErr = fmt.Fprintf(out, "%s\n%s\n", text(ja, "Result unconfirmed. Query this original preparation; do not send again:", "結果は未確認です。再送せず、この元の準備を照会してください："), cliCommandExample(dir, "send", "--status", id, "--process", *process))
		}
		return errors.Join(err, outputErr)
	}
	if action != "prepare" {
		payload := map[string]any{"processId": *process, "preparationId": id}
		if action == "commit" {
			payload["reviewRevision"], payload["confirm"] = *revision, true
		}
		raw, err := call(action, newCLIRequestID("cli-dispatch"), payload)
		if err != nil {
			return uncertain(action, err)
		}
		r, err := dispatchCLIDecode(raw, *process, id, ja)
		if err != nil {
			return uncertain(action, err)
		}
		if action == "commit" && r.Status != "committed" || action == "discard" && r.Status != "discarded" {
			return uncertain(action, dispatchCLIInvalid(ja))
		}
		return emit(raw, r)
	}
	var state struct {
		ProcessID string `json:"resourceCatalogProcessId"`
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := client(ctx, dir, "status", &state); err != nil {
		return &dispatchCLIError{"transfer_dispatch_control_unavailable", text(ja, "Could not read the agent process. Check that soba is running; no send preparation was requested.", "本体の起動状態を取得できません。soba の起動を確認してください。送信準備は要求していません。"), err}
	}
	if !resource.ValidDigest(state.ProcessID) {
		return &dispatchCLIError{"transfer_dispatch_invalid_response", text(ja, "The agent did not provide a valid process identity. No send preparation was requested.", "本体から有効な起動世代を取得できません。送信準備は要求していません。"), nil}
	}
	*process = state.ProcessID
	id = newCLIRequestID("cli-dispatch")
	raw, err := call("prepare", id, map[string]any{"processId": *process, "peerIds": peers, "paths": paths})
	if err != nil {
		return uncertain("prepare", err)
	}
	r, err := dispatchCLIDecode(raw, *process, id, ja)
	if err != nil || r.Status != "prepared" || !dispatchCLISamePeers(r.PeerIDs, peers) {
		return uncertain("prepare", dispatchCLIInvalid(ja))
	}
	if !*machine {
		if err := emit(raw, r); err != nil {
			return err
		}
	}
	if *machine && !*confirm {
		return emit(raw, r)
	}
	if !*confirm {
		p := newPrompts(in, out)
		p.ctx, p.ja = ctx, ja
		if err := p.confirm(text(ja, "Send the reviewed contents to every listed peer?", "確認した内容を一覧の全相手へ送りますか？"), false); err != nil {
			if ctx.Err() != nil {
				return err
			}
			discarded, discardErr := call("discard", newCLIRequestID("cli-dispatch"), map[string]any{"processId": *process, "preparationId": id})
			if discardErr != nil {
				return uncertain("discard", errors.Join(err, discardErr))
			}
			receipt, decodeErr := dispatchCLIDecode(discarded, *process, id, ja)
			if decodeErr != nil || receipt.Status != "discarded" {
				return uncertain("discard", dispatchCLIInvalid(ja))
			}
			return errors.Join(err, emit(discarded, receipt))
		}
	}
	committed, err := call("commit", newCLIRequestID("cli-dispatch"), map[string]any{"processId": *process, "preparationId": id, "reviewRevision": r.ReviewRevision, "confirm": true})
	if err != nil {
		return uncertain("commit", err)
	}
	receipt, err := dispatchCLIDecode(committed, *process, id, ja)
	if err != nil || receipt.Status != "committed" {
		return uncertain("commit", dispatchCLIInvalid(ja))
	}
	childPeers := []string{}
	for _, child := range receipt.Children {
		childPeers = append(childPeers, child.PeerID)
	}
	if !dispatchCLISamePeers(childPeers, peers) {
		return uncertain("commit", dispatchCLIInvalid(ja))
	}
	return emit(committed, receipt)
}

func dispatchCLISamePeers(a, b []string) bool {
	x, y := append([]string(nil), a...), append([]string(nil), b...)
	sort.Strings(x)
	sort.Strings(y)
	return reflect.DeepEqual(x, y)
}
func dispatchCLIDecode(raw []byte, process, id string, ja bool) (dispatchCLIReceipt, error) {
	var r dispatchCLIReceipt
	if err := resource.Decode(raw, len(raw), &r); err != nil || r.Version != 1 || r.ProcessID != process || r.PreparationID != id {
		return r, dispatchCLIInvalid(ja)
	}
	// Each receipt arm is closed: a committed response cannot carry a second,
	// conflicting review, and zero-byte preparations must still report totalBytes.
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return r, dispatchCLIInvalid(ja)
	}
	required := []string{"version", "processId", "preparationId", "status"}
	switch r.Status {
	case "prepared":
		required = append(required, "reviewRevision", "peerIds", "entries", "totalBytes")
	case "committed":
		required = append(required, "dispatchId", "children")
	case "discarded":
	default:
		return r, dispatchCLIInvalid(ja)
	}
	if len(fields) != len(required) {
		return r, dispatchCLIInvalid(ja)
	}
	for _, field := range required {
		if _, present := fields[field]; !present {
			return r, dispatchCLIInvalid(ja)
		}
	}
	switch r.Status {
	case "prepared":
		if !resource.ValidDigest(r.ReviewRevision) || len(r.PeerIDs) == 0 || len(r.Entries) == 0 || r.TotalBytes < 0 || r.DispatchID != "" || len(r.Children) != 0 {
			return r, dispatchCLIInvalid(ja)
		}
		seen := map[string]bool{}
		for _, peer := range r.PeerIDs {
			if !dispatchCLIOpaque(peer) || seen[peer] {
				return r, dispatchCLIInvalid(ja)
			}
			seen[peer] = true
		}
		var total int64
		entries := map[string]bool{}
		for _, entry := range r.Entries {
			if !dispatchCLIOpaque(entry.ID) || entries[entry.ID] || entry.Path == "" || entry.Size < 0 || entry.Kind != transfer.File && entry.Kind != transfer.Directory || entry.Kind == transfer.File && !resource.ValidDigest(entry.SHA256) || entry.Kind == transfer.Directory && (entry.Size != 0 || entry.SHA256 != "") {
				return r, dispatchCLIInvalid(ja)
			}
			entries[entry.ID] = true
			if entry.Size > (1<<63-1)-total {
				return r, dispatchCLIInvalid(ja)
			}
			total += entry.Size
		}
		if total != r.TotalBytes {
			return r, dispatchCLIInvalid(ja)
		}
	case "committed":
		if r.DispatchID != id || len(r.Children) == 0 {
			return r, dispatchCLIInvalid(ja)
		}
		peers, transfers := map[string]bool{}, map[string]bool{}
		for _, child := range r.Children {
			if !dispatchCLIOpaque(child.PeerID) || !dispatchCLIOpaque(child.TransferID) || peers[child.PeerID] || transfers[child.TransferID] {
				return r, dispatchCLIInvalid(ja)
			}
			peers[child.PeerID], transfers[child.TransferID] = true, true
		}
	case "discarded":
		if r.DispatchID != "" || len(r.Children) != 0 {
			return r, dispatchCLIInvalid(ja)
		}
	default:
		return r, dispatchCLIInvalid(ja)
	}
	return r, nil
}
func dispatchCLIShow(out io.Writer, dir string, ja bool, r dispatchCLIReceipt) error {
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %s\n%s: %s\n", text(ja, "Process", "起動世代"), r.ProcessID, text(ja, "Preparation", "準備 ID"), displayText(r.PreparationID))
	switch r.Status {
	case "prepared":
		fmt.Fprintln(&b, text(ja, "Prepared locally; no recipient batch has started.", "ローカル準備済み。相手への子バッチはまだありません。"))
		for _, peer := range r.PeerIDs {
			fmt.Fprintf(&b, "  %s: %s\n", text(ja, "Recipient", "宛先"), displayText(peer))
		}
		for _, e := range r.Entries {
			fmt.Fprintf(&b, "  %s %s (%d bytes) %s\n", e.Kind, displayText(e.Path), e.Size, e.SHA256)
		}
		fmt.Fprintf(&b, "%s: %d bytes\n", text(ja, "Total per recipient", "相手1台ごとの合計"), r.TotalBytes)
		fmt.Fprintln(&b, cliCommandExample(dir, "send", "--commit", r.PreparationID, "--process", r.ProcessID, "--revision", r.ReviewRevision, "--confirm"))
		fmt.Fprintln(&b, cliCommandExample(dir, "send", "--discard", r.PreparationID, "--process", r.ProcessID))
	case "committed":
		fmt.Fprintln(&b, text(ja, "Admitted, not delivered. Inspect each child transfer; retry only existing failed children.", "受付済みで、配達完了ではありません。子バッチごとの状態を確認し、既存の失敗した子だけ再試行してください。"))
		for _, child := range r.Children {
			fmt.Fprintf(&b, "  %s: %s\n", displayText(child.PeerID), displayText(child.TransferID))
		}
		fmt.Fprintln(&b, cliCommandExample(dir, "transfers"))
	case "discarded":
		fmt.Fprintln(&b, text(ja, "Prepared copy discarded; no child batches were admitted.", "準備済みコピーを破棄しました。子バッチは受け付けていません。"))
	}
	fmt.Fprintln(&b, cliCommandExample(dir, "send", "--status", r.PreparationID, "--process", r.ProcessID))
	n, err := io.WriteString(out, b.String())
	if err == nil && n != b.Len() {
		return io.ErrShortWrite
	}
	return err
}
