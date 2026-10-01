# Distribution and verification

## Current status: testing prerelease

The requested `v0.1.0-alpha.2` is an **experimental acceptance-testing prerelease**,
not a supported stable release. Publication and installation are verified by the
manually dispatched [prerelease workflow](https://github.com/webkaz-labs/tsnet-bridge/blob/main/.github/workflows/prerelease.yml).
Check its completed run and the [release page](https://github.com/webkaz-labs/tsnet-bridge/releases/tag/v0.1.0-alpha.2)
before treating the install path as available. A missing release page means
publication has not finished.

Ordinary CI remains credential-free and does not publish releases. The separate
prerelease workflow builds all four targets, creates genuine GitHub provenance,
signs a Packslip bundle using short-lived GitHub OIDC, verifies the published
bytes, and tests real mise installation on native runners. It does not enroll a
Tailscale node or start the embedded networking service.

Real tailnet login, standard-user Windows enrollment, RustDesk registration and
bidirectional screen/input acceptance remain **unverified**. These are required
before claiming a supported end-to-end product, not prerequisites for this
explicitly experimental testing distribution. Installation checks are not those
application tests. [Remaining acceptance](VERIFICATION.en.md)

The [official runner reference](https://docs.github.com/en/actions/reference/runners/github-hosted-runners)
explains why Windows hosted-runner success does not prove standard-user operation.

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

With mise **2026.9.18**, these commands work in PowerShell and Unix shells after
the release is published:

```sh
mise use -g "packslip:github.com/webkaz-labs/tsnet-bridge[prerelease=true]@0.1.0-alpha.2"
mise exec -- tsnet-bridge version
```

Expected application version: `tsnet-bridge 0.1.0-alpha.2`. The explicit
`prerelease=true` option opts into prerelease selection; the complete version pins
the tested release. Do not use `latest`. Packslip selects the native archive and
executable, including `.exe` on Windows. Go is not required for end users.

mise's default `minimum_release_age` is 24 hours for discovery/fuzzy selection.
**Exact version pins and lockfile selections are exempt in mise 2026.9.18**, so
this exact prerelease can be installed immediately after publication. Signature,
identity, digest, size, and platform checks remain active. No age override or
signature bypass is used by the release installation tests. See the
[pinned setting semantics](https://github.com/jdx/mise/blob/v2026.9.18/settings.toml#L1855-L1908)
and [exact-pin implementation](https://github.com/jdx/mise/blob/v2026.9.18/src/backend/packslip.rs#L726-L735).

For a project-scoped install, omit `-g` in a dedicated project directory. Review
and keep the resulting `mise.toml` and `mise.lock`; use `mise install --locked`
when the lockfile exists. Keep the same pinned version during acceptance. A
future upgrade or rollback changes the pinned version explicitly after stopping
the helper; binary installation does not migrate or delete the separate identity
state. Upgrade/rollback compatibility has not been established by a first release.

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
   digests, and execute the packaged help/version commands
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
   settings. Execute only offline help/version checks, inspect the installed
   metadata/notices, and verify the expected source commit

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
