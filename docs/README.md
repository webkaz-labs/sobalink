# sobalink の文書

[English](README.en.md) · [概要](../README.md)

実験的な OSS プロジェクトであり、利用者向けサポートは提供しません。文書は自分で確認するための資料で、回答・修正・互換性を約束するものではありません。

最初の操作は[利用ガイド](GENERIC.ja.md)から始めてください。公開版・開発ソース・実行済みの確認結果は[検証記録](VERIFICATION.md)を正本とします。各手順は対象版と未確認の条件を明記し、実機の受入を自動試験から推定しません。

この一覧は読む場所を案内します。操作は専用ガイド、保証・制約は契約、実行結果は検証記録、残る作業はロードマップに置き、同じ説明をここへ複製しません。日英版は同じ役割の対です。全体設計・配布・Web 受入提案は共通の英語文書を参照します。

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
- [非公開の認証・取消](SIGN_IN.ja.md)
- [SSH/HTTP 設定・RustDesk](CLIENT_HELPERS.ja.md)
- [保存定義・グループ・タスク](SAVED_SERVICES.ja.md)
- [起動・停止・ログアウト・OS サインイン](LIFECYCLE.ja.md)
- [明示的な起動時接続・非公開プロキシ設定](STARTUP.ja.md)
- [ファイル・フォルダーの永続送信待ち](DURABLE_OUTBOX.ja.md)
- [容量・予算・履歴](CAPACITY.ja.md)
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

## 検証記録・残る受入・履歴

- [公開基準・正確なソースごとの実行結果](VERIFICATION.md)
- [実装と残る受入](ROADMAP.ja.md)
- [再現可能なブラウザー受入の実行手順](../web/BROWSER_ACCEPTANCE.md)
- [日付ごとの UI レビュー履歴](../web/UX_ACCEPTANCE.md)
- [一括送信の受入条件](BATCH_SEND_ACCEPTANCE.ja.md)
- [公開バイナリの資源観測](RESOURCE_MEASUREMENT.ja.md)
- [隔離した中継の通信量観測](RELAY_TRAFFIC_MEASUREMENT.ja.md)
- [0.3 系列の機能対応履歴](FEATURE_PARITY.ja.md)
- [0.3 系列の利便性計画・検証履歴](CONVENIENCE_PLAN.ja.md)
- [Web 受入の設計・ソース別履歴](WEB_ACTIVATION_ACCEPTANCE_PROPOSAL.md)
