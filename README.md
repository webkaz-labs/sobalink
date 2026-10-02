# tsnet-bridge

[English](README.en.md) · **[名前付き接続の使い方](docs/GENERIC.ja.md)** · [実験的な RustDesk 手順](docs/VERIFICATION.md)

**このノードに共有されたサービスを選ぶ → 確認してつなぐ。** tsnet を組み込んだ、アプリ単位の tailnet 接続ツールです。現在のソースは、名前付き TCP/UDP 接続と相手・サービス・期限を限定した共有に、サービスから選ぶ探索を追加しています。日本語・英語は OS／実行環境のロケールから自動選択します。

**探索は現在のソースの機能で、公開済み `0.2.0-alpha.1` のバイナリには含まれません。** 新しい操作は[ソースからの導入手順](docs/GENERIC.ja.md#1-最初の一度だけ)を使います。以下の配布結果は記載したソースだけの証拠です。探索の公開・実 tailnet 受入の完了は意味しません。

OS 全体の VPN・経路・DNS は変更しません。管理者権限なしでの導入・利用を目指す設計ですが、接続先の権限やアクセス許可は必要です。Windows 標準ユーザーでの実認証は未確認です。

> **実験段階の検証用プレリリースです。** 対象ソースの4環境ネイティブ試験とパッケージ確認は成功しました。実 tailnet への参加、スマートフォン QR 認証、実際の ACL・アプリ、OS ログイン／スリープ、RustDesk の双方向画面・入力は未検証です。`ready` は通信の準備を表し、アプリの成功を保証しません。[確認済みと未確認の範囲](docs/VERIFICATION.en.md)

## 0.2.0-alpha.1 の確認状況

- [検証用プレリリースを公開済み](https://github.com/webkaz-labs/tsnet-bridge/releases/tag/v0.2.0-alpha.1)。2026-10-01 19:11:08 UTC、19配布物
- [対象ソース `236bd8e` の通常 CI](https://github.com/webkaz-labs/tsnet-bridge/actions/runs/36910805666)は全5ジョブ成功。[公開ワークフロー](https://github.com/webkaz-labs/tsnet-bridge/actions/runs/36911703369)も全15ジョブ成功
- 公開配布物を認証なしで取得し、署名・出所・内容を検証。Mac ARM64・Windows x64・Linux x64/ARM64 で mise の実導入と日英表示・OS 言語フォールバック・JSON の完全一致を確認
- 署名検証やリリース経過時間の設定は変更していません。追加の Linux 非 root オフライン導入も成功

公開・実導入の詳しい証拠と、未実施の実機試験は[配布記録](docs/DISTRIBUTION.md)・[検証報告](docs/VERIFICATION.en.md)で分けて示しています。

## 公開済み 0.2.0-alpha.1 の導入

Linux x64/ARM64、macOS Apple Silicon (ARM64)、Windows x64 が対象です。Intel Mac・Windows ARM64 は今回の配布対象に含みません。CI の OS は最小対応 OS や Windows 標準ユーザー動作の保証ではありません。

[配布先](https://github.com/webkaz-labs/tsnet-bridge/releases)で `v0.2.0-alpha.1` が **Pre-release** として公開され、`packslip.sigstore.json` と対象の配布物がそろっていることを確認してから実行してください。未公開・ファイル不足なら先へ進みません。確認対象の [mise](https://mise.jdx.dev/getting-started.html) は **2026.9.18**。PowerShell、Mac、Linux で同じコマンドを使い、Go や手動展開は不要です。

```sh
mise use -g "packslip:github.com/webkaz-labs/tsnet-bridge[prerelease=true]@0.2.0-alpha.1"
mise exec -- tsnet-bridge version
mise exec -- tsnet-bridge init
mise exec -- tsnet-bridge login
mise exec -- tsnet-bridge connect
```

`version` で `tsnet-bridge 0.2.0-alpha.1` を確認します。`init` は空の設定の保存だけで、接続や共有を始めません。`login` は既存の Tailscale アプリと別のノードを開始し、正規の認証ページを案内します。参加先と権限を確認してください。`connect` では現在の相手と用途を選び、実際の接続先を確認します。JSON 編集や RustDesk 公開鍵は不要です。

既存プロフィールは上書きしません。[移行・別プロフィールの手順](docs/GENERIC.ja.md#既存設定と持ち運び)を確認し、別の保存場所を使う場合は全コマンドの前に `--state-dir PATH` を指定します。

`prerelease=true` と完全な版番号を指定し、`latest` は使いません。mise 2026.9.18 の完全な版指定は24時間の経過時間フィルターの対象外です。署名・識別・ダイジェスト検証は有効なままにします。Packslip の署名は OS コード署名・公証や実アプリの動作保証とは別です。OS のセキュリティ警告で止まったら、回避せず中断してください。

## 現在のソースでの普段の使い方

先に[手順](docs/GENERIC.ja.md#1-最初の一度だけ)に沿ってソースをビルドします。以下は `bin/tsnet-bridge`（Windows は `bin/tsnet-bridge.exe`）を使います。旧公開版では相手・用途から選ぶ従来の操作になります。

```sh
bin/tsnet-bridge connect            # 相手のサービスを使う
bin/tsnet-bridge share              # 相手・サービス・期限を限定して渡す
bin/tsnet-bridge settings           # アプリへ入力する接続先を表示
bin/tsnet-bridge status
bin/tsnet-bridge doctor
bin/tsnet-bridge stop               # ノードを停止し、ログイン情報を保持
```

保存した接続は `connect web-demo`、個別停止は `stop web-demo` のように名前で操作します。名前は自分で保存したものへ置き換えます。共有は明示した loopback サービスだけを、選択した相手に必要な間だけ提供します。アプリ側の認証も必要です。期限・停止は既存通信も閉じますが、渡したデータの回収や遠隔ジョブの取消はできません。

- このノードに許可された共有を新しい認証済み応答から選び、相手・用途・通信方式・ポートを自動入力
- 通常の Tailscale・旧 bridge・既知の接続先は `connect --manual`。探索結果はアプリの正常動作を保証しない
- 新しい対話式共有では最小限の探索情報を確認。`share --no-discovery` で無効化し、既存設定は自動で公開しない
- Web・SSH/SFTP・DB・AI API の用途候補と、複数の名前付き TCP/UDP 接続
- 相手 ID 固定、共有の期限、グループ単位の開始・停止と部分失敗の巻き戻し
- ルール別 JSON、準備待機、タスク所有者・リースによる後片付け
- ↑↓ で候補選択、←→ で文字編集。番号・名前や、共有先のカンマ区切り入力も使え、保存・開始は別途明示的に確認
- 入力間違いの再試行、編集・戻る・取消。同じ開始操作で意図せず範囲や期限を変えない
- ブラウザー・端末内生成 QR・非公開リンクによる認証案内
- 希望制のユーザー単位自動起動。起動するのはルール未開始のノードだけ

探索できる共有には、提供側のアプリとサインイン済み bridge の実行、このノードを許可した期限内の共有、探索情報の確認が必要です。提供側の初回は `init` → `login` → `share`。設定済みなら `init` は省略します。通常の Tailscale ピアで公開済みのサービスには相手側の bridge は不要で、`connect --manual` を使います。用途候補はローカル AI API の `11434` など、変更できるポートの例であり、アプリの自動設定ではありません。[相手側の条件と用途候補](docs/GENERIC.ja.md#サービスを提供する相手側に必要なもの)

通常は言語指定不要です。必要なときだけコマンドの前に `--lang ja` / `en` / `auto` を指定します。コマンド名・入力値・機械向け JSON は翻訳しません。[日英表示・認証・接続・共有の詳しい手順](docs/GENERIC.ja.md)

## 旧版と RustDesk の実験的手順

`0.1.0-alpha.2` は従来の RustDesk 固定転送／SOCKS 向けの履歴です。新しい名前付き接続は含まれません。[公開版 alpha.2](https://github.com/webkaz-labs/tsnet-bridge/releases/tag/v0.1.0-alpha.2) の[署名・公開取得・4対象の mise 実導入](https://github.com/webkaz-labs/tsnet-bridge/actions/runs/36888899407)は2026-10-01に確認しました。この旧版の結果を新しい版の配布証拠にはしません。

`0.2.0-alpha.1` のソースでも従来の設定を保持していますが、RustDesk の実画面・入力は未検証です。[旧 alpha.2 に固定した受入手順](docs/VERIFICATION.md)は比較用に残しています。RustDesk の設定を自動変更・復元しません。

<details>
<summary>従来の固定転送・SOCKS と制限を読む（実験的）</summary>

### RustDesk 専用の初回設定

```sh
mise exec -- tsnet-bridge setup
mise exec -- tsnet-bridge
mise exec -- tsnet-bridge login
```

`setup` は ID サーバーの tailnet 名または IP と、RustDesk の公開鍵を聞きます。設定保存だけではネットワーク認証しません。通常起動でノードを開始し、`login` で表示される Tailscale の認証ページから参加を承認します。既存の Tailscale アプリとは別のノードになります。

ブラウザーを自動で開かない場合は `login --no-browser`。認証 URL は秘密として扱い、共有しないでください。認証キーを引数・設定・環境変数から受け取る方式は提供しません。

全てのコマンドで、別の保存場所を指定する場合はコマンドの前に `--state-dir PATH` を置きます。既存プロフィールの上書きは拒否します。変更時は停止してバックアップを取り、profile.json を編集します。

### RustDesk の固定転送プロフィール

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

#### SOCKS モードの制限

`setup --mode socks` は認証付き TCP CONNECT プロキシです。資格情報は `settings --show-secrets` を私的なターミナルで実行した時だけ表示します。

RustDesk 1.4.9 はプロキシ利用時に TCP 登録へ切り替わりますが、OSS server 1.1.16 は TCP RegisterPk に NOT_SUPPORT を返します。**SOCKS だけで接続される側の登録を満たすことはできません。** 操作側のみの用途も実測が必要です。アプリ全体のプロキシなので、更新確認・API など許可リスト外の通信は失敗し得ます。[技術的な根拠](docs/ARCHITECTURE.md)

### 従来プロフィールの日常操作

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

</details>

## 安全性と開発

通信は許可した現在の tailnet ピアとポートに限定し、OS DNS・通常経路へフォールバックしません。共有先は明示した数値 loopback のみです。GUI、サブネットルーター、Exit Node、汎用インターネットプロキシは含みません。

保存状態は Unix でディレクトリ 0700／ファイル 0600、Windows でユーザー限定 DACL を使用します。暗号化や同一ユーザーのプロセス間の完全な隔離ではありません。固定転送に SOCKS 認証は付かないため、最小限の tailnet ポリシーとアプリ側の認証を併用してください。

開発には mise と Go 1.27.1 を使います。ソースを取得してから:

```sh
mise install
mise exec -- go test -race ./...
mise exec -- go vet ./...
mise exec -- go build -trimpath -o bin/tsnet-bridge ./cmd/tsnet-bridge
```

Windows の出力名は `bin/tsnet-bridge.exe` にします。ソースビルドは署名付き配布物とは別です。試すときは例の `mise exec -- tsnet-bridge` を、その実行ファイルのパスへ置き換えます。

[開発・使いやすさの方針](docs/DEVELOPMENT_PRINCIPLES.ja.md) · [セキュリティ](SECURITY.md) · [設計](docs/ARCHITECTURE.md) · [検証](docs/VERIFICATION.en.md) · [配布](docs/DISTRIBUTION.md) · [活用案](docs/USE_CASES.ja.md) · [残る受入条件と計画](docs/ROADMAP.ja.md)

MIT ライセンス。依存ソフトウェアのライセンスは各配布物の notices に同梱します。
