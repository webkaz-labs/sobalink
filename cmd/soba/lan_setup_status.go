package main

import (
	"errors"
	"fmt"
	"io"
)

type humanLANStatus struct {
	Policy        *humanLANPolicy `json:"policy"`
	Configured    bool            `json:"configured"`
	ListenerReady bool            `json:"listenerReady"`
	RelayReady    bool            `json:"relayReady"`
	Relay         *struct {
		Kind    string `json:"kind"`
		Address string `json:"address"`
	} `json:"relay"`
	Certificate *struct {
		State     string `json:"state"`
		NotBefore string `json:"notBefore"`
		NotAfter  string `json:"notAfter"`
	} `json:"certificate"`
}

func writeHumanLANStatus(out io.Writer, ja bool, status *humanLANStatus) {
	if status == nil || status.Relay == nil {
		return
	}
	saved := text(ja, "not configured", "未設定")
	if status.Configured {
		saved = text(ja, "configured", "設定済み")
	}
	ready := text(ja, "not ready", "未準備")
	if status.ListenerReady {
		ready = text(ja, "ready", "準備完了")
	}
	fmt.Fprintf(out, "%s: %s; %s: %s; %s\n", text(ja, "LAN relay", "LANリレー"), displayText(status.Relay.Address), text(ja, "saved", "保存状態"), saved, text(ja, "pairing listener", "ペアリング待受")+": "+ready)
	if status.Policy != nil {
		fmt.Fprintf(out, "%s: %s\n", text(ja, "Destination policy", "接続先ポリシー"), displayText(status.Policy.Mode))
	}
	if status.Relay.Kind != "host" {
		return
	}
	hostReady := text(ja, "not running", "停止中")
	if status.RelayReady {
		hostReady = text(ja, "running", "稼働中")
	}
	fmt.Fprintf(out, "%s: %s\n", text(ja, "Hosted relay listener", "この端末のリレー待受"), hostReady)
	if status.Certificate == nil {
		return
	}
	fmt.Fprintf(out, "%s: %s\n", text(ja, "Certificate expires", "証明書の有効期限"), displayText(status.Certificate.NotAfter))
	switch status.Certificate.State {
	case "expiring":
		fmt.Fprintln(out, text(ja, "Certificate expires within 30 days. Plan explicit replacement and pairing again.", "証明書は30日以内に期限切れになります。明示的な更新と再ペアリングを予定してください。"))
	case "expired", "not-yet-valid":
		fmt.Fprintln(out, text(ja, "Certificate is outside its valid period. Check the clock before explicit replacement.", "証明書の有効期間外です。明示的な更新の前に時計を確認してください。"))
	}
}

func localizeLANSetupError(ja bool, err error) error {
	if !ja {
		return err
	}
	var coded interface{ ErrorCode() string }
	if !errors.As(err, &coded) {
		return err
	}
	messages := map[string]string{
		"mixed_selection_invalid":  "異なる接続方式を2つまたは3つ、優先順に明示してください",
		"mixed_setup_required":     "mixedを開始する前に、各方式を個別に設定し、必要なペアリング・サインインを完了してください",
		"mixed_strict_boundary":    "LAN限定の範囲から外部通信を自動追加しません。停止して各方式の通信範囲を明示的に確認してください",
		"mixed_network_required":   "確認済みのmixedネットワークを開始してから相手を関連付けてください",
		"mixed_binding_selection":  "異なる接続方式の認証済み経路を2つまたは3つ選んでください",
		"mixed_binding_unverified": "同一の相手であることを確認できませんでした。両端末の正確な経路・識別子・許可を確認してください",
		"mixed_recovery_required":  "許可の保存状態を確認できないためmixedを停止しました。保存済みの許可を確認してから再起動してください",
		"mixed_operation_failed":   "mixedの操作が完了しませんでした。各方式の準備状況と保存済み設定を確認してから再試行してください",

		"direct_lan_unavailable": "direct LANの待受が未準備です。選択した接続先を確認し、設定済みネットワークを開始してください",
		"direct_lan_pair_state":  "保存済みのdirect LANペアを確認してください。招待を置き換える前に既存のペアを解除してください",
		"direct_lan_failed":      "direct LAN操作が完了しませんでした。招待・選択した接続先・到達性を確認してください。招待を置き換える前に既存の招待を取り消してください",

		"direct_lan_qr_failed":                "非公開QRをローカル生成できませんでした。テキストの招待を作成してください",
		"direct_lan_address_unavailable":      "選択したLANアドレスは稼働中のインターフェースに割り当てられていません。元のネットワークに再接続するか、停止中に設定を明示的に変更してください",
		"direct_lan_address_unknown":          "ローカルインターフェースの利用可否を確認できません。別経路を選ぶ前にアクセス権とネットワーク状態を確認してください",
		"wan_candidates_invalid":              "重複しない正規表記の数値STUN IP:portと正の有限な探索予算を指定するか、IPv6候補を明示的に有効にしてください",
		"wan_candidates_setup_required":       "WAN候補の設定前に、証明書を固定したリレーを選択してください",
		"wan_candidates_restricted":           "LAN接続先の許可範囲を選ぶ前にWAN候補を無効にしてください。WAN候補には trusted-relay が必要です",
		"direct_lan_name_invalid":             "制御文字を含まない、空でない128バイト以下の表示名を指定してください",
		"direct_lan_backend_pending":          "direct LANの有効化にはユーザー空間netstackバックエンドの検証完了が必要です",
		"direct_lan_setup_required":           "先にdirect LANの正確な数値待受IP:ポートと許可範囲を明示して設定してください",
		"direct_lan_state_invalid":            "秘密のdirect LAN設定を読み込めません。停止し、保護された設定を確認してから再起動してください",
		"direct_lan_port_reserved":            "direct LANのポートが管理画面やプロキシと競合します。別の高位ポートを選んでください",
		"direct_lan_start_failed":             "選択したdirect LANアドレスで待受を開始できません。端末にそのIPが割り当てられ、ポートが空いていることを確認してください",
		"direct_lan_pairs_present":            "接続先や許可範囲の変更前にdirect LANペアを解除し、変更後にペアリングとアプリの信頼を別途許可してください",
		"direct_lan_remote_paired_local_save": "相手ではペアリングしましたが、この端末の保存を確認できません。停止して秘密の設定を確認し、相手側のペアも解除してから再試行してください",
		"direct_lan_pair_uncertain":           "ペアリングの応答を受信できませんでした。相手側で完了したペアがないか確認し、解除してから再試行してください",
		"direct_lan_recovery_required":        "秘密の設定保存に失敗したためdirect LANを停止しました。停止して保護された設定を確認してから再起動してください",
		"direct_lan_policy":                   "正確な数値のプライベート・ループバックIP、予約されていない高位ポート、正規表記の許可CIDRを指定してください",
		"direct_lan_identity":                 "相手の正確なdirect LAN公開IDを指定してください",
		"direct_lan_invitation_invalid":       "direct LANの招待が無効または期限切れです。この公開ID宛ての新しい招待を依頼してください",
		"direct_lan_capacity":                 "direct LANの容量上限です。不要なペアを解除するか、実行中の処理が終わってから再試行してください",

		"lan_policy_invalid":                "trusted-relay は範囲を指定せず、allowed-lan-destinations は正規表記のプライベート・ULA・ループバックCIDRを明示してください",
		"lan_policy_setup_required":         "先にLANリレーを選択するか、setup でリレーとポリシーを一緒に保存してください",
		"lan_policy_relay_outside":          "選択済みリレーと追加候補のリレーを許可範囲に含めるか、不要なリレー候補を先に削除してください",
		"lan_certificate_rotation_required": "保存済み証明書を再利用できません。時計を確認し、保存済みLANペアを解除してください。soba を停止して start --offline で起動し直し、ホスト設定に --rotate-certificate を追加してください。新しい指紋を確認して再ペアリングします",
		"lan_certificate_rotation_invalid":  "証明書の更新には、既存のローカルリレーと明示的に選んだホストアドレスが必要です",
		"lan_relay_pairs_present":           "リレーの変更や証明書の更新前に保存済みLANペアを解除してください。変更後は再ペアリングし、アプリの信頼も別途許可してください",
		"lan_certificate_expired":           "保存済みリレー証明書の有効期間外です。時計を確認してください。更新する場合は保存済みLANペアを解除し、停止・start --offline で起動し直してから --rotate-certificate を付けてホスト設定し、新しい指紋を確認して再ペアリングしてください",
		"network_restart_required":          "利用中のネットワーク・リレー・端末名や証明書を変更するには、soba を停止して start --offline で起動し直してください。現在の接続と転送は終了します",
	}
	if message := messages[coded.ErrorCode()]; message != "" {
		return &localizedDiskSpaceError{err, message}
	}
	return err
}
