# CI matched to the change

[日本語](CI_EFFICIENCY.ja.md) · [Verification](VERIFICATION.en.md) · [Development principles](DEVELOPMENT_PRINCIPLES.en.md)

Run the checks affected by the complete change. Ordinary documentation does not need an application build or four operating systems. Frontend changes need frontend and browser checks. Sensitive, shared or uncertain changes keep the complete native matrix. Every prerelease still runs the independent full validation and distribution/signature/install gates.

## Scope

| Complete change | Checks |
| --- | --- |
| Ordinary root README/SECURITY or top-level `docs/*.md` prose | Lightweight Git scope proof and required-document presence; application tests, builds and packages are not run |
| `web/src` TypeScript/TSX/CSS or existing `web/browser/*.mjs` acceptance code, optionally with ordinary prose | Linux frontend unit tests, two matching exact-lock builds, fixture safety tests and real browser acceptance |
| Accompanying generated `web/dist` changes | Included in frontend scope only with a frontend source change; regeneration must match |
| Reviewed Go-only changes in `internal/servicepresets` or `internal/boundedlog`, optionally with ordinary prose | Changed packages and transitive reverse dependencies, including test imports, tested with race detection and vet on Linux |
| Reviewed CLI presentation changes on PRs or main, optionally with ordinary prose | All four native short/safety suites, browser and package/manifest checks; three real-time lifecycle/lease gates are not run |
| Transport, authentication, Core, configuration, timers, shared helpers, dependencies, lockfiles, build configuration, CI policy/workflows, or any unknown path | Full native Linux amd64/arm64, macOS arm64, Windows amd64, browser and package/manifest validation |
| Combinations of the reviewed CLI presentation, frontend and scoped-Go domains | All four native short/safety suites, browser and package/manifest checks; three real-time lifecycle/lease gates are not run |
| Platform-specific Go, cgo, unreviewed imports, policy/provenance Markdown, unsafe modes/types or unverifiable input | Full |
| Daily scheduled validation, manual full validation or any prerelease | Full |

### Native-short for reviewed changes

The `native-short` allowlist contains exactly `cmd/soba/help.go`, `cmd/soba/errors.go`, `cmd/soba/errors_test.go`, `cmd/soba/human_output.go` and `cmd/soba/human_output_test.go`. New paths, new imports, build directives and mixtures with unreviewed categories remain full. Combinations with the existing frontend and scoped-Go domains use all four native-short targets. Pure docs/frontend/scoped-Go changes retain their narrower existing scopes. Both old and new changed blobs are checked, including complete affected scoped-Go packages even in mixed plans. The complete authenticated PR merge or main-push diff must qualify; a later presentation-only commit cannot hide an earlier runtime change. Generated assets still require a corresponding frontend source change.

This scope omits only natural direct-LAN rekey/idle lifecycle, guarded relay real-time lease continuity, and relay-only real-time lease/idle continuity. Four-target race/vet, repeated IPC, Windows directory barriers, context TCP/TLS control, managed activation/restart, functional direct/relay recovery, synthetic expiry/rekey, browser, packages and manifest validation remain. The independent seven-case Web and two-case product jobs retain their PR/manual entry conditions and failure behavior; scheduled full runs require both. Test repetition counts and production timers are unchanged.

Authenticated main pushes can use the same closed native-short selection as PRs. Created/deleted/forced main pushes and unsupported events still require full validation. Keep the exact-source full validation gate below in place before deploying this main-shortening policy; a short green workflow cannot establish release eligibility. Related transport/lifecycle changes remain full; no new file allowlist, per-family long-test routing or test-count reduction is introduced. Daily full runs are described below.

The Go allowlist is deliberately small. It is not permission to run any Go change on one operating system. OS-specific files, build constraints and dependencies outside the reviewed package boundary require the full matrix. The scoped runner uses current Go import graphs, including integration-tag variants for reverse-dependency selection, but executes ordinary Linux tests rather than the long native integration suites. If graph discovery is uncertain, it runs all ordinary Go packages. Failed commands and missing actual test passes remain failures.

Markdown used as development policy or upstream source provenance is not ordinary prose. The required root documents must remain present. Guides are included in release archives, but that alone does not require rebuilding the application for every prose edit. Code and release validations continue checking the package contents. The separate resource and relay measurement workflows are triggered by their implementation inputs, not wording changes in their reports.

## Complete diffs and an honest required check

The workflow runs on every supported PR/main push. It does not use a whole-workflow `paths-ignore` filter, which could leave the required check pending.

For a PR, the classifier checks the complete effective change between the verified base parent and the tested merge commit. For a main push it checks event `before` through `after`, including all pushed commits. It never judges only the latest commit. A PR that contains a Core change therefore remains full even if its latest commit fixes a browser test. Previous successful jobs are not reused.

For PRs, the tested source is the Actions `GITHUB_SHA` and exact `refs/pull/<number>/merge` checkout. Its two parents must match the event's base and head commits in that order. A well-formed `merge_commit_sha` hint from the PR's background mergeability calculation may be stale; it cannot replace or override this proof. Malformed hints, wrong refs or mismatched actual parents still require full validation. This uses the fixed event and Git objects, not a mutable live-PR API read. See GitHub's [Actions merge-branch identity](https://docs.github.com/en/actions/reference/workflows-and-actions/events-that-trigger-workflows#pull_request) and [mergeability field semantics](https://docs.github.com/en/rest/pulls/pulls#get-a-pull-request).

The classifier checks full Git tree inventories, both rename endpoints, file modes and repository/event identity. Missing history, incomplete or malformed diffs and unknown changes choose full validation. The aggregate repeats the scope proof; an artifact cannot grant itself a smaller scope. A classifier failure does not silently skip application validation.

`ci-required` remains the required status. It runs even when application jobs are skipped and verifies the actual required jobs and named steps for the current attempt. Documentation results say **“Documentation only; application tests, builds and packages NOT RUN”**. Frontend and Go results identify their limited coverage. Only complete native, browser and package execution can produce `full_native=true`. Schema-3 `native-short` receipts identify each target as `short_checks_passed`, each target’s `long_checks` as `not_run`, and `full_native=false`; browser and manifest checks are recorded as successful only after they pass. Other unexecuted domains remain `not_run`. Short coverage is never a reusable full baseline. The aggregate also requires the three omitted real-time steps to be explicitly skipped, not failed, missing or silently treated as passed.

Repository protection settings need no change when this workflow is adopted: keep requiring `ci-required`. Main runs have separate concurrency identities, so a later documentation push cannot cancel an earlier code-validation run. Updated commits to the same PR may still cancel superseded PR runs.

To request every check, use **Actions → Cross-platform CI → Run workflow** and leave **force_full** checked. There is no force-skip override. Release workflows do not consume the change-impact plan. A partial job rerun without complete current-attempt evidence cannot issue a full receipt.

## Exact-source full validation before release

After the reviewed commit is on main, run **Cross-platform CI → Run workflow → force_full=true** for that exact commit. The prerelease entry requires an explicit main `workflow_dispatch` full-validation run. Every manual CI dispatch is conservatively treated as full intent; a dispatch with `force_full=false` cannot qualify by omitting required checks. An ordinary main push, even if green, is not this explicit release proof.

The release gate audits the latest manual attempt by attempt-start time, not merely run ID or the latest successful result. A newer failed/cancelled full attempt blocks an older success; any pending manual validation must finish or be cancelled and superseded by a later successful full attempt. A newer ordinary canonical CI success does not erase unchanged full evidence. A newer ordinary failure, cancellation or pending run blocks publication until resolved or superseded by a later successful full validation. Unclear chronology and incomplete API inventories fail closed.

GitHub's effective current-attempt jobs can include carried-forward successful work when only failed jobs are rerun. Those current-attempt records are accepted, including original timestamps. The verifier never assembles successes across different runs or manually fills missing jobs from old attempts. Past failures remain in GitHub's history.

The proof binds exact commit/tree, workflow and policy source hashes, current effective attempt, all four native runner targets and every required named step, including real-time gates, browser, manifest, ci-required, Web7 and product2. Both the entry gate and publication-time check independently query read-only GitHub APIs. A saved JSON receipt cannot authorize a pass by itself. Publication can use a newer successful full attempt for the same source, but cannot ignore a newer failure. Proof artifacts are retained for 90 days; they are audit evidence, not additional signed release assets. Distribution signing and installed-package checks remain independent and unchanged.

Docs/frontend/scoped-Go/native-short successes all fail full-release eligibility until a complete explicit full run is established. Scheduled runs add coverage but do not replace this manual release proof.

## Daily full validation

The reviewed workflow schedules one full run per day at **18:00 UTC, or 03:00 JST (UTC+09:00) the following day**, using `0 18 * * *` in `.github/workflows/ci.yml`. It becomes active only after that workflow is merged into the default branch (`main`). GitHub uses the latest default-branch commit, including when no source changed. The classifier explicitly selects `full`; it cannot select `native-short` from an empty or documentation-only delta.

Each scheduled run requires all four native targets and all real-time gates, browser, package/manifest, seven-case Web acceptance and two-case product acceptance. `nightly-full-check` waits for `ci-required` and both acceptance jobs, and checks their actual required steps; skipped, missing, cancelled or failed gates cannot produce a successful receipt. Existing PR/manual acceptance entry conditions and ordinary `ci-required` dependencies remain intact; short PR feedback does not wait for this schedule-only aggregate. Separate run concurrency identities prevent a schedule from cancelling a push or explicit manual release audit. Existing cache trust rules and read-only repository permissions remain unchanged.

This consumes one additional complete CI run per day, including the longer native and acceptance jobs. Review observed stability, runner time and cost before reducing frequency; change the single cron entry through review, never automatically taper it. A scheduled success is **not** the manual release proof required above. A newer scheduled failure or pending run blocks release until resolved or superseded by a later successful manual full validation of the same source.

GitHub may delay or drop scheduled work under load, especially at the start of an hour; 03:00 JST is a requested start time, not an exact-minute guarantee. Public-repository schedules may be disabled after 60 days without repository activity. See [GitHub scheduled-workflow conditions](https://docs.github.com/en/actions/reference/workflows-and-actions/events-that-trigger-workflows#schedule). No external scheduler or new credential is used.

## Reading a documentation-only result

Open the latest **Cross-platform CI** run for the exact PR head, then read the **ci-required** job summary on the workflow run’s **Summary** page. The impact decision and `ci-coverage` receipt must identify scope `docs`, the required check must succeed, and the result must explicitly say that application tests, builds and packages were **NOT RUN**. A green skipped native job on its own is not the required result. The receipt records `full_native=false` and the application domains as `not_run`.

This path proves that the complete change is ordinary prose; it does not borrow an earlier application-test pass. The full validation of the workflow change is a separate rollout check. If a PR also contains source/configuration changes, use its complete diff and the scope table above rather than judging its last commit.

## Caches and measurements

Compilation caches accelerate builds; they do not turn a previous test result into a new pass. Native development caches, scoped-Go caches and trusted-main caches use separate namespaces. Only complete main native validation writes trusted-main cache entries. Release retains exact-source restoration and reruns its full gates. Scoped Go tests use `-count=1` to execute their tests.

Content-free timings record fixed suite/job names, elapsed time, result and return code. They do not contain command arguments, environment values, file contents or raw logs. A command failure remains a failure if timing recording fails. Compare cold and warm runs separately; parallel job durations must not be added to describe elapsed waiting time.

The earlier real-time-only optimization was measured before this scope policy: full canonical main took 29m11s; a documentation PR still running ordinary native/browser/package checks took 24m57s with four development-cache misses, then 12m57s with four fallback cache hits. Both selected runs omitted 12 real-time steps and explicitly recorded `full_native=false`. Those are historical observations of a different coverage policy, not timing promises for this workflow. [Full run](https://github.com/webkaz-labs/sobalink/actions/runs/37456053817) · [Cold selected run](https://github.com/webkaz-labs/sobalink/actions/runs/37460427507) · [Warm selected run](https://github.com/webkaz-labs/sobalink/actions/runs/37464624901)

The warm run's longest job was Windows at 11m46s: ordinary/native test stages took 7m31s, frontend checks 1m41s, and cache restoration/saving 1m09s. This is why prose changes now avoid those application jobs instead of relying on more caching.

## Native fixture and release coverage

Full native validation retains race detection, vet, Windows retirement barriers, repeated IPC cleanup, synthetic key checks, engine negative controls, native discovery, direct/relay recovery and the original natural lifecycle/lease waits. Production timers, payloads, assertions and test timeouts are not shortened by scope selection.

Fixtures requiring TCP and UDP on one endpoint reserve both protocols on the exact loopback address, with bounded real-bind attempts and cleanup. The helper is imported only by tests. Browser teardown drains pending intercepted requests before stopping the fixture and still propagates failures. A failed required target or step fails `ci-required`.

Automated CI evidence remains separate from physical-device enrollment, network, OS-login and suspend acceptance.

## Isolated real-time step concurrency

The three existing natural-lifecycle, guarded-lease and relay-only lease gates use
GitHub Actions `background` steps with a named, required `wait` immediately before
packaging. Their names, exact expected Go pass events, timeouts, race flags and
native-short skip rules are unchanged. The four native targets remain required.
The wait and each individual gate must succeed for full-CI or nightly evidence;
failed, cancelled, skipped or missing gates never receive full-coverage credit.

Only these wait-heavy groups overlap. They start after the serial native tests
have built the same tagged package variants; build caches may still need work
if entries are evicted. Package creation, uploads, cache saves and timing summaries
follow the wait. Only natural-lifecycle writes the existing timing JSONL in this
region, so there are no concurrent timing writers. Adding another requires
separate storage and a validated post-wait merge, rather than shared appends.

These are synthetic, separate-process fixtures: application ports belong to
separate userspace network stacks; host sockets request ephemeral ports; state is
in memory or test-local. The guarded engine retains its normal UDP socket behavior
and loopback-only destination policy. This does not establish real-device or
physical-network acceptance. Host resource contention and existing ephemeral-port
reservation/rebind races still require four-target CI verification.

No speedup is claimed from source review or offline tests. Compare successful
full runs on the same targets and cache conditions, using job wall time and the
individual named step times. Verify native-short skipped-step joins separately.
See [GitHub's background and wait syntax](https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-syntax#jobsjob_idstepsbackground).
