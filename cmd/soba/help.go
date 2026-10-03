package main

import "encoding/json"

func previewPayload(name string, raw json.RawMessage) json.RawMessage {
	if privateProxyCommand(name) {
		return redactProxyPayload(raw)
	}
	if name != "lan.inspect" && name != "lan.join" && name != "lan.cancel" {
		return raw
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return json.RawMessage("{}")
	}
	if _, ok := fields["invitation"]; ok {
		replacement, _ := json.Marshal("[private input omitted]")
		fields["invitation"] = replacement
	}
	scrubbed, err := json.Marshal(fields)
	if err != nil {
		return json.RawMessage("{}")
	}
	return scrubbed
}
func helpTopic(topic string, ja bool) (string, bool) {
	if help, ok := workflowHelp(topic, ja); ok {
		return help, true
	}
	switch topic {
	case "receive":
		return text(ja,
			"soba receive recovery confirm [--reviewed] [--json]\n\nWithout --reviewed, preview only. Review previous default, per-peer and manual receive folders, unfinished staging, and saved output. Keep saved files. Resolve unfinished old receives before confirming so no untracked partial data remains. Unknown old locations need your own review. --reviewed durably initializes only a missing legacy index; it cannot discard damaged records. Start soba first; this operation uses local control only.",
			"soba receive recovery confirm [--reviewed] [--json]\n\n--reviewed なしは確認対象の表示だけです。以前の既定・相手別・手動の受信先、途中保存の残骸、保存済みファイルを確認してください。保存済みファイルは残します。確認の前に未追跡の途中保存データを整理してください。不明な旧保存先はご自身で確認が必要です。--reviewed は旧版の未作成索引だけを永続初期化し、破損記録は破棄しません。先に soba を起動してください。ローカル操作のみです。"), true
	case "proxy":
		return text(ja, proxyHelpEN, proxyHelpJA), true
	case "doctor":
		return text(ja, doctorHelpEN, doctorHelpJA), true

	case "examples":
		return text(ja, examplesEN, examplesJA), true
	case "upgrade":
		return text(ja, upgradeEN, upgradeJA), true
	case "lan":
		return text(ja, lanHelpEN, lanHelpJA), true
	case "service":
		return text(ja, serviceHelpEN, serviceHelpJA), true
	}
	return "", false
}

const lanHelpEN = `LAN identity and pairing

  soba lan addresses
  soba lan identity
  soba setup --network lan --host 192.168.20.10:54546
  soba setup --network lan --relay 192.168.20.10:54546 --certificate SHA256
  soba lan invite --to PUBLIC_ID [--name NAME] [--ttl 5m]
  soba lan inspect --json-file INVITATION_FILE
  soba lan join --json-file INVITATION_FILE
  soba lan cancel --json-file INVITATION_FILE
  soba lan revoke PEER_ID

The IP above is fictional: choose an address returned by lan addresses.
identity explicitly creates a local identity if none exists and returns only
its public code. Copy that public code to the inviting device.
invite returns private invitation JSON only after an explicit request.
Store/share invitations privately; inspect/join/cancel also accept --stdin
from a pipe. They accept either raw invitation JSON or lan invite's envelope.
inspect reviews the recipient/host/expiry/relay without joining.
Configure the exact inspected relay before join; join never changes relay or
grants application trust. Approve that identity separately with soba trust.
cancel invalidates your outgoing invitation; revoke removes a saved LAN pair.
For changed network settings, stop soba and restart with start --offline.`

const lanHelpJA = `LANの公開ID・ペアリング

  soba lan addresses
  soba lan identity
  soba setup --network lan --host 192.168.20.10:54546
  soba setup --network lan --relay 192.168.20.10:54546 --certificate SHA256
  soba lan invite --to PUBLIC_ID [--name NAME] [--ttl 5m]
  soba lan inspect --json-file INVITATION_FILE
  soba lan join --json-file INVITATION_FILE
  soba lan cancel --json-file INVITATION_FILE
  soba lan revoke PEER_ID

上のIPは架空の例です。lan addresses が表示したアドレスから選んでください。
identity は未作成ならローカルIDを明示的に作成し、公開コードだけを返します。
この公開コードを招待する側へ渡してください。
invite は明示的な要求に対してだけ機密の招待JSONを返します。
招待は非公開で保管・共有してください。inspect/join/cancel はパイプの --stdin
にも対応し、生の招待JSONと lan invite の出力形式の両方を受け取ります。
inspect は参加せず、受取人・ホスト・有効期限・中継の情報を確認します。
join の前に確認した正確な中継を設定してください。join は中継を自動変更せず、
アプリの信頼も与えません。soba trust でその端末を別途許可してください。
cancel は自分が発行した招待を無効にし、revoke は保存済みLANペアを解除します。
ネットワークを変更する場合は本体を停止し、start --offline で起動し直してください。`

const serviceHelpEN = `Create, inspect and reuse service settings

  soba service save share --backend tailnet --ports 8080 --peers PEER_ID
  soba service save connect --backend tailnet --preset ssh --peer PEER_ID
  soba service delete SERVICE_ID

service save stores a stopped definition, including while the network is offline.
Add global --offline before service to save with the agent stopped; this locks
only profile metadata and never starts a network or generates credentials.
Use --replace SERVICE_ID to explicitly replace a stopped definition.
service delete previews affected groups and active state; apply the displayed
review with --apply --review REVISION. --stop-active and --remove-from-groups
make those effects explicit. An emptied group is removed.

  soba connect --preset web --peer PEER_ID
  soba share --preset ssh --peers PEER_ID
  soba share --ports 8080 --local-port 3000 --peers PEER_ID
  soba share --preset postgres --peers PEER_ID --lifetime until-revoked

Presets web, ssh (SSH/SFTP), postgres and local-ai are editable examples.
Use soba connect --help for their actual TCP ports. They do not install or
configure applications; local-ai is an API example, not a universal AI port.
Connections default to explicit until-stopped; shares default to finite 1h.
--ttl 72h selects a custom finite lifetime in whole seconds. --lifetime
until-revoked explicitly removes a share deadline. --loopback-host accepts
only 127.0.0.1 or ::1. --local-port maps one shared port to an application
port; shared ranges keep the same ports. Reconnecting never renews a grant.
Saved settings do not automatically start after an application restart.
--dry-run checks configuration; runtime capacity is not checked until apply.


  soba service show SERVICE_ID
  soba --dry-run service copy SERVICE_ID [--name NAME] [OPTIONS]
  soba service copy SERVICE_ID [--name NAME] [OPTIONS]
  soba --dry-run service restart SERVICE_ID [OPTIONS]
  soba service restart SERVICE_ID [OPTIONS]

copy explicitly starts a new service, with an unused name by default.
restart explicitly replaces and starts a stopped service with its saved
settings. Ports, exclusions, peer scope and permission lifetime are retained
unless overridden. A fresh permission lifetime begins only when applied.
Use --help after copy/restart for override flags. Review with --dry-run first.
Active services must be stopped separately with soba stop-service ID.
Revision conflicts leave saved settings unchanged: show the latest and retry.
No command silently stops an active entry, replaces another name, or switches
a known backend. Older unknown-backend entries require --backend tailnet|lan.
Service and peer reachability are still checked by the agent when applied.`

const serviceHelpJA = `サービスの作成・確認・再利用

  soba service save share --backend tailnet --ports 8080 --peers PEER_ID
  soba service save connect --backend tailnet --preset ssh --peer PEER_ID
  soba service delete SERVICE_ID

service save はネットワークがオフラインでも停止状態の設定を保存します。
本体も停止している場合は service の前に共通指定 --offline を付けます。
設定だけを排他ロックし、ネットワークを開始せず認証情報も生成しません。
停止中の既存設定を明示的に置き換えるには --replace SERVICE_ID を使います。
service delete は影響するグループと稼働状態を表示します。確認後に
--apply --review REVISION で適用します。稼働中の停止には --stop-active、
グループの参照削除には --remove-from-groups が必要です。空のグループも削除します。

  soba connect --preset web --peer PEER_ID
  soba share --preset ssh --peers PEER_ID
  soba share --ports 8080 --local-port 3000 --peers PEER_ID
  soba share --preset postgres --peers PEER_ID --lifetime until-revoked

web、ssh（SSH/SFTP）、postgres、local-ai は変更できる入力例です。
実際のTCPポートは soba connect --help で確認できます。アプリの導入・設定は
行いません。local-ai はAPIの例であり、全AIアプリ共通のポートではありません。
接続の既定は until-stopped、共有の既定は有限の1時間です。
--ttl 72h で任意の有限期間を整数秒単位で指定できます。共有の期限をなくすには
--lifetime until-revoked を明示します。--loopback-host は127.0.0.1または::1
のみです。--local-port は共有ポート1個をアプリ側ポートへ対応させます。
範囲共有は同じポートです。再接続では許可を延長しません。
アプリ再起動後、保存済み設定は自動開始しません。
--dry-run は設定を確認します。実行時のリソース残量は適用時に確認します。


  soba service show SERVICE_ID
  soba --dry-run service copy SERVICE_ID [--name NAME] [OPTIONS]
  soba service copy SERVICE_ID [--name NAME] [OPTIONS]
  soba --dry-run service restart SERVICE_ID [OPTIONS]
  soba service restart SERVICE_ID [OPTIONS]

copy は新しいサービスを明示的に開始します。既定では未使用の名前を選びます。
restart は停止中のサービスを保存済み設定で置き換え、明示的に開始します。
ポート・除外・相手・有効期間は、指定で変更しなければ引き継ぎます。
許可の有効期間は適用時に新しく始まります。
変更できる指定は copy/restart の後の --help で確認できます。先に --dry-run
で内容を点検してください。稼働中なら別途 soba stop-service ID で停止します。
版が競合した場合は保存済み設定を変更しません。最新を表示して再確認してください。
稼働中サービスの自動停止・別名の上書き・既知のネットワークの自動変更はしません。
ネットワークが不明な旧設定には --backend tailnet|lan の明示が必要です。
サービス・相手の到達状態は適用時に本体が確認します。`

const examplesEN = `Short sobalink workflows

Start soba, then open its local URL. soba ui provides a new sign-in code.
Human setup and selecting peers are guided in the Web UI; command output
stays JSON by default for scripts and agents; explicit login --qr/--link/--browser
provides private human sign-in output.

Tailnet: soba setup --network tailnet, then soba login and soba peers.
LAN: soba lan --help explains public-code and private-invitation exchange.

Share or connect (fictional peer ID peer-123)
  soba --dry-run share --preset ssh --peers peer-123
  soba share --preset ssh --peers peer-123
  soba connect --preset ssh --peer peer-123
  soba share --ports 8000-8010 --exclude 8005 --peers peer-123 --ttl 30m
An unused name is generated; --name overrides it. Existing names are never
silently replaced. Presets are editable defaults, not an application check.
Use service show/copy/restart to reuse settings with explicit fresh permission.

Files and receiving
  soba send peer-123 ./file.txt ./folder
  soba accept TRANSFER_ID ./received
  soba receive-dir ./received
  soba autosave peer-123 --on
  soba autosave peer-123 --off
Autosave applies only to the approved identity. Omitted directory uses its
saved folder or the receive-dir setting. Turning autosave off preserves pause.
pause/resume PEER_ID affect messages and transfers; stop-service stops a share.

Recovery
  soba reconnect peer-123 refreshes reachability without renewing grants.
  soba retry TRANSFER_ID retries unfinished files; cancel TRANSFER_ID stops.
  soba revoke peer-123 removes application trust; lan revoke unpairs LAN.
  Stop soba, then soba start --offline to repair network settings locally.

--dry-run before an action previews JSON without applying it. Saved settings
may be read to choose names or copy fields. This is not a peer identity,
network reachability or free-port test. Private invitations are redacted.
Keep --state-dir consistent on every command. Use soba help upgrade to update.`

const examplesJA = `sobalink の短い操作例

soba を起動してローカルURLを開きます。新しいコードは soba ui で取得できます。
人が設定したり相手を選んだりする操作は画面から案内します。
通常のコマンド結果はスクリプト・エージェント向けのJSONです。
login --qr/--link/--browser は、明示的に人向けの非公開サインイン表示を選びます。

Tailnet: soba setup --network tailnet → soba login → soba peers
LAN: soba lan --help で公開コードと機密の招待の交換手順を確認します。

共有・接続（架空の相手ID peer-123）
  soba --dry-run share --preset ssh --peers peer-123
  soba share --preset ssh --peers peer-123
  soba connect --preset ssh --peer peer-123
  soba share --ports 8000-8010 --exclude 8005 --peers peer-123 --ttl 30m
未使用の名前を生成します。--name で指定もできます。既存名は自動上書きしません。
プリセットは変更可能な初期値であり、接続先アプリの起動確認ではありません。
service show/copy/restart で設定を再利用し、新しい期間の許可を明示的に適用できます。

送信・受信
  soba send peer-123 ./file.txt ./folder
  soba accept TRANSFER_ID ./received
  soba receive-dir ./received
  soba autosave peer-123 --on
  soba autosave peer-123 --off
自動保存は許可済みのその端末だけに適用します。保存先を省略すると相手の保存先、
または receive-dir の設定を使います。無効化しても一時停止は維持します。
pause/resume PEER_ID はメッセージ・転送を停止／再開し、stop-service は共有を停止します。

復旧
  soba reconnect peer-123 は許可を更新せず、到達状態を再確認します。
  soba retry TRANSFER_ID は未完了ファイルを再試行し、cancel TRANSFER_ID は停止します。
  soba revoke peer-123 はアプリの信頼を解除し、lan revoke はLANペアを解除します。
  ネットワークの修復時は本体を停止して soba start --offline で起動します。

操作の前に --dry-run を付けると変更せずJSONを確認できます。名前の選択や設定の
再利用のため、保存済みの状態を読む場合があります。相手の本人確認・ネットワーク到達・
ポートの空きの確認ではありません。機密の招待内容は表示しません。
すべてのコマンドで同じ --state-dir を使ってください。更新は soba help upgrade を参照します。`

const upgradeEN = `Updating a development build

No released sobalink upgrade path is available yet.
1. Record soba version, then stop the agent with soba stop.
2. Keep any state backup private; it contains identity and peer information.
3. Build the intended source using its frontend and Go build instructions.
4. Run soba version and soba start --offline to inspect retained settings.
5. Restart normally after review and explicitly restart any desired services.

Use the same --state-dir throughout. A fresh directory creates a separate
identity and requires its own pairing and trust. Never restore a backup while
the agent is running. No automatic upgrade or permission renewal occurs.`

const upgradeJA = `開発ビルドの更新

sobalink のリリース版更新経路はまだありません。
1. soba version で版を記録し、soba stop で本体を停止します。
2. 状態のバックアップは非公開で保管します。秘密鍵・相手情報を含みます。
3. 対象ソースの手順に従って画面とGo実行ファイルをビルドします。
4. soba version と soba start --offline で保存済み設定を点検します。
5. 確認後に通常起動し、必要なサービスは明示的に開始し直します。

すべてのコマンドで同じ --state-dir を使ってください。新しいフォルダーは別の
端末IDになり、ペアリング・信頼も別途必要です。稼働中にバックアップを戻さないでください。
自動更新や許可の自動更新は行いません。`
