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
| Transport, authentication, Core, configuration, timers, shared helpers, dependencies, lockfiles, build configuration, CI policy/workflows, or any unknown path | Full native Linux amd64/arm64, macOS arm64, Windows amd64, browser and package/manifest validation |
| Mixed frontend and Go changes, platform-specific Go, cgo, unreviewed imports, policy/provenance Markdown, unsafe modes/types or unverifiable input | Full |
| Manual full validation or any prerelease | Full |

The Go allowlist is deliberately small. It is not permission to run any Go change on one operating system. OS-specific files, build constraints and dependencies outside the reviewed package boundary require the full matrix. The scoped runner uses current Go import graphs, including integration-tag variants for reverse-dependency selection, but executes ordinary Linux tests rather than the long native integration suites. If graph discovery is uncertain, it runs all ordinary Go packages. Failed commands and missing actual test passes remain failures.

Markdown used as development policy or upstream source provenance is not ordinary prose. The required root documents must remain present. Guides are included in release archives, but that alone does not require rebuilding the application for every prose edit. Code and release validations continue checking the package contents. The separate resource and relay measurement workflows are triggered by their implementation inputs, not wording changes in their reports.

## Complete diffs and an honest required check

The workflow runs on every supported PR/main push. It does not use a whole-workflow `paths-ignore` filter, which could leave the required check pending.

For a PR, the classifier checks the complete effective change between the verified base parent and the tested merge commit. For a main push it checks event `before` through `after`, including all pushed commits. It never judges only the latest commit. A PR that contains a Core change therefore remains full even if its latest commit fixes a browser test. Previous successful jobs are not reused.

The classifier checks full Git tree inventories, both rename endpoints, file modes and repository/event identity. Missing history, incomplete or malformed diffs and unknown changes choose full validation. The aggregate repeats the scope proof; an artifact cannot grant itself a smaller scope. A classifier failure does not silently skip application validation.

`ci-required` remains the required status. It runs even when application jobs are skipped and verifies the actual required jobs and named steps for the current attempt. Documentation results say **“Documentation only; application tests, builds and packages NOT RUN”**. Frontend and Go results identify their limited coverage. Only complete native, browser and package execution can produce `full_native=true`; all other domains are recorded as `not_run`, never as successful tests or a reusable full baseline.

Repository protection settings need no change when this workflow is adopted: keep requiring `ci-required`. Main runs have separate concurrency identities, so a later documentation push cannot cancel an earlier code-validation run. Updated commits to the same PR may still cancel superseded PR runs.

To request every check, use **Actions → Cross-platform CI → Run workflow** and leave **force_full** checked. There is no force-skip override. Release workflows do not consume the change-impact plan. A partial job rerun without complete current-attempt evidence cannot issue a full receipt.

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
