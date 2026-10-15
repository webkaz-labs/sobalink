# Screen examples / 画面例

These are real UI captures with synthetic demo data. They are not evidence of physical-device connection, delivery, or application compatibility. Language, viewport and state variants are not additional scenes.

デモデータを実UIに表示して撮影した9場面です。実機での接続・配送・アプリ動作の検証結果ではありません。日英・画面幅・状態差分を別場面には数えません。

## Source / 撮影元

- Base source: `7f2f79880f3ebd4689b642a6dc9c2d367c0a8e46`. Existing isolated Go browser fixtures, embedded production UI, and pinned Chromium 155.0.8059.79.
- Message examples omit only the fixture's initial message/offer seeds. The product UI is unchanged; the capture does not send a message.
- Recovery examples remain blocked and unchecked. No recovery approval, service start/save, invitation creation or OS registration is performed by these captures.
- `setup-fixed-*` shows the **entry-wording correction included with these documentation changes**, using demo data. It explains default request/TOFU and optional full-fingerprint verification. The capture source is the baseline above plus the three source changes and hashes recorded in `manifest.json`; this is not a released-build claim.
- No authentication URL/code/QR, proxy credential form, private invitation, or real personal information is included. Names and addresses are fictional fixture values.
- Exact selected image hashes and source scope: [manifest](manifest.json).

## Placement / 配置

- [Web controls](../../WEB_CONTROLS.en.md) / [Web操作](../../WEB_CONTROLS.ja.md): navigation, message/receive controls, stopped-save/start review, recovery and startup guidance.
- [Direct LAN](../../DIRECT_LAN.en.md) / [直接LAN](../../DIRECT_LAN.ja.md): exact endpoint/prefix review before start.
- [Connection setup](../../CONNECTION_SETUP.en.md) / [接続設定](../../CONNECTION_SETUP.ja.md): entry-copy correction from the same change set, labeled with its capture source.

Each placement retains text steps, alt text and a full-size link. Narrow command fields may scroll; copyable command text remains the primary command source. A screenshot does not prove that the shown operation completed.

各配置には手順・代替テキスト・原寸リンクを併記します。狭い画面のコマンド欄は横スクロールするため、コピー可能な本文を正本にします。画面の表示は操作完了を意味しません。
