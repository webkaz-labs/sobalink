# Distribution and verification

## Current release: 0.2.0-alpha.1 testing prerelease

`0.2.0-alpha.1` adds the [named connection and time-limited sharing workflow](GENERIC.en.md), with automatic Japanese/English locale selection. It is an **experimental acceptance-testing prerelease**, not a supported stable product.

- Exact source: [`236bd8e217f213a93b667f3d8d0509811d4f5464`](https://github.com/webkaz-labs/tsnet-bridge/commit/236bd8e217f213a93b667f3d8d0509811d4f5464)
- [Ordinary CI](https://github.com/webkaz-labs/tsnet-bridge/actions/runs/36910805666): all five jobs passed, including four native race/real-IPC/package jobs and the Packslip fixture
- [Release workflow](https://github.com/webkaz-labs/tsnet-bridge/actions/runs/36911703369): all 15 jobs passed for that source, including its source gate, four native package/reproducibility jobs, provenance, signing, publication, public verification and four actual mise-install jobs. Each native job ran 25 repeated real IPC regressions, two identical package builds, archive execution and Japanese/English/actual OS-locale/exact JSON smoke checks. All four restored the exact-source trusted-main Go cache
- Publication: [v0.2.0-alpha.1](https://github.com/webkaz-labs/tsnet-bridge/releases/tag/v0.2.0-alpha.1) was published on 2026-10-01 at 19:11:08 UTC as a prerelease with 19 assets. The tag resolves to the exact source above; Packslip signing, artifact and bundle provenance, staging and publication jobs passed
- Independent unauthenticated public download, signature and provenance verification: [Passed](https://github.com/webkaz-labs/tsnet-bridge/actions/runs/36911703369): all 19 public assets downloaded without authentication, with Packslip signature, provenance and content verification
- Actual public-release mise installation on all four targets: [Passed on all four targets](https://github.com/webkaz-labs/tsnet-bridge/actions/runs/36911703369): actual exact-pin installation with mise 2026.9.18, version/source metadata, SBOM/notices, Japanese/English output, actual OS-locale fallback and exact JSON equality; no signature bypass or release-age override

Native mise checks completed between 19:12:34 and 19:12:48 UTC on 2026-10-01, about 1½ minutes after publication; the full workflow completed at 19:12:54 UTC. No signature bypass or release-age override was used.

An additional local check installed the exact public Packslip pin as a non-root Linux user in fresh isolated HOME/XDG/mise directories. Installation took 50.4 seconds; activation, version/source metadata, native-binary digest, SBOM/notices, Japanese/English/native-locale/exact JSON and v2 offline checks all passed. No real node was started. This is additional local evidence, separate from the native release jobs.

Publication, public-asset verification and actual native mise installation are complete for this version. These results establish distribution and offline executable behavior, not real enrollment or application compatibility.

The pipeline is defined in the [exact-source prerelease workflow](https://github.com/webkaz-labs/tsnet-bridge/blob/236bd8e217f213a93b667f3d8d0509811d4f5464/.github/workflows/prerelease.yml). Ordinary CI is credential-free and never publishes. The separate prerelease workflow builds all four targets, creates GitHub provenance, signs a Packslip bundle using short-lived GitHub OIDC, verifies public bytes, then tests actual mise installation on native runners. Its offline smoke checks do not enroll a Tailscale node or start the embedded networking service.

Real enrollment, phone QR authentication, actual tailnet ACL/application behavior, Windows standard-user enrollment, OS login/sleep and RustDesk bidirectional screen/input remain **unverified**. They are separate acceptance gates before claiming a supported end-to-end product. Installation checks do not satisfy them. [Remaining acceptance](VERIFICATION.en.md)

The [official runner reference](https://docs.github.com/en/actions/reference/runners/github-hosted-runners) explains why Windows hosted-runner success does not prove standard-user operation.

## Historical release: 0.1.0-alpha.2

[v0.1.0-alpha.2](https://github.com/webkaz-labs/tsnet-bridge/releases/tag/v0.1.0-alpha.2) is the published experimental RustDesk fixed-forwarding/SOCKS release; it does not include named connections. Its [complete release workflow](https://github.com/webkaz-labs/tsnet-bridge/actions/runs/36888899407) succeeded from source `0069e38732227c8913ee6ceb0ec21784ae03d862`. All 19 public assets were downloaded without authentication and verified, followed by actual mise installation on all four native targets.

Publication was at 16:07:59 UTC on 2026-10-01; installation checks completed between 16:09:42 and 16:10:12 UTC, about two minutes later, with no age override or disabled signature verification. These timestamps and results belong only to `0.1.0-alpha.2`, not the new release. The [alpha.2-pinned RustDesk acceptance procedure](VERIFICATION.en.md#legacy-rustdesk-acceptance-procedure) remains experimental.

## Targets

| GOOS | GOARCH | Native runner | Archive |
| --- | --- | --- | --- |
| linux | amd64 | ubuntu-24.04 | tar.gz |
| linux | arm64 | ubuntu-24.04-arm | tar.gz |
| darwin | arm64 | macos-26 | tar.gz |
| windows | amd64 | windows-2025 | zip |

Intel macOS and Windows ARM64 are not current distribution targets. Testing on these images does not
establish a minimum supported OS version. Binaries use `CGO_ENABLED=0`, with
baseline `GOAMD64=v1` / `GOARM64=v8.0`; Linux archives are not bound to a host libc.
No installer, kernel driver, service installation, or elevated startup is added
by packaging.

## Build with mise now

Install [mise](https://mise.jdx.dev/getting-started.html), review the checkout and
trust its configuration if prompted, then run:

```sh
mise install
mise exec -- go test -race ./...
mise exec -- go vet ./...
mise run build
```

This uses the exact Go version in `mise.toml`. A local source build is not a signed
release. The command-line executable is `bin/tsnet-bridge` on Unix-like systems;
for a Windows source build use:

```powershell
mise exec -- go build -trimpath -o bin/tsnet-bridge.exe ./cmd/tsnet-bridge
```

## Install the testing prerelease through mise

With mise **2026.9.18**, use these commands in PowerShell and Unix shells only after `v0.2.0-alpha.1` is published with the signed bundle and target archive. Stop if publication or required assets cannot be confirmed:

```sh
mise use -g "packslip:github.com/webkaz-labs/tsnet-bridge[prerelease=true]@0.2.0-alpha.1"
mise exec -- tsnet-bridge version
```

Expected application version: `tsnet-bridge 0.2.0-alpha.1`. The explicit
`prerelease=true` option opts into prerelease selection; the complete version pins
the tested release. Do not use `latest`. Packslip selects the native archive and
executable, including `.exe` on Windows. Go is not required for end users. After confirming the version, follow [init → login → connect](GENERIC.en.md#first-use), with automatic Japanese/English selection. Existing profiles require deliberate migration or a separate state directory; installation alone does not change the profile.

mise's default `minimum_release_age` is 24 hours for discovery/fuzzy selection.
**Exact version pins and lockfile selections are exempt in mise 2026.9.18**, so
this exact prerelease can be installed immediately after publication. Signature,
identity, digest, size, and platform checks remain active. No age override or
signature bypass is used by the release installation workflow. The completed native installation jobs used that configured policy. See the
[pinned setting semantics](https://github.com/jdx/mise/blob/v2026.9.18/settings.toml#L1855-L1908)
and [exact-pin implementation](https://github.com/jdx/mise/blob/v2026.9.18/src/backend/packslip.rs#L726-L735).

For a project-scoped install, omit `-g` in a dedicated project directory. Review
and keep the resulting `mise.toml` and `mise.lock`; use `mise install --locked`
when the lockfile exists. Keep the same pinned version during acceptance. A
future upgrade or rollback changes the pinned version explicitly after stopping
the helper; binary installation does not migrate or delete the separate identity
state. Upgrade/rollback compatibility is not established by first-install verification.

The workflow sets GitHub's prerelease flag, but Packslip also recognizes the semantic
prerelease suffix. A published release must contain `packslip.sigstore.json`;
a tag or archive alone is insufficient. [mise Packslip documentation](https://mise.jdx.dev/dev-tools/backends/packslip.html)

## Local package commands

Run from the repository root after installing the pinned toolchain. Example for a
local Linux AMD64 build in a Git checkout:

```sh
export SOURCE_DATE_EPOCH="$(git log -1 --format=%ct)"
mise exec -- go run ./cmd/package-tool build 0.0.0-dev.1 linux amd64 "$(git rev-parse HEAD)"
mise exec -- go run ./cmd/package-tool checksums
```

Repeat `build` with each supported target to prepare the complete matrix, then:

```sh
mise exec -- go run ./cmd/package-tool manifest 0.0.0-dev.1
mise exec -- go run ./cmd/package-tool checksums
```

Cross-compilation prepares bytes; it is not a substitute for native execution.
The commit argument is an explicit source label, not an attestation. CI derives it
from its checked-out Git commit. Local callers must use an unmodified checkout of
that commit if they intend the label to identify the complete source exactly.

Each package contains:

```text
bin/tsnet-bridge[.exe]
share/tsnet-bridge/LICENSE
share/tsnet-bridge/README.md
share/tsnet-bridge/README.en.md
share/tsnet-bridge/SECURITY.md
share/tsnet-bridge/docs/*.md
share/tsnet-bridge/go.mod
share/tsnet-bridge/go.sum
share/tsnet-bridge/build.json
share/tsnet-bridge/bom.cdx.json
share/tsnet-bridge/third-party-notices.json
share/tsnet-bridge/licenses/...
```

The SBOM, build metadata and notices index are also emitted as separate assets.
`package-tool checksums` covers the regular files present in `dist` when it runs,
apart from `SHA256SUMS` itself. The release inventory is generated before signing:
it lists 17 files and does not include the later `packslip.sigstore.json`. The
bundle is independently signature-verified and has its own GitHub provenance.
From the download directory use `sha256sum --check SHA256SUMS` for the listed
files. Checksums alone provide integrity, not publisher authentication.

### Reproducibility boundaries

The packager pins Go 1.27.1, disables cgo, workspace overrides, saved Go settings,
experimental compiler features and alternate FIPS modules, uses readonly
module resolution, trims source paths, disables VCS auto-embedding, clears the
linker build ID, and sets an explicit program version. Archives have sorted
entries, normalized modes/owners, and timestamps from `SOURCE_DATE_EPOCH` (zero
when omitted; ZIP timestamps before 1980 are normalized to 1980). Symlinks and
special archive entries are rejected. No build-host path is included in metadata.

The prerelease workflow rebuilds each target twice and compares all output
digests in the same runner. Ordinary CI builds each target once while retaining
all tests, archive/SBOM/notices checks, and the Packslip fixture. The two-build
release check establishes repeatability for identical inputs; it does not yet establish
independent-builder reproducibility, code signing/notarization, or OS-wide runtime
compatibility. Final platform signing, if introduced, must precede hashing and
Packslip signing because it changes artifact bytes.

### CI and prerelease caches

Go module downloads and compiled objects are cached in a dedicated trusted-main
namespace. Only successful native CI jobs on the canonical repository's main
branch save that namespace. The key binds the OS, CPU architecture, runner label,
Go version, dependency manifests (`go.mod` and `go.sum`), and producing commit. Ordinary CI may
reuse an earlier main cache only within the same exact dependency/toolchain
prefix; pull requests never write this trusted namespace.

The prerelease restores only the full **tested-commit key**, with no fallback and
no cache save. Its successful-CI gate ensures that the source commit completed
the normal workflow before publication. Hit/miss and matched keys are printed in
the job logs. A missing cache triggers an ordinary build, not a skipped check.

Caching does not reuse test results: `go test -race -count=1` executes tests every
time. Both package builds and their digest comparison remain mandatory. This is
repeatability with cached compilation, not a claim of independent-builder
reproducibility. The first run in a new namespace populates the cache and can
still take as long as a cold build.

### Dependency and licensing inventory

The SBOM is CycloneDX 1.5 JSON. `go list -deps -json` under the same target and cgo
settings provides package dependencies, aggregated into module-level edges. Test
and build-tool dependencies are outside that runtime inventory. The Go standard
library is represented separately; system libraries are not vendored.

Each selected external module records its version, Go module sum and copied
notice-file SHA-256 values. Notice files are collected recursively while excluding
`.git` and `testdata`; the exact toolchain's notices and selected embedded
standard-library notices are included too. Module replacements, absent source
provenance or missing notices cause packaging to fail. The index records original
texts without guessing SPDX identifiers. Module notice directories use hashes for
portable names; the index maps them to human-readable module identities.

This is an auditable notice inventory, not an automatic legal determination.
Source-only terms outside conventional notice files may need additional inclusion;
review those before authorizing distribution.

## Packslip signing and publication flow

`package-tool manifest VERSION` checks that all four target packages, SBOMs,
metadata and notices indexes exist and refer to one source commit. It generates
`dist/packslip.toml`, which is **signing input, not a signature**. It declares
explicit platforms, archive formats, executable paths, and per-artifact SBOM
resources.

Ordinary CI signs a disposable example-identity fixture offline. Its `--no-log`
and `--allow-unlogged` options are confined to that fixture and are **not used for
public prereleases**. No fixture key or bundle is published.

The manual prerelease workflow separates permissions and responsibilities:

1. Require a strict prerelease version, the exact current main SHA, and a
   successful Cross-platform CI run for that SHA
2. Test and build all four targets natively, build each package twice, compare
   digests, and execute the packaged help/version and offline named-rule workflows in both languages, including actual OS-locale fallback and exact JSON comparison
3. Assemble the complete manifest/checksums and attest the final distribution
   bytes using GitHub build provenance
4. In a separate job with `contents: read`, `id-token: write` and no checkout,
   run SHA-pinned Packslip 1.4.0 with `attest: link` only after genuine attestations
   have been independently verified; verify the bundle, exact workflow identity,
   issuer, and all archive/SBOM digests without transparency-log exceptions. A
   separate job also attests the finished bundle
5. Recheck the source and CI, create the exact tag and an unpublished prerelease,
   and upload final assets. Refuse to replace an already-published version or
   change an existing tag
6. Use a separate minimal write-enabled job to upload the verified bundle,
   compare every staged asset digest, and publish as a prerelease, never as the
   stable latest release
7. Download the public files without download authentication and separately
   verify all signatures and provenance. Then install the complete version on
   all four native targets through mise with its default signature and age
   settings. Execute offline help/version and named-rule workflows in Japanese/English, check actual OS-locale fallback and exact JSON equality, inspect the installed metadata/notices, and verify the expected source commit

The dispatch workflow identity is
`https://github.com/webkaz-labs/tsnet-bridge/.github/workflows/prerelease.yml@refs/heads/main`.
The release tag and source commit are passed explicitly; they do not turn that
identity into a tag-triggered workflow identity. The expected OIDC issuer is
`https://token.actions.githubusercontent.com`.

**Packslip and mise do not themselves fetch and verify linked build provenance.**
The release workflow performs that additional verification with `gh attestation
verify`, binding it to this repository, workflow, source SHA, main ref and
GitHub-hosted runners. Merely seeing an attestation link is not that verification.

No long-lived signing key is created. OS code signing/notarization is a separate
property and is not promised by the Packslip signature. No real tailnet secrets,
interactive enrollment, or remote-control sessions are part of release CI.

References: [Packslip publishing](https://packslip.dev/docs/publishing/),
[artifact configuration](https://packslip.dev/docs/describing-releases/),
[GitHub attestation verification](https://cli.github.com/manual/gh_attestation_verify).
