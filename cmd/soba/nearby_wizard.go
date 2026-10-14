package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

func (c *nearbyCLI) wizard() error {
	p := newPrompts(c.in, c.out)
	p.ctx, p.ja = c.ctx, c.ja
	s, _, err := c.snapshot("status", map[string]any{}, "")
	if err != nil {
		return c.uncertain("", err)
	}
	if s.Process.State != "ready" || s.State == "stop_required" {
		return nearbyWriteHuman(c.out, c.dir, c.ja, s)
	}
	if s.State == "idle" || s.State == "cancelled" || s.State == "expired" {
		input, err := c.wizardStart(p)
		if err != nil {
			return err
		}
		s, _, err = c.snapshot("start", input, "")
		if err != nil {
			return c.uncertain("", err)
		}
	}
	for {
		if s.State == "cancelling" {
			s, err = c.awaitCleanup(s, 200*time.Millisecond)
			if err != nil {
				return c.uncertain(s.SessionID, err)
			}
		}
		if err := nearbyWriteHuman(c.out, c.dir, c.ja, s); err != nil {
			return err
		}
		if s.State == "failed" && s.NextAction == "try_again" && s.Process.State == "ready" && !s.Paired && !s.Connected {
			answer, promptErr := p.ask(text(c.ja, "Review a fresh setup on both devices? Alias, scope, relay role and permission must be chosen again [y/N]: ", "両端末で新しいセットアップを確認しますか。表示名・範囲・中継役・許可を改めて選びます [y/N]: "))
			if errors.Is(promptErr, errPromptCanceled) || promptErr == nil && !affirmativeInput(answer) {
				return nil
			}
			if promptErr != nil {
				return promptErr
			}
			input, startErr := c.wizardStart(p)
			if errors.Is(startErr, errPromptCanceled) {
				return nil
			}
			if startErr != nil {
				return startErr
			}
			s, _, err = c.snapshot("start", input, "")
			if err != nil {
				return c.uncertain("", err)
			}
			continue
		}
		if s.State == "connected" {
			next, started, err := c.wizardAnotherDevice(p, s)
			if err != nil {
				return err
			}
			if !started {
				return nil
			}
			s = next
			continue
		}
		switch s.State {
		case "cancelled", "expired", "failed", "recovery_required", "stop_required", "needs_restart":
			return nil
		}
		if s.Process.State != "ready" {
			return nil
		}
		question := text(c.ja, "Choose a device number, r to refresh, m to paste contact, x to leave running, or q to cancel: ", "端末の番号、r で更新、m で連絡先を貼付け、x で起動したまま終了、q で取消: ")
		if s.Plan != nil && !s.LocalApproved {
			question = text(c.ja, "v to compare and approve, r to refresh, x to leave running, or q to cancel: ", "v で照合と承認、r で更新、x で起動したまま終了、q で取消: ")
		}
		answer, err := p.ask(question)
		if errors.Is(err, errPromptCanceled) {
			// A cancelled/closed prompt never approves a plan. Keep uncertain results
			// observable under the same session; do not create a replacement.
			if c.ctx.Err() != nil {
				return c.ctx.Err()
			}
			canceled, _, cancelErr := c.snapshot("cancel", map[string]any{"sessionId": s.SessionID}, s.SessionID)
			if cancelErr != nil {
				return c.uncertain(s.SessionID, cancelErr)
			}
			canceled, cancelErr = c.awaitCleanup(canceled, 200*time.Millisecond)
			if cancelErr != nil {
				return c.uncertain(s.SessionID, cancelErr)
			}
			return nearbyWriteHuman(c.out, c.dir, c.ja, canceled)
		}
		if err != nil {
			return err
		}
		originalSession := s.SessionID
		switch strings.ToLower(answer) {
		case "x":
			return nil
		case "", "r":
			s, _, err = c.snapshot("status", map[string]any{"sessionId": s.SessionID}, s.SessionID)
		case "m":
			input, inputErr := p.ask(text(c.ja, "Paste only the other device's complete contact line (q returns): ", "相手の連絡先の1行を全体で貼り付けてください（q で戻る）: "))
			if errors.Is(inputErr, errPromptCanceled) {
				continue
			}
			if inputErr != nil {
				return inputErr
			}
			peer, inspectErr := nearbyInspectContact(input, time.Now())
			if inspectErr != nil {
				fmt.Fprintln(c.out, nearbyError(c.ja, "setup_invalid"))
				continue
			}
			fmt.Fprintf(c.out, text(c.ja, "Select unverified device %s at %s? This does not grant trust.\n", "未検証の端末 %s（%s）を選択しますか。信頼は許可しません。\n"), displayText(peer.Alias), displayText(peer.Bootstrap))
			if confirmErr := p.confirm(text(c.ja, "Select this contact", "この連絡先を選択する"), false); confirmErr != nil {
				if errors.Is(confirmErr, errPromptCanceled) {
					continue
				}
				return confirmErr
			}
			s, _, err = c.snapshot("manual", map[string]any{"sessionId": s.SessionID, "contactText": strings.TrimSpace(input)}, s.SessionID)
		case "v":
			if s.Plan == nil || s.LocalApproved {
				fmt.Fprintln(c.out, text(c.ja, "Refresh to inspect the current plan first.", "先に更新して現在の計画を確認してください。"))
				continue
			}
			s, err = c.wizardApprove(p, s)
		default:
			index, parseErr := strconv.Atoi(answer)
			if parseErr != nil || index < 1 || index > len(s.Candidates) {
				fmt.Fprintln(c.out, text(c.ja, "Choose a shown number, refresh, or cancel.", "表示された番号、更新、取消のいずれかを選んでください。"))
				continue
			}
			candidate := s.Candidates[index-1]
			if !candidate.ExpiresAt.After(time.Now()) {
				fmt.Fprintln(c.out, nearbyError(c.ja, "setup_stale"))
				continue
			}
			fmt.Fprintf(c.out, text(c.ja, "Select %s (%s), shown as unverified?\n", "未検証の候補 %s（%s）を選択しますか。\n"), displayText(candidate.Contact.Alias), displayText(candidate.Contact.Bootstrap))
			if confirmErr := p.confirm(text(c.ja, "Select only this device", "この端末だけを選択する"), false); confirmErr != nil {
				if errors.Is(confirmErr, errPromptCanceled) {
					continue
				}
				return confirmErr
			}
			s, _, err = c.snapshot("select", map[string]any{"sessionId": s.SessionID, "candidateId": candidate.CandidateID}, s.SessionID)
		}
		if err != nil {
			return c.uncertain(originalSession, err)
		}
	}
}
func (c *nearbyCLI) wizardStart(p *prompts) (nearbyStartInput, error) {
	scopes, _, err := c.scopes()
	if err != nil {
		return nearbyStartInput{}, err
	}
	if len(scopes.Scopes) == 0 {
		return nearbyStartInput{}, nearbyError(c.ja, "setup_scope_unavailable")
	}
	fmt.Fprintln(c.out, text(c.ja, "Choose a scope and review its network effect before starting. b goes back at the review; q cancels.", "開始前に使う範囲とネットワークへの影響を確認します。確認画面の b で戻り、q で取り消せます。"))
	for {
		alias, err := p.ask(text(c.ja, "Your display alias (shown to nearby devices): ", "この端末の表示名（近くの端末に表示されます）: "))
		if err != nil {
			return nearbyStartInput{}, err
		}
		if !nearbyAlias(alias) {
			fmt.Fprintln(c.out, nearbyError(c.ja, "setup_invalid"))
			continue
		}
		scope := scopes.Scopes[0]
		if len(scopes.Scopes) > 1 {
			for i, s := range scopes.Scopes {
				fmt.Fprintf(c.out, "%d. ", i+1)
				if err := nearbyWriteScope(c.out, c.ja, s); err != nil {
					return nearbyStartInput{}, err
				}
			}
			for {
				answer, err := p.ask(text(c.ja, "Choose the network scope number: ", "ネットワーク範囲の番号を選んでください: "))
				if err != nil {
					return nearbyStartInput{}, err
				}
				n, err := strconv.Atoi(answer)
				if err != nil || n < 1 || n > len(scopes.Scopes) {
					fmt.Fprintln(c.out, text(c.ja, "Choose one of the shown numbers.", "表示された番号から選んでください。"))
					continue
				}
				scope = scopes.Scopes[n-1]
				break
			}
		} else {
			fmt.Fprintln(c.out, text(c.ja, "One unambiguous local scope is available; review it before consenting:", "ローカルの範囲は一つです。承認前に確認してください:"))
		}
		if err := nearbyWriteScope(c.out, c.ja, scope); err != nil {
			return nearbyStartInput{}, err
		}
		fmt.Fprintln(c.out, text(c.ja,
			"For a new connection, we recommend allowing one device to relay it. Ability to host and reachability are not yet confirmed. If already connected through another host, keep using that host. Both devices declining prevents this setup from connecting.",
			"新しい接続には、どちらか一方で中継を許可することを推奨します。中継できるか、相手から到達できるかはまだ未確認です。すでに別のホスト経由で接続中なら、そのホストを使い続けてください。両方で許可しない場合、このセットアップでは接続できません。"))
		var allowHost bool
		for {
			hostAnswer, err := p.ask(text(c.ja, "Allow this device to relay this connection? y to allow, n to use the other device, q to cancel (choose y/n): ", "接続のため、この端末を中継に使うことを許可しますか。y で許可、n で相手を中継に使用、q で取消（y/n を選択）: "))
			if err != nil {
				return nearbyStartInput{}, err
			}
			if affirmativeInput(hostAnswer) {
				allowHost = true
				break
			}
			if strings.EqualFold(hostAnswer, "n") || strings.EqualFold(hostAnswer, "no") || hostAnswer == "いいえ" {
				break
			}
			fmt.Fprintln(c.out, text(c.ja, "Choose y or n explicitly. No relay permission has been granted.", "y または n を明示的に選んでください。中継の許可はまだ行っていません。"))
		}
		input := nearbyStartInput{Alias: alias, ScopeID: scope.ScopeID, BootstrapPort: 54546, AllowHost: allowHost, RelayPort: 54547}
		if !input.AllowHost {
			input.RelayPort = 0
		}
		editInputs := false
		for {
			if err := nearbyWritePermission(c.out, c.ja, input.Alias, scope, int(input.BootstrapPort), int(input.RelayPort), input.AllowHost); err != nil {
				return nearbyStartInput{}, err
			}
			if err := nearbyWriteApprovalEffect(c.out, c.dir, c.ja); err != nil {
				return nearbyStartInput{}, err
			}
			answer, err := p.ask(text(c.ja, "Start within exactly this scope? y to consent, p to edit ports, b to edit all, q to cancel [N]: ", "この正確な範囲で開始しますか。y で承認、p でポート編集、b で全体編集、q で取消 [N]: "))
			if err != nil {
				return nearbyStartInput{}, err
			}
			if strings.EqualFold(answer, "p") {
				if err := c.wizardPorts(p, &input); err != nil {
					return nearbyStartInput{}, err
				}
				continue
			}
			if strings.EqualFold(answer, "b") {
				editInputs = true
				break
			}
			if !affirmativeInput(answer) {
				return nearbyStartInput{}, errPromptCanceled
			}
			break
		}
		if editInputs {
			continue
		}
		input.Consent = true
		return input, nil
	}
}

// Editing ports is local-only. The exact new endpoints are reviewed again;
// neither corrections nor a blank answer can grant network permission.
func (c *nearbyCLI) wizardPorts(p *prompts, input *nearbyStartInput) error {
	fmt.Fprintln(c.out, text(c.ja, "Choose distinct ports from 1024–65535, excluding 54543–54545 and the local management port. Availability is checked only when starting; no automatic remapping occurs.", "1024〜65535から、54543〜54545とローカル管理用を除く重複しないポートを選んでください。利用可能かは開始時に確認し、自動変更はしません。"))
	readPort := func(question string, current uint16, other uint16) (uint16, error) {
		for {
			answer, err := p.ask(fmt.Sprintf("%s [%d]: ", question, current))
			if err != nil {
				return 0, err
			}
			port := int(current)
			if answer != "" {
				port, err = strconv.Atoi(answer)
			}
			if err != nil || !nearbyPort(port) || uint16(port) == other {
				fmt.Fprintln(c.out, nearbyError(c.ja, "setup_port_invalid"))
				continue
			}
			return uint16(port), nil
		}
	}
	bootstrap, err := readPort(text(c.ja, "Setup port (Enter keeps it, q cancels)", "セットアップ用ポート（Enter で維持、q で取消）"), input.BootstrapPort, 0)
	if err != nil {
		return err
	}
	relay := input.RelayPort
	if input.AllowHost {
		relay, err = readPort(text(c.ja, "Relay port (Enter keeps it, q cancels)", "中継用ポート（Enter で維持、q で取消）"), relay, bootstrap)
		if err != nil {
			return err
		}
	}
	input.BootstrapPort, input.RelayPort = bootstrap, relay
	return nil
}

func (c *nearbyCLI) wizardApprove(p *prompts, s nearbySnapshot) (nearbySnapshot, error) {
	original := s
	answer, err := p.ask(text(c.ja, "Independently obtain and paste the OTHER device's verification line or all 64 fingerprint digits (q returns): ", "独立した方法で相手から取得した照合情報の1行か指紋の64桁すべてを貼り付けてください（q で戻る）: "))
	if errors.Is(err, errPromptCanceled) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	fingerprint := strings.TrimPrefix(answer, nearbyVerifyPrefix)
	if !nearbyHex(fingerprint) || fingerprint != s.Plan.Fingerprint {
		fmt.Fprintln(c.out, nearbyError(c.ja, "setup_fingerprint_mismatch"))
		return s, nil
	}
	if err := nearbyWriteApprovalEffect(c.out, c.dir, c.ja); err != nil {
		return s, err
	}
	// Equality is verification input, never consent. Require a distinct explicit
	// local decision bound to the displayed revision before sending verified=true.
	if err := p.confirm(text(c.ja, "The complete fingerprints match. Connect to the displayed identity using this exact scope, relay, certificate and expiry? Receiving trust remains separate", "指紋の全桁が一致しました。表示した相手へ、この正確な範囲・中継・証明書・有効期限で接続しますか。受信の信頼は別操作です"), false); err != nil {
		if errors.Is(err, errPromptCanceled) {
			return s, nil
		}
		return s, err
	}
	latest, _, err := c.snapshot("status", map[string]any{"sessionId": s.SessionID}, s.SessionID)
	if err != nil {
		return original, err
	}
	if latest.Plan == nil || latest.Plan.Revision != s.Plan.Revision || latest.Plan.Fingerprint != s.Plan.Fingerprint || latest.Plan.VerificationText != s.Plan.VerificationText || !latest.Plan.ExpiresAt.After(time.Now()) || latest.State != "awaiting_approval" {
		return original, nearbyError(c.ja, "setup_stale")
	}
	approved, _, err := c.snapshot("approve", map[string]any{"sessionId": s.SessionID, "revision": s.Plan.Revision, "fingerprint": fingerprint, "verified": true}, s.SessionID)
	if err != nil {
		return original, err
	}
	return approved, nil
}

// A historical success is not permission to replace the receipt session or to
// start discovery again. Keep the established LAN and pair while independently
// reviewing the next setup's alias, network scope and role permission.
func (c *nearbyCLI) wizardAnotherDevice(p *prompts, original nearbySnapshot) (nearbySnapshot, bool, error) {
	if original.Process.State != "ready" {
		return original, false, nil
	}
	answer, err := p.ask(text(c.ja,
		"Connect another device? This closes only the previous setup receipt session if needed; the existing LAN connection and saved pair remain. A new setup needs fresh consent [y/N]: ",
		"別の端末を接続しますか。必要な場合は前の接続設定の確認応答セッションだけを終了し、既存のLAN接続と保存済みのペアは維持します。新しい接続設定には改めて承認が必要です [y/N]: "))
	if errors.Is(err, errPromptCanceled) {
		return original, false, nil
	}
	if err != nil {
		return original, false, err
	}
	if !affirmativeInput(answer) {
		return original, false, nil
	}
	current := original
	if current.NextAction != "start_new_setup" {
		var callErr error
		current, _, callErr = c.snapshot("cancel", map[string]any{"sessionId": original.SessionID}, original.SessionID)
		if callErr != nil {
			return original, false, c.uncertain(original.SessionID, callErr)
		}
	}
	current, err = c.awaitReceiptJoined(current, 200*time.Millisecond)
	if err != nil {
		return current, false, c.uncertain(original.SessionID, err)
	}
	var input any
	if original.Version == 2 {
		input, err = c.requestWizardStart(p)
	} else {
		input, err = c.wizardStart(p)
	}
	if errors.Is(err, errPromptCanceled) {
		return current, false, nil
	}
	if err != nil {
		return current, false, err
	}
	next, _, err := c.snapshot("start", input, "")
	if err != nil {
		// A fresh start has a new, not-yet-known session locator. Inspect the current
		// session instead of accidentally querying only the closed historical one.
		return current, false, c.uncertain("", err)
	}
	if original.Version == 2 && !nearbyV2StartMatches(next, input.(map[string]any)) {
		return next, false, c.uncertain(next.SessionID, nearbyError(c.ja, "setup_invalid_response"))
	}
	return next, true, nil
}

func (c *nearbyCLI) awaitReceiptJoined(s nearbySnapshot, interval time.Duration) (nearbySnapshot, error) {
	announced := false
	for {
		if s.Process.State != "ready" {
			return s, nearbyError(c.ja, "setup_restart_required")
		}
		if s.State == "connected" && s.NextAction == "start_new_setup" {
			return s, nil
		}
		if s.State != "connected" && s.State != "cancelling" {
			return s, nearbyError(c.ja, "setup_state_changed")
		}
		if !announced {
			if _, err := fmt.Fprintln(c.out, text(c.ja,
				"Waiting for the original setup receipt session to close. The existing LAN connection and saved pair remain; no new setup has started.",
				"元の接続設定の確認応答セッションの終了を待っています。既存のLAN接続と保存済みのペアは維持し、新しい接続設定はまだ開始していません。")); err != nil {
				return s, err
			}
			announced = true
		}
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
}
