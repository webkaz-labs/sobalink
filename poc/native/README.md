# Native UI PoC / Native UI 試作

An isolated MyGo v0.7.4 prototype, read-only by default. The existing daemon, CLI, React UI,
root Go module and release process are unchanged. This is not a replacement UI.

既存の本体・CLI・React画面を変更しない、MyGo v0.7.4の試作です。既定では読み取り専用です。
接続・共有・許可・停止・更新の操作は実装していません。

## Compact desktop layout / コンパクト表示

The default window is 460x560 DIPs, minimum 360x440. Network state and peer
availability come first. Click a device row to disclose its permissions; background
explanation lives under About. A separate messages/files pane keeps interactive controls outside device-row buttons. Settings remain in the existing Web UI/CLI.

既定460×560、最小360×440の小窓です。接続状態と端末一覧を優先し、許可の情報は
端末行のクリック、補足は「この試作について」にまとめています。やり取りは別画面で開き、設定は既存のWeb UI/CLIで扱います。

## Run / 起動

Go 1.27.3 or later is required **only for this nested module**. Build from this
folder. Its local replace imports the repository's actual internal/control code;
no copied IPC implementation or new authentication boundary is introduced.

```sh
go run ./cmd/prepare-engine
go run ./cmd/prepare-engine --verify
cd poc/native
go run . --demo
go run . --demo --locale ja
go run . --demo --tray
go run . --state-dir /path/to/existing/profile
CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o soba-native .
```

Choose exactly one of `--demo` and `--state-dir`. Live mode performs no automatic
request: select Refresh status. By default only the literal `status` command is sent through the existing user-private local IPC with a three-second timeout. With `--enable-send`, reviewed message/file commands reuse the same protected IPC. Mutations use the app lifetime context, not the status timeout. The daemon is
never started or stopped. Failure clears stale displayed data; repeated requests
are single-flight. IPC success and network health are displayed separately.

`--demo` は架空データです。実IPCモードは既存プロファイルを明示し、「状態を取得」
を押した時だけ読み取ります。本体を自動起動・停止しません。取得失敗時は古い表示を
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

画像はMyGoの実UIをheadless描画したものです。実OSウィンドウのスクリーンショット
ではありません。GPU・IME・読み上げ・ウィンドウ装飾の受入結果には含めません。
実データの画像出力は拒否します。

## Checks / 確認

```sh
go test -race ./...
go vet ./...
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
runtime check. No signed installer or updater is included. Tray/panel code is implemented but native execution remains unverified.

未確認: 実本体との統合、Windows named pipe、各OSの実ウィンドウ、日本語IME、
スクリーンリーダー、GPU・高DPI、休止復帰、長時間動作、署名・実導入、起動時間・
待機CPU・メモリ。クロスビルド成功を実機動作成功とは扱いません。

Device rows use MyGo ButtonBase with a chevron, hover background, pointer cursor
and keyboard focus border. Enter/Space toggle the focused row; Tab moves between
rows. The row has an explicit button role, device-name label, toggle description
and expanded state. There are no nested interactive controls. These semantics are
configured in code; native screen-reader behavior remains unverified.

端末行全体をクリックできます。Enter/Spaceで開閉、Tabで移動でき、hoverとfocusを
表示します。button role・名前・操作説明・展開状態を設定していますが、各OSの
スクリーンリーダーでの実確認は未完了です。


## Reviewed exchange / 確認付きのやり取り

`--state-dir /path/to/profile --enable-send` explicitly enables reviewed actions.
The default is read-only. Messages freeze recipient and text before confirmation.
Files use existing prepare/review/commit dispatch, and incoming transfers require
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
and `--render-pane files`. See TRAY_DESIGN.md and TYPOGRAPHY.md.

Runner integration tests use the same checksum-verified engine adaptations as the repository. Run the preparation commands above first, then `go test -race -tags=native_integration ./...`. The tag enables real protected IPC/Core fixtures, not real enrollment or installed-window acceptance.
