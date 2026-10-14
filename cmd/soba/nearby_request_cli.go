package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/netip"
	"strings"
	"time"
)

type nearbyV2Target struct {
	Revision               uint64    `json:"revision"`
	RelayAddress           string    `json:"relayAddress"`
	ScopePrefix            string    `json:"scopePrefix"`
	RelayCertificateSHA256 string    `json:"relayCertificateSHA256"`
	SessionID              string    `json:"sessionId"`
	PeerKey                string    `json:"peerKey"`
	Alias                  string    `json:"alias"`
	Address                string    `json:"address"`
	ExpiresAt              time.Time `json:"expiresAt"`
	Verification           string    `json:"verification"`
	KeyStatus              string    `json:"keyStatus"`
}
type nearbyV2Request struct {
	RequestID    string    `json:"requestId"`
	PeerKey      string    `json:"peerKey"`
	Alias        string    `json:"alias"`
	Address      string    `json:"address"`
	ExpiresAt    time.Time `json:"expiresAt"`
	Verification string    `json:"verification"`
	KeyStatus    string    `json:"keyStatus"`
}

func nearbyV2Command(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "request", "inspect-target", "reject", "revoked", "reenroll":
		return true
	}
	for _, a := range args[1:] {
		if a == "--mode" || strings.HasPrefix(a, "--mode=") || a == "--request" || strings.HasPrefix(a, "--request=") {
			return true
		}
	}
	return false
}
func nearbyV2Verification(v string) bool { return v == "tofu" || v == "full-sha256" }
func nearbyV2KeyStatus(v string) bool    { return v == "new" || v == "saved" || v == "revoked" }
func nearbyV2Address(s string) bool {
	a, e := netip.ParseAddrPort(s)
	return e == nil && a.String() == s && !a.Addr().Is4In6() && a.Addr().Zone() == "" && a.Addr().IsPrivate() && !a.Addr().IsLoopback() && nearbyPort(int(a.Port()))
}
func nearbyV2ParticipantAddress(value string, outbound bool) bool {
	a, e := netip.ParseAddrPort(value)
	if e != nil || a.String() != value || a.Addr().Is4In6() || a.Addr().Zone() != "" || !a.Addr().IsPrivate() || a.Addr().IsLoopback() {
		return false
	}
	if outbound {
		return a.Port() == 0
	}
	return nearbyPort(int(a.Port()))
}
func nearbyValidV2Snapshot(s nearbySnapshot) bool {
	if !nearbyAlias(s.Alias) {
		return false
	}
	if s.Mode != "request" && s.Mode != "receive" && s.Mode != "outbound" || !nearbyV2Verification(s.Verification) {
		return false
	}
	if s.Mode == "outbound" && (s.Discovery.State == "active" || s.Contact != nil || s.ContactText != "") {
		return false
	}
	if s.RequestID != "" && !nearbyHex(s.RequestID) {
		return false
	}
	if t := s.Target; t != nil {
		prefix, err := netip.ParsePrefix(t.ScopePrefix)
		if err != nil || prefix != prefix.Masked() || prefix.Bits() == 0 || !nearbyV2Address(t.RelayAddress) || !nearbyHex(t.RelayCertificateSHA256) {
			return false
		}
		relay, _ := netip.ParseAddrPort(t.RelayAddress)
		target, _ := netip.ParseAddrPort(t.Address)
		if !prefix.Contains(relay.Addr()) || !prefix.Contains(target.Addr()) || !prefix.Addr().IsPrivate() {
			return false
		}
		if t.Revision == 0 || t.Revision > 9007199254740991 {
			return false
		}
		if !nearbyHex(t.SessionID) || !nearbyHex(t.PeerKey) || !nearbyAlias(t.Alias) || !nearbyV2Address(t.Address) || t.ExpiresAt.IsZero() || t.Verification != s.Verification || !nearbyV2KeyStatus(t.KeyStatus) {
			return false
		}
	}
	if r := s.Request; r != nil {
		if !nearbyV2Address(r.Address) && !(s.Mode == "receive" && nearbyV2ParticipantAddress(r.Address, true)) {
			return false
		}
		if s.Mode != "receive" && (s.Target == nil || r.PeerKey != s.Target.PeerKey || r.Address != s.Target.Address || r.RequestID != s.SessionID) {
			return false
		}
		if !nearbyHex(r.RequestID) || r.RequestID != s.RequestID || !nearbyHex(r.PeerKey) || !nearbyAlias(r.Alias) || r.ExpiresAt.IsZero() || r.Verification != s.Verification || !nearbyV2KeyStatus(r.KeyStatus) {
			return false
		}
	}
	if s.Contact != nil {
		if s.Contact.Version != 2 || s.Contact.SessionID != s.SessionID || !nearbyAlias(s.Contact.Alias) || !nearbyV2Address(s.Contact.Bootstrap) || s.Contact.ExpiresAt.IsZero() {
			return false
		}
	}

	if s.ContactText != "" || len(s.ContactQR) > 0 {
		return false
	}
	if p := s.Plan; p != nil {
		local, remote := 0, 0
		for _, peer := range p.Participants {
			if peer.Protocol != 2 || peer.Verification != s.Verification || !nearbyV2ParticipantAddress(peer.Bootstrap, peer.Mode == "outbound") {
				return false
			}
			if peer.Mode == "receive" {
				if !peer.AllowHost || peer.Key != p.HostKey {
					return false
				}
				remote++
			} else if peer.Mode == "request" || peer.Mode == "outbound" {
				if peer.AllowHost {
					return false
				}
				local++
			} else {
				return false
			}
			if peer.SessionID == s.SessionID && peer.Mode != s.Mode {
				return false
			}
		}
		if local != 1 || remote != 1 || !nearbyV2Address(p.Relay.Address) {
			return false
		}
		if s.Request == nil || s.RequestID == "" {
			return false
		}
		for _, peer := range p.Participants {
			if peer.SessionID == s.SessionID {
				if peer.Alias != s.Alias || s.Scope == nil || peer.Scope.Prefix != s.Scope.Prefix || peer.Scope.LocalAddress != s.Scope.LocalAddress {
					return false
				}
				continue
			}
			r := s.Request
			if peer.Key != r.PeerKey || peer.Alias != r.Alias || peer.Bootstrap != r.Address || !r.ExpiresAt.Equal(p.ExpiresAt) {
				return false
			}
			if s.Selected != nil && (s.Selected.Version != 2 || s.Selected.SessionID != peer.SessionID || s.Selected.Alias != peer.Alias || s.Selected.Bootstrap != peer.Bootstrap) {
				return false
			}
			if s.Mode == "receive" {
				if r.RequestID != peer.SessionID {
					return false
				}
			} else {
				t := s.Target
				if t == nil || t.SessionID != peer.SessionID || t.PeerKey != peer.Key || t.Alias != peer.Alias || t.Address != peer.Bootstrap || t.ScopePrefix != peer.Scope.Prefix || t.RelayAddress != p.Relay.Address || t.RelayCertificateSHA256 != p.Relay.CertificateSHA256 || !t.ExpiresAt.Equal(p.ExpiresAt) {
					return false
				}
			}
		}
	}
	return true
}
func (c *nearbyCLI) v2Command(args []string) error {
	action := args[0]
	f := flag.NewFlagSet("nearby "+action, flag.ContinueOnError)
	f.SetOutput(io.Discard)
	f.BoolVar(&c.machine, "json", false, "")
	var session, requestID, mode, verification, alias, scope, address, candidate, key, target, fingerprint string
	var consent, ack, verified, host bool
	var revision, targetRevision, expectedGeneration uint64
	var bootstrap, relay, duration int
	switch action {
	case "revoked":
	case "reenroll":
		f.StringVar(&key, "peer-key", "", "")
		f.Uint64Var(&expectedGeneration, "generation", 0, "")
		f.BoolVar(&consent, "consent", false, "")
	case "start":
		f.StringVar(&mode, "mode", "", "")
		f.StringVar(&verification, "verification", "tofu", "")
		f.StringVar(&alias, "alias", "", "")
		f.StringVar(&scope, "scope", "", "")
		f.IntVar(&bootstrap, "bootstrap-port", 48445, "")
		f.IntVar(&relay, "relay-port", 48443, "")
		f.IntVar(&duration, "expires-in", 120, "")
		f.BoolVar(&host, "allow-host", false, "")
		f.BoolVar(&consent, "consent", false, "")
	case "inspect-target":
		f.StringVar(&session, "session", "", "")
		f.StringVar(&address, "address", "", "")
		f.StringVar(&candidate, "candidate", "", "")
	case "request":
		f.StringVar(&session, "session", "", "")
		f.StringVar(&target, "target-session", "", "")
		f.Uint64Var(&targetRevision, "target-revision", 0, "")
		f.StringVar(&key, "peer-key", "", "")
		f.BoolVar(&ack, "acknowledge-first-use", false, "")
	case "approve":
		f.StringVar(&session, "session", "", "")
		f.StringVar(&requestID, "request", "", "")
		f.Uint64Var(&revision, "revision", 0, "")
		f.StringVar(&fingerprint, "fingerprint", "", "")
		f.BoolVar(&ack, "acknowledge-first-use", false, "")
		f.BoolVar(&verified, "verified", false, "")
	case "reject":
		f.StringVar(&session, "session", "", "")
		f.StringVar(&requestID, "request", "", "")
	default:
		return nearbyError(c.ja, "setup_invalid")
	}
	if e := f.Parse(args[1:]); e != nil {
		if errors.Is(e, flag.ErrHelp) {
			_, e = fmt.Fprintln(c.out, text(c.ja, nearbyV2HelpEN, nearbyV2HelpJA))
			return e
		}
		return nearbyError(c.ja, "setup_invalid")
	}
	if c.machine {
		c.ja = false
	}
	if f.NArg() != 0 {
		return nearbyError(c.ja, "setup_invalid")
	}
	var payload any
	switch action {
	case "revoked":
		payload = map[string]any{}
	case "reenroll":
		if !nearbyHex(key) || expectedGeneration == 0 || expectedGeneration > 9007199254740991 || !consent {
			return nearbyError(c.ja, "setup_invalid")
		}
		payload = map[string]any{"peerKey": key, "expectedGeneration": expectedGeneration, "consent": true}
	case "start":
		if (mode != "request" && mode != "receive" && mode != "outbound") || !nearbyV2Verification(verification) || !nearbyAlias(alias) || !nearbyHex(scope) || duration < 120 || duration > 1200 || !consent {
			return nearbyError(c.ja, "setup_invalid")
		}
		if mode == "outbound" {
			if host {
				return nearbyError(c.ja, "setup_invalid")
			}
			explicit := false
			f.Visit(func(o *flag.Flag) {
				if o.Name == "bootstrap-port" || o.Name == "relay-port" {
					explicit = true
				}
			})
			if explicit {
				return nearbyError(c.ja, "setup_invalid")
			}
			bootstrap, relay = 0, 0
		} else {
			if !nearbyPort(bootstrap) || host && (!nearbyPort(relay) || relay == bootstrap) {
				return nearbyError(c.ja, "setup_invalid")
			}
			if !host {
				relay = 0
			}
		}
		if mode == "request" && host {
			return nearbyError(c.ja, "setup_role_invalid")
		}
		if mode == "receive" && !host {
			return nearbyError(c.ja, "setup_consent_required")
		}
		payload = map[string]any{"mode": mode, "verification": verification, "alias": alias, "scopeId": scope, "bootstrapPort": bootstrap, "relayPort": relay, "allowHost": host, "consent": true, "expiresInSeconds": duration}
	case "inspect-target":
		if !nearbyHex(session) || (address == "") == (candidate == "") || address != "" && !nearbyV2Address(address) || candidate != "" && !nearbyHex(candidate) {
			return nearbyError(c.ja, "setup_invalid")
		}
		payload = map[string]any{"sessionId": session, "address": address, "candidateId": candidate}
	case "request":
		if !nearbyHex(session) || !nearbyHex(target) || !nearbyHex(key) || targetRevision == 0 || targetRevision > 9007199254740991 {
			return nearbyError(c.ja, "setup_invalid")
		}
		payload = map[string]any{"sessionId": session, "targetSessionId": target, "expectedPeerKey": key, "targetRevision": targetRevision, "tofuAcknowledged": ack}
	case "approve":
		if !nearbyHex(session) || !nearbyHex(requestID) || revision == 0 || !nearbyHex(fingerprint) || (!ack && !verified) || ack && verified {
			return nearbyError(c.ja, "setup_invalid")
		}
		payload = map[string]any{"sessionId": session, "requestId": requestID, "revision": revision, "fingerprint": fingerprint, "verified": verified, "tofuAcknowledged": ack}
	case "reject":
		if !nearbyHex(session) || !nearbyHex(requestID) {
			return nearbyError(c.ja, "setup_invalid")
		}
		payload = map[string]any{"sessionId": session, "requestId": requestID}
	}
	if c.dryRun {
		return nearbyWriteJSON(c.out, map[string]any{"applied": false, "command": "setup." + action, "payload": payload, "validation": "local-input-only"})
	}
	if action == "revoked" || action == "reenroll" {
		return c.revocationCommand(action, payload, key, expectedGeneration)
	}
	s, raw, e := c.snapshot(action, payload, session)
	if e != nil {
		return c.uncertain(session, e)
	}
	if action == "start" && !nearbyV2StartMatches(s, payload.(map[string]any)) {
		return c.uncertain(s.SessionID, nearbyError(c.ja, "setup_invalid_response"))
	}
	if c.machine {
		return nearbyWriteJSON(c.out, raw)
	}
	return nearbyWriteV2Human(c.out, c.dir, c.ja, s)
}
func nearbyWriteV2Human(out io.Writer, dir string, ja bool, s nearbySnapshot) error {
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %s\n%s: %s | %s: %s\n", text(ja, "Connection state", "接続の状態"), nearbyV2StateLabel(ja, s.State), text(ja, "Mode", "操作方式"), nearbyV2ModeLabel(ja, s.Mode), text(ja, "Identity check", "相手の確認方式"), text(ja, map[bool]string{true: "First accepted key", false: "Independent full fingerprint"}[s.Verification == "tofu"], map[bool]string{true: "初回に受け入れた鍵", false: "独立した指紋全文照合"}[s.Verification == "tofu"]))
	if s.Verification == "tofu" {
		fmt.Fprintln(&b, text(ja, "First-use acceptance remembers the key. A name alone does not verify identity. File receiving and services need separate permission.", "初回に受け入れた鍵を記憶します。名前だけでは本人確認できません。ファイル受信とサービス利用は別に許可します。"))
	}
	if s.Target != nil || s.Request != nil || s.Plan != nil {
		fmt.Fprintln(&b, text(ja, "Approving saves this exact network/relay connection and key beyond the setup deadline. It may resume on the next online start. Stopping the app does not revoke the saved pair. File receiving, autosave and services still need separate permission.", "承認すると、この正確なネットワーク・中継接続と鍵をセットアップの期限後も保存します。次のオンライン起動時に再開する場合があります。アプリの停止は保存済みペアの解除ではありません。ファイル受信、自動保存、サービスは別に許可します。"))
	}
	if s.Mode == "outbound" {
		fmt.Fprintln(&b, text(ja, "Starts connections only; no LAN listener or discovery socket. Traffic can be bidirectional once connected.", "接続の開始のみ。LAN待受や探索用socketは作りません。接続後の通信は双方向です。"))
	}
	if !s.ExpiresAt.IsZero() {
		fmt.Fprintf(&b, "%s: %s\n", text(ja, "Expires", "有効期限"), s.ExpiresAt.Local().Format(time.RFC3339))
	}
	if s.Scope != nil {
		fmt.Fprintf(&b, "%s: %s\n", text(ja, "Scope", "範囲"), displayText(s.Scope.Prefix))
	}
	if s.Contact != nil {
		fmt.Fprintf(&b, "%s: %s\n", text(ja, "Address for the other device (not identity proof)", "相手が入力する接続先（本人確認の証拠ではありません）"), displayText(s.Contact.Bootstrap))
	}
	if s.Mode == "request" && len(s.Candidates) == 0 && s.RequestID == "" {
		if s.Discovery.ReasonCode != "" {
			fmt.Fprintln(&b, nearbyError(ja, s.Discovery.ReasonCode))
		} else if s.Discovery.State == "active" {
			fmt.Fprintln(&b, text(ja, "No receiving devices found yet. Keep the other device's receiving screen open, or enter its address.", "受付中の端末はまだ見つかっていません。相手の受付画面を開くか、接続先を入力してください。"))
		} else {
			fmt.Fprintln(&b, text(ja, "No receiving devices are listed. Enter the address on the other device's receiving screen.", "受付中の端末は一覧にありません。相手の受付画面の接続先を入力してください。"))
		}
	}
	for i, d := range s.Candidates {
		fmt.Fprintf(&b, "%d  %s  %s\n", i+1, displayText(d.Contact.Alias), displayText(d.Contact.Bootstrap))
	}
	if t := s.Target; t != nil {
		fmt.Fprintf(&b, "%s: %d\n", text(ja, "Target review revision", "接続先確認の版"), t.Revision)
		fmt.Fprintf(&b, "%s: %s | %s: %s\n", text(ja, "Key status", "鍵の状態"), nearbyKeyStatusLabel(ja, t.KeyStatus), text(ja, "Effective deadline", "実際の有効期限"), t.ExpiresAt.Local().Format(time.RFC3339))
		fmt.Fprintf(&b, "%s: %s | %s: %s\n%s: %s\n", text(ja, "Recipient scope", "相手の範囲"), displayText(t.ScopePrefix), text(ja, "Relay", "中継先"), displayText(t.RelayAddress), text(ja, "Relay certificate SHA-256", "中継の証明書 SHA-256"), t.RelayCertificateSHA256)
		fmt.Fprintf(&b, "%s: %s | %s\n%s: %s\n%s: %s\n", text(ja, "Selected target", "確認中の接続先"), displayText(t.Alias), displayText(t.Address), text(ja, "Exact key", "正確な鍵"), t.PeerKey, text(ja, "Target session", "相手のセッション"), t.SessionID)
	}
	if r := s.Request; r != nil {
		fmt.Fprintf(&b, "%s: %s\n", text(ja, "Key status", "鍵の状態"), nearbyKeyStatusLabel(ja, r.KeyStatus))
		fmt.Fprintf(&b, "%s: %s\n%s: %s | %s\n", text(ja, "Request ID (not an authentication code)", "依頼ID（本人確認コードではありません）"), r.RequestID, text(ja, "Request peer", "依頼の相手"), displayText(r.Alias), r.PeerKey)
	}
	if p := s.Plan; p != nil {
		for _, peer := range p.Participants {
			fmt.Fprintf(&b, "%s: %s | %s: %s | %s: %s\n", text(ja, "Device", "端末"), displayText(peer.Alias), text(ja, "Key", "鍵"), peer.Key, text(ja, "Scope", "範囲"), displayText(peer.Scope.Prefix))
		}
		fmt.Fprintf(&b, "%s: %s\n%s: %s\n", text(ja, "Effective deadline", "実際の有効期限"), p.ExpiresAt.Local().Format(time.RFC3339), text(ja, "Relay certificate SHA-256", "中継の証明書 SHA-256"), p.Relay.CertificateSHA256)
		fmt.Fprintf(&b, "%s: %s | %s: %d\n", text(ja, "Relay", "中継先"), displayText(p.Relay.Address), text(ja, "Plan revision", "計画の版"), p.Revision)
		if p.Verification == "tofu" {
			fmt.Fprintf(&b, "%s: %s\n", text(ja, "Plan binding digest (not identity verification)", "計画の結び付け用ダイジェスト（本人確認ではありません）"), p.Fingerprint)
		}
		if p.Verification == "full-sha256" {
			fmt.Fprintln(&b, text(ja, "Compare the complete value independently before approval:", "承認前に独立した方法で全文を照合してください:"), p.VerificationText)
		}
	}
	fmt.Fprintf(&b, "%s: %s | %s: %s\n", text(ja, "Paired", "ペア保存"), nearbyYes(ja, s.Paired), text(ja, "Connected", "通信確認"), nearbyYes(ja, s.Connected))
	if s.ReasonCode != "" {
		fmt.Fprintln(&b, nearbyError(ja, s.ReasonCode))
	}
	if s.SessionID != "" {
		fmt.Fprintln(&b, cliCommandExample(dir, "nearby", "status", "--session", s.SessionID))
	}
	_, e := io.WriteString(out, b.String())
	return e
}

const nearbyV2HelpEN = `Connection requests (v2; no implicit approval)
nearby start --mode receive --alias NAME --scope ID --allow-host --consent [--expires-in 120..1200]
nearby start --mode outbound --alias NAME --scope ID --consent [--verification tofu|full-sha256]
nearby start --mode request --alias NAME --scope ID --consent
nearby inspect-target --session ID --address IP:PORT | --candidate ID
nearby request --session ID --target-session ID --peer-key KEY --target-revision N [--acknowledge-first-use]
nearby approve --session ID --request ID --revision N --fingerprint FULL (--acknowledge-first-use | --verified)
nearby reject --session ID --request ID
nearby revoked
nearby reenroll --peer-key KEY --generation N --consent
Choose review time before starting. Expired requests need a fresh session and consent.
Only numeric private addresses within the selected scope. No DNS, scans or firewall changes.
--json preserves machine fields; --dry-run validates without Core calls.`
const nearbyV2HelpJA = `接続依頼 v2（暗黙に承認しません）
nearby start --mode receive --alias NAME --scope ID --allow-host --consent [--expires-in 120..1200]
nearby start --mode outbound --alias NAME --scope ID --consent [--verification tofu|full-sha256]
nearby start --mode request --alias NAME --scope ID --consent
nearby inspect-target --session ID --address IP:PORT | --candidate ID
nearby request --session ID --target-session ID --peer-key KEY --target-revision N [--acknowledge-first-use]
nearby approve --session ID --request ID --revision N --fingerprint FULL (--acknowledge-first-use | --verified)
nearby reject --session ID --request ID
nearby revoked
nearby reenroll --peer-key KEY --generation N --consent
開始前に確認時間を選びます。期限切れ後は新しいsessionと承認が必要です。
選んだ範囲内の数値privateアドレスのみ。DNS、走査、ファイアウォール変更はしません。
--jsonは機械出力。--dry-runはCore呼出しなしの入力確認です。`

func nearbyV2ModeLabel(ja bool, mode string) string {
	switch mode {
	case "request":
		return text(ja, "Select and request", "相手を選んで依頼")
	case "receive":
		return text(ja, "Receive requests", "依頼を受け付ける")
	case "outbound":
		return text(ja, "Start connections only", "接続の開始のみ")
	}
	return displayText(mode)
}
func nearbyV2StateLabel(ja bool, state string) string {
	switch state {
	case "selecting":
		return text(ja, "Choose the receiving device", "受付中の相手を選択")
	case "receiving":
		return text(ja, "Waiting for a request", "接続依頼を待っています")
	case "requesting":
		return text(ja, "Waiting for the other device", "相手の確認を待っています")
	case "request_received":
		return text(ja, "Review the incoming request", "届いた依頼を確認してください")
	case "awaiting_approval":
		return text(ja, "Waiting for the required approval", "必要な承認を待っています")
	case "rejected":
		return text(ja, "Request declined", "依頼が拒否されました")
	}
	return nearbyStateLabel(ja, state)
}

func nearbyV2StartMatches(s nearbySnapshot, payload map[string]any) bool {
	if s.Version != 2 || s.Alias != payload["alias"] || s.Mode != payload["mode"] || s.Verification != payload["verification"] || s.Scope == nil || s.Scope.ScopeID != payload["scopeId"] || s.SessionID == "" {
		return false
	}
	if s.Mode == "outbound" {
		return s.Contact == nil && s.Discovery.State != "active"
	}
	if s.Contact == nil {
		return false
	}
	a, e := netip.ParseAddrPort(s.Contact.Bootstrap)
	return e == nil && int(a.Port()) == payload["bootstrapPort"] && a.Addr().String() == s.Scope.LocalAddress
}

func nearbyKeyStatusLabel(ja bool, value string) string {
	switch value {
	case "new":
		return text(ja, "First accepted key", "初回に受け入れる鍵")
	case "saved":
		return text(ja, "Saved key", "保存済みの鍵")
	case "revoked":
		return text(ja, "Removed key", "解除した鍵")
	}
	return displayText(value)
}
