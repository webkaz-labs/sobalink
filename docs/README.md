# sobalink の文書

[English](README.en.md) · [概要](../README.md)

公開済みの基準は [0.3.0-alpha.4](https://github.com/webkaz-labs/sobalink/releases/tag/v0.3.0-alpha.4) で、複数中継の経路復旧を含みます。明示的なLAN送信先制限・中継運用・中継なしLAN・WAN探索・方式の併用は開発版で検証中です。中継なしLANの初回接続の不具合は修正し、localhostのTCP／UDP継続試験で確認しました。最終4対象native・ブラウザー・配布受入は未完了です。目的に合う手順から始めてください。実装、自動試験、署名付き配布、実端末の受入を検証記録で区別します。

LAN外の接続にはTailscaleを使います。中継の起動と今後の配置改善はLAN内を対象にし、外部中継の配備は当面対象外です。任意の高度なWAN候補設定には、到達可能な互換中継と固定pinを既に用意している必要があります。

| 目的 | 手順 |
| --- | --- |
| まず使う | [GENERIC.ja.md](GENERIC.ja.md) |
| 案内付きCLI・自動化 | [CLI_GUIDE.ja.md](CLI_GUIDE.ja.md) |
| ローカル画面の操作 | [WEB_CONTROLS.ja.md](WEB_CONTROLS.ja.md) |
| SSH/HTTP設定・RustDesk | [CLIENT_HELPERS.ja.md](CLIENT_HELPERS.ja.md) |
| 非公開の認証と取消 | [SIGN_IN.ja.md](SIGN_IN.ja.md) |
| 保存定義・グループ・タスク | [SAVED_SERVICES.ja.md](SAVED_SERVICES.ja.md) |
| 起動・停止・ログアウト・OSサインイン | [LIFECYCLE.ja.md](LIFECYCLE.ja.md) |
| 明示的な外向き起動時接続・非公開プロキシ設定 | [STARTUP.ja.md](STARTUP.ja.md) |
| 範囲を限定したプロキシ・診断 | [PROXY_DIAGNOSTICS.ja.md](PROXY_DIAGNOSTICS.ja.md) |
| 明示的な中継ペアリング・復旧 | [LAN.ja.md](LAN.ja.md) |
| 開発版：LAN送信先の制限 | [LAN送信先](LAN_DESTINATIONS.ja.md) |
| 開発版：中継なしLAN | [Direct LAN](DIRECT_LAN.ja.md) |
| 開発版：方式の併用・新しい接続の切替 | [接続方式の併用](MIXED_CONNECTIONS.ja.md) |
| 準備した経路の実装と条件 | [経路復旧](ROUTE_RECOVERY_DESIGN.ja.md) |
| 容量・予算・履歴 | [CAPACITY.ja.md](CAPACITY.ja.md) |
| 開発・使いやすさの基本方針 | [DEVELOPMENT_PRINCIPLES.ja.md](DEVELOPMENT_PRINCIPLES.ja.md) |
| 機能対応と受入 | [FEATURE_PARITY.ja.md](FEATURE_PARITY.ja.md) |
| ソースごとの検証 | [確認範囲](VERIFICATION.md) |
| 資源使用量と通信量の観測 | [公開実行ファイルの資源](RESOURCE_MEASUREMENT.ja.md) · [隔離した中継通信](RELAY_TRAFFIC_MEASUREMENT.ja.md) |
| 安全性の境界 | [安全性](SECURITY.ja.md) |
| 設計と配布 | [設計](ARCHITECTURE.md) · [配布](DISTRIBUTION.md) |
| 残る受入 | [ロードマップ](ROADMAP.ja.md) |
