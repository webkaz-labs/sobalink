# 相手を選んでつなぐ・必要な間だけ渡す

`0.2.0-alpha.1` 向けの名前付き接続ガイドです。`0.1.0-alpha.2` にはこの機能はありません。従来の RustDesk 専用設定は実験的な別手順として残しています。[旧版の RustDesk 手順](VERIFICATION.md) · [English](GENERIC.en.md)

**[0.2.0-alpha.1 は公開済み](https://github.com/webkaz-labs/tsnet-bridge/releases/tag/v0.2.0-alpha.1)です。** [対象ソースの通常 CI](https://github.com/webkaz-labs/tsnet-bridge/actions/runs/36910805666)と[公開・4対象の mise 実導入](https://github.com/webkaz-labs/tsnet-bridge/actions/runs/36911703369)が全成功しました。日英表示・OS 言語フォールバック・JSON の完全一致も確認しています。実機での認証・アプリ受入は未完了です。[詳しい配布記録](DISTRIBUTION.md)

**普段は「相手 → 用途 → 内容を確認」の順で進めます。** JSON の編集、相手の IP や識別子の手入力は不要です。Web・SSH/SFTP・DB・AI API はポートの候補を用意し、違うサービスは `custom` または詳細指定で使います。候補はサービスが存在するという保証ではありません。

管理者権限なしで導入・利用することを目指す設計です。OS の VPN・経路・DNS を変更せず、通常のユーザー権限で動く単体アプリとして、選んだ通信だけをつなぎます。接続先の管理権限やアクセス許可を不要にするものではありません。`0.2.0-alpha.1` の Linux 非 root オフライン導入も確認済みですが、Windows 標準ユーザーの実認証、実 tailnet の ACL・新機能、スマートフォンの QR 認証、実アプリ、OS ログイン・スリープ復帰は未確認です。

## 表示言語

基本はロケール自動判定です。日本語のロケールでは日本語、その他・不明なロケールでは英語を表示します。優先順は `LC_ALL` → `LC_MESSAGES` → `LANG`。これらが未設定なら、Windows はユーザーの UI 言語、Mac は優先言語を確認します。言語を手動で選ぶ場合だけ、コマンドの前へ指定します。

```sh
mise exec -- tsnet-bridge --lang ja help
mise exec -- tsnet-bridge --lang en help
mise exec -- tsnet-bridge --lang auto help
```

`TSNET_BRIDGE_LANG=ja` / `en` / `auto` による指定も可能です。コマンド名・フラグ名・機械向け JSON は同じです。接続名・相手・実際の入力値は翻訳しません。接続とグループの名前には日本語も使えます。確認では `y` / `はい`、編集は `e` / `編集`、戻るは `back` / `戻る`、取消は `q` / `キャンセル` が使えます。

## 1. 最初の一度だけ

[配布先](https://github.com/webkaz-labs/tsnet-bridge/releases)で `v0.2.0-alpha.1` が Pre-release として公開され、`packslip.sigstore.json` と対象の配布物がそろっていることを確認してから実行してください。未公開・ファイル不足の場合は先へ進みません。対象は Linux x64/ARM64、Mac ARM64、Windows x64 です。確認対象の mise は 2026.9.18 です。

```sh
mise use -g "packslip:github.com/webkaz-labs/tsnet-bridge[prerelease=true]@0.2.0-alpha.1"
mise exec -- tsnet-bridge version
mise exec -- tsnet-bridge init
mise exec -- tsnet-bridge login
```

`version` の期待値は `tsnet-bridge 0.2.0-alpha.1` です。完全な版番号の指定を保ち、`latest` に変えません。mise 2026.9.18 の完全指定は経過時間フィルターの対象外ですが、署名・識別・ダイジェストの検証は有効なままにします。OS のセキュリティ警告で止まったら、回避せず中断してください。

- `init` は空の設定を保存するだけ。ネットワークへ接続しません
- `login` はノードを開始し、正規の Tailscale 認証ページを案内します。既存 Tailscale アプリとは別のノードです。参加先と追加権限を自分で確認してください
- パスワード・秘密鍵・認証 URL は共有しません。ブラウザーを手動で開くには `login --no-browser`
- 既存の RustDesk 設定があると `init` は上書きしません。[移行](#既存設定と持ち運び)か別の `--state-dir PATH` を使います

### 認証は使いやすい方法を選ぶ

| 方法 | コマンド | 操作 |
| --- | --- | --- |
| この端末のブラウザー | `login` | 自動で開く正規の認証ページで、アカウント・tailnet を確認する |
| スマートフォンでスキャン | `login --qr` | 私的なターミナルの QR を、信頼できるスマートフォンのカメラで読み取り、スマートフォン側のブラウザーで認証する |
| リンクを手動で開く | `login --link` または `login --no-browser` | 表示された非公開 URL をコピーして、自分の信頼できるブラウザーで開く |

[Tailscale 公式の QR 認証手順](https://tailscale.com/docs/features/access-control/device-management/how-to/set-up-qr-code)と同じ考え方で、**認証されるのは bridge のノード**です。スマートフォンを追加する操作とは別です。アカウントのログイン・追加認証やノード承認が必要なら省略しません。この bridge と実際のスマートフォンを使う一連の操作は、実機確認が残っています。

QR は正規の認証 URL から端末内だけで生成し、外部の QR 作成サービスへ送りません。QR と URL はノード参加に使える非公開情報です。スクリーンショット・画面録画・共有ターミナル・ログ転送を避け、使い終わったら私的な画面から片付けてください。`--qr` はファイルやパイプへのリダイレクトを拒否します。必要な幅がないと QR を描かず、必要な列数とリンク方式を案内します。読み取りにくい場合は窓を広げるか `--qr-format large`、またはリンク方式を使います。

表示は「待機中」「認証完了」「ノード承認待ち」を区別します。標準の待機は 5 分で、必要なら `--timeout 10m`。待機時間は認証リンクの有効期限とは別です。有効期限切れなら `login` を再実行し、現在表示される QR／リンクを確認します。上流側が既存リンクを返す場合もあり、再実行だけで必ず再発行・失効するとは限りません。リンクのサーバー側の期限は推測して表示しません。Ctrl+C は待機の中止だけで、既に表示したリンクの失効やノード停止にはなりません。ノードを止めるには `stop` を使います。

## 2. 相手のサービスを使う

```sh
mise exec -- tsnet-bridge connect
```

1. 現在の相手の一覧から番号を選ぶ
2. 用途を選ぶ。`web` / `ssh` / `db` / `ai` / `custom`
3. 表示された相手・実際の接続先・ローカル側の入口を確認して開始。確認画面の `e` でポート・有効期間、`p` で相手、`u` で用途、`r` で名前を編集し、全体を確認し直せる

サービスポートとルール名は提案値をそのまま Enter で使えます。相手・用途・ポート・名前・有効期間の入力間違いは、その場で直せます。用途や確認画面で `back` または `戻る` を入力すると相手選びへ戻ります。どの質問でも `q` または `キャンセル` で保存せず終了できます。以後はその短い名前だけで再開できます。例の `web-demo` は、自分が保存した名前に置き換えます。

```sh
mise exec -- tsnet-bridge connect web-demo
mise exec -- tsnet-bridge settings
mise exec -- tsnet-bridge stop web-demo
```

`settings` の接続先をアプリへコピーします。「ローカル側の入口」は SOCKS のプロキシ欄に入れる値ではありません。SSH の案内はホスト鍵確認を残します。HTTPS の証明書名・SNI、ブラウザーの origin・Cookie・CORS は単に localhost へ変えるだけでは整わない場合があります。検証を無効にして解決しないでください。

同じ番号のポートを最初に提案します。SSH の 22 など権限が必要なローカル番号や使用中の番号には、代替案を見せて確認します。**無言の変更・昇格はしません。** 相手のサービスは 1〜65535、こちらのローカル待受は 1024〜65535 に指定できます。RustDesk の固定ポート条件は別です。

<details>
<summary>詳細を指定したいとき</summary>

```sh
# demo は現在の一覧に実在する相手名へ置き換える
mise exec -- tsnet-bridge connect --peer demo --purpose web --port 8080 --name web-demo
mise exec -- tsnet-bridge connect --peer demo --purpose ssh --listen-port 2222 --name ssh-demo
mise exec -- tsnet-bridge connect --peer demo --purpose custom --network udp --port 9000 --name udp-demo
```

保存だけなら `--save-only`。既存の名前の置換は、停止して内容を見直したうえで `--replace` を指定します。同じ表示名で別の端末へ置き換わっても、保存済み接続は勝手に付け替えません。再度、現在の相手を選び直してください。

</details>

## 3. この端末のサービスを渡す

渡すアプリを先に通常の方法で起動し、そのアプリの認証を有効にします。

```sh
mise exec -- tsnet-bridge share
```

1. 接続を許可する相手を選ぶ。複数は番号をカンマで区切る
2. 用途と、この端末のサービスを選ぶ
3. **何を・誰に・いつまで渡すか**を確認して開始

明示した `127.0.0.1` または `::1` のサービスだけを、bridge ノードの tailnet アドレスで受け付けます。共有先のアプリは自分側の localhost ではなく、表示された **提供側ノードの tailnet アドレス**へ接続します。HTTPS 等のアプリ設定は別です。

```sh
mise exec -- tsnet-bridge shares
mise exec -- tsnet-bridge stop api-demo
mise exec -- tsnet-bridge stop-shares
```

確認中に相手・サービス・グループ内容が変わると、開始せず再確認を求めます。開始済みの期限を変える場合は、停止して新しい期限で開始し直します。既に開始中で相手・サービス・所有者・期限条件が同じなら、再確認せず現在の状態を表示するだけです。元の終了時刻は延ばしません。

共有には 1 秒〜24 時間の期限が必要です。保存済み共有の再開例:

```sh
mise exec -- tsnet-bridge share --ttl 30m api-demo
```

TCP と UDP の両方に対応します。詳細は `--network udp`、IPv6 のローカルサービスは `--loopback ::1` を指定します。LAN・公開 IP・任意のホスト名への中継、全サービスの一括公開はしません。

**重要:** ローカルのアプリには bridge が接続しているように見えます。「localhost なら認証不要」という設定のまま機密 API や操作用 API を渡さないでください。tailnet 側のアクセス制御と、選択した相手の識別確認を重ねます。期限や停止は待受と既存通信を閉じますが、既に渡したデータの回収や、起動済み遠隔ジョブの取消はできません。

## 4. いくつかの接続をまとめる

必要なルールだけをグループへ保存します。グループ保存だけでは通信を開始しません。

```sh
mise exec -- tsnet-bridge group save dev web-demo ssh-demo
mise exec -- tsnet-bridge group start dev
mise exec -- tsnet-bridge group stop dev
```

開始の途中で失敗すると、今回新しく開始した分だけを戻します。他の作業が使っていた接続は止めません。共有を含むグループは `group start --ttl 30m dev` のように期限を指定し、共有内容も確認します。

## 5. 状態・診断・終了

```sh
mise exec -- tsnet-bridge status
mise exec -- tsnet-bridge doctor
mise exec -- tsnet-bridge status --json
mise exec -- tsnet-bridge stop
```

| 表示 | 次にすること |
| --- | --- |
| `idle` | ノードは起動中。必要な接続を選ぶ |
| `needs-login` / `approval-required` | 正規の認証／ノード承認を行う |
| `ready` | 対象ルールの待受が準備済み。実アプリの成功は別途確認 |
| `partial` | 使える項目と、使えない項目を個別に確認 |
| `recovering` | 同じ相手・許可・期限を保持して再確認中。通信が閉じる場合がある |
| `failed` | 理由とポート・相手を見直して、明示的に開始し直す |
| `stopped` / `expired` | 終了済み。復帰・再接続・再起動では自動再開しない |

JSON はルールごとの方向・相手・実待受・理由コード・期限・確認時刻・タスク所有者を返します。認証 URL や SOCKS 資格情報は含めませんが、端末名や接続先は個人・組織を識別する情報になり得ます。公開する前に確認してください。

`reconnect` は現在開始中の接続だけを作り直します。アプリの要求は再送しません。ルール単位の停止はノードを残し、引数なしの `stop` はノードごと停止してログイン情報を残します。

<details>
<summary>自動処理から使う</summary>

```sh
mise exec -- tsnet-bridge wait-ready --timeout 30s web-demo
mise exec -- tsnet-bridge task --rules web-demo --timeout 30s -- curl http://127.0.0.1:8080/
```

`task` は自分の所有者 ID で開始し、準備待機後に指定コマンドを直接実行します。終了・エラー・取消時に自分が始めたルールだけを止めます。30 秒のリースを 10 秒ごとに更新し、呼出元が強制終了しても最後のリースから失効します。別タスクの接続を再利用・停止しません。共有には `--ttl` と共有内容の確認が必要です。

準備完了は遠隔ジョブ完了ではありません。HTTP API/MCP の認証・ツール認可・ジョブ ID・取消・結果照会は実行側の責任です。stdio MCP を TCP 転送だけで HTTP 化する機能はありません。

</details>

<details>
<summary>既存設定と持ち運び</summary>

### 既存設定と持ち運び

- `migrate` は RustDesk v1 → 名前付きルールの変更をプレビューするだけです。適用は現在の相手 ID を確認し、ノードを停止して `migrate --confirm --id-peer-id ID`。別 relay は `--relay-peer-id ID` も指定します。元ファイルを私的なバックアップへ残します
- 既存 SOCKS 設定は引き続き v1 で使います。固定転送へ無理に変換しません
- `export` は設定の内容と注意点を表示します。`export --output FILE --confirm` で無効状態のローカルコピーを保存します。認証情報・ノードのログイン状態は含めません。相手の名前・識別子・サービス情報は含むため、配布前に必ず確認します
- `import FILE` は確認用です。適用は `import --confirm FILE`。既存設定を置換する場合だけ `--replace`。全ルールは無効のまま保存され、既存ノードの識別情報は保持します

</details>

<details>
<summary>希望したときだけ自動起動する</summary>

```sh
mise exec -- tsnet-bridge autostart enable
mise exec -- tsnet-bridge autostart enable --apply
mise exec -- tsnet-bridge autostart disable --apply
```

最初のコマンドは変更内容の表示だけです。適用は自分のユーザーのログイン時起動に限定します。Linux は user systemd、Mac は LaunchAgents、Windows は最小権限のログオンタスクを使います。**起動するのはルール未開始の v2 ノードだけ**。共有・転送の自動再開や継続共有の承認を兼ねません。自動起動解除は、既に起動中のノードの停止とは別です。

OS の実ログインによる登録・解除・実認証は未検証です。利用環境に必要なユーザーサービス機能や許可がない場合、昇格せず案内して終了します。実行ファイルの移動・mise の版更新後は登録先のパスを確認してください。

</details>

## どこまで確認したか

対象ソース `236bd8e217f213a93b667f3d8d0509811d4f5464` には、名前付きルール・グループ、送受信の TCP/UDP、相手 ID 固定、期限・タスクリース、状態 JSON、準備待機、希望制のユーザー登録が含まれます。4環境の実パッケージで、OS 言語フォールバック・日英表示・JSON の完全一致を確認しました。公開版の mise 実導入でも同じオフライン確認に成功しました。ローカル／模擬試験や配布検証は、実 tailnet・実アプリ・OS のログイン／スリープ試験を代替しません。現在の証拠と未確認範囲は [日本語の検証概要](VERIFICATION.md#current-verification)と[詳しい検証報告](VERIFICATION.en.md)で確認できます。
