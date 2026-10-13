package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/webkaz-labs/sobalink/internal/core"
	"github.com/webkaz-labs/sobalink/internal/webui"
	"golang.org/x/term"
)

const nearbyMaxInput = 1536
const nearbyContactPrefix = "sobalink-setup-contact-v1:"
const nearbyVerifyPrefix = "sobalink-setup-verify-v1:"

type nearbyCLI struct {
	ctx                 context.Context
	dir                 string
	ja, machine, dryRun bool
	out                 io.Writer
	in                  io.Reader
	client              controlCaller
}
type nearbyStartInput struct {
	Alias         string `json:"alias"`
	ScopeID       string `json:"scopeId"`
	BootstrapPort uint16 `json:"bootstrapPort"`
	AllowHost     bool   `json:"allowHost"`
	RelayPort     uint16 `json:"relayPort"`
	Consent       bool   `json:"consent"`
}

func nearbyCLICommand(ctx context.Context, args []string, dir string, ja, jsonErrors, dryRun, offline bool, out io.Writer, in io.Reader, client controlCaller) error {
	help := func() error { _, err := fmt.Fprintln(out, text(ja, nearbyHelpEN, nearbyHelpJA)); return err }
	if len(args) == 1 && (args[0] == "help" || args[0] == "--help" || args[0] == "-h") {
		return help()
	}
	interactive := len(args) == 1 && args[0] == "--interactive"
	if len(args) == 0 {
		if f, ok := in.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
			interactive = true
		} else {
			return help()
		}
	}
	c := &nearbyCLI{ctx: ctx, dir: dir, ja: ja && !jsonErrors && !hasFlag(args, "json"), dryRun: dryRun, out: out, in: in, client: client}
	if offline {
		return nearbyError(c.ja, "setup_offline_unavailable")
	}
	if interactive {
		if dryRun {
			return nearbyError(c.ja, "setup_scope_required")
		}
		if os.Getenv("TEA_TRACE") != "" {
			return nearbyError(c.ja, "setup_invalid")
		}
		return c.wizard()
	}
	if len(args) == 0 {
		return help()
	}
	action := args[0]
	f := flag.NewFlagSet("nearby "+action, flag.ContinueOnError)
	f.SetOutput(io.Discard)
	f.Usage = func() {}
	f.BoolVar(&c.machine, "json", false, "")
	var session, candidate, path, output, fingerprint, alias, scope string
	var qr, pipe, verified, consent, allowHost bool
	var revision uint64
	bootstrapPort, relayPort := 54546, 54547
	switch action {
	case "scopes":
	case "start":
		f.StringVar(&alias, "alias", "", "")
		f.StringVar(&scope, "scope", "", "")
		f.IntVar(&bootstrapPort, "bootstrap-port", bootstrapPort, "")
		f.IntVar(&relayPort, "relay-port", relayPort, "")
		f.BoolVar(&allowHost, "allow-host", false, "")
		f.BoolVar(&consent, "consent", false, "")
	case "status", "contact", "verification":
		f.StringVar(&session, "session", "", "")
		f.BoolVar(&qr, "qr", false, "")
		if action == "contact" || action == "verification" {
			f.StringVar(&output, "output", "", "")
		}
	case "select":
		f.StringVar(&session, "session", "", "")
		f.StringVar(&candidate, "candidate", "", "")
	case "manual":
		f.StringVar(&session, "session", "", "")
		f.StringVar(&path, "file", "", "")
		f.BoolVar(&pipe, "stdin", false, "")
	case "approve":
		f.StringVar(&session, "session", "", "")
		f.Uint64Var(&revision, "revision", 0, "")
		f.StringVar(&fingerprint, "fingerprint", "", "")
		f.StringVar(&path, "file", "", "")
		f.BoolVar(&pipe, "stdin", false, "")
		f.BoolVar(&verified, "verified", false, "")
	case "cancel":
		f.StringVar(&session, "session", "", "")
	default:
		return nearbyError(c.ja, "setup_invalid")
	}
	if err := f.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return help()
		}
		return nearbyError(c.ja, "setup_invalid")
	}
	if f.NArg() != 0 {
		return nearbyError(c.ja, "setup_invalid")
	}
	if c.machine {
		c.ja = false
	} // JSON error text as well as codes is locale-independent.
	if session != "" && !nearbyHex(session) {
		return nearbyError(c.ja, "setup_invalid")
	}
	if action != "scopes" && action != "start" && action != "status" && session == "" {
		return nearbyError(c.ja, "setup_invalid")
	}
	if qr && c.machine {
		return nearbyError(c.ja, "setup_invalid")
	}
	var payload any = map[string]any{}
	switch action {
	case "start":
		if !nearbyAlias(alias) || !nearbyPort(bootstrapPort) || (allowHost && (!nearbyPort(relayPort) || bootstrapPort == relayPort)) || (scope != "" && !nearbyHex(scope)) {
			return nearbyError(c.ja, "setup_invalid")
		}
		if !allowHost {
			explicitRelay := false
			f.Visit(func(option *flag.Flag) {
				if option.Name == "relay-port" {
					explicitRelay = true
				}
			})
			if explicitRelay {
				return nearbyError(c.ja, "setup_invalid")
			}
			relayPort = 0
		}
		if !consent {
			return nearbyError(c.ja, "setup_consent_required")
		}
		if dryRun && scope == "" {
			return nearbyError(c.ja, "setup_scope_required")
		}
		if !dryRun {
			choices, _, err := c.scopes()
			if err != nil {
				return err
			}
			selected, err := c.chooseExplicitScope(choices, scope)
			if err != nil {
				return err
			}
			scope = selected.ScopeID
			if !c.machine {
				if err := nearbyWriteScope(out, ja, selected); err != nil {
					return err
				}
				if err := nearbyWritePermission(out, ja, alias, selected, bootstrapPort, relayPort, allowHost); err != nil {
					return err
				}
			}
		}
		payload = nearbyStartInput{alias, scope, uint16(bootstrapPort), allowHost, uint16(relayPort), consent}
	case "status", "contact", "verification", "cancel":
		payload = map[string]any{"sessionId": session}
	case "select":
		if !nearbyHex(candidate) {
			return nearbyError(c.ja, "setup_invalid")
		}
		payload = map[string]any{"sessionId": session, "candidateId": candidate}
	case "manual":
		input, err := nearbyReadInput(ctx, path, pipe, in, c.ja)
		if err != nil {
			return err
		}
		if _, err = nearbyInspectContact(input, time.Now()); err != nil {
			return nearbyError(c.ja, "setup_invalid")
		}
		payload = map[string]any{"sessionId": session, "contactText": strings.TrimSpace(input)}
	case "approve":
		if revision == 0 {
			return nearbyError(c.ja, "setup_invalid")
		}
		if fingerprint != "" && (path != "" || pipe) {
			return nearbyError(c.ja, "setup_input_required")
		}
		if fingerprint == "" {
			input, err := nearbyReadInput(ctx, path, pipe, in, c.ja)
			if err != nil {
				return err
			}
			fingerprint = strings.TrimPrefix(strings.TrimSpace(input), nearbyVerifyPrefix)
		}
		if !nearbyHex(fingerprint) {
			return nearbyError(c.ja, "setup_invalid")
		}
		if !verified {
			return nearbyError(c.ja, "setup_verification_required")
		}
		payload = map[string]any{"sessionId": session, "revision": revision, "fingerprint": fingerprint, "verified": true}
	}
	if dryRun {
		preview := payload
		if action == "manual" {
			preview = map[string]any{"sessionId": session, "contactText": "[contact input omitted]"}
		}
		previewAction := action
		if action == "contact" || action == "verification" {
			previewAction = "status"
		}
		return nearbyWriteJSON(out, map[string]any{"applied": false, "command": "setup." + previewAction, "payload": preview, "validation": "local-input-only"})
	}
	if action == "scopes" {
		scopes, raw, err := c.scopes()
		if err != nil {
			return err
		}
		if c.machine {
			return nearbyWriteJSON(out, raw)
		}
		for _, s := range scopes.Scopes {
			if err := nearbyWriteScope(out, ja, s); err != nil {
				return err
			}
		}
		if len(scopes.Scopes) == 0 {
			_, err = fmt.Fprintln(out, text(ja, "No usable local scope found. Check the intended local network; no discovery was started.", "使用可能なローカルの範囲がありません。使うネットワークを確認してください。探索は開始していません。"))
		}
		return err
	}
	if action == "approve" && !c.machine {
		if err := nearbyWriteApprovalEffect(out, c.dir, ja); err != nil {
			return err
		}
	}
	callAction := action
	if action == "contact" || action == "verification" {
		callAction = "status"
	}
	s, raw, err := c.snapshot(callAction, payload, session)
	if err != nil {
		return c.uncertain(session, err)
	}
	if action == "cancel" && !c.machine {
		s, err = c.awaitCleanup(s, 200*time.Millisecond)
		if err != nil {
			return c.uncertain(session, err)
		}
	}
	if action == "contact" {
		if s.Contact == nil || s.ContactText == "" {
			return nearbyError(c.ja, "setup_stale")
		}
		if output != "" {
			if err := nearbyWriteFile(output, s.ContactText+"\n", c.ja); err != nil {
				return err
			}
		}
		if c.machine {
			return nearbyWriteJSON(out, map[string]any{"version": 1, "kind": "sobalink-setup-contact-export", "sessionId": s.SessionID, "contact": s.Contact, "contactText": s.ContactText, "written": output != ""})
		}
		fmt.Fprintln(out, text(ja, "Public contact only; it does not prove identity or approve a connection:", "公開連絡先のみです。本人確認や接続の承認ではありません:"))
		fmt.Fprintln(out, s.ContactText)
		if output != "" {
			fmt.Fprintln(out, text(ja, "Saved to the new private file.", "新しい非公開ファイルに保存しました。"))
		}
	} else if action == "verification" {
		if s.Plan == nil || !s.Plan.ExpiresAt.After(time.Now()) {
			return nearbyError(c.ja, "setup_stale")
		}
		if output != "" {
			if err := nearbyWriteFile(output, s.Plan.VerificationText+"\n", c.ja); err != nil {
				return err
			}
		}
		if c.machine {
			return nearbyWriteJSON(out, map[string]any{"version": 1, "kind": "sobalink-setup-verification-export", "sessionId": s.SessionID, "revision": s.Plan.Revision, "fingerprint": s.Plan.Fingerprint, "verificationText": s.Plan.VerificationText, "expiresAt": s.Plan.ExpiresAt, "written": output != ""})
		}
		fmt.Fprintln(out, text(ja, "Share this complete verification line through an independent channel. Export does not approve a connection:", "独立した方法で、この照合情報の1行を全体で交換してください。書出しでは接続を承認しません:"))
		fmt.Fprintln(out, s.Plan.VerificationText)
		if output != "" {
			fmt.Fprintln(out, text(ja, "Saved to the new private file.", "新しい非公開ファイルに保存しました。"))
		}
	} else if c.machine {
		return nearbyWriteJSON(out, raw)
	} else if err := nearbyWriteHuman(out, dir, ja, s); err != nil {
		return err
	}
	if qr {
		bits := s.ContactQR
		if action != "contact" && s.Plan != nil {
			bits = s.Plan.VerificationQR
		}
		return writeDeviceCardQR(out, bits, ja)
	}
	return nil
}
func (c *nearbyCLI) call(action string, payload any) (json.RawMessage, error) {
	if err := c.ctx.Err(); err != nil {
		return nil, err
	}
	if c.dir == "" {
		dir, err := core.DefaultDir()
		if err != nil {
			return nil, nearbyError(c.ja, "setup_control_unavailable")
		}
		c.dir = dir
	}
	p, err := json.Marshal(payload)
	if err != nil {
		return nil, nearbyError(c.ja, "setup_invalid")
	}
	wire, err := json.Marshal(webui.Command{RequestID: newCLIRequestID("cli-nearby"), Name: "setup." + action, Payload: p})
	if err != nil {
		return nil, nearbyError(c.ja, "setup_invalid")
	}
	var result json.RawMessage
	if err := c.client(c.ctx, c.dir, string(wire), &result); err != nil {
		return nil, nearbyControlError(c.ja, err)
	}
	return result, nil
}
func (c *nearbyCLI) scopes() (nearbyScopes, json.RawMessage, error) {
	raw, err := c.call("scopes", map[string]any{})
	if err != nil {
		return nearbyScopes{}, nil, err
	}
	var s nearbyScopes
	if len(raw) > 128<<10 || json.Unmarshal(raw, &s) != nil || s.Kind != "sobalink-setup-scopes" || s.Version != 1 || s.Scopes == nil || len(s.Scopes) > 256 {
		return s, nil, nearbyError(c.ja, "setup_invalid_response")
	}
	seen := map[string]bool{}
	for _, scope := range s.Scopes {
		if !nearbyValidScope(scope) || seen[scope.ScopeID] {
			return s, nil, nearbyError(c.ja, "setup_invalid_response")
		}
		seen[scope.ScopeID] = true
	}
	return s, raw, nil
}
func (c *nearbyCLI) chooseExplicitScope(scopes nearbyScopes, id string) (nearbyScope, error) {
	if id == "" {
		return nearbyScope{}, nearbyError(c.ja, "setup_scope_required")
	}
	for _, s := range scopes.Scopes {
		if s.ScopeID == id {
			return s, nil
		}
	}
	return nearbyScope{}, nearbyError(c.ja, "setup_scope_unavailable")
}
func (c *nearbyCLI) snapshot(action string, payload any, session string) (nearbySnapshot, json.RawMessage, error) {
	raw, err := c.call(action, payload)
	if err != nil {
		return nearbySnapshot{}, nil, err
	}
	s, err := nearbyDecodeSnapshot(raw, session, c.ja)
	return s, raw, err
}
func (c *nearbyCLI) uncertain(session string, err error) error {
	if c.machine {
		return err
	}
	args := []string{"nearby", "status"}
	if session != "" {
		args = append(args, "--session", session)
	}
	_, writeErr := fmt.Fprintln(c.out, text(c.ja, "Check the original session in this profile; do not blindly repeat the action:", "操作を繰り返す前に、このプロファイルの元のセッションを照会してください:"), cliCommandExample(c.dir, args...))
	return errors.Join(err, writeErr)
}

// The acknowledgement requests cancellation. Only a later terminal snapshot
// confirms owner cleanup; never issue cancel again when its reply is uncertain.
func (c *nearbyCLI) awaitCleanup(s nearbySnapshot, interval time.Duration) (nearbySnapshot, error) {
	if s.State != "cancelling" {
		return s, nil
	}
	if _, err := fmt.Fprintln(c.out, text(c.ja, "Stopping setup; listener cleanup is not yet confirmed. If you interrupt this wait, check the original session again:", "接続設定を終了中です。待受の終了はまだ確認できません。この待機を中断した場合は元のセッションを再照会してください:"), cliCommandExample(c.dir, "nearby", "status", "--session", s.SessionID)); err != nil {
		return s, err
	}
	for s.State == "cancelling" {
		timer := time.NewTimer(interval)
		select {
		case <-c.ctx.Done():
			timer.Stop()
			return s, c.ctx.Err()
		case <-timer.C:
		}
		next, _, err := c.snapshot("status", map[string]any{"sessionId": s.SessionID}, s.SessionID)
		if err != nil {
			return s, err
		}
		s = next
	}
	return s, nil
}
func nearbyHex(value string) bool {
	if len(value) != 64 || value != strings.ToLower(value) || value == strings.Repeat("0", 64) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
func nearbyAlias(value string) bool {
	return len(value) >= 1 && len(value) <= 80 && utf8.ValidString(value) && strings.TrimSpace(value) == value && !strings.ContainsFunc(value, func(r rune) bool {
		return unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) || r == '\u2028' || r == '\u2029'
	})
}
func nearbyPort(port int) bool {
	return port >= 1024 && port <= 65535 && (port < 54543 || port > 54545)
}
func nearbyValidScope(scope nearbyScope) bool {
	address, ae := netip.ParseAddr(scope.LocalAddress)
	prefix, pe := netip.ParsePrefix(scope.Prefix)
	return nearbyHex(scope.ScopeID) && scope.InterfaceIndex > 0 && ae == nil && pe == nil && address.Zone() == "" && !address.Is4In6() && (address.IsPrivate() || address.IsLoopback()) && prefix == prefix.Masked() && prefix.Bits() > 0 && prefix.Contains(address) && prefix.Addr().Is4() == address.Is4()
}
func nearbyInspectContact(input string, now time.Time) (nearbyContact, error) {
	var contact nearbyContact
	invalid := errors.New("invalid setup contact")
	if len(input) > nearbyMaxInput {
		return contact, invalid
	}
	input = strings.TrimSpace(input)
	if !strings.HasPrefix(input, nearbyContactPrefix) {
		return contact, invalid
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(strings.TrimPrefix(input, nearbyContactPrefix))
	if err != nil {
		return contact, invalid
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&contact) != nil {
		return contact, invalid
	}
	canonicalContact := contact
	canonicalContact.ExpiresAt = canonicalContact.ExpiresAt.UTC()
	canonical, err := json.Marshal(canonicalContact)
	if err != nil || !bytes.Equal(raw, canonical) || contact.Kind != "sobalink-setup-contact" || contact.Version != 1 || !nearbyHex(contact.SessionID) || !nearbyAlias(contact.Alias) || !contact.ExpiresAt.After(now) || contact.ExpiresAt.After(now.Add(120*time.Second)) {
		return contact, invalid
	}
	addr, err := netip.ParseAddrPort(contact.Bootstrap)
	if err != nil || addr.String() != contact.Bootstrap || addr.Addr().Zone() != "" || addr.Addr().Is4In6() || (!addr.Addr().IsPrivate() && !addr.Addr().IsLoopback()) || !nearbyPort(int(addr.Port())) {
		return contact, invalid
	}
	return contact, nil
}
func nearbyReadInput(ctx context.Context, path string, pipe bool, in io.Reader, ja bool) (string, error) {
	if (path == "") == !pipe {
		return "", nearbyError(ja, "setup_input_required")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if path != "" {
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			return "", nearbyError(ja, "setup_input_unavailable")
		}
		if info.Size() > nearbyMaxInput {
			return "", nearbyError(ja, "setup_input_too_large")
		}
		file, err := os.Open(path)
		if err != nil {
			return "", nearbyError(ja, "setup_input_unavailable")
		}
		defer file.Close()
		opened, err := file.Stat()
		if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
			return "", nearbyError(ja, "setup_input_unavailable")
		}
		in = file
	} else if f, ok := in.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		return "", nearbyError(ja, "setup_input_required")
	}
	if in == nil {
		return "", nearbyError(ja, "setup_input_unavailable")
	}
	stop := func() bool { return false }
	if closer, ok := in.(io.Closer); ok {
		stop = context.AfterFunc(ctx, func() { _ = closer.Close() })
	}
	defer stop()
	raw, err := io.ReadAll(io.LimitReader(in, nearbyMaxInput+1))
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err != nil {
		return "", nearbyError(ja, "setup_input_unavailable")
	}
	if len(raw) > nearbyMaxInput {
		return "", nearbyError(ja, "setup_input_too_large")
	}
	return string(raw), nil
}
func nearbyWriteFile(path, value string, ja bool) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nearbyError(ja, "setup_file_exists")
	}
	_, writeErr := io.WriteString(file, value)
	syncErr := file.Sync()
	closeErr := file.Close()
	if errors.Join(writeErr, syncErr, closeErr) != nil {
		return nearbyError(ja, "setup_file_exists")
	}
	return nil
}
func nearbyDecodeSnapshot(raw []byte, session string, ja bool) (nearbySnapshot, error) {
	var s nearbySnapshot
	bad := func() (nearbySnapshot, error) { return s, nearbyError(ja, "setup_invalid_response") }
	if len(raw) > 256<<10 || json.Unmarshal(raw, &s) != nil || s.Kind != "sobalink-setup" || s.Version != 1 || s.Candidates == nil || len(s.Candidates) > 64 {
		return bad()
	}
	if !outboxCLIRequiredFields(raw, "localApproved", "remoteApproved", "paired", "connected", "trusted", "process", "discovery") {
		return bad()
	}
	if s.SessionID != "" && !nearbyHex(s.SessionID) || session != "" && s.SessionID != session {
		return bad()
	}
	switch s.State {
	case "idle", "discovering", "selecting", "authenticating", "awaiting_approval", "preparing", "connecting", "connected", "recovery_required", "cancelling", "cancelled", "expired", "stop_required", "needs_restart", "failed":
	default:
		return bad()
	}
	switch s.Process.State {
	case "ready", "stop_required", "needs_restart":
	default:
		return bad()
	}
	if !s.Process.ProfilePreserved || s.Process.ApprovalsSurviveRestart {
		return bad()
	}
	if s.Connected && (!s.Paired || !s.LocalApproved || !s.RemoteApproved || (s.State != "connected" && s.State != "cancelling")) || s.State == "connected" && !s.Connected {
		return bad()
	}
	if s.Scope != nil && !nearbyValidScope(*s.Scope) {
		return bad()
	}
	if s.SessionID == "" && s.State != "idle" && s.State != "stop_required" && s.State != "needs_restart" {
		return bad()
	}
	if s.Contact != nil {
		contact := *s.Contact
		contact.ExpiresAt = contact.ExpiresAt.UTC()
		encoded, _ := json.Marshal(contact)
		canonical := nearbyContactPrefix + base64.RawURLEncoding.EncodeToString(encoded)
		if contact.SessionID != s.SessionID || s.ContactText != canonical || contact.ExpiresAt.IsZero() {
			return bad()
		}
		// Expired sessions remain inspectable; validate structure against their
		// own lifetime instead of treating an expired contact as current consent.
		if _, err := nearbyInspectContact(canonical, contact.ExpiresAt.Add(-time.Minute)); err != nil {
			return bad()
		}
	} else if s.ContactText != "" {
		return bad()
	}
	if (s.State == "awaiting_approval" || s.LocalApproved || s.RemoteApproved || s.Connected) && s.Plan == nil {
		return bad()
	}
	for _, c := range s.Candidates {
		if !nearbyHex(c.CandidateID) || !nearbyHex(c.Contact.SessionID) || !nearbyAlias(c.Contact.Alias) || c.Verification != "unverified" {
			return bad()
		}
	}
	if s.Plan != nil {
		p := s.Plan
		if p.Revision == 0 || !nearbyHex(p.Fingerprint) || p.Verification != "full-sha256" || p.VerificationText != nearbyVerifyPrefix+p.Fingerprint || p.Backend != "lan" || !nearbyHex(p.HostKey) || !nearbyHex(p.Relay.CertificateSHA256) || p.ExpiresAt.IsZero() {
			return bad()
		}
		match := false
		for _, peer := range p.Participants {
			if !nearbyHex(peer.Key) || !nearbyHex(peer.SessionID) || !nearbyAlias(peer.Alias) {
				return bad()
			}
			if peer.SessionID == s.SessionID {
				match = true
			}
		}
		if !match || p.Participants[0].Key == p.Participants[1].Key || p.Participants[0].SessionID == p.Participants[1].SessionID {
			return bad()
		}
	}
	return s, nil
}
func nearbyWritePermission(out io.Writer, ja bool, alias string, scope nearbyScope, bootstrap, relay int, host bool) error {
	var b strings.Builder
	fmt.Fprintf(&b, text(ja, "Publish alias %s and a short-lived contact in %s. Open bootstrap %s.\n", "表示名 %s と短命の連絡先を %s 内へ公開し、接続待受 %s を開きます。\n"), displayText(alias), displayText(scope.Prefix), displayText(net.JoinHostPort(scope.LocalAddress, strconv.Itoa(bootstrap))))
	if host {
		fmt.Fprintf(&b, text(ja, "Also allow a provisional relay on %s, restricted to this reviewed setup.\n", "この確認済み接続に限定した、一時的な中継 %s も許可します。\n"), displayText(net.JoinHostPort(scope.LocalAddress, strconv.Itoa(relay))))
	} else {
		fmt.Fprintln(&b, text(ja, "This device is not permitted to host a relay. The other device needs that permission and reachability.", "この端末は中継役になりません。相手側で中継の許可と到達性が必要です。"))
	}
	fmt.Fprintln(&b, text(ja, "No OS/firewall settings change. Receiving trust remains a separate decision. Cancel requests cleanup of setup discovery and temporary listeners; check status until it finishes.", "OSやファイアウォールの設定は変更しません。受信の信頼は別の判断です。取消は探索と一時的な待受の終了処理を要求します。状態を照会して完了を確認してください。"))
	n, err := io.WriteString(out, b.String())
	if err == nil && n != b.Len() {
		return io.ErrShortWrite
	}
	return err
}
