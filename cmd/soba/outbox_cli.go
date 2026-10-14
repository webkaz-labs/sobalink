package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/webkaz-labs/sobalink/internal/resource"
	"github.com/webkaz-labs/sobalink/internal/transfer"
	"github.com/webkaz-labs/sobalink/internal/webui"
)

const outboxHelpEN = `Queue files for exact recipients until a fixed deadline

  soba send --queue --peer PEER_A [--peer PEER_B] [--expires-in 24h] [--confirm] [--json] PATH...
  soba outbox list [--json]
  soba outbox show OUTBOX_ID [--json]
  soba outbox show --request REQUEST_ID [--json]
  soba outbox cancel OUTBOX_ID [--delivery DELIVERY_ID] [--json]
  soba outbox commit OUTBOX_ID --process PROCESS_ID --revision REVIEW --confirm [--json]
  soba outbox discard OUTBOX_ID --process PROCESS_ID [--json]

Queue mode stages a local copy. Review the exact contents, recipient identities and absolute
sending deadline. The default is 24h; accepted durations are 1m through 168h (seven days).
After confirmation, automatic attempts may resume after restart until that original deadline.
Both devices must run Sobalink and become reachable. Receiving approval still applies.
Offline compatibility can be unverified; durable sending waits for authenticated support.
No fallback to immediate sending occurs. Immediate sending is a separate action.
--json without --confirm prepares only, never prompts or authorizes sending.
Use the returned processId, outboxId and reviewRevision for a later explicit commit.
Prepared copies are process-bound. Enqueued show/cancel require no old process ID.
A lost or storage_busy response is unconfirmed: check the original request, never create a replacement blindly.
If it is still prepared and the review is valid, retry the exact same commit without changing its deadline.
Retained evidence has its own finite budget, separate from active work and physical staged bytes.
History cleanup may not free evidence capacity; never erase uncertain evidence to admit a new send.
Keep the returned ob1_ request ID unchanged when showing, committing, discarding or cancelling.
New requests use the device clock; if admission rejects the clock, check it before any new review.
Cancel stops further sends; files already saved on receiving devices remain.
Queued means accepted locally, not delivered. Confirmed means saved, not read by a person.
Put send options before paths; use -- before paths beginning with a dash.
--dry-run validates local inputs only: no agent calls, staging, capability check or sending.`

const outboxHelpJA = `正確な宛先と固定の期限を指定してファイル送信を予約する

  soba send --queue --peer PEER_A [--peer PEER_B] [--expires-in 24h] [--confirm] [--json] PATH...
  soba outbox list [--json]
  soba outbox show OUTBOX_ID [--json]
  soba outbox show --request REQUEST_ID [--json]
  soba outbox cancel OUTBOX_ID [--delivery DELIVERY_ID] [--json]
  soba outbox commit OUTBOX_ID --process PROCESS_ID --revision REVIEW --confirm [--json]
  soba outbox discard OUTBOX_ID --process PROCESS_ID [--json]

予約モードはローカルコピーを準備します。正確な内容・宛先の識別情報・絶対日時の送信期限を
確認してください。期限の既定は 24h、指定できる期間は 1m～168h（7日）です。
承認後は、再起動後も元の期限まで自動で送信を試みます。両端末で Sobalink が起動して
接続可能になる必要があります。受信側の承認も引き続き必要です。
オフラインでは対応状況が未確認の場合があり、認証済みの対応確認まで送信を待ちます。
即時送信へ自動で切り替わりません。即時送信は別の操作です。
--confirm のない --json は準備だけを行い、質問も送信承認も行いません。
後で確定するには、返された processId・outboxId・reviewRevision を正確に指定します。
未確定の準備は現在の起動中のみ有効です。受付後の照会・停止に以前の起動 ID は不要です。
応答消失・storage_busy は結果未確認です。代わりの要求を作らず、元の要求を照会してください。
準備済みで確認内容がまだ有効なら、期限を変えず同じ commit を再試行してください。
保持する到着の記録には、進行中の枠や実際のコピー容量とは別の有限予算があります。
履歴整理で記録容量が空くとは限りません。新規送信のために結果不明の記録を消さないでください。
照会・確定・破棄・停止では、返された ob1_ 要求 ID を変更せずに指定してください。
新しい要求には端末の時計を使います。時刻が拒否された場合は、新しい確認の前に時計を確認してください。
cancel は追加の送信を停止します。相手の端末に保存済みのファイルは残ります。
予約済みはローカルでの受付を示します。到着確認は保存の記録で、人が読んだ証明ではありません。
送信オプションはパスより前に置き、ハイフンで始まるパスの前には -- を置いてください。
--dry-run はローカル入力検査のみで、本体への接続・コピー準備・対応確認・送信を行いません。`

type outboxCLIFile struct {
	BatchID string `json:"batchId"`
	FileID  string `json:"fileId"`
	Size    int64  `json:"size"`
	SHA256  string `json:"sha256"`
}
type outboxCLIChild struct {
	PeerID          string          `json:"peerId"`
	DeliveryID      string          `json:"deliveryId"`
	Status          string          `json:"status"`
	DeliveryOutcome string          `json:"deliveryOutcome"`
	ReasonCode      string          `json:"reasonCode"`
	Files           []outboxCLIFile `json:"files"`
}
type outboxCLIRecord struct {
	Version         int              `json:"version"`
	Kind            string           `json:"kind"`
	OutboxID        string           `json:"outboxId"`
	RequestID       string           `json:"requestId"`
	ReviewRevision  string           `json:"reviewRevision"`
	Admission       string           `json:"admission"`
	Status          string           `json:"status"`
	DeliveryOutcome string           `json:"deliveryOutcome"`
	ReasonCode      string           `json:"reasonCode"`
	ProcessID       string           `json:"processId,omitempty"`
	PeerIDs         []string         `json:"peerIds"`
	Entries         []transfer.Entry `json:"entries"`
	TotalBytes      int64            `json:"totalBytes"`
	CreatedAt       string           `json:"createdAt"`
	ExpiresAt       string           `json:"expiresAt"`
	UpdatedAt       string           `json:"updatedAt"`
	Children        []outboxCLIChild `json:"children"`
}

// Durable admission keys have a bounded creation epoch and independent 128-bit
// random identity. Observations only validate syntax: age never prevents looking
// up an existing record, and must not trigger creation of a replacement key.
func outboxCLIRequestTime(id string) (time.Time, bool) {
	if len(id) != 50 || !strings.HasPrefix(id, "ob1_") || id[17] != '_' {
		return time.Time{}, false
	}
	for _, c := range id[4:17] {
		if c < '0' || c > '9' {
			return time.Time{}, false
		}
	}
	for _, c := range id[18:] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return time.Time{}, false
		}
	}
	millis, err := strconv.ParseInt(id[4:17], 10, 64)
	if err != nil || strconv.FormatInt(millis, 10) != id[4:17] {
		return time.Time{}, false
	}
	created := time.UnixMilli(millis).UTC()
	if created.Add(8*24*time.Hour+5*time.Minute).UnixMilli() > 9_999_999_999_999 {
		return time.Time{}, false
	}
	return created, true
}

func outboxCLIValidRequestID(id string) bool {
	_, ok := outboxCLIRequestTime(id)
	return ok
}

func newOutboxCLIRequestID(created time.Time) (string, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	id := "ob1_" + strconv.FormatInt(created.UnixMilli(), 10) + "_" + hex.EncodeToString(nonce[:])
	if !outboxCLIValidRequestID(id) {
		return "", errors.New("outbox request timestamp is outside the supported range")
	}
	return id, nil
}

// Identify an option-led queue send without interpreting a path or flag value
// named --queue as consent to change the existing immediate-send workflow.
func outboxCLIWantsQueue(args []string) bool {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" || !strings.HasPrefix(arg, "-") {
			return false
		}
		name := strings.TrimLeft(strings.SplitN(arg, "=", 2)[0], "-")
		if name == "queue" {
			return true
		}
		if !strings.Contains(arg, "=") {
			switch name {
			case "peer", "process", "revision", "status", "commit", "discard", "expires-in":
				i++
			}
		}
	}
	return false
}

type outboxCLI struct {
	ctx         context.Context
	dir         string
	ja, machine bool
	out         io.Writer
	client      controlCaller
	id, process string
}

func outboxCLIUsage(ja bool) error {
	return &dispatchCLIError{"outbox_invalid", text(ja, "Invalid outbox inputs; use soba outbox --help.", "送信待ちの入力が正しくありません。soba outbox --help を参照してください。"), nil}
}
func outboxCLIInvalid(ja bool) error {
	return &dispatchCLIError{"outbox_invalid_response", text(ja, "The outbox response did not match the original request. Check that original request before sending again.", "送信待ちの応答が元の要求と一致しません。再送する前に元の要求を照会してください。"), nil}
}
func outboxCLIControlError(ja bool, cause error) error {
	code := "outbox_control_unavailable"
	var coded interface{ ErrorCode() string }
	if errors.As(cause, &coded) && validErrorCode(coded.ErrorCode()) {
		code = coded.ErrorCode()
	}
	message := text(ja, "Result not confirmed. Check the original outbox request; do not create a replacement send.", "結果を確認できません。代わりの送信を作らず、元の送信待ち要求を照会してください。")
	switch code {
	case "outbox_storage_busy", "outbox_evidence_capacity", "durable_evidence_capacity":
		message = outboxCLIReason(ja, code)
	case "outbox_invalid", "outbox_stale", "outbox_conflict":
		message = text(ja, "The original review is invalid, changed or unavailable. Check the original request and exact recipients before any new review.", "元の確認内容が無効・変更済み・取得不能です。新たに確認する前に元の要求と正確な宛先を照会してください。")
	case "outbox_retired":
		message = text(ja, "This request ID is retired and cannot be reused. That does not prove non-delivery. Review any original delivery evidence before a new send.", "この要求 ID は有効期間を終えており、再利用できません。未到着を意味するものではありません。新しい送信の前に元の到着の記録を確認してください。")
	case "outbox_not_found":
		message = text(ja, "This outbox request was not found in the selected profile. Verify the original ID and profile before creating any replacement.", "選択中のプロファイルに送信待ち要求がありません。代わりの要求を作る前に元の ID とプロファイルを確認してください。")
	case "outbox_capacity":
		message = text(ja, "There is not enough outbox capacity. Review existing work and available storage; active work is not automatically removed.", "送信待ちの容量が足りません。既存の要求と保存容量を確認してください。進行中の要求は自動で削除されません。")
	case "outbox_storage_uncertain", "outbox_recovery_required":
		message = text(ja, "Stored outbox state needs review. Check the original request; delivery may already have happened. Do not blindly replay it.", "保存された送信待ちの状態を確認する必要があります。すでに届いている可能性があるため、元の要求を照会し、確認せずに再送しないでください。")
	case "outbox_clock_rollback":
		message = text(ja, "The device clock needs review. Check the clock and original deadline; do not extend the sending authority automatically.", "端末の時計を確認する必要があります。時計と元の期限を確認し、送信期限を自動で延長しないでください。")
	case "outbox_peer_upgrade_required":
		message = text(ja, "The recipient cannot confirm durable delivery support. Check its Sobalink status; immediate sending is a separate action.", "相手の端末の永続送信対応を確認できません。相手の Sobalink の状態を確認してください。即時送信は別の操作です。")
	}
	return &dispatchCLIError{code, message, cause}
}
func (c *outboxCLI) call(action, requestID string, payload any) (json.RawMessage, error) {
	if err := c.ctx.Err(); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	wire, err := json.Marshal(webui.Command{RequestID: requestID, Name: "outbox." + action, Payload: raw})
	if err != nil {
		return nil, err
	}
	var result json.RawMessage
	if err := c.client(c.ctx, c.dir, string(wire), &result); err != nil {
		return nil, outboxCLIControlError(c.ja, err)
	}
	return result, nil
}
func (c *outboxCLI) uncertain(phase string, err error) error {
	var outputErr error
	if c.machine {
		code := "outbox_invalid_response"
		var coded interface{ ErrorCode() string }
		if errors.As(err, &coded) && validErrorCode(coded.ErrorCode()) {
			code = coded.ErrorCode()
		}
		outputErr = json.NewEncoder(c.out).Encode(map[string]any{"version": 1, "kind": "outbox", "requestId": c.id, "outboxId": c.id, "observation": "unconfirmed", "phase": phase, "code": code, "nextAction": "status"})
	} else {
		_, outputErr = fmt.Fprintf(c.out, "%s\n%s\n", text(c.ja, "Result not confirmed. Check the original request before sending again:", "結果を確認できません。再送する前に元の要求を照会してください："), cliCommandExample(c.dir, "outbox", "show", c.id))
	}
	return errors.Join(err, outputErr)
}
func (c *outboxCLI) emit(raw json.RawMessage, record outboxCLIRecord) error {
	if c.machine {
		return json.NewEncoder(c.out).Encode(raw)
	}
	return outboxCLIShow(c.out, c.dir, c.ja, record)
}

func outboxSendCLI(ctx context.Context, args []string, dir string, ja, dryRun bool, out io.Writer, in io.Reader, client controlCaller) error {
	f := commandFlags("send --queue --peer PEER [OPTIONS] PATH...", ja, io.Discard)
	queue := f.Bool("queue", false, "")
	var peers dispatchCLIStrings
	f.Var(&peers, "peer", "")
	machine := f.Bool("json", false, "")
	confirm := f.Bool("confirm", false, "")
	lifetime := f.Duration("expires-in", 24*time.Hour, "")
	f.Usage = func() { fmt.Fprintln(out, text(ja, outboxHelpEN, outboxHelpJA)) }
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return outboxCLIUsage(ja)
	}
	invalid := func() error {
		return &dispatchCLIError{"outbox_invalid", text(ja, "Use send --queue --peer PEER [--expires-in 24h] [--confirm] [--json] PATH...; choose unique recipients and a duration from 1m through 168h.", "send --queue --peer PEER [--expires-in 24h] [--confirm] [--json] PATH... を使い、重複しない宛先と 1m～168h の期間を指定してください。"), nil}
	}
	if !*queue || len(peers) == 0 || f.NArg() == 0 || *lifetime < time.Minute || *lifetime > 7*24*time.Hour {
		return invalid()
	}
	seen := map[string]bool{}
	for _, peer := range peers {
		if !dispatchCLIOpaque(peer) || seen[peer] {
			return invalid()
		}
		seen[peer] = true
	}
	paths := make([]string, 0, f.NArg())
	for _, path := range f.Args() {
		if path == "" {
			return invalid()
		}
		absolute, err := filepath.Abs(path)
		if err != nil {
			return invalid()
		}
		paths = append(paths, absolute)
	}
	// Persist and review an absolute deadline once; no retry/restart resets it.
	expires := time.Now().UTC().Truncate(time.Millisecond).Add(*lifetime).Format(time.RFC3339Nano)
	payload := map[string]any{"peerIds": peers, "paths": paths, "expiresAt": expires}
	if dryRun {
		return json.NewEncoder(out).Encode(map[string]any{"applied": false, "command": "outbox.prepare", "payload": payload, "validation": "local-input-only", "requiresProcessState": true})
	}
	c := outboxCLI{ctx: ctx, dir: dir, ja: ja, machine: *machine, out: out, client: client}
	var state struct {
		ProcessID string `json:"resourceCatalogProcessId"`
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := client(ctx, dir, "status", &state); err != nil {
		return &dispatchCLIError{"outbox_control_unavailable", text(ja, "Could not read the running agent. No outbox preparation was requested.", "稼働中の本体を取得できません。送信待ちの準備は要求していません。"), err}
	}
	if !resource.ValidDigest(state.ProcessID) {
		return outboxCLIInvalid(ja)
	}
	// Generate only at this explicit preparation boundary, after process lookup.
	// The deadline and key share millisecond precision, including the 7-day edge.
	created := time.Now().UTC().Truncate(time.Millisecond)
	requestID, err := newOutboxCLIRequestID(created)
	if err != nil {
		return &dispatchCLIError{"outbox_invalid", text(ja, "Could not create an outbox request identity. Check the device clock; no preparation was requested.", "送信待ちの要求 ID を作成できません。端末の時計を確認してください。準備は要求していません。"), err}
	}
	c.process, c.id = state.ProcessID, requestID
	expires = created.Add(*lifetime).Format(time.RFC3339Nano)
	payload["expiresAt"] = expires
	payload["processId"] = c.process
	raw, err := c.call("prepare", c.id, payload)
	if err != nil {
		return c.uncertain("prepare", err)
	}
	review, err := outboxCLIDecode(raw, c.process, c.id, ja)
	if err != nil || review.Admission != "prepared" || review.Status != "prepared" || review.ExpiresAt != expires || !reflect.DeepEqual(review.PeerIDs, []string(peers)) {
		return c.uncertain("prepare", outboxCLIInvalid(ja))
	}
	if !*machine {
		if err := c.emit(raw, review); err != nil {
			return err
		}
	}
	if *machine && !*confirm {
		return c.emit(raw, review)
	}
	if !*confirm {
		p := newPrompts(in, out)
		p.ctx, p.ja = ctx, ja
		deadline, _ := time.Parse(time.RFC3339Nano, review.ExpiresAt)
		question := fmt.Sprintf(text(ja, "Queue the reviewed contents for every listed recipient until %s?", "確認した内容を一覧の全相手へ %s まで送信予約しますか？"), deadline.Local().Format(time.RFC3339Nano))
		if err := p.confirm(question, false); err != nil {
			if ctx.Err() != nil {
				return err
			}
			raw, discardErr := c.call("discard", newCLIRequestID("cli-outbox"), map[string]any{"processId": c.process, "outboxId": c.id})
			if discardErr != nil {
				return c.uncertain("discard", errors.Join(discardErr, err))
			}
			record, decodeErr := outboxCLIDecode(raw, c.process, c.id, ja)
			if decodeErr != nil || record.Status != "discarded" || !outboxCLISameReview(review, record) {
				return c.uncertain("discard", outboxCLIInvalid(ja))
			}
			return errors.Join(err, c.emit(raw, record))
		}
	}
	raw, err = c.call("commit", newCLIRequestID("cli-outbox"), map[string]any{"processId": c.process, "outboxId": c.id, "reviewRevision": review.ReviewRevision, "confirm": true})
	if err != nil {
		return c.uncertain("commit", err)
	}
	record, err := outboxCLIDecode(raw, "", c.id, ja)
	if err != nil || record.Admission != "enqueued" || !outboxCLISameReview(review, record) {
		return c.uncertain("commit", outboxCLIInvalid(ja))
	}
	return c.emit(raw, record)
}

func outboxCommandCLI(ctx context.Context, args []string, dir string, ja, dryRun bool, out io.Writer, client controlCaller) error {
	if len(args) == 0 || len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		_, err := fmt.Fprintln(out, text(ja, outboxHelpEN, outboxHelpJA))
		return err
	}
	action, args := args[0], args[1:]
	switch action {
	case "list", "show", "cancel", "commit", "discard":
	default:
		return outboxCLIUsage(ja)
	}
	f := commandFlags("outbox "+action, ja, io.Discard)
	machine := f.Bool("json", false, "")
	process, revision, request, delivery := "", "", "", ""
	confirm := false
	if action == "commit" || action == "discard" {
		f.StringVar(&process, "process", "", "")
	}
	if action == "commit" {
		f.StringVar(&revision, "revision", "", "")
		f.BoolVar(&confirm, "confirm", false, "")
	}
	if action == "show" {
		f.StringVar(&request, "request", "", "")
	}
	if action == "cancel" {
		f.StringVar(&delivery, "delivery", "", "")
	}
	f.Usage = func() { fmt.Fprintln(out, text(ja, outboxHelpEN, outboxHelpJA)) }
	id := ""
	// Accept both "show ID --json" and "show --json ID".
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		id, args = args[0], args[1:]
	}
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return outboxCLIUsage(ja)
	}
	if id == "" && f.NArg() == 1 {
		id = f.Arg(0)
	} else if f.NArg() != 0 {
		return outboxCLIUsage(ja)
	}
	invalid := func() error {
		return &dispatchCLIError{"outbox_invalid", text(ja, "Invalid outbox selection or confirmation; use soba outbox --help.", "送信待ちの選択または承認が正しくありません。soba outbox --help を参照してください。"), nil}
	}
	empty := false
	f.Visit(func(v *flag.Flag) {
		if v.Name != "json" && v.Name != "confirm" && v.Value.String() == "" {
			empty = true
		}
	})
	if empty || action == "list" && id != "" || action != "list" && id == "" && request == "" || request != "" && (id != "" || !outboxCLIValidRequestID(request)) || id != "" && !outboxCLIValidRequestID(id) || delivery != "" && !dispatchCLIOpaque(delivery) {
		return invalid()
	}
	if (action == "commit" || action == "discard") && !resource.ValidDigest(process) || action == "commit" && (!confirm || !resource.ValidDigest(revision)) {
		return invalid()
	}
	payload := map[string]any{}
	if id != "" {
		payload["outboxId"] = id
	}
	if request != "" {
		payload["requestId"] = request
		id = request
	}
	if process != "" {
		payload["processId"] = process
	}
	if revision != "" {
		payload["reviewRevision"], payload["confirm"] = revision, true
	}
	if delivery != "" {
		payload["deliveryId"] = delivery
	}
	wireAction := action
	if action == "show" {
		wireAction = "status"
	}
	if dryRun {
		return json.NewEncoder(out).Encode(map[string]any{"applied": false, "command": "outbox." + wireAction, "payload": payload, "validation": "local-input-only"})
	}
	c := outboxCLI{ctx: ctx, dir: dir, ja: ja, machine: *machine, out: out, client: client, id: id, process: process}
	raw, err := c.call(wireAction, newCLIRequestID("cli-outbox"), payload)
	if err != nil {
		if action == "list" {
			return err
		}
		return c.uncertain(wireAction, err)
	}
	if action == "list" {
		list, err := outboxCLIListDecode(raw, ja)
		if err != nil {
			return err
		}
		if *machine {
			return json.NewEncoder(out).Encode(raw)
		}
		if list.Status == "blocked_recovery" {
			if _, err = fmt.Fprintf(out, "%s (%s)\n", text(ja, "Stored outbox state needs review.", "保存された送信待ちの状態を確認する必要があります。"), list.ReasonCode); err != nil {
				return err
			}
		}
		if len(list.Items) == 0 {
			_, err = fmt.Fprintln(out, text(ja, "No outbox requests.", "送信待ちの要求はありません。"))
			return err
		}
		for _, item := range list.Items {
			if err := outboxCLIShow(out, dir, ja, item); err != nil {
				return err
			}
		}
		return nil
	}
	record, err := outboxCLIDecode(raw, process, id, ja)
	if err != nil {
		return c.uncertain(wireAction, err)
	}
	if action == "commit" && (record.Admission != "enqueued" || record.ReviewRevision != revision) || action == "discard" && record.Status != "discarded" || action == "cancel" && record.Admission != "enqueued" {
		return c.uncertain(wireAction, outboxCLIInvalid(ja))
	}
	if action == "cancel" {
		for _, child := range record.Children {
			if delivery == "" || delivery == child.DeliveryID {
				switch child.Status {
				case "cancel_requested", "cancelled", "completed", "expired":
				default:
					return c.uncertain(wireAction, outboxCLIInvalid(ja))
				}
			}
		}
	}
	if delivery != "" {
		found := false
		for _, child := range record.Children {
			if child.DeliveryID == delivery {
				found = true
			}
		}
		if !found {
			return c.uncertain(wireAction, outboxCLIInvalid(ja))
		}
	}
	return c.emit(raw, record)
}

func outboxCLISameReview(a, b outboxCLIRecord) bool {
	if len(a.Children) != len(b.Children) {
		return false
	}
	children := map[string]string{}
	for _, child := range a.Children {
		children[child.PeerID] = child.DeliveryID
	}
	for _, child := range b.Children {
		if children[child.PeerID] != child.DeliveryID {
			return false
		}
	}
	return a.OutboxID == b.OutboxID && a.RequestID == b.RequestID && a.ReviewRevision == b.ReviewRevision && a.CreatedAt == b.CreatedAt && a.ExpiresAt == b.ExpiresAt && a.TotalBytes == b.TotalBytes && reflect.DeepEqual(a.PeerIDs, b.PeerIDs) && reflect.DeepEqual(a.Entries, b.Entries)
}
func outboxCLIRequiredFields(raw []byte, keys ...string) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return false
	}
	for _, key := range keys {
		if _, present := fields[key]; !present {
			return false
		}
	}
	return true
}
func outboxCLIRelativePath(p string) bool {
	if strings.ContainsAny(p, "\\\x00\r\n") {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}
func outboxCLIWorkStatus(status string) bool {
	switch status {
	case "prepared", "discarded", "queued", "waiting_for_peer", "sending", "awaiting_receipt", "completed", "paused", "blocked_permission", "blocked_recovery", "cancel_requested", "cancelled", "expired":
		return true
	}
	return false
}
func outboxCLIOutcome(outcome string) bool {
	switch outcome {
	case "not_attempted", "unknown", "partial", "confirmed":
		return true
	}
	return false
}
func outboxCLIDecode(raw []byte, process, id string, ja bool) (outboxCLIRecord, error) {
	var r outboxCLIRecord
	invalid := func() (outboxCLIRecord, error) { return r, outboxCLIInvalid(ja) }
	if resource.Decode(raw, 16<<20, &r) != nil || r.Version != 1 || r.Kind != "outbox" || !outboxCLIValidRequestID(r.OutboxID) || r.RequestID != r.OutboxID || id != "" && r.OutboxID != id || !resource.ValidDigest(r.ReviewRevision) || !outboxCLIWorkStatus(r.Status) || !outboxCLIOutcome(r.DeliveryOutcome) || r.ReasonCode != "" && !validErrorCode(r.ReasonCode) {
		return invalid()
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return invalid()
	}
	for _, key := range []string{"version", "kind", "outboxId", "requestId", "reviewRevision", "admission", "status", "deliveryOutcome", "peerIds", "entries", "totalBytes", "createdAt", "expiresAt", "updatedAt", "children", "reasonCode"} {
		if _, ok := fields[key]; !ok {
			return invalid()
		}
	}
	requestCreated, _ := outboxCLIRequestTime(r.OutboxID)
	created, err1 := time.Parse(time.RFC3339Nano, r.CreatedAt)
	expires, err2 := time.Parse(time.RFC3339Nano, r.ExpiresAt)
	_, err3 := time.Parse(time.RFC3339Nano, r.UpdatedAt)
	if err1 != nil || err2 != nil || err3 != nil || !expires.After(created) || expires.Sub(created) > 7*24*time.Hour || expires.After(requestCreated.Add(7*24*time.Hour)) || len(r.PeerIDs) == 0 || len(r.Entries) == 0 || r.TotalBytes < 0 {
		return invalid()
	}
	peers := map[string]bool{}
	for _, peer := range r.PeerIDs {
		if !dispatchCLIOpaque(peer) || peers[peer] {
			return invalid()
		}
		peers[peer] = true
	}
	entries := map[string]transfer.Entry{}
	var total int64
	paths := map[string]bool{}
	var rawEntries []json.RawMessage
	if json.Unmarshal(fields["entries"], &rawEntries) != nil {
		return invalid()
	}
	for i, e := range r.Entries {
		if !outboxCLIRequiredFields(rawEntries[i], "id", "path", "kind", "size") || paths[e.Path] || !outboxCLIRelativePath(e.Path) || !dispatchCLIOpaque(e.ID) || e.Path == "" || e.Size < 0 || e.Kind != transfer.File && e.Kind != transfer.Directory || e.Kind == transfer.File && !resource.ValidDigest(e.SHA256) || e.Kind == transfer.Directory && (e.Size != 0 || e.SHA256 != "") || e.Size > (1<<63-1)-total {
			return invalid()
		}
		if _, ok := entries[e.ID]; ok {
			return invalid()
		}
		entries[e.ID] = e
		paths[e.Path] = true
		total += e.Size
	}
	if total != r.TotalBytes {
		return invalid()
	}
	switch r.Admission {
	case "prepared":
		if !resource.ValidDigest(r.ProcessID) || process != "" && r.ProcessID != process || r.Status != "prepared" && r.Status != "discarded" || r.DeliveryOutcome != "not_attempted" {
			return invalid()
		}
	case "enqueued":
		if _, present := fields["processId"]; present {
			return invalid()
		}
		if r.ProcessID != "" || r.Status == "prepared" || r.Status == "discarded" {
			return invalid()
		}
	default:
		return invalid()
	}
	if len(r.Children) != len(peers) {
		return invalid()
	}
	seenPeers, deliveries := map[string]bool{}, map[string]bool{}
	var rawChildren []json.RawMessage
	if json.Unmarshal(fields["children"], &rawChildren) != nil {
		return invalid()
	}
	for i, child := range r.Children {
		if !outboxCLIRequiredFields(rawChildren[i], "peerId", "deliveryId", "status", "deliveryOutcome", "reasonCode", "files") {
			return invalid()
		}
		if r.Admission == "prepared" && child.Status != r.Status || r.Admission == "enqueued" && (child.Status == "prepared" || child.Status == "discarded") {
			return invalid()
		}
		if child.PeerID != r.PeerIDs[i] || !peers[child.PeerID] || seenPeers[child.PeerID] || !dispatchCLIOpaque(child.DeliveryID) || deliveries[child.DeliveryID] || !outboxCLIWorkStatus(child.Status) || !outboxCLIOutcome(child.DeliveryOutcome) || child.ReasonCode != "" && !validErrorCode(child.ReasonCode) {
			return invalid()
		}
		seenPeers[child.PeerID], deliveries[child.DeliveryID] = true, true
		if (r.Admission == "prepared" || r.DeliveryOutcome == "not_attempted") && child.DeliveryOutcome != "not_attempted" || r.DeliveryOutcome == "confirmed" && child.DeliveryOutcome != "confirmed" || child.DeliveryOutcome == "not_attempted" && len(child.Files) != 0 {
			return invalid()
		}
		var childFields struct {
			Files []json.RawMessage `json:"files"`
		}
		if json.Unmarshal(rawChildren[i], &childFields) != nil {
			return invalid()
		}
		files := map[string]bool{}
		for j, file := range child.Files {
			if !outboxCLIRequiredFields(childFields.Files[j], "batchId", "fileId", "size", "sha256") {
				return invalid()
			}
			entry, exists := entries[file.FileID]
			if !exists || files[file.FileID] || file.BatchID != child.DeliveryID || file.Size != entry.Size || file.SHA256 != entry.SHA256 {
				return invalid()
			}
			files[file.FileID] = true
		}
		if child.DeliveryOutcome == "confirmed" && len(files) != len(entries) {
			return invalid()
		}
		if child.Status == "completed" && child.DeliveryOutcome != "confirmed" {
			return invalid()
		}
	}
	if r.Status == "completed" && r.DeliveryOutcome != "confirmed" {
		return invalid()
	}
	return r, nil
}

type outboxCLIList struct {
	Version    int               `json:"version"`
	Kind       string            `json:"kind"`
	Status     string            `json:"status"`
	ReasonCode string            `json:"reasonCode"`
	Items      []outboxCLIRecord `json:"items"`
}

func outboxCLIListDecode(raw []byte, ja bool) (outboxCLIList, error) {
	var list outboxCLIList
	if resource.Decode(raw, 16<<20, &list) != nil || list.Version != 1 || list.Kind != "outbox" || list.Items == nil || list.ReasonCode != "" && !validErrorCode(list.ReasonCode) {
		return list, outboxCLIInvalid(ja)
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || len(fields) != 5 {
		return list, outboxCLIInvalid(ja)
	}
	if list.Status != "ready" && list.Status != "blocked_recovery" {
		return list, outboxCLIInvalid(ja)
	}
	var envelope struct {
		Items []json.RawMessage `json:"items"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return list, outboxCLIInvalid(ja)
	}
	seen := map[string]bool{}
	for _, raw := range envelope.Items {
		item, err := outboxCLIDecode(raw, "", "", ja)
		if err != nil || seen[item.OutboxID] {
			return list, outboxCLIInvalid(ja)
		}
		seen[item.OutboxID] = true
	}
	return list, nil
}

func outboxCLIStatusLabel(ja bool, status string) string {
	labels := map[string][2]string{
		"prepared": {"Prepared locally", "ローカルに準備済み"}, "discarded": {"Preparation discarded", "準備を破棄済み"}, "queued": {"Queued", "送信待ち"}, "waiting_for_peer": {"Waiting for device", "相手の接続待ち"}, "sending": {"Sending", "送信中"}, "awaiting_receipt": {"Checking delivery", "到着状況を確認中"}, "completed": {"Saved on receiving device", "相手の端末に保存済み"}, "paused": {"Paused", "一時停止中"}, "blocked_permission": {"Permission needs review", "権限の確認が必要"}, "blocked_recovery": {"Delivery needs review", "到着状況の確認が必要"}, "cancel_requested": {"Stopping further sends", "追加の送信を停止中"}, "cancelled": {"Further sends stopped", "追加の送信を停止済み"}, "expired": {"Sending deadline passed", "送信期限切れ"},
	}
	label := labels[status]
	return text(ja, label[0], label[1])
}
func outboxCLIReason(ja bool, code string) string {
	var en, jp string
	switch code {
	case "outbox_storage_busy":
		en, jp = "Storage is busy. Check the original request status first; if it is still prepared and the confirmation remains valid, retry the exact same commit with its original process, revision and deadline. Do not create a replacement or assume nothing was sent.", "保存処理が使用中です。まず元の要求を照会し、まだ準備済みで確認が有効なら、元の起動 ID・リビジョン・期限を保って同じ commit を再試行してください。代わりの要求を作ったり、何も送られていないと決めつけないでください。"
	case "outbox_evidence_capacity":
		en, jp = "The sender's retained delivery-evidence budget is full. This is separate from active work and physical staged-file capacity. Check original requests; status and cancellation remain available. Visible history cleanup may not free this budget. Never erase uncertain evidence to admit a new send.", "送信側の保持する到着の記録の容量が満杯です。進行中の枠や実際の準備済みファイル容量とは別です。元の要求を確認してください。状態照会と停止は引き続き可能です。表示履歴の整理でこの容量が空くとは限りません。新しい送信のために結果不明の記録を消さないでください。"
	case "durable_evidence_capacity":
		en, jp = "The recipient's retained delivery-evidence budget is full. This is separate from active receives and physical file capacity. Keep the original request and check its status; receiver history cleanup may not free evidence capacity. Never erase uncertain evidence or create a replacement send to bypass this limit.", "受信側の保持する到着の記録の容量が満杯です。進行中の受信枠や実際のファイル容量とは別です。元の要求を保って状態を照会してください。受信履歴の整理で記録容量が空くとは限りません。結果不明の記録の消去や代わりの送信で、この制限を回避しないでください。"
	case "outbox_capability_unknown":
		en, jp = "Waiting to check compatibility.", "対応状況の確認待ちです。"
	case "outbox_peer_upgrade_required", "outbox_blocked_peer_upgrade_required":
		en, jp = "The receiving device cannot confirm durable delivery support. No immediate-send fallback will run.", "相手の端末の永続送信対応を確認できません。即時送信には自動で切り替えません。"
	case "outbox_permission", "outbox_identity_changed":
		en, jp = "Review the exact device identity and its permission. A changed identity needs a new review.", "正確な相手の識別情報と権限を確認してください。相手が変わった場合は新しい確認が必要です。"
	case "outbox_peer_unavailable":
		en, jp = "Waiting for the selected device to become reachable.", "選んだ相手への接続を待っています。"
	case "outbox_receipt_unknown", "outbox_recovery_required":
		en, jp = "Delivery evidence needs review. Check the original request; do not replay it as a new send.", "到着の記録を確認する必要があります。元の要求を確認し、新しい送信として再送しないでください。"
	case "outbox_clock_rollback":
		en, jp = "Check the device clock and the original sending deadline.", "端末の時計と元の送信期限を確認してください。"
	case "outbox_payload_missing", "outbox_payload_corrupt":
		en, jp = "The staged copy is missing or changed. Review the original request and any possible delivery before preparing new content.", "準備済みコピーがないか、変更されています。新しい内容を準備する前に元の要求と到着の可能性を確認してください。"
	default:
		return code
	}
	return text(ja, en, jp) + " (" + code + ")"
}
func outboxCLIOutcomeLabel(ja bool, outcome string) string {
	labels := map[string][2]string{"not_attempted": {"Not attempted", "未試行"}, "unknown": {"May already have arrived", "すでに届いている可能性があります"}, "partial": {"Some saves confirmed; inspect each recipient", "一部の保存を確認済み。相手ごとの結果を確認してください"}, "confirmed": {"Save confirmed; this does not mean read", "保存を確認済み。既読の確認ではありません"}}
	label := labels[outcome]
	return text(ja, label[0], label[1])
}
func outboxCLIShow(out io.Writer, dir string, ja bool, r outboxCLIRecord) error {
	var b strings.Builder
	deadline, _ := time.Parse(time.RFC3339Nano, r.ExpiresAt)
	fmt.Fprintf(&b, "%s: %s\n%s: %s\n%s: %s\n%s: %s\n", text(ja, "Outbox request", "送信待ち ID"), r.OutboxID, text(ja, "Work status", "処理状態"), outboxCLIStatusLabel(ja, r.Status), text(ja, "Delivery evidence", "到着の記録"), outboxCLIOutcomeLabel(ja, r.DeliveryOutcome), text(ja, "Sending deadline", "送信期限"), deadline.Local().Format(time.RFC3339Nano))
	if r.ReasonCode != "" {
		fmt.Fprintf(&b, "%s: %s\n", text(ja, "Reason", "理由"), outboxCLIReason(ja, r.ReasonCode))
	}
	if r.Admission == "prepared" && r.Status == "prepared" {
		fmt.Fprintln(&b, text(ja, "Prepared locally. No sending is authorized yet. One local staged copy is required.", "ローカルに準備済み。送信はまだ承認されていません。ローカルの準備済みコピーが必要です。"))
		for _, peer := range r.PeerIDs {
			fmt.Fprintf(&b, "  %s: %s\n", text(ja, "Exact recipient identity", "正確な宛先 ID"), displayText(peer))
		}
		for _, e := range r.Entries {
			fmt.Fprintf(&b, "  %s %s (%d bytes) %s\n", e.Kind, displayText(e.Path), e.Size, e.SHA256)
		}
		fmt.Fprintf(&b, "%s: %d; %s: %d bytes\n", text(ja, "Entries", "項目数"), len(r.Entries), text(ja, "Total per recipient", "相手1台ごとの合計"), r.TotalBytes)
		fmt.Fprintln(&b, text(ja, "Confirmation permits automatic attempts, including after restart, until this exact deadline. Both devices must run Sobalink and become reachable; receiving approval still applies. Offline compatibility may be unverified.", "承認すると、再起動後もこの正確な期限まで自動で送信を試みます。両端末で Sobalink が起動して接続可能になる必要があり、受信側の承認も必要です。オフラインでは対応状況が未確認の場合があります。"))
		fmt.Fprintln(&b, cliCommandExample(dir, "outbox", "commit", r.OutboxID, "--process", r.ProcessID, "--revision", r.ReviewRevision, "--confirm"))
		fmt.Fprintln(&b, cliCommandExample(dir, "outbox", "discard", r.OutboxID, "--process", r.ProcessID))
	} else if r.Admission == "enqueued" {
		fmt.Fprintln(&b, text(ja, "Queued on this device. Check delivery evidence for each recipient.", "この端末の送信待ちに登録済みです。相手ごとの到着の記録を確認してください。"))
		if r.DeliveryOutcome != "confirmed" {
			fmt.Fprintln(&b, text(ja, "Delivery is not yet confirmed for every recipient.", "すべての相手への到着はまだ確認できていません。"))
		}
		for _, child := range r.Children {
			fmt.Fprintf(&b, "  %s: %s | %s | %s\n", displayText(child.PeerID), child.DeliveryID, outboxCLIStatusLabel(ja, child.Status), outboxCLIOutcomeLabel(ja, child.DeliveryOutcome))
			if child.ReasonCode != "" {
				fmt.Fprintf(&b, "    %s: %s\n", text(ja, "Reason", "理由"), outboxCLIReason(ja, child.ReasonCode))
			}
			for _, file := range child.Files {
				fmt.Fprintf(&b, "    %s: %s (%d bytes) %s\n", text(ja, "Saved file", "保存済みファイル"), file.FileID, file.Size, file.SHA256)
			}
		}
		fmt.Fprintln(&b, text(ja, "Cancel stops further sends. Files already saved on receiving devices remain.", "停止は追加の送信を止めます。相手の端末に保存済みのファイルは残ります。"))
		fmt.Fprintln(&b, cliCommandExample(dir, "outbox", "cancel", r.OutboxID))
	} else {
		fmt.Fprintln(&b, text(ja, "Prepared copy discarded; no sending was authorized.", "準備済みコピーを破棄しました。送信は承認されていません。"))
	}
	fmt.Fprintln(&b, cliCommandExample(dir, "outbox", "show", r.OutboxID))
	n, err := io.WriteString(out, b.String())
	if err == nil && n != b.Len() {
		return io.ErrShortWrite
	}
	return err
}
