package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/core"
	"github.com/webkaz-labs/sobalink/internal/resource"
	"github.com/webkaz-labs/sobalink/internal/webui"
)

// These are format bounds, not the configured (normally 16 KiB) text quota.
// Core applies the current profile's smaller admission and retention budgets.
const textQueueCLIHardTextBytes = ((16 << 20) - 4096) / 6

func textQueueCLIRequestTime(id string) (time.Time, bool) {
	parts := strings.Split(id, "_")
	if len(parts) != 3 || parts[0] != "mt1" || len(parts[1]) != 13 || !textQueueCLIHex(parts[2], 16) {
		return time.Time{}, false
	}
	ms, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || ms < 1_000_000_000_000 || strconv.FormatInt(ms, 10) != parts[1] {
		return time.Time{}, false
	}
	created := time.UnixMilli(ms).UTC()
	if created.Add(8*24*time.Hour+5*time.Minute).UnixMilli() > 9_999_999_999_999 {
		return time.Time{}, false
	}
	return created, true
}
func textQueueCLIHex(value string, size int) bool {
	raw, err := hex.DecodeString(value)
	return err == nil && len(raw) == size && hex.EncodeToString(raw) == value
}
func newTextQueueCLIRequestID(created time.Time) (string, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	id := "mt1_" + strconv.FormatInt(created.UnixMilli(), 10) + "_" + hex.EncodeToString(nonce[:])
	if _, ok := textQueueCLIRequestTime(id); !ok {
		return "", errors.New("text request timestamp is outside the supported range")
	}
	return id, nil
}
func textQueueCLIHash(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}
func textQueueCLIValidText(body string) bool {
	return len(body) <= textQueueCLIHardTextBytes && strings.TrimSpace(body) != "" && utf8.ValidString(body) && !strings.ContainsRune(body, 0)
}

// Repeated options must not silently replace a selected recipient or approval.
func textQueueCLIRepeatedFlags(args []string) bool {
	seen := map[string]bool{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" || !strings.HasPrefix(arg, "-") {
			break
		}
		key := strings.TrimLeft(strings.SplitN(arg, "=", 2)[0], "-")
		if seen[key] {
			return true
		}
		seen[key] = true
		if !strings.Contains(arg, "=") {
			switch key {
			case "peer", "peer-name", "expires-in", "request", "process", "revision", "cursor":
				i++
			}
		}
	}
	return false
}
func textQueueCLIUsage(ja bool) error {
	return &dispatchCLIError{"message_invalid", text(ja, "Invalid text queue inputs; use soba message --help. Select one recipient and a duration from 1m through 168h.", "テキスト送信待ちの入力が正しくありません。soba message --help を参照し、相手1台と 1m～168h の期間を指定してください。"), nil}
}
func textQueueCLIInvalid(ja bool) error {
	return &dispatchCLIError{"message_invalid_response", text(ja, "The text queue response did not match the original request. Check that original request before sending again.", "テキスト送信待ちの応答が元の要求と一致しません。再送する前に元の要求を照会してください。"), nil}
}
func textQueueCLIControlError(ja bool, cause error) error {
	code := "message_control_unavailable"
	var coded interface{ ErrorCode() string }
	if errors.As(cause, &coded) && validErrorCode(coded.ErrorCode()) {
		code = coded.ErrorCode()
	}
	return &dispatchCLIError{code, textQueueCLIReason(ja, code), cause}
}

type textQueueCLI struct {
	ctx                   context.Context
	dir                   string
	ja, machine           bool
	out                   io.Writer
	client                controlCaller
	id, process, revision string
	original              *core.TextOutboxRecord
}

func (c *textQueueCLI) call(action, requestID string, payload any) (json.RawMessage, error) {
	if err := c.ctx.Err(); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	wire, err := json.Marshal(webui.Command{RequestID: requestID, Name: "message.queue." + action, Payload: raw})
	if err != nil {
		return nil, err
	}
	var result json.RawMessage
	if err = c.client(c.ctx, c.dir, string(wire), &result); err != nil {
		return nil, textQueueCLIControlError(c.ja, err)
	}
	return result, nil
}

// An ambiguous reply never generates another preparation key or resolves a new
// deadline. The returned recovery locator and original input stay reviewable.
func (c *textQueueCLI) uncertain(phase string, cause error) error {
	code := "message_invalid_response"
	var coded interface{ ErrorCode() string }
	if errors.As(cause, &coded) && validErrorCode(coded.ErrorCode()) {
		code = coded.ErrorCode()
	}
	var err error
	if c.machine {
		result := map[string]any{"version": 1, "kind": "text_outbox", "requestId": c.id, "outboxId": c.id, "messageId": c.id, "observation": "unconfirmed", "phase": phase, "code": code, "nextAction": "status"}
		if c.process != "" {
			result["processId"] = c.process
		}
		if c.revision != "" {
			result["reviewRevision"] = c.revision
		}
		if c.original != nil {
			r := c.original
			result["original"] = map[string]any{"peerId": r.PeerID, "text": r.Text, "textBytes": r.TextBytes, "sha256": r.SHA256, "createdAt": r.CreatedAt, "expiresAt": r.ExpiresAt}
		}
		err = json.NewEncoder(c.out).Encode(result)
	} else {
		var b strings.Builder
		fmt.Fprintln(&b, text(c.ja, "Result not confirmed. Keep the original ID and deadline; do not prepare a replacement.", "結果を確認できません。元の ID と期限を保ち、代わりの準備を作らないでください。"))
		if c.original != nil {
			fmt.Fprintf(&b, "%s: %s\n%s: %s\n%s: %s\n", text(c.ja, "Original recipient", "元の宛先"), displayText(c.original.PeerID), text(c.ja, "Original deadline", "元の期限"), c.original.ExpiresAt.Format(time.RFC3339Nano), text(c.ja, "Original text (quoted)", "元の本文（引用表記）"), strconv.Quote(c.original.Text))
		}
		fmt.Fprintln(&b, cliCommandExample(c.dir, "message", "queue", "show", c.id))
		if c.process != "" && c.revision != "" {
			fmt.Fprintln(&b, text(c.ja, "Only after checking the original status, retry the same still-valid review if it is prepared:", "元の状態を確認し、準備済みで確認内容がまだ有効な場合にだけ、同じ確認を再試行してください："))
			fmt.Fprintln(&b, cliCommandExample(c.dir, "message", "queue", "commit", c.id, "--process", c.process, "--revision", c.revision, "--confirm"))
		}
		_, err = io.WriteString(c.out, b.String())
	}
	return errors.Join(cause, err)
}
func (c *textQueueCLI) emit(raw json.RawMessage, r core.TextOutboxRecord) error {
	if c.machine {
		return json.NewEncoder(c.out).Encode(raw)
	}
	return textQueueCLIShow(c.out, c.dir, c.ja, r, false)
}

func textQueueSendCLI(ctx context.Context, args []string, dir string, ja, dryRun bool, out io.Writer, in io.Reader, client controlCaller) error {
	f := commandFlags("message --queue --peer PEER [OPTIONS] TEXT", ja, io.Discard)
	queue := f.Bool("queue", false, "")
	peer, peerName := f.String("peer", "", ""), f.String("peer-name", "", "")
	machine, confirm := f.Bool("json", false, ""), f.Bool("confirm", false, "")
	lifetime := f.Duration("expires-in", 24*time.Hour, "")
	f.Usage = func() { fmt.Fprintln(out, text(ja, textQueueHelpEN, textQueueHelpJA)) }
	if textQueueCLIRepeatedFlags(args) {
		return textQueueCLIUsage(ja)
	}
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return textQueueCLIUsage(ja)
	}
	if !*queue || f.NArg() != 1 || !textQueueCLIValidText(f.Arg(0)) || (*peer == "") == (*peerName == "") || *peer != "" && !config.ValidPeerID(*peer) || *peerName != "" && strings.TrimSpace(*peerName) == "" || *lifetime < time.Minute || *lifetime > 7*24*time.Hour {
		return textQueueCLIUsage(ja)
	}
	body := f.Arg(0)
	created := time.Now().UTC().Truncate(time.Millisecond)
	payload := map[string]any{"text": body, "expiresAt": created.Add(*lifetime)}
	if *peer != "" {
		payload["peerId"] = *peer
	} else {
		payload["peerName"] = *peerName
	}
	if dryRun {
		return json.NewEncoder(out).Encode(map[string]any{"applied": false, "command": "message.queue.prepare", "payload": payload, "validation": "local-input-only", "requiresProcessState": true})
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var state struct {
		ProcessID string       `json:"resourceCatalogProcessId"`
		Peers     []guidedPeer `json:"peers"`
	}
	if err := client(ctx, dir, "status", &state); err != nil {
		return &dispatchCLIError{"message_control_unavailable", text(ja, "Could not read the running agent. No text preparation was requested. Start the local backend and verify its identity.", "稼働中の本体を取得できません。テキストの準備は要求していません。ローカルの接続機能を起動し、識別情報を確認してください。"), err}
	}
	if !resource.ValidDigest(state.ProcessID) {
		return textQueueCLIInvalid(ja)
	}
	if *peerName != "" {
		query := func(target any) error {
			raw, err := json.Marshal(guidedSnapshot{Peers: state.Peers})
			if err != nil {
				return err
			}
			return json.Unmarshal(raw, target)
		}
		if err := resolveNamedPeerPayload(payload, ja, query); err != nil {
			return err
		}
		*peer = payload["peerId"].(string)
	}
	// The single explicit creation boundary is after local state/peer resolution.
	created = time.Now().UTC().Truncate(time.Millisecond)
	id, err := newTextQueueCLIRequestID(created)
	if err != nil {
		return &dispatchCLIError{"message_invalid", text(ja, "Could not create a text request ID. Check the device clock; no preparation was requested.", "テキスト要求 ID を作成できません。端末の時計を確認してください。準備は要求していません。"), err}
	}
	expires := created.Add(*lifetime)
	payload["processId"], payload["expiresAt"] = state.ProcessID, expires
	expected := core.TextOutboxRecord{OutboxID: id, RequestID: id, MessageID: id, PeerID: *peer, ProcessID: state.ProcessID, Text: body, TextBytes: int64(len(body)), SHA256: textQueueCLIHash(body), CreatedAt: created, ExpiresAt: expires}
	c := textQueueCLI{ctx: ctx, dir: dir, ja: ja, machine: *machine, out: out, client: client, id: id, process: state.ProcessID, original: &expected}
	raw, err := c.call("prepare", id, payload)
	if err != nil {
		return c.uncertain("prepare", err)
	}
	review, err := textQueueCLIDecode(raw, c.process, id, ja, false)
	if err != nil || review.Status != "prepared" || !textQueueCLISameOriginal(expected, review) || review.Text != body {
		return c.uncertain("prepare", textQueueCLIInvalid(ja))
	}
	c.revision = review.ReviewRevision
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
		question := fmt.Sprintf(text(ja, "Queue this exact text for %s until %s?", "この正確な本文を %s へ %s まで送信予約しますか？"), displayText(review.PeerID), review.ExpiresAt.Local().Format(time.RFC3339Nano))
		if err := p.confirm(question, false); err != nil {
			if ctx.Err() != nil {
				return err
			}
			raw, discardErr := c.call("discard", newCLIRequestID("cli-text"), map[string]any{"outboxId": id, "processId": c.process})
			if discardErr != nil {
				return c.uncertain("discard", errors.Join(discardErr, err))
			}
			discarded, decodeErr := textQueueCLIDecode(raw, c.process, id, ja, false)
			if decodeErr != nil || discarded.Status != "discarded" || !textQueueCLISameReview(review, discarded) {
				return c.uncertain("discard", textQueueCLIInvalid(ja))
			}
			return errors.Join(err, c.emit(raw, discarded))
		}
	}
	raw, err = c.call("commit", newCLIRequestID("cli-text"), map[string]any{"outboxId": id, "processId": c.process, "reviewRevision": c.revision, "confirm": true})
	if err != nil {
		return c.uncertain("commit", err)
	}
	record, err := textQueueCLIDecode(raw, "", id, ja, false)
	if err != nil || record.Admission != "enqueued" || !textQueueCLISameReview(review, record) {
		return c.uncertain("commit", textQueueCLIInvalid(ja))
	}
	return c.emit(raw, record)
}

func textQueueCommandCLI(ctx context.Context, args []string, dir string, ja, dryRun bool, out io.Writer, client controlCaller) error {
	if len(args) == 0 || len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		_, err := fmt.Fprintln(out, text(ja, textQueueHelpEN, textQueueHelpJA))
		return err
	}
	action, args := args[0], args[1:]
	switch action {
	case "list", "show", "status", "commit", "cancel", "discard":
	default:
		return textQueueCLIUsage(ja)
	}
	f := commandFlags("message queue "+action, ja, io.Discard)
	machine := f.Bool("json", false, "")
	process, revision, request, cursor := "", "", "", ""
	confirm := false
	if action == "commit" || action == "discard" {
		f.StringVar(&process, "process", "", "")
	}
	if action == "commit" {
		f.StringVar(&revision, "revision", "", "")
		f.BoolVar(&confirm, "confirm", false, "")
	}
	if action == "show" || action == "status" {
		f.StringVar(&request, "request", "", "")
	}
	if action == "list" {
		f.StringVar(&cursor, "cursor", "", "")
		f.StringVar(&revision, "revision", "", "")
	}
	f.Usage = func() { fmt.Fprintln(out, text(ja, textQueueHelpEN, textQueueHelpJA)) }
	id := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		id, args = args[0], args[1:]
	}
	if textQueueCLIRepeatedFlags(args) {
		return textQueueCLIUsage(ja)
	}
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return textQueueCLIUsage(ja)
	}
	if id == "" && f.NArg() == 1 {
		id = f.Arg(0)
	} else if f.NArg() != 0 {
		return textQueueCLIUsage(ja)
	}
	empty := false
	f.Visit(func(v *flag.Flag) {
		if v.Name != "json" && v.Name != "confirm" && v.Value.String() == "" {
			empty = true
		}
	})
	if empty || id != "" && request != "" {
		return textQueueCLIUsage(ja)
	}
	if request != "" {
		id = request
	}
	if action == "list" {
		if id != "" || (cursor == "") != (revision == "") {
			return textQueueCLIUsage(ja)
		}
		if cursor != "" {
			if _, ok := textQueueCLIRequestTime(cursor); !ok || !resource.ValidDigest(revision) {
				return textQueueCLIUsage(ja)
			}
		}
	} else if _, ok := textQueueCLIRequestTime(id); !ok {
		return textQueueCLIUsage(ja)
	}
	if (action == "commit" || action == "discard") && !resource.ValidDigest(process) || action == "commit" && (!confirm || !resource.ValidDigest(revision)) {
		return textQueueCLIUsage(ja)
	}
	payload := map[string]any{}
	if id != "" {
		payload["outboxId"] = id
	}
	if process != "" {
		payload["processId"] = process
	}
	if action == "commit" {
		payload["reviewRevision"], payload["confirm"] = revision, true
	}
	if action == "list" && cursor != "" {
		payload["cursor"], payload["revision"] = cursor, revision
	}
	wireAction := action
	if action == "show" {
		wireAction = "status"
	}
	if dryRun {
		return json.NewEncoder(out).Encode(map[string]any{"applied": false, "command": "message.queue." + wireAction, "payload": payload, "validation": "local-input-only"})
	}
	c := textQueueCLI{ctx: ctx, dir: dir, ja: ja, machine: *machine, out: out, client: client, id: id, process: process, revision: revision}
	raw, err := c.call(wireAction, newCLIRequestID("cli-text"), payload)
	if err != nil {
		if action == "list" {
			return err
		}
		return c.uncertain(wireAction, err)
	}
	if action == "list" {
		page, err := textQueueCLIListDecode(raw, cursor, revision, ja)
		if err != nil {
			return err
		}
		if *machine {
			return json.NewEncoder(out).Encode(raw)
		}
		if page.Status == "blocked" {
			_, err := fmt.Fprintln(out, textQueueCLIReason(ja, page.ReasonCode))
			if err != nil {
				return err
			}
		}
		if len(page.Items) == 0 {
			_, err := fmt.Fprintln(out, text(ja, "No text queue records in this page.", "このページにテキスト送信待ちの記録はありません。"))
			return err
		}
		for _, item := range page.Items {
			if err := textQueueCLIShow(out, dir, ja, item.TextOutboxRecord, true); err != nil {
				return err
			}
		}
		if page.NextCursor != "" {
			_, err := fmt.Fprintln(out, cliCommandExample(dir, "message", "queue", "list", "--cursor", page.NextCursor, "--revision", page.Revision))
			return err
		}
		return nil
	}
	record, err := textQueueCLIDecode(raw, process, id, ja, false)
	if err != nil {
		return c.uncertain(wireAction, err)
	}
	if action == "commit" && (record.Admission != "enqueued" || record.ReviewRevision != revision) || action == "discard" && record.Status != "discarded" {
		return c.uncertain(wireAction, textQueueCLIInvalid(ja))
	}
	if action == "cancel" {
		if record.Admission != "enqueued" {
			return c.uncertain(wireAction, textQueueCLIInvalid(ja))
		}
		switch record.Status {
		case "cancel_requested", "cancelled", "completed", "expired":
		default:
			return c.uncertain(wireAction, textQueueCLIInvalid(ja))
		}
	}
	return c.emit(raw, record)
}
