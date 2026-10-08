# Opt-in Web activation acceptance proposal

This document retains the **Linux-only synthetic-owner acceptance design**, originally based on `0c14689`. [PR #26](https://github.com/webkaz-labs/sobalink/pull/26) now integrates the suite as the independent `web-activation` CI job. At head `d5412926` / tree `9c664d3`, [CI 37734409976](https://github.com/webkaz-labs/sobalink/actions/runs/37734409976) passed the Web job’s preparation, pure policy checks, fixture compilation and synthetic asset build, but failed its runtime: seven-case success and native cleanup were not established. The sanitized result did not expose individual case outcomes or a cause. The reviewed fixed-schema diagnostics added afterward require new exact-source CI evidence; this record does not infer a browser or helper cause. The [previous run](https://github.com/webkaz-labs/sobalink/actions/runs/37732701867) failed dependency preparation before these seven runtime cases; that failure is not erased by the source correction. Compilation, synthetic asset build and pure checks do not establish browser/helper acceptance. Product platform support is unchanged. See the current evidence summaries in [English](CONVENIENCE_PLAN.en.md#current-alpha6-evidence-snapshot) and [日本語](CONVENIENCE_PLAN.ja.md#alpha6の現在の証拠).

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

## Exact opt-in build and execution selectors

Use the repository's already reviewed offline dependency preparation and pinned toolchains first. Do not install tools/browsers implicitly.

1. Build a **test fixture binary**, not a distributable product:

   `go test -race -tags='managed_restart_native web_activation_native' -c -o <private-artifact-dir>/soba-web-activation.test ./cmd/soba`

2. Build only the synthetic browser entry, using existing installed dependencies:

   From `web`: `node node_modules/vite/bin/vite.js build --config playwright.activation.vite.config.mjs`

3. After explicit browser/native authorization, execute exactly:

   From `web`, set `SOBA_WEB_ACTIVATION_ACCEPTANCE=1`, `SOBA_WEB_ACTIVATION_BINARY` to the absolute reviewed fixture binary and `SOBA_WEB_ACTIVATION_ASSETS` to the absolute `.activation-browser-assets` directory. Then run:

   `node node_modules/@playwright/test/cli.js test --config playwright.activation.config.mjs`

Use `node browser/activation-runner.mjs` as the sole execution entry instead of invoking Playwright directly. It fixes selection and flags, creates a 0700 private run directory, uses a restricted child environment and writes only the fixed sanitized report outside that directory. Set `SOBA_ACTIVATION_CHROMIUM` to the absolute independently verified installed Chromium executable and optionally `SOBA_ACTIVATION_REPORT` to a sanitized-result destination. The direct Playwright command above describes the wrapper’s fixed child invocation, not an alternative authorized entry.

The selected file is `browser/activation-native.acceptance.mjs`. Its suffix deliberately does not match the ordinary browser suite's `*.spec.mjs`. Both Go build tags and Linux are required. The fixture TestMain dispatch is absent from ordinary product builds. The original standalone proposal changed no CI selector. PR #26 adds the separate `web-activation` job and its bounded wrapper; it does not add the opt-in endpoint-following native selectors to CI.

Existing native restart cases can be rerun separately with `managed_restart_native` and an exact `^TestManagedRestartNative...$` allowlist. This proposal does not authorize them or broaden the test run implicitly.

## Budgets, native cleanup and browser confinement

- One browser worker, no retries and seven exact named cases. The reporter requires all seven unique names, passed status, expected passed status, retry zero, no unexpected cases, no global errors and a passed final runner result. Missing/import/setup/global failures cannot look successful.
- A 150-second shared case deadline starts before resource acquisition. Body/setup is capped at 120 seconds to reserve teardown time; cleanup waits use the remaining absolute deadline instead of stacking independent budgets. Browser-context close has a five-second sub-budget within that same deadline.
- Playwright has a 20-minute global timeout; the private wrapper requests termination of its exact Playwright child at 21 minutes and gives up with failed/retained-private-state at 23 minutes. The integrated CI compile/run step has a 40-minute timeout and its whole job a 55-minute timeout; these outer limits do not extend the wrapper or case deadlines. A timeout never authorizes profile deletion or a passing summary.
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

## CI integration and retained design boundaries

The original proposal preferred a manually dispatched Linux x64 job. The integrated `web-activation` job now runs under the repository CI using the reviewed existing executor restrictions, Chromium background-network launch defaults and exact owned-origin routes, with the already approved Go/Node/dependency caches and pinned Playwright Chromium. Do not change OS firewall or network settings. Review the exact compiled fixture binary and browser assets before allowing loopback/process/browser execution. Upload only a sanitized result summary and source/binary hashes, never private runtime trees. A failure must retain the original stage result; source fixes require a new frozen source and renewed review before rerun.

Separate macOS/Windows descendant-supervision browser fixtures would need their own design and review; this document does not add them as release gates. This Linux subreaper harness is not portable acceptance evidence. Existing platform-native restart selectors remain separate. Do not mark unsupported/skipped jobs as acceptance. Keep real Core activation, genuine peer exchanges, installed binaries and actual OS-browser launcher observations distinct; missing coverage here alone does not add new release gates.


## Still-unperformed coverage

This seven-case source does not provide genuine timer/lease expiry, original-deadline expiry, malicious-origin/source denial, concurrent-tab, BFCache, background-throttling, changed-scope or full production-Core acceptance. Existing mocked tests for some of those paths are not counted as genuine browser/native coverage. Actual Core authorization dispatch, full React-panel usability, real peer exchanges, installed binaries and the OS browser launcher remain distinct evidence categories. The agreed product activation and distribution requirements still apply; the list of unperformed browser combinations does not add new release conditions.
