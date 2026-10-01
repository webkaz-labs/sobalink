# Distribution and verification

## Current status

This repository is a development preview. CI prepares artifacts, checks repeatable
packaging, and exercises Packslip signing with a disposable, offline test key. It
**does not create a tag, publish a release, sign a production manifest, or connect
to a real tailnet**. A successful CI run is not evidence that a real RustDesk
session or a standard-user Windows login works.

Required before the first public binary release:

- All five native CI targets pass at the exact release commit
- Real tailnet login, restart, logout, node expiry/revocation, ACL denial and
  reconnect are exercised with an explicitly authorized test account
- A real RustDesk connection works through the intended TCP/UDP routes, including
  interruption, reconnect and relay fallback; configuration examples alone do not
  establish compatibility
- Windows standard-user operation is tested separately. GitHub's Windows runners
  run as administrators with UAC disabled, so their passing tests cannot prove it
- Review target-specific dependency notices and source-embedded license terms,
  then resolve any remaining redistribution obligations
- Explicitly approve release/tag creation, production signing identity and final
  publication, then verify installation from the published assets through mise

The [official runner reference](https://docs.github.com/en/actions/reference/runners/github-hosted-runners)
confirms the workflow labels and the Windows runner privilege limitation.

## Targets

| GOOS | GOARCH | Native runner | Archive |
| --- | --- | --- | --- |
| linux | amd64 | ubuntu-24.04 | tar.gz |
| linux | arm64 | ubuntu-24.04-arm | tar.gz |
| darwin | arm64 | macos-26 | tar.gz |
| darwin | amd64 | macos-15-intel | tar.gz |
| windows | amd64 | windows-2025 | zip |

Windows ARM64 is not a distribution target yet. Testing on these images does not
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

## Install a future signed release through mise

**These release installation commands are intentionally not available until the
release blockers above are resolved and a signed release actually exists.**
Replace `X.Y.Z` with a published stable version:

```sh
mise use -g packslip:github.com/webkaz-labs/tsnet-bridge@X.Y.Z
mise exec packslip:github.com/webkaz-labs/tsnet-bridge@X.Y.Z -- tsnet-bridge --version
```

The same tool identifier works on Linux, macOS and Windows. Packslip selects the
native archive and declared executable, including `.exe` on Windows. A recent
mise version with the Packslip backend is required; CI uses mise 2026.9.18.

Keep mise's signature, identity, digest and release-age checks enabled. Its default
minimum release age is 24 hours; a new release may take time to become eligible.
For a project-scoped install, omit `-g`, install once, and commit the resulting
`mise.toml` and `mise.lock`; subsequent installs can use `mise install --locked`.
See [mise's Packslip documentation](https://mise.jdx.dev/dev-tools/backends/packslip.html).

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
`SHA256SUMS` covers every regular file currently in `dist`, apart from itself;
verify from that directory with `sha256sum --check SHA256SUMS`. Checksums alone
provide integrity, not publisher authentication.

### Reproducibility boundaries

The packager pins Go 1.27.1, disables cgo, workspace overrides, saved Go settings,
experimental compiler features and alternate FIPS modules, uses readonly
module resolution, trims source paths, disables VCS auto-embedding, clears the
linker build ID, and sets an explicit program version. Archives have sorted
entries, normalized modes/owners, and timestamps from `SOURCE_DATE_EPOCH` (zero
when omitted; ZIP timestamps before 1980 are normalized to 1980). Symlinks and
special archive entries are rejected. No build-host path is included in metadata.

CI rebuilds each target twice and compares all output digests in the same runner.
This checks repeatability for identical inputs; it does not yet establish
independent-builder reproducibility, code signing/notarization, or OS-wide runtime
compatibility. Final platform signing, if introduced, must precede hashing and
Packslip signing because it changes artifact bytes.

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

## Packslip signing flow

`package-tool manifest VERSION` checks that all five target packages, SBOMs,
metadata and notices indexes exist and refer to one source commit. It generates
`dist/packslip.toml`, which is **TOML input, not a signature**. It declares explicit
platforms, archive formats, executable paths, and per-artifact SBOM resources.

CI installs Packslip 1.4.0 through mise and performs a local signing/verification
round-trip with the reserved project `tsnet-bridge-ci.example.com`. The disposable
key is deleted on exit; neither key nor test bundle is uploaded. `--no-log` and
`--allow-unlogged` are used only for this isolated fixture. No OIDC permissions or
production signing credentials are requested.

After the release gates and publication are explicitly approved, a production
workflow should follow this sequence:

1. Build and verify final artifacts at the reviewed commit, including any desired
   platform signing. Create an unpublished release and upload final assets
2. Attest final bytes in a dedicated build-provenance job
3. In a separate signing job with `contents: read`, `id-token: write` and no checkout,
   run `jdx/packslip@87479dfc6443253dff69601cace5fc6ea07e6df5` (1.4.0), with
   `manifest: dist/packslip.toml`, all archives and SBOM assets in `artifacts`,
   `attest: link` only if they were actually attested, and `upload: false`
4. Verify the returned bundle with the expected GitHub workflow/repository identity,
   GitHub OIDC issuer, and every supplied archive/SBOM digest. Do not use the CI
   fixture key, example project identity, or unlogged-signature exceptions
5. Use a minimal separate `contents: write` job to upload only the verified bundle,
   then publish the release under the approved release procedure
6. Install that exact published version through mise on each native platform and
   verify the installed executable and packaged notices

This production flow is deliberately documentation only at this stage; no current
workflow can create a release or a tag. See the upstream
[Packslip publishing guide](https://packslip.dev/docs/publishing/) and
[artifact configuration](https://packslip.dev/docs/describing-releases/).
