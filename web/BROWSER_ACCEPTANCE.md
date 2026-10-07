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
- `browser/compact-screens.spec.mjs` checks Japanese/English service, saved-service, network and settings forms at 375×844, 390×844 and 844×390. It verifies readable input sizes, visible keyboard focus around fixed headings/action areas, cancellation without runtime changes and full touch targets in compact navigation. These new cases require execution against the final integrated candidate; listing them is not a browser pass.
- `browser/workflows.spec.mjs` checks explicit send, retained drafts, UTF-8 recovery, reviewed receiving destinations and exact fixture bytes, additive attachments, upload failure/retry, trust, automatic receiving, service start/stop/copy/restart, advertised selection and scoped shares
- `browser/lifecycle-saved.spec.mjs` checks JA/EN logout cancellation, CLI-only startup guidance and offline create/edit/copy/reviewed-delete without visible peers. A separate logout case uses the fictional embedded backend and verifies one explicit acknowledgement followed by Go process exit; it never performs real enrollment or OS registration
- `browser/advanced.spec.mjs` checks JA/EN exact proxy review, fictional private-input cancellation and no automatic start, plus explicit TCP transport failure and retained runtime history after stop. It uses the existing fictional peers and does not perform enrollment or proxy start; real proxy authentication/lifecycle and application compatibility remain separate gates
- `browser/network.spec.mjs` checks offline setup, guided host review without activation, invalid relay inputs and whole-application Stop. Shutdown begins with an active service; cancellation preserves it, and one confirmed Stop must make the actual Go process exit zero within 20 seconds. Forced cleanup never counts as shutdown success
- `browser/saved-host.spec.mjs` adds a production-backed offline saved-host review/cancel flow in JA/EN at 1440px and 390px. A tagged helper seeds a fresh private synthetic host through production validation/writing, with no pairs or application trust. The real Core supplies the revision and public review; the fixture denies activation and all non-presentation commands. Review, Cancel, Close and reopen must submit no commands and leave state unchanged. These four cases require hosted execution; listing or unit-testing them is not browser acceptance. Actual relay start and physical-device pairing remain separate gates.

- `browser/advertised-overview.spec.mjs` covers JA/EN compact advertised-service review, cancellation, Back/Forward and one explicit start through the real Core API over the existing fictional backend. Its separate stale-route cases inject snapshot observations and failure; they establish presentation behavior, not a physical transport failure or recovery.
- `browser/group-failure-states.spec.mjs` covers JA/EN reviewed identities and observed member states after an uncertain action. Group review, mutation errors and resulting snapshots are synthetic API substitutions; these cases do not establish Core rollback or real application outcomes. A read-only retry does not repeat the mutation.

User interactions use Playwright locators and assertions through the real UI. Default fixture cases exercise the production Go management API. Selected UI regressions substitute responses: `layouts.spec.mjs` supplies synthetic layout state, and `message-uncertainty.spec.mjs` injects a message-error response. Those substitutions establish presentation/error-path behavior, not the corresponding backend outcome. The explicitly labelled `lan-pairing-review.spec.mjs` cases replace LAN state and fulfill or abort inspection/join responses for one fixed, invalid synthetic invitation. Those intercepted commands never reach Core and do not establish pairing, enrollment or permission acceptance. The helper preserves raw attempt counts and rejects unaccounted enrollment. Read-only observations and fixture setup remain separate from user interactions; instrumentation keeps command names and upload identity comparisons in memory. Synthetic composition events are likewise limited to frontend regression coverage.

## Local PNG card scope

`browser/device-card-png.spec.mjs` uses the unchanged production document CSP,
manifest-bound worker policy and embedded pinned reader. It checks twelve
synthetic digest-pinned images in both languages/modes (24 UI reads), a cropped
public-QR screenshot after the same capture privacy checks, malformed/oversized/
animated/metadata inputs, duplicate QR rejection, stale asset completion and
explicit Review. A separate temporary page performs intentional CSP denial
probes without replacing product scripts or weakening normal UI health checks.
No camera, real pairing, transport or external decoder service is involved.

The twelve representatives total 140,593 PNG bytes; they cover QR versions
22/23/24, minimum/Japanese/escaped aliases, optional hints, 256–2048-pixel samples,
rotation and transparency. The private 227-case research comparison is not a
product-browser result. Worker/raster mocks and Node WASM runs establish only
their stated boundaries. Run this existing Chromium gate against the exact
integrated source; no extra browser matrix is required by this checkpoint.

## Evidence and privacy

Upload only the explicit screenshot directory’s `*.png`, `typography-*.json` and `playwright-report.json`. The report contains named test results and source locations without raw error stacks, call logs or private values. A green report requires every collected test to run and pass.

Session files, access codes and temporary input/runtime files stay outside artifacts. Authentication errors omit raw call logs. Screenshots require the authenticated workspace and absent or empty private controls. Proxy credential forms and fields are always excluded, even when empty; all nonempty fields marked `data-private` are rejected. Automatic screenshots, video, traces and storage exports are disabled. Automatic failure-page snapshots are disabled; disposable Playwright output is private and removed. Failure screenshots use the same authenticated capture guard as successful screenshots.

Inspect the actual PNGs before accepting spacing, contrast, clipping or Japanese glyphs. Service-form captures include the lower action area. Keep each report and image attached to its exact source revision and CI run.

Real enrollment, device pairing, relay hosting, native IME/clipboard/folder dialogs, real-device delivery, external application compatibility, installed binaries, OS login and suspend/resume remain separate gates. Ready listeners, saved settings and in-process transfer success do not establish those results.

## Optional agent exploration

[agent-browser](https://github.com/vercel-labs/agent-browser) can be used for interactive agent exploration in an authorized browser environment. It is not a required dependency or a duplicate scripted CI suite. Read the installed version’s help and [official command guide](https://agent-browser.dev/commands), follow actual page observations and record the exact source under inspection.

Use an isolated private runtime with a short session/socket path. Keep login codes out of command arguments, captures and reports; start evidence capture only after authentication and inspect it for private controls. Never export cookies, storage, traces or raw session files. Findings from exploration become focused Playwright regressions when repeatable. Do not turn blocked local browser or socket access into an alternate execution route.


Client-helper cases in `browser/client-helpers.spec.mjs` use only fictional peer IDs and a synthetic public key. They cover JA/EN four-flow preview/back/cancel/save-only, separate saved-group selection/start review, common-relay/application caveats, narrow layouts, copyable HTTP settings with TLS guidance, and a reviewed finite runtime override followed by stop while the saved lifetime stays unchanged. These authored cases require the integrated Core helper contracts and must pass the pinned browser job before acceptance; source review or DOM tests alone are not browser evidence. No external client is launched/configured, no real enrollment occurs, and existing private capture guards remain unchanged.


`browser/startup-proxies.spec.mjs` adds authored JA/EN future-startup review/cancel/save/disable checks with no process restart, and fictional private proxy input/save/reveal/hide/delete. It verifies sanitized snapshots and never captures a private credential view. These cases use the real isolated Core command route when run in the browser CI; they have not been locally executed. Synthetic DOM tests cover stale revisions, persistence failures, generated credentials never being revealed automatically, late private responses, private-field erasure and lifecycle effects. Real OS launch, installed permissions, reconnection and application compatibility remain separate gates.

## Readability correction

`browser/readability.spec.mjs` covers rendered normal, hover, keyboard focus, disabled and read-only controls in light/dark themes at desktop, 375px touch and 1024px coarse-pointer/no-hover sizes. It checks painted text/icon contrast and accessible action names before hover, purposeful input boundaries, settings, service review, import controls and unsent composition. It does not require a contrasting perimeter around every button; a visible label or recognizable icon can identify that control. It preserves the private capture guards and does not start a service or send the draft. Field checks cover pointer and keyboard focus on search, selects, standalone/read-only textareas and the composed message editor, unchanged geometry, a single joined contour, validation color, toolbar focus ownership and forced-colors visibility. These cases must run against the final integrated source; enumeration is not execution. `src/readability.test.ts` independently measures the actual semantic palette, including soft/selected surfaces and matching system-dark values. Inactive-control readability is an explicit product choice, not a claim that WCAG requires disabled-control contrast.

Reference consulted: the official [UI UX Pro Max skill](https://github.com/nextlevelbuilder/ui-ux-pro-max-skill/blob/main/.claude/skills/ui-ux-pro-max/SKILL.md), its quick-reference and pro-rules, and the `Data-Dense Dashboard` / `Drill-Down Analytics` entries in its styles data. Applied guidance covers compact task grouping, visible focus and normal-state affordances, typography, semantic colors and progressive disclosure. The upstream installer/search scripts were not executed; no generated design-system match is claimed. Dense analytic dashboards are a reference for scanning and grouping, not a reason to add charts or reduce touch targets.

The advanced-connection review cases also require focus on the new review heading, the first private credential field and the restored name field on editing. DOM regressions cover returning while a list refresh is pending: an enabled heading receives focus without a delayed jump when the request completes. These assertions do not change or bypass the private-input capture guards.

The sanitized reporter retains only the failing test source basename and numeric line/column coordinates from allowlisted frames. It does not publish raw assertion values, stacks, call logs or private paths. Failure image names include a one-way test-identity suffix so language/theme/device profiles cannot overwrite one another.
