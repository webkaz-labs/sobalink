package core

import "strings"

// DiagnosticGuidance describes an already reported local state. It neither
// probes a target nor establishes route, application or permission success.
// Actions identify a review surface, never an operation to execute implicitly.
type DiagnosticGuidance struct {
	Code      string            `json:"code"`
	Category  string            `json:"category"`
	Action    string            `json:"action"`
	Summary   map[string]string `json:"summary"`
	NextSteps map[string]string `json:"nextSteps"`
}

func guidance(code, category, action, en, ja, nextEN, nextJA string) *DiagnosticGuidance {
	return &DiagnosticGuidance{Code: code, Category: category, Action: action,
		Summary: map[string]string{"en": en, "ja": ja}, NextSteps: map[string]string{"en": nextEN, "ja": nextJA}}
}

func networkDiagnosticGuidance(state, code string, hasError bool, mode string) *DiagnosticGuidance {
	switch code {
	case "direct_lan_recovery_required", "mixed_recovery_required", "direct_lan_state_invalid":
		return guidance(code, "recovery", "review_network", "Saved network state needs review.", "保存済みの接続状態の確認が必要です。", "Stop sobalink and inspect the saved approvals before restarting. Retrying must not reactivate uncertain permissions.", "sobalinkを停止し、保存済みの許可を確認・修正してから再起動してください。保存が不確かな許可を再試行で復活させないでください。")
	case "direct_lan_address_unavailable":
		return guidance(code, "address", "review_network", "The selected LAN address is no longer available.", "選択したLANアドレスを利用できません。", "Reconnect the selected network, or stop sobalink and review its exact endpoint. Keep the current identity and permission scope in view before reconfiguring.", "選択したネットワークへ戻すか、sobalinkを停止して正確な端点を確認してください。再設定前に現在のIDと許可範囲を確認してください。")
	case "direct_lan_address_unknown":
		return guidance(code, "unknown", "refresh_state", "The selected LAN address could not be checked.", "選択したLANアドレスを確認できませんでした。", "Refresh the state and check local interface availability. This result does not prove that the address disappeared or authorize another route.", "状態を更新し、ローカルinterfaceを確認してください。アドレスの消失を確定した結果ではなく、別経路への許可でもありません。")
	case "direct_lan_start_failed", "direct_lan_port_reserved":
		return guidance(code, "listener", "review_network", "The selected local listener could not start.", "選択したローカル待受を開始できません。", "Check that the selected address is assigned and the high port is available. Review a different port explicitly; no port has been changed automatically.", "選択したアドレスの割当と高位portの空きを確認してください。別portへの変更は明示的に確認してください。自動変更はしていません。")
	case "lan_relay_mismatch", "direct_lan_identity":
		return guidance(code, "identity", "review_network", "The peer or relay identity needs verification.", "相手または中継のIDの確認が必要です。", "Verify the exact peer key or relay address and certificate pin through the chosen private exchange. Do not bypass the mismatch or trust a discovered name.", "選択した非公開の交換方法で、正確な相手鍵または中継アドレスと証明書pinを確認してください。不一致を無視したり、発見した名前だけで信頼したりしないでください。")
	case "lan_certificate_expired", "lan_certificate_rotation_required":
		return guidance(code, "certificate", "review_network", "The relay certificate needs attention.", "中継の証明書の確認が必要です。", "Check the clock and certificate status. Follow the explicit certificate-update and pairing review before accepting a new pin.", "時計と証明書の状態を確認してください。新しいpinを受け入れる前に、明示的な証明書更新とペアリングの確認を行ってください。")
	case "direct_lan_capacity", "lan_relay_presence_capacity":
		return guidance(code, "capacity", "review_capacity", "A network resource budget was reached.", "接続用の資源上限に達しました。", "Review current usage and finite capacity settings, or wait for active work to finish. Saved peers and permissions have not been removed.", "使用量と有限の容量設定を確認するか、稼働中の処理の終了を待ってください。保存済みの相手や許可は削除していません。")
	case "lan_environment_proxy", "lan_environment_override":
		return guidance(code, "configuration", "review_network", "An incompatible process environment is selected.", "この接続方式と両立しない環境設定があります。", "Review the reported environment setting for this sobalink process, then restart explicitly. Do not change operating-system routes or router settings.", "このsobalinkプロセスに指定した環境設定を確認し、明示的に再起動してください。OSの経路やルーター設定を変更する必要はありません。")
	case "direct_lan_setup_required", "lan_policy_setup_required", "direct_lan_policy", "lan_policy_invalid", "lan_policy_relay_outside":
		return guidance(code, "configuration", "review_network", "The selected network settings need review.", "選択した接続設定の確認が必要です。", "Review the exact endpoints and allowed destinations. Correct the selected configuration without silently widening its scope.", "正確な端点と許可された送信先を確認してください。許可範囲を無断で広げずに、選択した設定を修正してください。")
	}
	if code != "" || hasError {
		if code == "" {
			code = "network_failure_unclassified"
		}
		return guidance(code, "unknown", "review_network", "The selected network has a reported problem.", "選択した接続で問題が報告されています。", "Review the reported details and selected configuration. Reachability, authentication and application health have not been established by this failure.", "報告された詳細と選択した設定を確認してください。この失敗だけでは、到達状態・認証・アプリの正常動作を確定できません。")
	}
	switch strings.ToLower(state) {
	case "needs-login", "needslogin", "needsmachineauth":
		return guidance("network_authentication_required", "authentication", "review_network", "The selected network needs sign-in or device approval.", "選択した接続にログインまたは端末承認が必要です。", "Open network setup and complete the selected account's sign-in or device approval. Saved service definitions remain available.", "接続設定を開き、選択したアカウントへのログインまたは端末承認を完了してください。保存済みサービス定義は保持されています。")
	case "starting", "stopping":
		return guidance("network_transition_pending", "availability", "wait", "The selected network is changing state.", "選択した接続の状態が切り替わっています。", "Wait for the next state update. Starting does not prove peer reachability or application success.", "次の状態更新を待ってください。開始中の表示は、相手への到達やアプリの成功を意味しません。")
	case "idle", "none", "stopped":
		if mode == "" || mode == "none" {
			return guidance("network_selection_required", "configuration", "review_network", "Choose a connection method.", "接続方式を選択してください。", "Review a LAN connection or Tailscale in network setup. Saved definitions can be managed without starting a network.", "接続設定でLAN接続またはTailscaleを確認してください。接続を開始しなくても保存済み定義を管理できます。")
		}
		return guidance("network_stopped", "availability", "review_network", "The saved network is not running.", "保存済みの接続は停止中です。", "Review the saved connection method before starting it. Saved definitions alone do not start services or renew permission.", "開始前に保存済みの接続方式を確認してください。保存済み定義だけではサービスを開始したり、許可を延長したりしません。")
	case "error", "unavailable", "unknown":
		return guidance("network_state_unconfirmed", "unknown", "refresh_state", "Current network readiness is unconfirmed.", "現在の接続準備状況は未確認です。", "Refresh the state, then review network settings if it remains unavailable. No direct or relay path has been confirmed by this state.", "状態を更新し、利用できない場合は接続設定を確認してください。この状態だけでは直接経路や中継経路を確認できません。")
	}
	return nil
}

func (c *Core) networkGuidance() *DiagnosticGuidance {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return networkDiagnosticGuidance(c.networkState, c.networkErrorCode, c.networkError != "", c.profile.Settings.Network)
}
