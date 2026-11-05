# sobalink の文書

[English](README.en.md) · [概要](../README.md)

実験的な OSS プロジェクトであり、利用者向けサポートは提供しません。文書は自分で確認するための資料で、回答・修正・互換性を約束するものではありません。

最初の操作は[利用ガイド](GENERIC.ja.md)から始めてください。公開版・開発ソース・実行済みの確認結果は[検証記録](VERIFICATION.md)を正本とします。各手順は対象版と未確認の条件を明記し、実機の受入を自動試験から推定しません。

この一覧は読む場所を案内します。操作は専用ガイド、動作条件と制約は仕様書、実行結果は検証記録、残る作業は開発計画（ロードマップ）にまとめ、同じ説明をここへ複製しません。日本語版と英語版では、同じ内容を案内します。全体設計・配布・Web 受入提案は共通の英語文書を参照します。

## 目的から選ぶ

| 目的 | 最初に読む | 次に確認する |
| --- | --- | --- |
| 全体像を知る | [概要](../README.md) | [よくある疑問](FAQ.ja.md) |
| 導入・更新する | [署名付き配布とプロフィールの境界](DISTRIBUTION.md) | [最初の起動](GENERIC.ja.md) |
| サービスを使う | [相手のサービスを使う](GENERIC.ja.md#相手のサービスを使う) | 新しいペアが必要なら[接続セットアップ](CONNECTION_SETUP.ja.md) |
| 上限や許可を変える | [容量](CAPACITY.ja.md) | [安全性](SECURITY.ja.md) |
| 問題を切り分ける | [最初に止まった段階](TROUBLESHOOTING.ja.md) | 下記の専用ガイド |
| 開発・検証する | [開発の基本方針](DEVELOPMENT_PRINCIPLES.ja.md) | [検証記録](VERIFICATION.md) |

## 使い始める・日常の操作

- [最初の起動から送信・停止まで](GENERIC.ja.md)
- [案内付き CLI・自動化](CLI_GUIDE.ja.md)
- [ローカル画面](WEB_CONTROLS.ja.md)
- [任意で使えるデスクトップ画面](NATIVE_UI.ja.md)
- [非公開の認証・取消](SIGN_IN.ja.md)
- [SSH/HTTP 設定・RustDesk](CLIENT_HELPERS.ja.md)
- [保存定義・グループ・タスク](SAVED_SERVICES.ja.md)
- [起動・停止・ログアウト・OS サインイン](LIFECYCLE.ja.md)
- [明示的な起動時接続・非公開プロキシ設定](STARTUP.ja.md)
- [ファイル・フォルダーの永続送信待ち](DURABLE_OUTBOX.ja.md)
- [容量・使用量の上限・履歴](CAPACITY.ja.md)
- [範囲を限定したプロキシ・診断](PROXY_DIAGNOSTICS.ja.md)
- [用途例](USE_CASES.ja.md)
- [よくある疑問](FAQ.ja.md)
- [自分で確認できること](TROUBLESHOOTING.ja.md)

## ネットワークの操作

- [明示した中継でのペアリング・復旧](LAN.ja.md)
- [中継ホスト・証明書・資源予算](RELAY_OPERATIONS.ja.md)
- [LAN 送信先の制限](LAN_DESTINATIONS.ja.md)
- [中継なし LAN](DIRECT_LAN.ja.md)
- [接続方式の併用](MIXED_CONNECTIONS.ja.md)
- [端末カードの確認・交換](DEVICE_CARDS.ja.md)
- [代替ポートの確認](PORT_PROPOSALS.ja.md)

## 接続・送信待ち・リソース設定

v0.5.1-alpha.1 の機能の入口です。公開版への収録と実機の受入完了は別です。各ガイドの条件と正確なソースの検証記録を確認してください。

- [接続セットアップ](CONNECTION_SETUP.ja.md)
- [テキストの永続送信待ち](DURABLE_TEXT.ja.md)
- [リソース一覧](RESOURCE_CATALOG.ja.md)
- [ローカルのリソース設定](RESOURCE_SETTINGS.ja.md)
- [許可した相手の参照](RESOURCE_INSPECTION.ja.md)
- [許可した相手の転送設定管理](RESOURCE_MANAGEMENT.ja.md)
- [固定した相手グループの設定](RESOURCE_GROUP.ja.md)
- [Web 操作の引継ぎと確認](WEB_ACTIVATION_HANDOFF.ja.md)

## 設計・安全性・開発上の契約

- [開発・使いやすさの基本方針](DEVELOPMENT_PRINCIPLES.ja.md)
- [全体設計・通信境界](ARCHITECTURE.md)
- [ローカル画面 API と相手側の境界](../web/API.md)
- [説明図の生成元と保守（オンラインのソースリポジトリ）](https://github.com/webkaz-labs/sobalink/blob/main/docs/assets/visuals/README.md)
- [安全性の境界](SECURITY.ja.md)
- [経路復旧の契約](ROUTE_RECOVERY_DESIGN.ja.md)
- [カタログ表示の契約](CATALOG_VIEW_CONTRACT.ja.md)
- [導入・ビルド・署名付き配布](DISTRIBUTION.md)
- [変更内容に応じた CI](CI_EFFICIENCY.ja.md)

<a id="検証記録残る受入履歴"></a>

## 検証記録・残る受入

- [公開基準・正確なソースごとの実行結果](VERIFICATION.md)
- [実装と残る受入](ROADMAP.ja.md)
- [再現可能なブラウザー受入の実行手順](../web/BROWSER_ACCEPTANCE.md)
- [一括送信の受入条件](BATCH_SEND_ACCEPTANCE.ja.md)
- [公開バイナリの資源観測](RESOURCE_MEASUREMENT.ja.md)
- [隔離した中継の通信量観測](RELAY_TRAFFIC_MEASUREMENT.ja.md)
- [Web 受入の設計・ソース別履歴](WEB_ACTIVATION_ACCEPTANCE_PROPOSAL.md)

## 過去の記録

[履歴の一覧](history/README.md)で、0.3 系列の機能対応・計画と日付ごとの Web UI レビューを現行手順から分けています。ソース ID、失敗、当時の未確認事項も保持します。以前の階層のパスや見出しへのリンクからも、保存した記録へ進めます。

## 文書の置き場所と保守

| 内容 | 正本を置く場所 |
| --- | --- |
| 最初の操作と案内 | ルートの README、この一覧、利用ガイド。要点を示し、詳細へリンクする |
| 一連の操作と機能の契約 | 上記の専用ガイド。コマンド例は、その前提となる契約と一緒に保守する |
| 現在の実装構成 | 全体設計、ローカル API、コードの隣にある `internal/`・`web/`・`poc/native/` の文書 |
| 実行済み検査と公開の証拠 | 検証記録。各ガイドへ成功の記述を複製せず、ソースごとの詳細記録へリンクする |
| 残る作業 | ロードマップ。過去の計画の未完了項目を現在の残件にしない |
| 過去の時点の記録 | `docs/history/`。実装の隣にある既存のレビュー記録は、その場所に保持してよい |

現行ガイドは安定したパスを維持します。フォルダーを増やすことだけを目的に全ファイルを移しません。操作に必要な短い安全上の注意は残し、詳しいコマンド・上限・復旧条件は一つの担当ガイドにまとめます。日英の文書は意図した翻訳であり、削除すべき重複ではありません。

文書変更の統合前に、[開発の基本方針](DEVELOPMENT_PRINCIPLES.ja.md)と[文書の CI 契約](CI_EFFICIENCY.ja.md#必須の文書検証)に従います。両言語と参照元のリンクを一緒に更新し、公開済みページを統合するときは以前の見出しへのリンクも維持します。リポジトリ全体のリンクと画像を検査し、配置を変える場合は配布物内の相対リンクも確認します。文書の配布対象は `docs/`・`docs/history/`・`web/` の各直下の Markdown に限定し、コードの隣にある開発用メモは含めません。下位階層や未知の変更には、安全側の CI 分類を維持します。
