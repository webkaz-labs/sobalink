# Native UI validation / Native UI の確認結果

## Current prerelease integration / 今回の統合

The current integration starts from main `64a92eeb`, which includes PR #66,
and incorporates the Native UI from PR #67. This is source preparation, not a
published-release or installed-binary receipt. Exact-source full CI, packaging,
signatures/provenance, public-asset retrieval and installed-binary checks must be
recorded for the final release source. Historical unsigned device-test kits are
not release assets. Distribution integrity signatures are not OS developer
signing, Windows publisher trust or macOS notarization.

今回の統合は PR #66 を含む main と PR #67 のNative UIを対象に準備しています。
以下の古いソースの検証結果は今回の合格証跡ではありません。最終ソースのCI・配布・
実導入の結果を別途確認してください。実GUI、IME、読み上げ、実ネットワーク通信の
合格は未確認です。OSの保護機能を回避して試験しません。

## Historical prototype evidence / 過去の試作の証跡

The sections below preserve the original source-specific record, including its
pending and blocked states. Those states describe that recorded attempt, not a
claim about the final prerelease. Later CI results must identify their own source.


2026-11-04, Linux x64 build environment. Original local prototype base commit:
`a08b2cb0d257db7ea2fb4be29e7abd88619a9fb5`. MyGo v0.7.4, Go 1.27.3.

## Local baseline passed / ローカル基準結果

- 39 focused UI/model, tray, message and file tests with `-race`: locale rules, failure clears old data,
  single-flight refresh, filter/details/language interactions, narrow empty state,
  successful IPC response carrying failed network state remains visible, compact peer/detail bounds, About disclosure, row keyboard activation, and whole-row edge click/hover.
- `go vet ./...` and gofmt.
- CGO-disabled cross-builds, stripped with `-s -w`:
  - Linux x64: 10,277,127 bytes.
  - Windows x64: 10,907,136 bytes (`-H=windowsgui`).
  - macOS ARM64: 9,930,610 bytes.
- MyGo headless rendering: Japanese 640x560 and 420x560; expanded details at
  360x560 DIPs with 2x rendering (720x1120 pixels).
  Japanese wide/narrow images inspected visually; whole-row controls, readable text,
  fictional-data marking and scroll viewport visible. This is limited visual QA,
  not all states or OS-window acceptance.
- Export with a live profile is rejected before creating an output file.
- Root go.mod/go.sum and existing cmd/internal/web tracked files unchanged.

Sizes describe only this GUI client, not an integrated sobalink distribution.
Build results do not establish native execution on the target OS.

## Blocked / 環境制約

The complete test suite includes `TestNativeIPCReadOnly`, which creates a
user-private temporary fixture and invokes actual control.Serve/control.Call.
The environment rejects Unix socket creation (`socket: operation not permitted`),
including a permission-reviewed retry. That test failed at listen, before the
client round trip. It is not skipped or represented as passing. Other focused
tests do not establish real IPC integration.

Full suite command: `go test -race ./...`.
Successful focused command:

```sh
go test -race -run 'Test(Exchange|FilePane|MessagePane|Conversation|Locale|Refresh|Repeated|View|Narrow|Network|Compact|Device|NativeTray|JapaneseFont)' ./...
```

全体テストはUnix socket作成制限で未合格です。成功した限定テストと分けて扱います。

## Not tested / 未確認

Actual daemon state, actual devices, Windows named pipes, OS window rendering,
GPU, Japanese native IME, accessibility APIs, high-DPI desktop operation,
startup latency, idle CPU, resident memory, suspend/resume, signed installers,
long-running operation. No runtime performance claim is made. This report distinguishes the local baseline from the separate runner validation below.

## Compact refinement

Default window reduced to 460x560 DIPs with a 360x440 minimum. Main content now
prioritizes network state and peers. Device-row activation replaces the redundant
Details button; permissions are collapsed initially. Settings are not implemented.
Message/file demo actions are explicitly simulated; real IPC actions require opt-in and review.

Bounds tests cover 360/420/640-DIP widths and 2x scale for expanded Japanese
content. Pointer edge activation, hover cursor, focus, Enter, Space and Tab are
covered with MyGo's headless UI tester. Button role, label, description and
expanded semantics are set explicitly; the public Tester API does not expose its
accessibility tree. Native VoiceOver/Narrator/NVDA acceptance is not claimed.

Menu-bar/tray implementation is documented separately in TRAY_DESIGN.md.
It is implemented and unit-tested, but native OS tray interaction remains unverified.


## Exchange and persistent panel update

- Protected IPC adapter tests validate command names/payloads, read-only guards,
  review matching, cancellation and no retry after uncertainty.
- Per-peer messages: immutable review, single-flight submission, uncertainty lock,
  demo never calling transport and Japanese UI interaction tests.
- File pane: exact incoming awaiting-acceptance contract, review identity matching,
  picker cancellation, mutation request retention, receipt/child status retention.
- Tray: supported platform/fallback policy, close-to-hide, original icon generation,
  persistent panel settings and tray/display geometry unit tests.
- Integration: recipient draft isolation, read-only labeling and Japanese review
  renders at 360x560 DIPs and 2x scale. Message/file review images visually checked.
- No real message/file was sent or received. Native pickers, protected IPC and
  daemon-to-peer transfers remain unverified. Injected test responses are not an
  actual Core or network integration test.
- Native screen-reader announcements, initial/reverse focus traversal, IME,
  native font scaling/high contrast and panel/device lifecycle remain acceptance work.
- Manual snapshot Refresh is deliberate; the display is not a live subscription.

The Japanese punctuation issue was reproduced and fixed by selecting installed
Japanese font faces for the whole text run. Language tagging alone was insufficient
in the Linux renderer. See TYPOGRAPHY.md for the exact evidence and font licensing.


## Runner validation / ランナー検証

The dedicated Native UI PoC workflow uses isolated temporary profiles and fictional
test peers only. It runs the nested module on Linux x64/ARM64, macOS ARM64 and
Windows x64. No credentials, enrollment, user profile or actual devices are used.
Runner results are pending; creating a workflow is not a passing receipt.

Protocol fixtures must distinguish actual protected local IPC and unchanged Core
message/file handlers from the injected peer transport. These do not demonstrate
an installed daemon binary, real network enrollment, or native tray/IME behavior.
The UI's fake-response safety tests remain separately identified.

Tagged TestNativeProtectedIPCCoreExchange starts an isolated test-helper process
hosting two unchanged Core instances. The parent uses actual control.Call through
private sockets/named pipes. Injected net.Pipe peer streams carry the real Core
message and file HTTP protocols; no external network is contacted. Assertions cover
bidirectional message delivery, request replay/no duplicate, changed-payload replay
rejection, staging hash/size, no offer before confirmation, commit replay/status,
explicit receive acceptance, both completion counters and saved file bytes/hash.
The helper shutdown is joined. This is a test daemon process, not the installed
production soba executable.

TestFailureIntegration tests use scripted adapter responses for post-acceptance
errors, malformed/stale responses and no-retry UI behavior. They passed ten local
race repetitions. They do not prove daemon failure injection or real delivery.

The runner draft is based independently on main commit
`dbd50b0ecb9a4c8b6f1d1b182b78fcd445432579`; unrelated diagnostic changes
from the original prototype base are not included. Core/IPC sources are identical
between these bases.

First runner attempt on commit `905bc60` reached the real IPC/Core message path,
but the file subtest failed on all four platforms. The synthetic profile had been
written before first Core startup without receive-accounting state, correctly
triggering the existing legacy receive-recovery guard. The fixture now starts a
fresh Core and configures its fictional network/trust through ordinary Core
commands. No receive guard or production code is weakened. A separate explicitly
in-process diagnostic reproduces the issue without sockets; it is not a substitute
for the corrected cross-process runner test. Corrected runner results are pending.
