# Native UI prototype / ネイティブ UI の試作

An optional, separate `soba-native` client using MyGo v0.7.4, read-only by default.
This page describes the source implementation, not a verified published package.
For release-specific results, see [verification](../../docs/VERIFICATION.en.md).
The existing `soba` CLI, daemon and React Web UI remain available. This is not a replacement UI.
See the [English usage guide](../../docs/NATIVE_UI.en.md) and [日本語ガイド](../../docs/NATIVE_UI.ja.md).

MyGo v0.7.4 を使う、必要なときに起動する別クライアント `soba-native` です。
この文書はソースの実装を説明するもので、公開済み配布物の動作確認を示すものではありません。
リリースごとの結果は[検証状況](../../docs/VERIFICATION.md)を参照してください。既定は読み取り専用で、
既存の `soba` CLI・本体・Web UIは引き続き使えます。設定・接続・許可・本体停止・更新は既存UI/CLIで扱います。

Implementation policy: [English](UI_IMPLEMENTATION.en.md) / [日本語](UI_IMPLEMENTATION.ja.md).
画面定義・表示用の計算・操作処理の分離、部品化、テーマ、検査の方針を記載しています。

## Compact desktop layout / コンパクト表示

The normal window is 460×560 DIPs (display-independent size units), with a
360×440 minimum. The tray panel is 460×620 and shrinks to fit the display work
area. Network state and peer availability come first. Click a device row to show its permissions; background
explanation lives under About. A separate messages/files pane keeps interactive controls outside device-row buttons. Settings remain in the existing Web UI/CLI.

通常ウィンドウは既定 460×560、最小 360×440 です。単位は画面の拡大率に依存しない
DIP です。トレイの小窓は 460×620 で、画面の作業領域に収まるよう縮小します。
接続状態と端末一覧を優先し、許可の情報は
端末行のクリック、補足は「この試作について」にまとめています。やり取りは別画面で開き、設定は既存のWeb UI/CLIで扱います。

## Run / 起動

Go 1.27.3 or later is required **only for this nested module**. Build from this
repository root for preparation, then enter this folder. Its local module replacement imports the repository's actual `internal/control` code;
IPC means inter-process communication on the same computer. No copied IPC
implementation or new authentication boundary is introduced.

```sh
go run ./cmd/prepare-engine
go run ./cmd/prepare-engine --verify
cd poc/native
go run . --demo --tray --locale auto
go run . --demo --locale ja
go run . --demo --tray
go run . --state-dir /path/to/existing/profile
CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o soba-native .
```

Choose exactly one of `--demo` and `--state-dir`. Live mode performs no automatic
request: select Refresh status. By default only the literal `status` command is sent through the existing user-private local IPC with a three-second timeout. With `--enable-send`, reviewed message/file commands reuse the same protected IPC. Mutations use the app lifetime context, not the status timeout. The daemon is
never started or stopped. Failure clears stale displayed data; only one status request
runs at a time. IPC success and network health are displayed separately.

`--demo` は架空データです。実 IPC モードでは、同じ端末内の本体と通信します。
既存のプロファイル（設定と状態の保存先）を指定し、「状態を取得」を押したときだけ読み取ります。本体を自動起動・停止しません。取得失敗時は古い表示を
消去します。IPCへの接続成功は、ネットワーク通信やアプリ動作の成功ではありません。

Native windows select Japanese/English from MyGo's OS locale resolution, with an
explicit `--locale` override and an in-window language switch. Headless rendering
uses environment locale or the explicit override. All demo names are fictional.

## UI rendering / 画像確認

```sh
go run . --demo --locale ja --render demo-ja.png
go run . --demo --locale ja --render-width 420 --render demo-narrow.png
go run . --demo --locale ja --render-width 360 --render-scale 2 --render-details --render demo-details-2x.png
```

PNG export is restricted to fictional demo mode. These are MyGo's real headless
software-rendered views, not browser mockups or captures of an OS window. They do
not validate GPU rendering, native IME, accessibility bridges or window chrome.

画像は MyGo の実 UI を、OS のウィンドウを開かずに描画したものです。実OSウィンドウのスクリーンショット
ではありません。GPU・IME・読み上げ・ウィンドウ装飾の受入結果には含めません。
実データの画像出力は拒否します。

## Checks / 確認

```sh
go test -race ./...
go run github.com/egoist/mygo/cmd/mygo vet .
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags='-H=windowsgui' .
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build .
```

The native IPC test starts only an isolated temporary fixture using the actual
control server/client. Its response is fictional, and it refuses any command
other than status. It is not real daemon or physical-peer acceptance. A sandbox
that blocks Unix sockets cannot run this test; report that block, do not interpret
UI-only tests as an IPC pass.

Test coverage includes locale selection, filtering, details open/close, language
switching, empty state, network-failure visibility, single-flight refresh and
stale-data removal. Narrow-screen text existence is not visual acceptance.

Remaining: real daemon integration; Windows named-pipe run; macOS and Windows
window operation; native Japanese IME and screen readers; GPU/high-DPI behavior;
suspend/resume; long sessions; signing and installed-distribution verification;
real startup latency, idle CPU and process memory. Cross-compilation is not a
runtime check. Distribution signature/provenance verification is separate from OS
developer signing/notarization. No signed OS installer or updater is included,
and historical unsigned test kits are not release assets. Tray/panel code is implemented but native execution remains unverified.

未確認: 実本体との統合、Windows named pipe、各OSの実ウィンドウ、日本語IME、
スクリーンリーダー、GPU・高DPI、休止復帰、長時間動作、署名・実導入、起動時間・
待機CPU・メモリ。クロスビルド成功を実機動作成功とは扱いません。

Device rows use MyGo ButtonBase with a chevron, hover background, pointer cursor
and keyboard focus border. Enter/Space toggle the focused row; Tab moves between
rows. The row has an explicit button role, device-name label, toggle description
and expanded state. There are no nested interactive controls. These semantics are
configured in code; native screen-reader behavior remains unverified.

端末行全体をクリックできます。Enter/Spaceで開閉、Tabで移動でき、マウスポインターの重なりとキーボードの選択位置を
表示します。読み上げ用にボタンとしての役割・名前・操作説明・展開状態を設定していますが、各OSの
スクリーンリーダーでの実確認は未完了です。


## Confirm before sending or receiving / 内容を確認して送受信する

`--state-dir /path/to/profile --enable-send` explicitly enables reviewed actions.
The default is read-only. Messages keep the recipient and text fixed during confirmation.
Files use the existing prepare, review and confirmed-send steps, and incoming transfers require
explicit destination review or decline. Trust and pause state are never changed.
A missing/untrusted/paused peer disables actions; the core remains authoritative.
Demo actions are simulations without filesystem, socket or device changes.

Errors can occur after receipt. No automatic retry is performed. Uncertain
operations retain request identities and are locked; refresh history/status.
Command acceptance is not a delivery claim. Drafts remain only in process memory
and survive hide/reopen, but not quitting. Message history shows the latest six
records available in the refreshed snapshot, not a full archival browser.

実送信は明示フラグと内容確認が必要です。デモは通信しません。結果不明の操作は
自動再送せず、履歴・状態で確認します。閉じて隠しても下書きは維持しますが、
UIを終了すると保存されていない下書きは失われます。

`--tray` enables a macOS menu-bar / Windows tray panel. Close/Escape hides it;
Quit UI ends only this client. Native file pickers/IME must not cause blur dismissal:
the panel intentionally remains open until explicitly hidden. Linux or tray errors
fall back to a normal window. No login item or persistent OS setting is installed.

Additional fictional renders:
`--demo --locale ja --render-pane messages --render messages.png`
and `--render-pane files`. See [tray design](TRAY_DESIGN.md) and [Japanese text rendering](TYPOGRAPHY.md).

Runner integration tests use the same checksum-verified engine adaptations as the repository. Run the preparation commands above first, then `go test -race -tags=native_integration ./...`. The tag enables real protected IPC/Core fixtures, not real enrollment or installed-window acceptance.
