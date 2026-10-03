# Browser acceptance

Playwright Test is the deterministic browser gate. The pinned `@playwright/test`, `playwright` and `playwright-core` versions are 1.63.0. Tests use production Go HTTP, authentication, CSRF, CSP and embedded frontend assets with fictional in-process peers. Each named test gets a fresh private fixture and browser context.

Run on an environment authorized to start the Go fixture and Chromium:

```sh
npm ci --prefix web
npm --prefix web run build
node web/node_modules/@playwright/test/cli.js install --with-deps chromium
go build -tags=soba_e2e,ts_omit_portmapper,ts_omit_captiveportal,ts_omit_useproxy -o /tmp/soba-e2e ./cmd/soba-e2e
SOBA_E2E_BINARY=/tmp/soba-e2e \
SOBA_SCREENSHOT_DIR=/tmp/soba-playwright-screenshots \
npm --prefix web run test:browser
```

Install Japanese sans-serif fonts, such as Noto Sans CJK JP, before glyph acceptance. `npm --prefix web run test:browser:safety` checks fixture and artifact privacy without launching a browser. `npm --prefix web run test:browser -- --list --reporter=list` enumerates tests without executing them; it is not browser evidence.

## Test structure

- `browser/fixtures.mjs` owns private session setup, authenticated capture, read-only state assertions, fictional input files and bounded process cleanup
- `browser/layouts.spec.mjs` checks Japanese/English, light/dark, four viewport widths, graph/list navigation, keyboard focus, details, connectors and actual Japanese font metadata
- `browser/workflows.spec.mjs` checks explicit send, retained drafts, UTF-8 recovery, reviewed receiving destinations and exact fixture bytes, additive attachments, upload failure/retry, trust, automatic receiving, service start/stop/copy/restart, advertised selection and scoped shares
- `browser/network.spec.mjs` checks offline setup, guided host review without activation, invalid relay inputs and whole-application Stop. Shutdown begins with an active service; cancellation preserves it, and one confirmed Stop must make the actual Go process exit zero within 20 seconds. Forced cleanup never counts as shutdown success

Actions use Playwright locators and assertions through the real UI. Tests do not replace API routes or issue mutation commands directly. Read-only state checks return only assertion booleans; request instrumentation keeps command names and upload identity comparisons in memory. Synthetic composition events are explicitly limited to frontend regression coverage.

## Evidence and privacy

Upload only the explicit screenshot directory’s `*.png`, `typography-*.json` and `playwright-report.json`. The report contains named test results and source locations without raw error stacks, call logs or private values. A green report requires every collected test to run and pass.

Session files, access codes and temporary input/runtime files stay outside artifacts. Authentication errors omit raw call logs. Screenshots require the authenticated workspace and absent or empty private controls. Automatic screenshots, video, traces and storage exports are disabled. Automatic failure-page snapshots are disabled; disposable Playwright output is private and removed. Failure screenshots use the same authenticated capture guard as successful screenshots.

Inspect the actual PNGs before accepting spacing, contrast, clipping or Japanese glyphs. Service-form captures include the lower action area. Keep each report and image attached to its exact source revision and CI run.

Real enrollment, device pairing, relay hosting, native IME/clipboard/folder dialogs, real-device delivery, external application compatibility, installed binaries, OS login and suspend/resume remain separate gates. Ready listeners, saved settings and in-process transfer success do not establish those results.

## Optional agent exploration

[agent-browser](https://github.com/vercel-labs/agent-browser) can be used for interactive agent exploration in an authorized browser environment. It is not a required dependency or a duplicate scripted CI suite. Read the installed version’s help and [official command guide](https://agent-browser.dev/commands), follow actual page observations and record the exact source under inspection.

Use an isolated private runtime with a short session/socket path. Keep login codes out of command arguments, captures and reports; start evidence capture only after authentication and inspect it for private controls. Never export cookies, storage, traces or raw session files. Findings from exploration become focused Playwright regressions when repeatable. Do not turn blocked local browser or socket access into an alternate execution route.
