package main

// Service discovery presentation is localized separately from its stable DTOs.
func init() {
	for source, target := range map[string]string{
		"Checking services shared with this node...":                       "このノード向けに共有中のサービスを確認しています…",
		"Saved configuration. Check running state with: %s status":         "保存済みの設定です。実行中の状態は %s status で確認してください",
		"%s (%s, saved configuration):":                                    "%s（%s、保存済みの設定）:",
		"  Service discovery: enabled while sharing (allowed peers only).": "  サービス発見: 共有中に有効（許可した相手のみ）",
		"  Service discovery: disabled.":                                   "  サービス発見: 無効",
		"only share rules may advertise discovery metadata":                "サービス発見の情報を通知できるのは共有の設定だけです",
		"tailnet service information unavailable; sign in and retry":       "tailnet のサービス情報を取得できません。サインイン後に再試行してください",
		"invalid discovery peer ID":                                        "サービス発見の相手 ID が無効です",
		"service discovery is already running; retry shortly":              "サービス発見を実行中です。少し待って再試行してください",
		"Service discovery is unavailable for this node. Sharing may still work with manual configuration; check the discovery listener and tailnet permissions.": "このノードのサービス発見を利用できません。手動設定なら共有を利用できる場合があります。サービス発見の待受と tailnet のアクセス許可を確認してください",
		"advanced: choose a peer and service port manually":                                                                                  "詳細設定: 相手とサービスのポートを手動で選びます",
		"do not announce service metadata to allowed peers":                                                                                  "許可した相手へのサービス情報の通知を無効にします",
		"allow service discovery with --confirm after reviewing metadata scope":                                                              "通知範囲を確認し、--confirm でもサービス発見を有効にします",
		"choose either --discoverable or --no-discovery":                                                                                     "--discoverable と --no-discovery はどちらか一方を指定してください",
		"Shared-service discovery is unavailable. Refresh to retry, or choose manual configuration for a known service.":                     "共有サービスを確認できません。更新して再試行するか、既知のサービスを手動で設定してください",
		"Choose service number, r refresh, m manual, or q cancel: ":                                                                          "サービスの番号、r 更新、m 手動設定、q キャンセル: ",
		"Manual configuration: the remote service has not been confirmed by discovery.":                                                      "手動設定: 相手のサービスの共有状態はサービス発見で確認していません",
		"Choose a displayed service number, r to refresh, m for manual configuration, or q to cancel.":                                       "表示されたサービスの番号、r 更新、m 手動設定、q キャンセルのいずれかを入力してください",
		"This sharing observation is stale or expired. Refreshing the service list; choose again.":                                           "共有の確認結果が古いか、有効期限が切れています。一覧を更新するので、選び直してください",
		"Services shared with this node (authenticated recent responses):":                                                                   "このノード向けに共有中のサービス（認証済みの最近の応答）:",
		"   Checked: %s | Sharing expires: %s":                                                                                               "   確認時刻: %s | 共有の有効期限: %s",
		"No currently confirmed shares for this node. The other peer may need to start sharing and allow this node; refresh after checking.": "このノード向けの共有を現在確認できません。相手側で共有の開始と、このノードへの許可が必要な場合があります。確認後に更新してください",
		"Discovery unconfirmed: %d peers; unsupported: %d peers. This does not establish that their services are stopped.":                   "サービス発見の応答未確認: %d 台、非対応: %d 台。サービスが停止中とは限りません",
		"The discovery scan was limited. Refresh, or use manual configuration for a known service.":                                          "サービス発見の確認範囲が上限に達しました。更新するか、既知のサービスを手動で設定してください",
		"Application behavior is unverified. Ordinary Tailscale services and older bridges remain available through manual configuration.":   "アプリの動作は未確認です。通常の Tailscale サービスや旧版の bridge には手動設定で接続できます",
		"selected share could not be rechecked; refresh and select it again before saving or starting":                                       "選んだ共有を再確認できません。保存・開始する前に更新して選び直してください",
		"selected share changed, expired or is no longer available to this node; refresh and select again":                                   "選んだ共有が変更・期限切れ、またはこのノードから利用できなくなりました。更新して選び直してください",
		"Selected sharing observation checked: %s; sharing expires: %s. Application behavior remains unverified.":                            "選んだ共有の確認時刻: %s、有効期限: %s。アプリの動作は未確認です",
		"Edit: e local port, s service, m manual, r name; back returns to services; q cancels.":                                              "編集: e ローカルポート / s サービス / m 手動設定 / r 名前。back でサービス選択へ戻り、q でキャンセルします",
		"rule saved disabled; %w": "接続を無効のまま保存しました。%w",
		"To change the remote service, choose s to select a share or m for manual configuration.":                                                                                               "相手のサービスを変更する場合は、s で共有を選び直すか、m で手動設定に進んでください",
		"Service discovery: while sharing, only allowed peers may read purpose, protocol, shared port and expiry. Rule names and local targets are not announced. Disable with --no-discovery.": "サービス発見: 共有中は許可した相手だけに用途・プロトコル・共有ポート・有効期限を通知します。ルール名やローカル接続先は通知しません。--no-discovery で無効にできます",
		"Service discovery: disabled. Peers can configure this service manually.":                                                                                                               "サービス発見: 無効。相手はこのサービスを手動で設定できます",
		"  connect             Choose a service shared with this node":                                                                                                                          "  connect             このノード向けに共有中のサービスを選ぶ",
		"  connect                  Choose shared service -> preview -> start":                                                                                                                  "  connect                  共有サービスを選ぶ -> 確認 -> 開始",
		"  connect --manual         Advanced peer, purpose and port configuration":                                                                                                              "  connect --manual         詳細設定: 相手・用途・ポートを指定",
		"Choose a recently confirmed share; peer, purpose, protocol and port are filled in. Application behavior remains unverified.":                                                           "最近共有を確認できたサービスを選ぶと、相手・用途・プロトコル・ポートを設定します。アプリの動作は未確認です",
		"Use --manual or remote-setting flags for ordinary Tailscale services and older bridges. Refresh/reselect if a share changes.":                                                          "通常の Tailscale サービスや旧版の bridge には --manual または接続先のフラグを使います。共有が変わったら更新して選び直してください",
		"Interactive shares announce only purpose, protocol, shared port and expiry to allowed peers; --no-discovery opts out.":                                                                 "対話式の共有では、用途・プロトコル・共有ポート・有効期限だけを許可した相手に通知します。--no-discovery で無効にできます",
		"With --confirm, discovery stays disabled unless --discoverable is explicit. Existing saved shares retain their setting.":                                                               "--confirm では、--discoverable を明示しない限りサービス発見は無効です。保存済みの共有は従来の設定を保持します",
	} {
		japaneseCatalog[source] = target
	}
	japanesePatterns = compileTranslations(japaneseCatalog)
}
