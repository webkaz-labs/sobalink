# sobalink

[English](README.en.md) · [使い方](docs/GENERIC.ja.md) · [確認範囲](docs/VERIFICATION.md)

**離れた端末を、そばに。** `soba` を起動し、ローカルの画面で相手を選んで、文字・画像・ファイル・フォルダーを送る。必要な TCP/UDP サービスも、相手と期限を決めて共有できます。Go の本体に React の画面を埋め込み、画面と CLI が同じ許可判定を使います。

**現在は新しい sobalink のローカル開発版です。公開版・リリース番号は未確定です。** 既存の `tsnet-bridge` の公開物に、ここで説明する画面・転送機能が入っているとは扱わないでください。リポジトリと Go モジュールの識別子は現在のままです。[配布方針と旧版の区別](docs/DISTRIBUTION.md)

単体・模擬試験と、実機での認証・通信・ブラウザー操作は別の確認です。現在の実行環境ではソケット作成が `operation not permitted` で制限され、ネイティブの全試験・実ブラウザー受入・実ネットワークの受入は完了していません。Tailcat は最初の開発スナップショットでは利用できず、統合・実通信の確認を継続します。接続準備や探索結果は、相手アプリの正常動作を保証しません。

## まず使う

開発用に、このチェックアウトからビルドします。Go **1.27.1**、Node **24.19.0**、npm **11.9.0** が対象です。画面の依存関係はロックファイルに固定しています。

```sh
npm --prefix web ci --no-audit --no-fund
npm --prefix web run build
go build -tags ts_omit_portmapper,ts_omit_captiveportal,ts_omit_useproxy -trimpath -o bin/soba ./cmd/soba
./bin/soba
```

Windows のビルドは `go build -tags ts_omit_portmapper,ts_omit_captiveportal,ts_omit_useproxy -trimpath -o bin/soba.exe ./cmd/soba`、起動は `./bin/soba.exe` です。導入後の実行に Node は不要です。

1. 表示された `http://127.0.0.1:ポート` を同じ端末のブラウザーで開き、端末に表示された一回用コードを入力します。コードは URL に含みません。再発行は別の端末ウィンドウで `soba ui`
2. ネットワークを選びます。既存の Tailnet を利用する場合は、sobalink の独立ノードを公式の Tailscale 認証ページで参加させます。Tailcat は明示した信頼できる中継先とペアリングを使う経路で、現在は統合確認中です
3. 相手の現在の識別情報を確認し、文字や転送申込みを受け入れる相手を信頼します。文字は送信操作で送り、画像・複数ファイル・フォルダーは内容を確認して一括送信します
4. 受信側は原則として一括ごとに保存先を決めて承認します。自動保存は、特定の信頼済み相手・信頼の世代・保存先を指定した場合だけ有効です
5. サービスは相手・ポート・期限を確認して共有または接続し、使い終わったら個別停止、信頼取消、または `soba stop` で終了します

例では `soba` を PATH 上の実行ファイルとして表記します。ソースビルドでは `./bin/soba`、Windows では `./bin/soba.exe` に読み替えます。起動した端末は開いたままにします。初回のネットワークは未選択で、勝手に認証や共有を始めません。[日英の画面・CLI手順](docs/GENERIC.ja.md)

## できることと境界

| 操作 | 範囲 |
| --- | --- |
| 文字・画像・ファイル・フォルダー | 明示して送信。複数項目を一括確認。文字のコピーも手動 |
| 受信 | 一括承認が既定。保存先を固定した相手別の自動保存は希望制 |
| 再試行 | 同じ起動中の未完了ファイルを先頭から再送。途中バイト・再起動後の再開はしない |
| サービス共有 | 最大32の明示した相手、1秒〜24時間。TCP の範囲は同じポート番号で loopback に対応 |
| 接続 | 通常の Tailnet ノードのサービスにも接続可能。相手側の sobalink は不要 |
| ローカル操作 | 正確な `127.0.0.1` の一時ポート、一回用コード、セッション・Origin・CSRF の検査 |

受信ファイルを自動で開く・実行する・既存ファイルを上書きすることはありません。フォルダーの実行属性やリンクは引き継ぎません。ブラウザーからの空フォルダー選択にはブラウザー API の制限があるため、完全なフォルダー構成が必要なら CLI でも確認してください。

TCP の広い共有範囲は、許可期間中にその範囲で新しく起動したアプリにも適用されます。必要な範囲まで狭め、除外を設定してください。探索 `54543`、ピア API `54544`、ペアリング `54545` とバックエンド内部の入口はサービス共有から除外します。UDP とローカル接続の実待受は合計64ポートに制限します。[安全性](SECURITY.md)

Direct・Relay は暗号化された通信の経路、再接続は通信を作り直す状態です。経路が不明なら不明と表示し、遅延から推測しません。ネットワーク方式を自動交換せず、既存 TCP 接続の維持も保証しません。Tailcat に任意の公開中継先への自動フォールバックはありません。[ネットワークと設計](docs/ARCHITECTURE.md)

## CLI の短い例

```sh
soba setup --network tailnet
soba login
soba peers
soba trust PEER_ID
soba message PEER_ID "Hello"
soba send PEER_ID ./image.png ./notes.txt ./sample-folder
soba share --name preview --network tcp --ports 3000-3003 --peers PEER_ID --ttl 1h
soba connect --name preview --network tcp --ports 3000 --peer PEER_ID --ttl 1h
soba status
soba stop
```

`PEER_ID` は現在の相手に置き換えます。共有元では対象アプリの起動と認証が必要です。言語は自動選択し、`soba --locale ja ...` / `en` で切り替えられます。JSON の項目名と利用者の値は翻訳しません。通常の操作に設定 JSON の編集は不要です。

## 開発と検証

[開発・使いやすさの基本方針](docs/DEVELOPMENT_PRINCIPLES.ja.md)を実装・文書・レビューの共通基準にします。[設計](docs/ARCHITECTURE.md)、[配布](docs/DISTRIBUTION.md)、[検証](docs/VERIFICATION.md)、[残る受入条件](docs/ROADMAP.ja.md)を参照してください。

Go の race / vet と画面の単体試験に加え、Linux x64/ARM64・macOS ARM64・Windows x64 のネイティブ CI、ロック済み画面資材の再生成、配布物の反復ビルド・署名・provenance・実導入確認を維持します。旧版の合格結果を新しいソースの証拠にはしません。
