# Menu-bar and tray design

The `--tray` option is implemented. Native OS behavior has not been verified by
the checks recorded here. See [device test steps](DEVICE_TEST.ja.md) for the
remaining manual checks.

MyGo v0.7.4's [official tray guide](https://mygo.egoist.dev/docs/menus)
supports macOS menu-bar status items and Windows notification-area icons through
NewTray. A menu can open on click, or without a menu OnClick can toggle a native
window. The official menu-bar example uses a frameless window positioned from
Tray.Bounds, hidden on blur or Escape. OnRightClick can offer a separate menu.

On macOS, ActivationPolicyAccessory hides the Dock icon. This controls the GUI
application's presence, not the independent soba daemon. On Windows, an icon may
be in overflow rather than visibly on the taskbar; a stable TrayOptions.ID helps
preserve the user's visibility choice. Do not promise an always-visible icon.

A tray icon alone does not keep the MyGo event loop running. User close must hide the window rather
than destroy the last window, while explicit UI Quit must bypass that interception.
An explicit menu action should say "Quit UI (daemon continues)" rather than imply
that all soba activity stops. Read-only status continues using the existing control
client. Autostart and daemon-stop controls would be separate scope, not automatic.

No administrator privilege is inherently needed for a normal user's tray app;
MyGo's [Windows distribution](https://mygo.egoist.dev/docs/distribution) uses a
per-user installer. Signing, execution policy and device restrictions still apply.

Pending real-device checks: activation/focus, Escape/hide, multiple displays,
display scaling (DPI), light/dark icon contrast, Windows overflow and Explorer restart, macOS menu
bar placement, UI quit versus daemon lifetime. Linux has additional AppIndicator
requirements and should not silently lose the only way to reopen the GUI.

macOS右上・Windows通知領域への常駐は公式APIで可能です。試作への組み込みは完了しましたが、実機では未検証です。「閉じる」は小窓を隠す操作、「UIを終了」はGUIだけ終了する
操作として、本体の停止とは分離する設計です。各OSでの実動作確認は未完了です。


Implementation uses a persistent keyboard-capable Panel, manually positioned near
Tray.Bounds and clamped to the display work area. It is not a transient native
popover: MyGo flyouts may dismiss on focus loss, interfering with IME/file pickers.
No OnBlur handler is installed. Explicit Escape, Hide, close and tray toggle retain
the same app state. Linux/tray failure opens a visible normal-window fallback.
No OS auto-start or global setting is changed. Unit geometry and lifecycle policy
tests are not a replacement for real-device checks of multiple displays, Japanese input (IME) and file dialogs.
