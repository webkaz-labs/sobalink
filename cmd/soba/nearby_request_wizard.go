package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// requestWizard shares the exact session/target/revision binding used by the
// explicit commands. Refresh never starts a replacement or grants consent.
func (c *nearbyCLI) requestWizard() error {
	p := newPrompts(c.in, c.out)
	p.ctx, p.ja = c.ctx, c.ja
	s, _, e := c.snapshot("status", map[string]any{}, "")
	if e != nil {
		return c.uncertain("", e)
	}
	if s.SessionID != "" && s.Version == 1 && !nearbyV2Terminal(s) {
		return c.wizard()
	}
	if s.Process.State != "ready" {
		return nearbyWriteHuman(c.out, c.dir, c.ja, s)
	}
	if s.State == "connected" {
		if e := nearbyWriteHuman(c.out, c.dir, c.ja, s); e != nil {
			return e
		}
		next, started, err := c.wizardAnotherDevice(p, s)
		if err != nil || !started {
			return err
		}
		s = next
	}
	if s.SessionID != "" && (s.State == "expired" || s.State == "cancelled" || s.State == "rejected") {
		if err := nearbyWriteHuman(c.out, c.dir, c.ja, s); err != nil {
			return err
		}
	}
	if s.SessionID == "" || s.State == "idle" || s.State == "cancelled" || s.State == "expired" || s.State == "rejected" || s.State == "failed" && s.NextAction == "try_again" && !s.Paired && !s.Connected {
		payload, e := c.requestWizardStart(p)
		if e != nil {
			if errors.Is(e, errPromptCanceled) {
				return nil
			}
			return e
		}
		s, _, e = c.snapshot("start", payload, "")
		if e != nil {
			return c.uncertain("", e)
		}
		if !nearbyV2StartMatches(s, payload) {
			return c.uncertain(s.SessionID, nearbyError(c.ja, "setup_invalid_response"))
		}
	}
	for {
		if e = nearbyWriteHuman(c.out, c.dir, c.ja, s); e != nil {
			return e
		}
		if nearbyV2Terminal(s) {
			return nil
		}
		if s.State == "cancelling" {
			s, e = c.awaitCleanup(s, 200*time.Millisecond)
			if e != nil {
				return c.uncertain(s.SessionID, e)
			}
			continue
		}
		question := nearbyV2Actions(c.ja, s)
		answer, askErr := p.ask(question)
		if errors.Is(askErr, errPromptCanceled) {
			answer = "q"
		} else if askErr != nil {
			return askErr
		}
		if c.ctx.Err() != nil {
			return c.uncertain(s.SessionID, c.ctx.Err())
		}
		original := s.SessionID
		switch strings.ToLower(answer) {
		case "x":
			return nil
		case "", "r":
			s, _, e = c.snapshot("status", map[string]any{"sessionId": original}, original)
		case "q":
			s, _, e = c.snapshot("cancel", map[string]any{"sessionId": original}, original)
			if e == nil {
				s, e = c.awaitCleanup(s, 200*time.Millisecond)
			}
			if e != nil {
				return c.uncertain(original, e)
			}
			return nearbyWriteHuman(c.out, c.dir, c.ja, s)
		case "m":
			if s.Mode == "receive" || s.RequestID != "" {
				fmt.Fprintln(c.out, text(c.ja, "This session is receiving or already has a request.", "このセッションは受付中か、既に依頼があります。"))
				continue
			}
			addr, err := p.ask(text(c.ja, "Enter the IP:port on the other device's receiving screen (q returns): ", "相手の受付画面にあるIP:ポートを入力（qで戻る）: "))
			if errors.Is(err, errPromptCanceled) {
				continue
			}
			if err != nil {
				return err
			}
			if !nearbyV2Address(addr) {
				fmt.Fprintln(c.out, nearbyError(c.ja, "setup_invalid"))
				continue
			}
			s, _, e = c.snapshot("inspect-target", map[string]any{"sessionId": original, "address": addr}, original)
		case "s":
			if s.Target == nil || s.RequestID != "" || !s.Target.ExpiresAt.After(time.Now()) {
				fmt.Fprintln(c.out, text(c.ja, "Select and inspect a current target first.", "先に現在の接続先を選んで確認してください。"))
				continue
			}
			t := *s.Target
			fmt.Fprintf(c.out, text(c.ja, "Request %s, key %s, effective expiry %s.\n", "%s、鍵 %s、有効期限 %s への依頼です。\n"), displayText(t.Alias), t.PeerKey, t.ExpiresAt.Local().Format(time.RFC3339))
			prompt := text(c.ja, "Send this exact request; independent full verification is still required", "この相手へ依頼する。独立した全文照合はこの後に必要")
			if s.Verification == "tofu" {
				fmt.Fprintln(c.out, text(c.ja, "First-use key acceptance is not independent identity verification.", "初回の鍵の受入は独立した本人確認ではありません。"))
				prompt = text(c.ja, "Send this exact request and remember the accepted key", "この正確な相手へ依頼し、承認された鍵を記憶する")
			}
			if err := p.confirm(prompt, false); err != nil {
				if errors.Is(err, errPromptCanceled) {
					continue
				}
				return err
			}
			s, _, e = c.snapshot("request", map[string]any{"sessionId": original, "targetSessionId": t.SessionID, "expectedPeerKey": t.PeerKey, "targetRevision": t.Revision, "tofuAcknowledged": s.Verification == "tofu"}, original)
		case "a":
			if s.Plan == nil || s.LocalApproved || s.RequestID == "" {
				fmt.Fprintln(c.out, text(c.ja, "Refresh until the exact plan is ready for review.", "現在の計画が確認できるまで更新してください。"))
				continue
			}
			plan := *s.Plan
			verified := false
			if s.Verification == "full-sha256" {
				value, err := p.ask(text(c.ja, "Compare independently, then paste the other device's complete fingerprint (q returns): ", "独立して照合し、相手の指紋全文を貼り付けてください（qで戻る）: "))
				if errors.Is(err, errPromptCanceled) {
					continue
				}
				if err != nil {
					return err
				}
				if strings.TrimPrefix(strings.TrimSpace(value), nearbyVerifyPrefix) != plan.Fingerprint {
					fmt.Fprintln(c.out, nearbyError(c.ja, "setup_fingerprint_mismatch"))
					continue
				}
				verified = true
			}
			if err := p.confirm(text(c.ja, "Approve and save this exact request; application permissions stay separate", "この正確な依頼を承認して保存する。アプリの権限は別途許可"), false); err != nil {
				if errors.Is(err, errPromptCanceled) {
					continue
				}
				return err
			}
			s, _, e = c.snapshot("approve", map[string]any{"sessionId": original, "requestId": s.RequestID, "revision": plan.Revision, "fingerprint": plan.Fingerprint, "verified": verified, "tofuAcknowledged": s.Verification == "tofu"}, original)
		case "n":
			if s.Mode != "receive" || s.Request == nil {
				fmt.Fprintln(c.out, text(c.ja, "No incoming request to decline.", "拒否する受信依頼がありません。"))
				continue
			}
			s, _, e = c.snapshot("reject", map[string]any{"sessionId": original, "requestId": s.Request.RequestID}, original)
		default:
			n, err := strconv.Atoi(answer)
			if err != nil || n < 1 || n > len(s.Candidates) || s.Mode == "receive" || s.RequestID != "" {
				fmt.Fprintln(c.out, nearbyError(c.ja, "setup_invalid"))
				continue
			}
			s, _, e = c.snapshot("inspect-target", map[string]any{"sessionId": original, "candidateId": s.Candidates[n-1].CandidateID}, original)
		}
		if e != nil {
			return c.uncertain(original, e)
		}
	}
}
func nearbyV2Terminal(s nearbySnapshot) bool {
	switch s.State {
	case "connected", "cancelled", "expired", "failed", "recovery_required", "stop_required", "needs_restart", "rejected":
		return true
	}
	return false
}
func (c *nearbyCLI) requestWizardStart(p *prompts) (map[string]any, error) {
	for {
		mode, e := p.ask(text(c.ja, "1 connect on this network / 2 receive requests / 3 start connections only (no LAN listener) / 4 removed keys / q back: ", "1 このネットワークの端末へ接続 / 2 接続依頼を受け付ける / 3 接続の開始のみ（LAN待受なし） / 4 解除した鍵 / q 戻る: "))
		if e != nil {
			return nil, e
		}
		if mode == "4" {
			if err := c.reenrollWizard(p); err != nil {
				return nil, err
			}
			continue
		}
		modes := map[string]string{"1": "request", "2": "receive", "3": "outbound"}
		mode, ok := modes[mode]
		if !ok {
			continue
		}
		alias, e := p.ask(text(c.ja, "Device display name: ", "この端末の表示名: "))
		if e != nil {
			return nil, e
		}
		if !nearbyAlias(alias) {
			fmt.Fprintln(c.out, nearbyError(c.ja, "setup_invalid"))
			continue
		}
		scopes, _, e := c.scopes()
		if e != nil {
			return nil, e
		}
		if len(scopes.Scopes) == 0 {
			return nil, nearbyError(c.ja, "setup_scope_unavailable")
		}
		for i, scope := range scopes.Scopes {
			fmt.Fprintf(c.out, "%d  %s  %s\n", i+1, displayText(scope.Interface), displayText(scope.Prefix))
		}
		choice, e := p.ask(text(c.ja, "Network scope number (q back): ", "使うネットワーク範囲の番号（qで戻る）: "))
		if e != nil {
			return nil, e
		}
		n, e := strconv.Atoi(choice)
		if e != nil || n < 1 || n > len(scopes.Scopes) {
			continue
		}
		scope := scopes.Scopes[n-1]
		seconds, e := p.ask(text(c.ja, "Review time in seconds, 120..1200 (1200 gives 20 minutes; default 120): ", "確認時間を120〜1200秒で指定（1200なら20分。空欄は120秒）: "))
		if e != nil {
			return nil, e
		}
		duration := 120
		if seconds != "" {
			duration, e = strconv.Atoi(seconds)
			if e != nil || duration < 120 || duration > 1200 {
				continue
			}
		}
		method, e := p.ask(text(c.ja, "1 remember the first accepted key / 2 independently compare the full fingerprint: ", "1 初回に受け入れた鍵を記憶 / 2 独立した方法で指紋全文を照合: "))
		if e != nil {
			return nil, e
		}
		verification := "tofu"
		if method == "2" {
			verification = "full-sha256"
		} else if method != "1" {
			continue
		}
		bootstrap, relay := 0, 0
		host := mode == "receive"
		if mode != "outbound" {
			bootstrap = 48445
		}
		if host {
			relay = 48443
		}
		if mode == "outbound" {
			fmt.Fprintln(c.out, text(c.ja, "No LAN listener or discovery socket. Enter the receiving device's address; the selected scope still limits destinations.", "LAN待受と探索socketは作りません。相手の受付画面の接続先を入力します。接続先は選択した範囲内だけです。"))
		} else {
			nearbyWritePermission(c.out, c.ja, alias, scope, bootstrap, relay, host)
		}
		fmt.Fprintf(c.out, text(c.ja, "Review expires after %d seconds; a shorter peer deadline may apply. No approval survives expiry or restart.\n", "確認期限は%d秒です。相手の期限が短ければそちらが適用されます。期限切れや再起動後に承認は引き継ぎません。\n"), duration)
		if verification == "tofu" {
			fmt.Fprintln(c.out, text(c.ja, "Names do not prove identity. First-use acceptance has weaker protection against an impersonator than independent verification.", "名前は本人確認になりません。初回の受入は、独立した照合よりなりすましへの防御が弱くなります。"))
		}

		if mode == "outbound" {
			if e = p.confirm(text(c.ja, "Start within exactly this scope and role", "この正確な範囲と役割で開始する"), false); e != nil {
				return nil, e
			}
		} else {
			for {
				answer, err := p.ask(text(c.ja, "Start within exactly this scope and role [y/N], or p to edit ports: ", "この正確な範囲と役割で開始 [y/N]、pでポートを編集: "))
				if err != nil {
					return nil, err
				}
				if affirmativeInput(answer) {
					break
				}
				if strings.ToLower(answer) != "p" {
					return nil, errPromptCanceled
				}
				nextBootstrap, nextRelay, err := c.requestWizardPorts(p, bootstrap, relay, host)
				if errors.Is(err, errPromptCanceled) {
					continue
				}
				if err != nil {
					return nil, err
				}
				bootstrap, relay = nextBootstrap, nextRelay
				if err = nearbyWritePermission(c.out, c.ja, alias, scope, bootstrap, relay, host); err != nil {
					return nil, err
				}
			}
		}
		return map[string]any{"mode": mode, "verification": verification, "expiresInSeconds": duration, "alias": alias, "scopeId": scope.ScopeID, "bootstrapPort": bootstrap, "relayPort": relay, "allowHost": host, "consent": true}, nil
	}
}

func nearbyV2Actions(ja bool, s nearbySnapshot) string {
	var actions []string
	if s.Mode != "receive" && s.RequestID == "" {
		if len(s.Candidates) > 0 {
			actions = append(actions, text(ja, "Device number", "端末番号"))
		}
		actions = append(actions, text(ja, "m enter address", "m 接続先入力"))
		if s.Target != nil {
			actions = append(actions, text(ja, "s send reviewed request", "s 確認した依頼を送信"))
		}
	}
	if s.Plan != nil && !s.LocalApproved && s.RequestID != "" {
		actions = append(actions, text(ja, "a approve exact plan", "a この計画を承認"))
	}
	if s.Mode == "receive" && s.Request != nil {
		actions = append(actions, text(ja, "n decline", "n 拒否"))
	}
	actions = append(actions, text(ja, "r refresh", "r 更新"), text(ja, "x leave running", "x 継続して画面を閉じる"), text(ja, "q cancel", "q 取消"))
	return strings.Join(actions, " / ") + ": "
}

func (c *nearbyCLI) requestWizardPorts(p *prompts, bootstrap, relay int, host bool) (int, int, error) {
	for {
		answer, err := p.ask(fmt.Sprintf(text(c.ja, "Bootstrap port (Enter keeps %d; q returns): ", "接続待受のポート（空欄は%d、qで戻る）: "), bootstrap))
		if err != nil {
			return bootstrap, relay, err
		}
		next := bootstrap
		if answer != "" {
			next, err = strconv.Atoi(answer)
		}
		if err != nil || !nearbyPort(next) {
			fmt.Fprintln(c.out, nearbyError(c.ja, "setup_invalid"))
			continue
		}
		nextRelay := relay
		if host {
			answer, err = p.ask(fmt.Sprintf(text(c.ja, "Relay port (Enter keeps %d; q returns): ", "中継のポート（空欄は%d、qで戻る）: "), relay))
			if err != nil {
				return bootstrap, relay, err
			}
			if answer != "" {
				nextRelay, err = strconv.Atoi(answer)
			}
			if err != nil || !nearbyPort(nextRelay) || next == nextRelay {
				fmt.Fprintln(c.out, nearbyError(c.ja, "setup_invalid"))
				continue
			}
		}
		return next, nextRelay, nil
	}
}
