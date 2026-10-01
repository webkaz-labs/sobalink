# tsnet-bridge

[English](README.en.md) · **[日本語の導入・動作確認手順（Windows / Mac / Linux）](docs/VERIFICATION.md)**

tsnet を組み込んだ、アプリ単位の tailnet 接続ツールです。macOS、Windows、Linux で動作する Go の単一バイナリを目指します。OS 全体の VPN、経路、DNS は変更しません。

> **実験段階です。** ローカル／模擬トランスポートの試験と実際の tailnet・RustDesk 遠隔操作は別です。実機での双方向の画面表示・入力、および Windows 標準ユーザーでの初回認証は未検証です。配布は検証用プレリリースです。安定版・実機確認済みの製品ではありません。詳細は [検証状況](docs/VERIFICATION.md) を参照してください。

## 活用案

**[日本語の活用案と必要な拡張](docs/USE_CASES.ja.md)**に、Web・SSH・データベースなどへの応用をまとめています。現在そのまま使える機能と、設定の一般化や追加実装が必要なものを分けています。各アプリとの実接続は未確認です。

## できること

- 独立した tsnet ノードによる対話認証とログイン状態の保存
- 127.0.0.1 の固定 TCP／UDP 転送。UDP は送信元ごとの対応を維持し、後から届く通知も返す
- 認証必須の SOCKS5 TCP CONNECT。BIND／UDP ASSOCIATE は受け付けない
- 設定した tailnet ピアとポートへの許可リスト。OS DNS／通常経路への転送フォールバックなし
- バックグラウンド起動、状態表示、診断、停止、転送再作成、ログアウト
- 同時起動の排他制御、ユーザー限定のローカル IPC と保存領域

RustDesk の設定ファイルを直接書き換えません。設定値と復元時の注意を表示します。GUI、自動起動登録、サブネットルーター、Exit Node、汎用インターネットプロキシは含みません。

## 配布対象

| OS | CPU | 配布形式 |
| --- | --- | --- |
| Linux | x64 / ARM64 | tar.gz |
| macOS | Apple Silicon (ARM64) | tar.gz |
| Windows | x64 | zip / exe |

Intel Mac は今回のプレリリース対象に含みません。CI の OS と、実際に保証できる最小 OS・権限条件は別です。現段階では最小対応 OS を保証しません。管理者権限やシステムサービス登録は要求しない設計ですが、Windows の標準ユーザーでの認証試験は残っています。

## mise で検証用プレリリースを導入する

[mise](https://mise.jdx.dev/getting-started.html) 2026.9.18 を確認対象にしています。Windows の PowerShell、Mac、Linux で同じコマンドです。Go のインストールや手動展開は不要です。

```sh
mise use -g "packslip:github.com/webkaz-labs/tsnet-bridge[prerelease=true]@0.1.0-alpha.2"
mise exec -- tsnet-bridge version
```

[Release v0.1.0-alpha.2](https://github.com/webkaz-labs/tsnet-bridge/releases/tag/v0.1.0-alpha.2) の公開後に利用できます。`prerelease=true` と完全な版番号を指定し、`latest` は使いません。完全な版指定は mise の24時間の経過時間フィルターの対象外です。署名・識別・ダイジェスト検証は有効なままで、待機時間の全体設定を変更する必要はありません。

**[日本語の詳しい手順](docs/VERIFICATION.md)**には、Windows / Mac / Linux 別のインストール、認証、RustDesk の設定、成功判定と終了まで記載しています。Packslip の署名は配布元と内容の確認であり、OS コード署名・公証や実際の遠隔操作成功の保証ではありません。

### ソースからビルドする場合

開発には mise と Go 1.27.1 を使います。リポジトリを取得してから:

```sh
mise install
mise exec -- go test -race ./...
mise exec -- go build -trimpath -o bin/tsnet-bridge ./cmd/tsnet-bridge
```

Windows では出力名を `bin/tsnet-bridge.exe` にしてください。ソースビルドしたものを試す場合は、以下の `mise exec -- tsnet-bridge` をその実行ファイルのパスへ置き換えます。[配布と検証の説明](docs/DISTRIBUTION.md)

## 初回設定

```sh
mise exec -- tsnet-bridge setup
mise exec -- tsnet-bridge
mise exec -- tsnet-bridge login
```

`setup` は ID サーバーの tailnet 名または IP と、RustDesk の公開鍵を聞きます。設定保存だけではネットワーク認証しません。通常起動でノードを開始し、`login` で表示される Tailscale の認証ページから参加を承認します。既存の Tailscale アプリとは別のノードになります。

ブラウザーを自動で開かない場合は `login --no-browser`。認証 URL は秘密として扱い、共有しないでください。認証キーを引数・設定・環境変数から受け取る方式は提供しません。

全てのコマンドで、別の保存場所を指定する場合はコマンドの前に `--state-dir PATH` を置きます。既存プロフィールの上書きは拒否します。変更時は停止してバックアップを取り、profile.json を編集します。

## RustDesk の固定転送プロフィール

**検証用の候補です。両方向の遠隔操作が確認済みという意味ではありません。**

既定値は次の通りです:

| 種別 | ローカル | 転送先 |
| --- | --- | --- |
| ID / 登録 | TCP と UDP 127.0.0.1:32116 | hbbs:21116 |
| 状態照会 | TCP 127.0.0.1:32115 | hbbs:21115 |
| リレー | TCP 127.0.0.1:32117 | hbbr:21117 |

1. 現在の RustDesk サーバー設定とプロキシ設定を控える
2. `mise exec -- tsnet-bridge settings` の ID サーバー、リレー、公開鍵を設定する
3. プロキシは空欄、UDP は有効、WebSocket は無効にする
4. このプロフィールを使う**全ての端点で同一の 127.0.0.1:32117** を使用する
5. 接続先 ID に `/r` を付けてリレーを選ぶ

relay アドレスは相手にも伝わるため、片側だけの導入・異なるローカル relay ポート・通常の tailnet 接続との混在は成立するとみなしません。hbbs 側の relay アドレス書換え設定も確認が必要です。`/r` は全ての直接試行がなくなる保証ではありません。UDP の NAT 判定やアドレス伝播も実測が必要です。

別の hbbr は `setup --relay-host <tailnet-host>` で指定できます。固定ローカルポートは自動変更しません。競合時は原因を表示し停止します。

### SOCKS モードの制限

`setup --mode socks` は認証付き TCP CONNECT プロキシです。資格情報は `settings --show-secrets` を私的なターミナルで実行した時だけ表示します。

RustDesk 1.4.9 はプロキシ利用時に TCP 登録へ切り替わりますが、OSS server 1.1.16 は TCP RegisterPk に NOT_SUPPORT を返します。**SOCKS だけで接続される側の登録を満たすことはできません。** 操作側のみの用途も実測が必要です。アプリ全体のプロキシなので、更新確認・API など許可リスト外の通信は失敗し得ます。[技術的な根拠](docs/ARCHITECTURE.md)

## 日常操作

```sh
mise exec -- tsnet-bridge                 # 起動済みなら状態を表示
mise exec -- tsnet-bridge status --json
mise exec -- tsnet-bridge doctor
mise exec -- tsnet-bridge reconnect       # 保存ログインを維持して転送を再作成
mise exec -- tsnet-bridge stop            # ログイン状態を残して停止
mise exec -- tsnet-bridge logout          # 稼働中のノードをログアウトして停止
mise exec -- tsnet-bridge run             # 前面実行。Ctrl+C で停止
```

`ready` は設定したピアと TCP ポートへの到達確認を意味します。RustDesk の画面表示・入力の成功は意味しません。状態は必ず `rustdesk: unverified` と区別します。

一時障害では待受を閉じ、間隔を増やしながら再確認します。認証・承認・宛先・ポート競合を区別します。ログアウトの API が失敗した場合は「ローカル停止済み、サーバー側ログアウト未確認」としてエラーを返します。Tailscale 管理画面のノード削除は別操作です。

停止や削除で RustDesk の設定は戻りません。控えた設定へ手動で戻してください。ログイン待機を Ctrl+C で中断した場合もバックグラウンドプロセスは残ります。必要なら `stop` してください。

## 安全性と開発

保存状態は Unix でディレクトリ 0700／ファイル 0600、Windows でユーザー限定 DACL を使用します。state が暗号化済みという意味ではありません。同一ユーザーのプロセスから完全に保護するものでもありません。固定転送に SOCKS の認証は付かないため、最小限の tailnet ポリシーと RustDesk 側の認証を併用してください。

[セキュリティ](SECURITY.md) · [アーキテクチャ](docs/ARCHITECTURE.md) · [検証状況](docs/VERIFICATION.md) · [配布](docs/DISTRIBUTION.md)

MIT ライセンス。依存ソフトウェアのライセンスは各配布物の notices に同梱します。
