# CI regression frequency

[日本語](CI_TESTING.ja.md)

The shared Linux job discovers every Python regression, in parallel with native
product checks. Native lanes retain actual file, process, permission and cleanup
checks. Go race tests, native discovery, browser acceptance and package validation
remain unchanged.
The separate [native UI-only scope](CI_EFFICIENCY.en.md#native-ui-only-changes) keeps
the complete companion race suite on four targets and Windows/macOS product
packages, while omitting unrelated backend qualification and browser jobs.

- Ordinary product edits select 53 native cases on Windows, 57 on Linux and 69 on
  macOS, before nested fixture checks. These include the three real Nearby child
  process tests for bounded output, timeout cleanup and stdin pipes.
- CI/toolchain/dependency changes, nightly and forced full runs, unknown paths,
  and invalid or incomplete change evidence retain broad native discovery.
  Only the 64 individually marked `FullProofTests` source/receipt-policy methods
  are excluded from that broad native path in the hosted Cross-platform CI job.
  They still run in the required common job. Outside that workflow, broad
  discovery remains unfiltered.
- Common Ubuntu x64 provides that target's full discovery; its native lane keeps
  the OS subset. Documentation, frontend-only and scoped-Go changes retain their
  existing narrower checks without an additional Python job.

Both consumers independently reproduce the current event's complete Git delta.
The same-run aggregate and exact-source release proof require the common job and
step to succeed, even if every native lane succeeds. No cache or earlier run can
replace this evidence. Native jobs do not wait for common; the aggregate joins them.

The method-local `common_only` marker in `test_ci_full_validation.py` does not
exempt a whole class or module. New unmarked methods/classes remain included in
broad discovery, including inherited methods and unmarked overrides. This is a
bounded intermediate reduction, not a complete classification: other broad
matrix duplication and the ordinary selector remain. A new OS-dependent test
still needs review for inclusion in the ordinary native subset.

The OS selectors are in `.github/scripts/ci-regressions.py`. macOS retains real
POSIX child ownership and terminal cleanup checks; Linux cannot substitute for
that evidence. Hosted-runner changes can affect unmarked helper fixtures, which
remain in the nightly matrix. The marked FullProof cases use source and synthetic
receipt fixtures, not native process behavior. No hosted timing improvement is
claimed; packaging, signing and source-bound release validation remain separate.

## Frontend test repetition

The required browser job runs the portable Vitest suite once for every scope
that includes frontend checks. This includes CI/toolchain/dependency edits,
nightly and forced full validation, and unknown or unverifiable changes.
Documentation-only and scoped-Go changes keep their narrower checks.

Native CI integration lanes and prerelease use
`python .github/scripts/check-frontend.py --build-only`. All four native targets
still run two clean, locked dependency installations and frontend builds,
compare their bytes with each other and the checked-in assets, and check that
the lockfile did not change. CI unit lanes skip only this duplicate build step;
the aggregate requires its success in every integration lane from the same
source, run and effective attempt. Pinned Node setup remains in unit lanes for
the real Node helper used by broad Python regressions. Prerelease is unchanged.
The helper's default browser/local
invocation still runs all frontend tests. It does not consult the Python
regression selector or an impact plan to decide whether to run Vitest.

The current Vitest cases exercise jsdom, pure logic and mocked boundaries.
Their Node file reads inspect static source/CSS/WASM or fixture bytes, not host
process, permission, socket or filesystem behavior that warrants OS repetition.
There is no extra periodic cross-OS Vitest job. If a new test exercises a real OS
contract, review that contract and its required targets rather than repeating
all portable tests.

Both the same-source browser job and its frontend step must pass for aggregate
coverage and full proof. Prerelease requires successful manual forced full CI
for its exact `tested_commit`, including that run's browser tests. A missing,
skipped or failed browser job/step cannot be replaced by native builds or a
cached result from a different commit. Browser acceptance and the independent
native and release gates remain required. No test case, assertion or production
timer is removed.

## Ordinary Go coverage and activation

CI and prerelease run each ordinary Go test once through the complete Linux
race suite or the disjoint whole-Core/remaining-package split on Windows/macOS.
Separate count-one reruns of `backendworker`, `connectionroute` and the Core
`TestMixed` subset are removed because those exact cases are already covered.
Tagged integration checks, IPC/DirectLAN repetition, vet and packaging remain.

The two-case product and seven-case Web activation jobs skip proven docs-only
same-repository PRs. Failed or unknown impact selection still runs them. Scoped
Go changes retain both jobs: service presets feed Core snapshots, and bounded
logging is used by CLI lifecycle code. Forced full and scheduled validation
require both jobs independently of the PR optimization.

## Browser preparation overlap

The Go engine preparer runs in the background while Node/frontend validation
proceeds. Its dependency closure contains only standard-library and adaptation
helpers; it does not compile embedded Web assets. An explicit wait joins it
after frontend reproduction. Then Chromium/font installation overlaps the Go
fixture diagnostic cases, with another wait before actual browser acceptance.
Both background steps and waits are required successful evidence.

Do not move the Go diagnostics ahead of frontend completion: `soba-e2e` embeds
`web/dist`, which Vite deletes and rebuilds. The diagnostic cases also retain
their original serial order and private listeners. No additional browser or
runner runs concurrently, and no test case or timeout is reduced. Go engine
preparation and npm work may still compete for CPU or downloads; hosted timings
are needed to measure the benefit. Background environment/outputs are consumed
only after the corresponding wait.
