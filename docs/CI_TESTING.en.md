# CI regression frequency

[日本語](CI_TESTING.ja.md)

CI policy fixtures run once in a shared Linux job, in parallel with native
product checks. Native lanes keep the Python checks that exercise actual file,
process, permission and cleanup behavior. This does not reduce Go product race
tests, native discovery, browser acceptance or package validation within a selected scope.
The separate [native UI-only scope](CI_EFFICIENCY.en.md#native-ui-only-changes) keeps
the complete companion race suite on four targets and Windows/macOS product
packages, while omitting unrelated backend qualification and browser jobs.

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

For ordinary product source changes selecting `native-short` or `full`, the
required browser job runs the portable Vitest suite once. Native lanes omit
only that repeated `npm run test` call. Scoped-Go changes keep their narrower
Go-only checks.
Every native target still runs two clean, locked dependency installations and
frontend builds, compares their bytes with each other and the checked-in assets,
and checks that the lockfile did not change. Browser acceptance remains required.

CI/toolchain/dependency edits, nightly or forced full validation, and unknown or
unverifiable changes also run Vitest on every native target. Browser, release
and local calls without a native plan always run the full frontend suite. Both
the same-source browser job and its frontend step must pass for full proof; a
missing, skipped or failed job cannot be replaced by the native build results.

Residual risk: OS-specific behavior in a portable unit test may be found later
by the nightly matrix rather than an ordinary product PR. Cross-platform locked
builds and the independent native and release gates remain in place. No test
case, assertion or production timer is removed.
