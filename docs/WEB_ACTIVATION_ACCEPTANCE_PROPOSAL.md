# Opt-in Web activation acceptance proposal

This is **opt-in Linux-only test source**, based on reviewed production `0c14689`. Product platform support is unchanged. The standalone acceptance source passed source review, offline tagged race compilation, synthetic asset build and pure checks. Actual browser/helper runtime acceptance remains unperformed; those component checks do not establish integrated-candidate runtime acceptance. This suite does not enable an existing CI or release gate. The exact integrated candidate requires review and separate runtime authorization.

## What the proposed suite genuinely executes

- Real Chromium pages against real numeric-loopback HTTP listeners.
- Production `webui.Server` origin/Fetch Metadata/session/CSRF/login and one-use session authorization code.
- Production frontend `api.ts` and `upgrade-handoff.ts`, bundled into an explicitly synthetic fixture presentation.
- Production `launchUpgradeHandoff`, anonymous bootstrap pipes, `runUpgradeHandoffProcess`, helper HTTP handler and actual embedded popup script.
- Production restart sequence, protected local IPC, real profile lock, ordered HTTP/IPC closure, shutdown acknowledgement and native old-process exit observer.
- A new real successor process, verified PID/instance/executable/offline state, real normal code issuance and real normal login against the successor Web server.
- An explicit `/open` request with real identity/URL verification. The final OS browser launcher is stubbed at its function boundary and records only a safe fixed marker. The test manually uses address-bar-style browser navigation for normal login.

## What remains synthetic or outside scope

Both old and successor owners use a small fictional backend. **No Core is constructed.** Review data, old attempted-network state and the successor's `network-started` result are scripted fixtures. They do not prove pair context, real network activation, transport, application readiness, real accounts, installed-binary behavior or actual OS browser opening.

The synthetic owner adapter invokes the production session-authorization check and delegates the stripped stop request to the production lifecycle stop route. It is not the production `Core.CheckWebUpgradeAuthorization` dispatch or `lifecycle.webHandoff(Core)` callback. Those concrete Core integrations require a separate synthetic-profile/product-Core acceptance stage. Existing pure/mock source tests and native restart tests are complementary evidence, not substitutes.

The fixture page imports the production opener/API but is not the full React settings panel. Actual full-panel layout/usability remains separate.

## Seven browser cases

1. English: review, popup, explicit confirmation/ack, genuine shutdown/exit/replacement, one-use code response, verified explicit OS-open stub, normal successor login, rejected acknowledgement/open replay.
2. Japanese: the same path and production localized popup.
3. Decline before confirmation: original owner stays alive; new explicit review becomes available.
4. Close before confirmation: no successor; original page recovers.
5. Logout before confirmation: originating session cannot authorize restart.
6. Logout after old bound run, while stop admission is paused in test-only glue: real authorization check denies consumption; no shutdown/replacement. Cancellation ends the remaining acknowledgement wait.
7. Missing shutdown acknowledgement: old owner exits without acknowledgement; no successor is launched. Cancellation ends the bounded wait.

The browser observation hook retains a claim token only in its page closure and submits actual HTTP replay probes. It neither stubs responses nor returns token/code bytes to reporting. Repeated authenticated `/status` proves private code is absent after first delivery. URLs are checked for query/fragment/credentials; private code values are compared against bounded child output/startup logs using booleans only.

## Exact opt-in build and execution selectors (proposal; not run)

Use the repository's already reviewed offline dependency preparation and pinned toolchains first. Do not install tools/browsers implicitly.

1. Build a **test fixture binary**, not a distributable product:

   `go test -race -tags='managed_restart_native web_activation_native' -c -o <private-artifact-dir>/soba-web-activation.test ./cmd/soba`

2. Build only the synthetic browser entry, using existing installed dependencies:

   From `web`: `node node_modules/vite/bin/vite.js build --config playwright.activation.vite.config.mjs`

3. After explicit browser/native authorization, execute exactly:

   From `web`, set `SOBA_WEB_ACTIVATION_ACCEPTANCE=1`, `SOBA_WEB_ACTIVATION_BINARY` to the absolute reviewed fixture binary and `SOBA_WEB_ACTIVATION_ASSETS` to the absolute `.activation-browser-assets` directory. Then run:

   `node node_modules/@playwright/test/cli.js test --config playwright.activation.config.mjs`

Use `node browser/activation-runner.mjs` as the sole execution entry instead of invoking Playwright directly. It fixes selection and flags, creates a 0700 private run directory, uses a restricted child environment and writes only the fixed sanitized report outside that directory. Set `SOBA_ACTIVATION_CHROMIUM` to the absolute independently verified installed Chromium executable and optionally `SOBA_ACTIVATION_REPORT` to a sanitized-result destination. The direct Playwright command above describes the wrapper’s fixed child invocation, not an alternative authorized entry.

The selected file is `browser/activation-native.acceptance.mjs`. Its suffix deliberately does not match the ordinary browser suite's `*.spec.mjs`. Both Go build tags and Linux are required. The fixture TestMain dispatch is absent from ordinary product builds. No npm script, existing CI selector or workflow was changed.

Existing native restart cases can be rerun separately with `managed_restart_native` and an exact `^TestManagedRestartNative...$` allowlist. This proposal does not authorize them or broaden the test run implicitly.

## Budgets, native cleanup and browser confinement

- One browser worker, no retries and seven exact named cases. The reporter requires all seven unique names, passed status, expected passed status, retry zero, no unexpected cases, no global errors and a passed final runner result. Missing/import/setup/global failures cannot look successful.
- A 150-second shared case deadline starts before resource acquisition. Body/setup is capped at 120 seconds to reserve teardown time; cleanup waits use the remaining absolute deadline instead of stacking independent budgets. Browser-context close has a five-second sub-budget within that same deadline.
- Playwright has a 20-minute global timeout; the private wrapper requests termination of its exact Playwright child at 21 minutes and gives up with failed/retained-private-state at 23 minutes. Proposed isolated CI job hard cap is 25 minutes. A timeout never authorizes profile deletion or a passing summary.
- A 45-second original review and the product ten-second helper lease are unchanged. Synthetic owners have a 70-second cooperative lifetime and an 80-second fixture-only self-exit watchdog. These do not add a product force-kill fallback.
- Each case creates a fresh protected `sr-` profile inside the wrapper’s 0700 `sa-` private directory. Cleanup ownership begins with allocation, including setup failures. Child environments contain fixture paths, temporary home and race options, not account credentials.
- A Linux test-only supervisor enables child subreaping before spawning the old owner. It owns the initial child, verifies role/parent registrations and opens native exit observers for old/helper/successor before owner work. Orphaned descendants, including failures before registration/readiness markers, become its waitable children. Success requires native observer completion and `wait4` reaching `ECHILD`; a closers marker or requested cancellation is never process-exit proof. Observer goroutines are joined before their handles close.
- Browser cancellation and a fixture-only stop marker request cooperative shutdown. Private profiles are removed only after the supervisor itself exits and its all-descendant/native-exit proof is verified. If exit remains uncertain, the directory stays private and the case fails. No-successor cases check the final registered-owner result after cleanup, not just immediate marker absence after Cancel.
- Each actual HTTP owner publishes its exact numeric-loopback origin through the protected supervisor channel before the browser receives readiness. The helper origin is registered from its real private-pipe descriptor. Origins are revoked when their native owner exits. Browser HTTP is denied by default unless its exact live origin is registered; credentials/query/fragment URLs are rejected. Service workers are blocked and all WebSockets are denied. Only sanitized violation counts are retained.
- Reviewed browser controls combine the pinned Chromium launch defaults that suppress background networking, the exact owned-origin routes above, and existing executor restrictions. Playwright routing is not an OS-wide firewall. Preserve these controls without changing OS firewall or network settings; no new outward-network-denied environment is required by this source review.

## Private diagnostics and artifact boundary

`PLAYWRIGHT_NO_COPY_PROMPT` suppresses page snapshots but does **not** prevent `error-context.md`. This proposal explicitly permits sensitive transient framework files only inside the wrapper-owned 0700 directory. No Playwright patch or zero-transient-file claim is made.

- Screenshots, traces, video, HAR/storage exports and debug logging remain disabled or rejected before sign-in.
- Test-body, private-operation and fixture failures are sanitized; raw framework diagnostics may nevertheless exist privately. Child stdout/stderr are bounded and discarded, never forwarded as artifacts.
- The wrapper exports only a newly constructed, fixed-schema sanitized summary, independently checks runner exit and exact-seven acceptance, and requires private native cleanup. It never copies a framework report, error object, attachment, log, page state, code, token or private path.
- After a successful runner exit and verified native-profile cleanup, the wrapper destroys the complete private run directory, including transient error output. On failure or uncertainty it conservatively retains that directory, categorically unexportable, until the isolated runner is terminated and its process scope has been independently cleaned up. Failure retention is not a successful cleanup claim.
- CI artifact rules must allow only the explicit sanitized report and independently generated source/binary/asset hashes. Never upload the private root, temporary directories, browser output trees or broad failure artifacts.

## CI proposal

Start with a manually dispatched Linux x64 job using the reviewed existing executor restrictions, Chromium background-network launch defaults and exact owned-origin routes, with the already approved Go/Node/dependency caches and pinned Playwright Chromium. Do not change OS firewall or network settings. Review the exact compiled fixture binary and browser assets before allowing loopback/process/browser execution. Upload only a sanitized result summary and source/binary hashes, never private runtime trees. A failure must retain the original stage result; source fixes require a new frozen source and renewed review before rerun.

After Linux behavior is established, design and independently review separate macOS/Windows descendant-supervision browser fixtures. This Linux subreaper harness is not portable acceptance evidence. Existing platform-native restart selectors remain separate. Do not mark unsupported/skipped jobs as acceptance. Keep real Core activation, genuine peer exchanges, installed binaries and actual OS-browser launcher acceptance as distinct gates.


## Still-unperformed coverage

This seven-case source does not provide genuine timer/lease expiry, original-deadline expiry, malicious-origin/source denial, concurrent-tab, BFCache, background-throttling, changed-scope or full production-Core acceptance. Existing mocked tests for some of those paths are not counted as genuine browser/native coverage. Actual Core authorization dispatch, full React-panel usability, real peer exchanges, installed binaries and the OS browser launcher remain distinct gates.
