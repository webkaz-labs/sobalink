# Change-aware native CI

[日本語](CI_EFFICIENCY.ja.md) · [Verification](VERIFICATION.en.md) · [Development principles](DEVELOPMENT_PRINCIPLES.en.md)

Every change keeps the fast safety/logic checks, all four native targets, browser acceptance, reproducible frontend checks and package/manifest smoke checks. Only the real-time natural-key, relay-lease and relay-presence idle waits can be omitted, after a conservative change-impact decision. Prerelease always runs the complete real-time coverage and existing distribution/signature/install gates.

## What always runs

- Native race tests, vet, formatting, Windows retirement barriers and repeated IPC cleanup
- Synthetic key expiry, flush and explicit rekey checks, plus direct-LAN/Core integration
- Guarded and relay-only functional pairing, identity, TLS/pin, TCP/UDP, revocation and teardown checks
- Engine admission/negative controls, native STUN and existing route recovery tests
- Frontend tests and two matching exact-lock builds, real browser acceptance, archive and manifest verification

The new functional relay tests use the same fixture and security assertions as their long counterparts. They do not claim that a natural lease boundary was crossed. The long counterparts still retain the original 130-second continuity check, the same TCP connections, the final post-boundary frame and re-admission assertions. The existing multi-relay presence test retains its 80-second stale-idle boundary; a separate fast counterpart checks the same functional recovery without claiming that boundary. Natural direct-LAN rekey and expiry retain the upstream timers; production timers are not shortened.

## When real-time checks run

The initial policy permits only a narrow positive allowlist:

| Complete cumulative change | Real-time checks |
| --- | --- |
| Root README/SECURITY prose or ordinary `docs/**/*.md` only | May be omitted with a verified full baseline |
| `web/src/**/*.css`, optionally with generated `web/dist` changes | May be omitted with a verified full baseline; frontend regeneration must still match |
| Identical tree to a verified full baseline | May be omitted |
| Go, `internal/**`, `cmd/**`, Core/config/auth/policy/timers, helpers, fixtures, workflows, toolchains, dependencies, lock files or unknown paths | Full |
| UI TS/TSX, API clients, event handlers, route/setup/login controls | Full; these are not classified as presentation-only |
| Generated assets without an accompanying safe CSS change | Full |
| Development/agent policy documents, file-mode/type changes or unsafe rename endpoints | Full |
| Missing, malformed, incomplete, dirty, shallow or unverifiable inputs | Full |
| Manual `force_full=true` or any prerelease | Full |

The classification includes both rename endpoints and compares the parsed change list against complete Git tree inventories. It does not rely on GitHub workflow-level path filters, the last pushed commit alone, a cache hit, or an AI interpretation of the diff. Additions to the safe allowlist require a reviewed policy change, which itself forces full coverage.

## Baseline and required result

The baseline must be an ancestor with a successful canonical `main` Cross-platform CI run, matching source/tree/policy fingerprints and the exact successful run attempt. Its `ci-coverage` receipt must prove all four targets, browser, manifest and full real-time coverage. PR receipts and selected-only runs cannot advance this baseline. Missing or expired evidence causes full execution; an initial rollout therefore runs full before any optimization is available.

The diff is from this full baseline to the tested checkout, including the PR merge tree where applicable. A failed transport change followed by a CSS change cannot hide the transport change. Baseline lookup reads a bounded recent history; inability to find usable evidence is an optimization miss, not permission to skip.

`ci-required` always evaluates the actual jobs and named steps for the current attempt. It rejects failed, canceled, missing or unexpectedly skipped required coverage. A valid selected run is labeled **“real-time tests NOT RUN for this change scope”**, with its full-baseline link; it is not labeled full validation. Configure branch protection/rulesets to require **`ci-required`** before relying on conditional CI for merge eligibility. This workflow does not change repository protection settings.

To request all checks, use **Actions → Cross-platform CI → Run workflow**, select the intended ref and leave **force_full** checked (the default). There is no force-skip override. Release workflows do not consume the change-impact plan. If an attempt only reruns failed jobs and therefore lacks a complete current-attempt inventory, rerun all jobs; incomplete attempt evidence cannot issue a full receipt.

## Caches and measurement

Development and trusted-main caches remain separate. Only a main native job that actually selects and completes all real-time gates can write the trusted-main namespace. Release retains its exact-source cache restoration and re-executes its native gates, including the extracted synthetic-key test.

The aggregate also records fixed-name job and cache-step durations. Each native job publishes content-free suite timings: fixed suite identifier, elapsed seconds, result and return code. Arguments, environment, file contents and raw logs are not placed in timing records. A command failure remains a failure even if recording fails. Compare cold and warm runs separately and keep total runner time distinct from elapsed critical-path time.

The earlier full CI at source `308d252` took 27m10s, with a 26m26s Windows job on a cache miss. Linux amd64/arm64 and macOS had cache hits and took 14m08s/13m31s/16m16s. These are observed baselines, not a promised duration for a changed test set. A reduction from omitted real-time waits must be reported as selected coverage, not as identical full-coverage performance. [Baseline run](https://github.com/webkaz-labs/sobalink/actions/runs/37412218933)

Before acceptance, verify both a full run and a presentation-only selected run, manual force-full, unknown/helper/dependency/rename inputs, and injected missing/failed/skipped target evidence. Physical-device network acceptance remains separate.

## Native port fixture portability

Fixtures that need TCP and UDP on one endpoint reserve both protocols on the exact IPv4/IPv6 loopback address. The shared test helper checks at most 100 spread candidates with real binds, retains both sockets until immediately before the real start, preserves explicit application-reserved ports and reports exhaustion or cleanup errors. Deterministic tests cover different TCP/UDP excluded ranges and bounded cleanup. The raw WireGuard fixture holds its two UDP reservations concurrently to keep the peer endpoints distinct.

These helpers are imported only by test files. Production listener policy, selected endpoints, timing limits and assertion failures remain unchanged. A native target failure still makes `ci-required` fail and prevents issuing a full-coverage receipt.
