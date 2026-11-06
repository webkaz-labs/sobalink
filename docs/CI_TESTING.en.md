# CI regression frequency

[日本語](CI_TESTING.ja.md)

CI policy fixtures run once in a shared Linux job, in parallel with native
product checks. Native lanes keep the Python checks that exercise actual file,
process, permission and cleanup behavior. This does not reduce Go product race
tests, native discovery, browser acceptance or package validation.

- For ordinary product source edits, the shared job discovers the complete
  Python suite. Native lanes select the reviewed OS contracts: 49 on Windows,
  53 on Linux and 65 on macOS, before any additional nested fixture checks.
- Changes to CI scripts/workflows, dependency manifests, engine adaptation or
  engine preparation require the complete Python suite on every target.
- The existing nightly run, forced full runs, unknown paths and invalid or
  incomplete change evidence also require the complete suite on every target.
- The shared Ubuntu x64 job provides that target's full suite. Its native lane
  keeps the OS subset, avoiding a fifth full run during periodic validation.
- Documentation, frontend-only and scoped-Go changes do not gain an additional
  Python job. Their existing scope-specific checks remain in place.

Both consumers independently reproduce the current event's complete Git delta.
No prior test result authorizes a skip. The aggregate and release proof require
the shared job to succeed, even if every native lane succeeds. A successful
cache restore is never evidence that a test passed.

The selected OS contracts are listed in `.github/scripts/ci-regressions.py`.
macOS retains actual POSIX child ownership and terminal cleanup tests; Linux
results do not substitute for that macOS behavior. Portable fixture checks for
Windows or Darwin metadata do not by themselves require those operating systems.

Residual risk: a hosted-runner update can affect a portable Python fixture on
an OS where it is not repeated for an ordinary product edit. The nightly full
matrix detects this; changes to the checked-in toolchain or helper policy
restore full coverage immediately. Packaging, signing and source-bound release
validation retain their independent checks.

Further reductions should distinguish source-contract checks from helper-only
fixtures and retain a full fallback for newly added or unclassified tests.
Changing the test implementation language alone does not remove subprocess,
Git history, repeated build or intentional real-time protocol work.

## Frontend test repetition

The required browser job runs the portable Vitest suite once for every scope
that includes frontend checks. This includes CI/toolchain/dependency edits,
nightly and forced full validation, and unknown or unverifiable changes.
Documentation-only and scoped-Go changes keep their narrower checks.

Native CI and prerelease use `python .github/scripts/check-frontend.py --build-only`.
Every native target still runs two clean, locked dependency installations and
frontend builds, compares their bytes with each other and the checked-in assets,
and checks that the lockfile did not change. The helper's default browser/local
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
