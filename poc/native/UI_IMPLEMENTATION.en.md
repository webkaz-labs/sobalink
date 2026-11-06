# Native UI implementation policy

[日本語](UI_IMPLEMENTATION.ja.md) · [Native companion](README.md) · [Development principles](../../docs/DEVELOPMENT_PRINCIPLES.en.md)

Keep the screen definition readable as an outline of the interface. Layout code composes named components and connects controls to named operations; it does not implement IPC, file selection, dispatch workflows or authorization decisions inside nested `Children` callbacks.

This policy applies to the optional native companion. It does not change the CLI/Web UI contracts or the native companion's experimental acceptance status.

## MyGo guidance and project choices

The following behavior is documented by **MyGo v0.7.4**, the version pinned in this module:

- A view is a Go function that builds the interface from application state. Helpers can receive the same context and build part of a view. A frame can have several build passes.
- `Clicked` can invoke an operation beside its control. Use deferred callbacks when an operation changes the collection currently being built.
- Give list controls stable keys before construction. An `Element` belongs to one build pass. Do not keep build values in application state or background workers; use `Handle` or `Services` for persistent identities and services.
- UI state is read on the main thread. `Window.Update` schedules changes there; invalidation alone does not synchronize unguarded shared state.
- Copy the theme before changing it. Read colors and metrics through `Context.Theme`, `Theme.Space` and `Theme.Rem` so custom elements follow the theme.
- `Context.Preferences` exposes desktop accent, high contrast, text scale and reduced motion. The default theme applies these settings.
- `ui.Tabs` exposes a selected tab list to assistive technology and provides arrow/Home/End navigation with one Tab stop.
- `mygo vet` checks native build-value lifetimes in addition to running Go vet.

Sources: [views and lifetimes](https://github.com/egoist/mygo/blob/v0.7.4/docs/ui/views.md), [styling and themes](https://github.com/egoist/mygo/blob/v0.7.4/docs/ui/styling.md), [accessibility](https://github.com/egoist/mygo/blob/v0.7.4/docs/ui/accessibility.md), [testing](https://github.com/egoist/mygo/blob/v0.7.4/docs/ui/testing.md), [tabs](https://github.com/egoist/mygo/blob/v0.7.4/docs/ui/tabs.md), [vet implementation](https://github.com/egoist/mygo/blob/v0.7.4/cmd/mygo/vet_ui.go), [desktop preference implementation](https://github.com/egoist/mygo/blob/v0.7.4/ui/preferences.go).

**The separation below is Sobalink's implementation policy**, not a claim that MyGo requires MVVM, a particular directory structure, or a separate component framework. Small direct bindings and presentation conditions remain ordinary Go. Avoid wrappers that only rename a single widget or obscure the screen's structure.

## Boundaries and source map

- **Screen composition:** `frame_view.go`, `view.go`, `conversation_view.go`, `messagepane_view.go`, `filepane_view.go`, `tray_view.go`. Keep top-level screens short. Use components with a UI meaning, such as a device row, network card, message history, outgoing review, incoming review and dispatch receipt. Conditional visibility and iteration over prepared display data belong here.
- **Presentation:** `presentation.go`, `messagepane_presentation.go`, `filepane_presentation.go`. Derive labels, filtered/recent history, counts, eligibility and display status outside layout nesting. Do not translate raw protocol identifiers, user content or machine JSON.
- **Application state and operations:** `app.go`, `messagepane.go`, `filepane.go`. Own navigation, retained peer-specific drafts, immutable reviewed values, busy state, request identity, operation guards and result handling. Controls call named methods. Guard the operation itself as well as showing a disabled control.
- **Native dialog adapter:** `filepane_dialog.go`. Capture window ownership on the UI thread; keep dialog configuration outside screen composition. Workers receive ordinary values and callbacks, never build-pass elements or contexts.
- **Transport boundary:** `exchange.go` and the IPC loader in `model.go`. Keep protocol validation, confirmation and protected IPC contracts here. A refactor must not broaden network access or permission scope.
- **Theme:** `theme.go`. Adapt the shared Web light/dark palette from [`internal/uibrand`](../../internal/uibrand/README.en.md) and retain native typography/spacing tokens. Copy the system theme and preserve its appearance selection and metrics. Keep decorative borders separate from the stronger control boundary. Desktop high-contrast adaptations take precedence over the whole companion palette. Preserve an explicit desktop accent together with its text, hover/pressed, selection and focus colors; never replace only part of that set. Use named tokens for shared screen/card/row padding, section/item/action gaps and corner radii; corner radii follow the theme radius. Keep functional limits such as text-input line counts and native window placement bounds outside the visual theme. Do not scatter color literals through view files. Optional visual effects require separate evaluation; no plugin is required by this policy.

## Shared visual identity

The Web baseline colors, three-bar mark and functional icon paths have one source in `internal/uibrand`. Native controls keep platform density, typography, keyboard behavior and OS accent choices. `brand.go` parses the shared vectors once; labeled action helpers compose the same icons with native buttons. Long action labels and review action rows can wrap when text is enlarged. Status labels retain their existing meaning: accepted/submitted requests are not colored as confirmed delivery.

The header, process-local window/Dock icon and single-color tray image derive from the same mark. The small tray variant makes all three bars opaque, and OS template tinting follows the menu bar/taskbar. The Web favicon retains a standalone brand color; native vectors inherit their foreground, including high contrast. PNGs are rendered once when needed from the shared vector using MyGo. This does not add an executable file icon, macOS app bundle, installer, persistent setting or new distribution format. Real OS icon appearance remains a separate acceptance check.

## Shared frame, headers and actions

Applied in the current source:

- `nativeFrame` applies pending status results, theme, language and font at one build boundary for normal, tray and fallback windows. `nativeScreen` shares the content inset; each screen retains its own spacing.
- `nativeHeader` shares title placement and action spacing. The device screen keeps its title and language switch; the conversation keeps Back, the peer name and Refresh. Message/file panes already share that conversation header and the standard tabs. This is shared structure, not identical controls on every screen.
- `nativeShell` owns window/tray lifetime. Its Hide/Quit controls and fallback notice remain outside screen content. Back changes screens; Hide preserves the app; Quit UI ends the client only. None of these controls changes the daemon or permissions.

For future screens or menus, reuse these boundaries only where responsibilities match. Keep screen actions visible at their point of use and window actions in the shell; introduce a shared menu only when actual repeated actions need it. A global settings/menu framework is not implemented. Do not move navigation state, operation guards or IPC into layout helpers, retain build callbacks, or add a new component framework merely to unify appearance.

## State, concurrency and safety

Workers publish bounded completion events; the UI thread drains them before presenting state. A wake requests a new frame but is not permission to mutate UI-owned data. Retain operation completion while a window is hidden, and reject stale results. Navigation and flyout dismissal must not lose a pending review or move a draft to another peer.

Keep these invariants in named operation methods:

- At most one operation per pane at a time. Double clicks must not duplicate a mutation.
- Explicit review precedes send, accept or decline. The reviewed recipient, process, revision, contents and incoming offer must still match where applicable.
- Cancel/back before submission must not submit. A cancelled application context must not start new work.
- The native message composer checks its 16,384 UTF-8 byte limit before review and dispatch; oversize drafts remain editable. Core can have a different configured limit. Only its direct, pre-dispatch `message_too_large` rejection permits editing after a submission. Other transport, peer and response errors remain uncertain. This is a local admission safeguard, not IPC-budget negotiation.
- An uncertain mutation keeps its request identity and stays locked against automatic or manual resend. Read-only status checks must not silently unlock it.
- Demo mode never invokes real transport. Live mutation requires the explicit sending flag and an eligible, trusted, unpaused peer.
- Do not expose raw transport errors or real profile data in test images. Use fictional fixtures and headless `--demo` rendering.

## Accessibility and validation

Keep native button/input behavior, accessible names, row roles, expanded state, stable keys, keyboard focus and activation. Status changes should be announced once, not on every build pass. Maintain Japanese and English labels and Japanese font fallback. Use the standard `ui.Tabs` for message/file selection rather than reproducing its selected state and keyboard behavior with buttons. Exercise narrow layouts, dark/light appearance, high contrast, desktop accent changes and text enlargement when theme tokens change. Headless keyboard tests verify arrows, Home/End and Tab focus; they do not establish real screen-reader acceptance.

From `poc/native`, with the repository's checksum-verified engine adaptations prepared:

```sh
go test -race -count=1 ./...
go run github.com/egoist/mygo/cmd/mygo vet .
```

The second command uses the module-pinned MyGo version, runs `go vet ./...`, and checks build lifetimes. The native CI matrix runs it on Linux x64/ARM64, macOS ARM64 and Windows x64; CI also enables the existing `native_integration` fixtures. Keep those fixtures and the package/build checks intact.

Tests must cover operation guards directly, in addition to tester-driven button interactions: repeat/cancel/back, changed recipient/process/offer, draft isolation, delayed completion, stale events, uncertain outcomes, locale, keyboard behavior and compact views. Headless tests and cross-compilation do not establish real-device screen-reader, native dialog, OS-login/suspend or network acceptance. Record those separately in [validation](VALIDATION.md).
