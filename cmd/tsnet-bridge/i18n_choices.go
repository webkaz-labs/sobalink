package main

func init() {
	for source, target := range map[string]string{
		"Purpose presets suggest editable ports, not protocols or app setup: web HTTP/8080, ssh SSH/SFTP/22, db PostgreSQL/5432, ai local AI API/11434, custom your actual port.": "用途は変更できるポートの提案です。通信方式やアプリ設定ではありません: web HTTP/8080、ssh SSH/SFTP/22、db PostgreSQL/5432、ai ローカル AI API/11434、custom 実際のポート",
		"TCP is the default; --network udp selects UDP. Discovery uses the provider's actual protocol and shared port.":                                                           "標準は TCP で、--network udp で UDP を指定します。サービス発見では提供側の実際の通信方式と共有ポートを使います",
		"TCP is the default; --network udp selects UDP. Set the actual local service port; discovery announces the shared port.":                                                  "標準は TCP で、--network udp で UDP を指定します。実際のローカルサービスのポートを設定します。サービス発見では共有ポートを通知します",

		"Purpose presets suggest editable ports; they do not configure applications or select a protocol.": "用途候補は変更できるポートの提案です。アプリの設定や通信方式の選択は行いません",
		"Protocol: %s (default tcp; set with --network tcp|udp).":                                          "通信方式: %s（標準は tcp。--network tcp|udp で指定）",
		"Web (web) | port 8080 | HTTP service example":                                                     "Web（web）| ポート 8080 | HTTP サービスの例",
		"SSH / file transfer (ssh) | port 22 | SSH/SFTP":                                                   "SSH・ファイル転送（ssh）| ポート 22 | SSH/SFTP",
		"Database (db) | port 5432 | PostgreSQL example":                                                   "データベース（db）| ポート 5432 | PostgreSQL の例",
		"AI API (ai) | port 11434 | local AI API example":                                                  "AI API（ai）| ポート 11434 | ローカル AI API の例",
		"Other (custom) | enter the actual service port":                                                   "その他（custom）| 実際のサービスのポートを指定",

		"A discovered share needs the provider's application and signed-in bridge running, an unexpired share allowing this node, and discovery enabled.":                      "サービス発見で選ぶには、提供側のアプリとサインイン済み bridge の実行、このノードを許可した期限内の共有、サービス発見の有効化が必要です",
		"On the provider: tsnet-bridge init (first use only), then tsnet-bridge login and tsnet-bridge share. Review the service, allowed peer, lifetime and discovery scope.": "提供側: tsnet-bridge init（初回のみ）→ tsnet-bridge login → tsnet-bridge share。サービス・許可する相手・有効期間・サービス発見の通知範囲を確認します",
		"Ordinary Tailscale services do not need a remote bridge; their service listener and tailnet permissions must allow the connection.":                                   "通常の Tailscale サービスなら相手側の bridge は不要です。サービスの待受と tailnet のアクセス許可が必要です",
		"No discovery response does not prove that a peer is offline or lacks a bridge.":                                                                                       "サービス発見の応答がなくても、相手がオフライン・bridge 未導入とは限りません",
		"Provider first use: tsnet-bridge init, then tsnet-bridge login. Skip init when a profile exists.":                                                                     "提供側の初回: tsnet-bridge init → tsnet-bridge login。設定がある場合は init を省略します",
		"Start the local application with authentication, then run tsnet-bridge share and choose the receiving bridge node.":                                                   "認証を備えたローカルアプリを開始し、tsnet-bridge share で受け取り側の bridge ノードを選びます",
		"Keep the application and bridge running. Only an active, unexpired share permitting the receiver can be used or discovered.":                                          "アプリと bridge を動かし続けます。受け取り側を許可した期限内の共有を開始している間だけ利用・サービス発見ができます",
		"The receiver runs tsnet-bridge connect to choose a discoverable share; tailnet permissions must allow both discovery and the shared service.":                         "受け取り側は tsnet-bridge connect でサービス発見を有効にした共有を選びます。サービス発見と共有サービスの両方に tailnet のアクセス許可が必要です",

		"Back to peers (back)":     "相手の選択へ戻る（back）",
		"Cancel (q)":               "キャンセル（q）",
		"Refresh services (r)":     "サービス一覧を更新（r）",
		"Manual configuration (m)": "手動設定（m）",
		"Arrow keys choose one peer. To share with several peers, type their numbers or names separated by commas.":               "矢印キーでは相手を1台選びます。複数の相手に共有する場合は、番号または名前をカンマで区切って入力してください",
		"On the provider: keep the application and signed-in bridge running, with an active, unexpired share allowing this node.": "提供側ではアプリとサインイン済みの bridge を動かし、このノードを許可した期限内の共有を開始しておきます",
		"Provider setup (skip init if a profile exists):":                                                                         "提供側の手順（設定がある場合は init を省略）:",
		"Review the peer, service, lifetime and discovery scope. With --confirm, discovery requires explicit --discoverable.":     "相手・サービス・有効期間とサービス発見の通知範囲を確認してください。--confirm では --discoverable の明示が必要です",
		"For an ordinary Tailscale service, no remote bridge is required. Use %s connect --manual with its known peer and port.":  "通常の Tailscale サービスなら相手側の bridge は不要です。%s connect --manual で既知の相手とポートを指定してください",
	} {
		japaneseCatalog[source] = target
	}
	japanesePatterns = compileTranslations(japaneseCatalog)
}
