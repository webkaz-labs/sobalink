# Native UI の実装方針

[English](UI_IMPLEMENTATION.en.md) · [Native companion](README.md) · [開発原則](../../docs/DEVELOPMENT_PRINCIPLES.ja.md)

画面定義を読むと、画面の構成が分かるようにします。レイアウトコードでは意味のある部品を組み合わせ、コントロールを名前付き操作メソッドへ接続します。ネストした `Children` 内に IPC、ファイル選択、送受信フロー、操作許可の判断を実装しません。

対象は任意導入の native companion です。CLI / Web UI の契約や、native companion の実験的な受入状況は変更しません。

## MyGo の説明とプロジェクト独自の方針

このモジュールが固定している **MyGo v0.7.4** の公式文書には、次が説明されています。

- view はアプリの状態から UI を組み立てる Go 関数です。同じ context をヘルパーに渡して画面の一部を構築できます。1 フレームで複数回の build が実行される場合があります。
- コントロールの近くで `Clicked` から操作を呼べます。構築中のコレクションを変更する操作には、構築後に実行する callback を使います。
- 一覧のコントロールには構築前に安定した key を渡します。`Element` の寿命は 1 回の build です。build 値をアプリ状態や worker に保持せず、持続する識別やサービスには `Handle` / `Services` を使います。
- UI の状態はメインスレッドで読みます。`Window.Update` はそこで状態を更新します。invalidate だけでは保護されていない共有状態を同期できません。
- テーマを変更するときはコピーします。色や寸法には `Context.Theme`、`Theme.Space`、`Theme.Rem` を使い、独自の部品もテーマに従わせます。
- `mygo vet` は Go vet に加えて native UI の build 値の寿命を検査します。

出典: [view と寿命](https://github.com/egoist/mygo/blob/v0.7.4/docs/ui/views.md)、[スタイルとテーマ](https://github.com/egoist/mygo/blob/v0.7.4/docs/ui/styling.md)、[アクセシビリティ](https://github.com/egoist/mygo/blob/v0.7.4/docs/ui/accessibility.md)、[テスト](https://github.com/egoist/mygo/blob/v0.7.4/docs/ui/testing.md)、[vet の実装](https://github.com/egoist/mygo/blob/v0.7.4/cmd/mygo/vet_ui.go)。

**以下の分離は Sobalink の実装方針です。** MyGo が MVVM、特定のディレクトリ構成、別の component framework を要求しているという意味ではありません。小さな値の binding や表示条件は通常の Go で記述します。単一の widget を別名で包むだけの抽象化や、画面構成を分かりにくくする仕組みは避けます。

## 責務とソースの対応

- **画面構成:** `view.go`、`conversation_view.go`、`messagepane_view.go`、`filepane_view.go`、`tray_view.go`。画面の入口を短く保ちます。端末行、ネットワーク状態、メッセージ履歴、送信確認、受信確認、送信受付結果など、UI 上の意味を持つ部品に分けます。表示条件や、準備された表示データの繰り返しはここに置きます。
- **表示用の計算:** `presentation.go`、`messagepane_presentation.go`、`filepane_presentation.go`。ラベル、絞り込み・直近の履歴、件数、操作可否、表示状態をレイアウトのネスト外で求めます。プロトコルの識別子、ユーザー入力、機械向け JSON は翻訳しません。
- **アプリ状態と操作:** `app.go`、`messagepane.go`、`filepane.go`。画面遷移、端末ごとの下書き、不変の確認内容、実行中状態、リクエスト識別、操作条件、結果の反映を担当します。コントロールは名前付きメソッドを呼びます。ボタンを無効表示するだけでなく、操作メソッド自身でも条件を確認します。
- **ネイティブダイアログ:** `filepane_dialog.go`。UI スレッドで親ウィンドウを取得し、ダイアログ設定を画面構成から分離します。worker へ渡すのは通常の値と callback であり、build 中の element や context は渡しません。
- **通信境界:** `exchange.go` と `model.go` の IPC loader。プロトコル検証、確認、保護された IPC の契約を維持します。リファクタリングでネットワークアクセスや許可範囲を広げません。
- **テーマ:** `theme.go`。色と用途別の文字・余白の指定を集約します。システムテーマをコピーし、寸法とダーク表示を保持し、独自部品にもテーマ色を使います。view ファイルに色のリテラルを分散させません。追加の視覚効果は別途評価し、この方針だけでプラグインを必須にはしません。

## 状態・並行処理・安全性

worker は容量を制限した channel に完了イベントを送り、UI スレッドが表示前に取り込みます。wake は再描画の要求であり、UI が所有するデータを worker が変更してよいという意味ではありません。ウィンドウを隠している間も操作結果を保持し、古い結果は適用しません。画面移動や flyout を閉じる操作で確認中の内容を失ったり、別端末へ下書きを移したりしません。

次の不変条件を名前付き操作メソッドで守ります。

- pane ごとの操作は同時に 1 件までです。連打で変更操作を重複させません。
- 送信、受信、辞退は明示的な確認を経ます。該当する操作では、確認した相手・プロセス・revision・内容・受信オファーが現在も一致することを確認します。
- 確定前の cancel / back は送信しません。アプリの context がキャンセル済みなら、新しい処理を開始しません。
- 結果不明の変更操作はリクエスト識別を保持し、自動・手動の再送をロックします。読み取り専用の状態照会だけでロックを解除しません。
- デモは実通信を呼びません。実操作には送信の明示指定と、許可済みで一時停止していない対象端末が必要です。
- 通信の生エラーや実プロファイル情報をテスト画像へ出しません。架空の fixture と headless の `--demo` 描画を使います。

## アクセシビリティと検証

native button / input の振る舞い、アクセス可能な名前、行の role、展開状態、安定した key、キーボードのフォーカスと操作を維持します。状態変化の読み上げは変化時に 1 回だけ行い、build ごとに繰り返しません。日英表示と日本語フォントの fallback を保ちます。テーマを変更したら狭幅、明色・暗色の画面を確認します。

リポジトリの checksum 検証付き engine adaptation を準備し、`poc/native` で実行します。

```sh
go test -race -count=1 ./...
go run github.com/egoist/mygo/cmd/mygo vet .
```

2 番目のコマンドはモジュール固定の MyGo を使い、`go vet ./...` と build 値の寿命を検査します。native CI は Linux x64 / ARM64、macOS ARM64、Windows x64 で実行し、既存の `native_integration` fixture も有効にします。package / build の検査も維持します。

ボタン経由のテストに加え、操作メソッドの条件を直接検証します。連打、cancel / back、相手・プロセス・オファーの変更、下書きの分離、遅延した完了、古いイベント、結果不明、locale、キーボード、狭幅表示を対象にします。headless テストや cross-compile だけで、実機の読み上げ、native dialog、OS ログイン・サスペンド、ネットワークの受入完了とは判断しません。[検証記録](VALIDATION.md)で別々に扱います。
