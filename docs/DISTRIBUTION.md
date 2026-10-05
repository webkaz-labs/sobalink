# Build and distribution

[日本語の概要](../README.md) · [English overview](../README.en.md) · [Verification](VERIFICATION.en.md)

**sobalink uses the executable `soba`.** Build a reviewed source checkout or install a complete signed prerelease from [sobalink Releases](https://github.com/webkaz-labs/sobalink/releases). The repository locator and Go module are `github.com/webkaz-labs/sobalink`. Publication and verification belong to an exact version and source commit; the existence of source code, a tag or installation instructions does not establish either.

The legacy `tsnet-bridge` releases, including `0.2.0-alpha.2`, belong to the earlier CLI. Their CI, signatures and installation results do not verify sobalink, its embedded UI, file protocol or Tailcat adapter. Historical signatures retain their original identities. [Legacy releases](https://github.com/webkaz-labs/tsnet-bridge/releases)

## Install a signed prerelease

[0.3.0-alpha.2](https://github.com/webkaz-labs/sobalink/releases/tag/v0.3.0-alpha.2) is published from `00cc6a99809df77bf1754936ea7bf5ca4c5d0741`. [Release run 37178488713](https://github.com/webkaz-labs/sobalink/actions/runs/37178488713) passed all 15 jobs, including the four native packages, signatures/provenance, unauthenticated public retrieval and actual mise installation on all four targets. The pin below installs that release. New prepared-route changes are unreleased source under verification, with no next version assigned here; they are not part of alpha.2. [Exact-source record](VERIFICATION.en.md#current-integration-and-published-baseline)

A complete release includes four native archives and each target's SBOM, build metadata and notice inventory, plus `packslip.toml`, `SHA256SUMS` and `packslip.sigstore.json`. The signed bundle must match the archives, SBOMs and manifest; GitHub provenance is checked separately. A tag, unsigned archive or checksum file alone is insufficient.

With mise **2026.9.18** available, the same explicit prerelease pin used by the publication workflow is:

```sh
mise install "packslip:github.com/webkaz-labs/sobalink[prerelease=true]@0.3.0-alpha.2"
mise use -g "packslip:github.com/webkaz-labs/sobalink[prerelease=true]@0.3.0-alpha.2"
mise exec -- soba version
mise exec -- soba
```

`mise use -g` selects this version in the global mise configuration. `mise exec --` runs it without depending on shell activation; with mise already activated, use `soba` directly. The manifest selects `bin/soba` or `bin/soba.exe` for the [native target](#native-targets). Keep signature, identity and digest checks enabled; do not use an age override, test signing key or verification bypass.

The workflow uses `mise where` with this exact pin and [verify-installed.py](https://github.com/webkaz-labs/sobalink/blob/main/.github/scripts/verify-installed.py) to inspect the actual installation. It checks the native target, version, source commit, binary and frontend-lock digests, SBOM and retained notices, then executes the installed binary. These checks do not establish real enrollment, application compatibility, native IME, OS sign-in or suspend behavior. [Source-specific evidence and remaining acceptance](VERIFICATION.en.md)

## Build this checkout

Pinned tool versions are Go **1.27.1**, Node **24.19.0** and npm **11.9.0**. `mise.toml` declares the development tools and required Go tags. Review the checkout and any tool-configuration trust prompt before enabling it.

With the pinned tools already available:

```sh
npm --prefix web ci --no-audit --no-fund
npm --prefix web run build
go build -tags ts_omit_portmapper,ts_omit_captiveportal,ts_omit_useproxy -trimpath -o bin/soba ./cmd/soba
./bin/soba version
```

For Windows, change the output to `bin/soba.exe` and run `./bin/soba.exe version`. Alternatively, `mise run build` runs the locked frontend build before the Go build. The resulting binary embeds `web/dist`; end users do not need Node or a development server.

The three omission tags are part of the supported build boundary, not optional performance tuning. They remove port mapping, captive-portal probes and system-proxy support. Tailcat activation also validates its runtime environment. A build without required tags must not be represented as a working trusted-relay configuration.

Run applicable source checks:

```sh
npm --prefix web test
npm --prefix web run typecheck
go test -tags ts_omit_portmapper,ts_omit_captiveportal,ts_omit_useproxy -race -count=1 -timeout=10m ./...
go vet -tags ts_omit_portmapper,ts_omit_captiveportal,ts_omit_useproxy ./...
python -m unittest discover -s .github/scripts -p 'test_*.py' -v
python .github/scripts/check-frontend.py
```

The frontend check uses the pinned environment to reproduce assets from the lockfile and compare them with the committed build. A failed, unavailable or blocked stage must be recorded separately from successful unit checks. A socket-restricted environment cannot establish native runtime acceptance. Each changed source needs its own affected checks; enumeration and DOM tests do not replace browser execution. Keep exact run outcomes in the [verification record](VERIFICATION.en.md#recorded-source-evidence).

## Native targets

| GOOS | GOARCH | Native runner | Archive |
| --- | --- | --- | --- |
| linux | amd64 | ubuntu-24.04 | tar.gz |
| linux | arm64 | ubuntu-24.04-arm | tar.gz |
| darwin | arm64 | macos-26 | tar.gz |
| windows | amd64 | windows-2025 | zip |

Intel macOS and Windows ARM64 are not current distribution targets. Runner success does not establish a minimum supported OS version or Windows standard-user enrollment. Packages use `CGO_ENABLED=0`, baseline `GOAMD64=v1` / `GOARM64=v8.0`, and no installed driver, privileged service or OS network configuration. Native execution remains required even when cross-compilation succeeds.

## Local package layout

A local `0.0.0-dev.1` label is a build identifier, not an announced release. From a reviewed checkout with the frontend already built:

```sh
export SOURCE_DATE_EPOCH="$(git log -1 --format=%ct)"
go run -tags ts_omit_portmapper,ts_omit_captiveportal,ts_omit_useproxy ./cmd/package-tool build 0.0.0-dev.1 linux amd64 "$(git rev-parse HEAD)"
go run -tags ts_omit_portmapper,ts_omit_captiveportal,ts_omit_useproxy ./cmd/package-tool checksums
```

Repeat for each supported target before preparing a complete manifest:

```sh
go run -tags ts_omit_portmapper,ts_omit_captiveportal,ts_omit_useproxy ./cmd/package-tool manifest 0.0.0-dev.1
go run -tags ts_omit_portmapper,ts_omit_captiveportal,ts_omit_useproxy ./cmd/package-tool checksums
```

The explicit commit argument is a source label, not an attestation. A dirty checkout cannot claim to be exactly that commit. CI derives source identity from its checkout; local draft edits require an honest local-build label.

Archives are named `sobalink-VERSION-GOOS-GOARCH.tar.gz` or `.zip` and contain:

```text
bin/soba[.exe]
share/sobalink/LICENSE
share/sobalink/README.md
share/sobalink/README.en.md
share/sobalink/SECURITY.md
share/sobalink/docs/*.md
share/sobalink/web/*.md
share/sobalink/go.mod
share/sobalink/go.sum
share/sobalink/build.json
share/sobalink/bom.cdx.json
share/sobalink/third-party-notices.json
share/sobalink/licenses/...
```

The embedded frontend is included in the binary; build metadata records its lockfile and asset hashes. The runtime inventory includes locked production npm dependencies and their notices alongside target-filtered Go modules. It must not silently omit production frontend dependencies because tree shaking removed some output.

The SBOM, build metadata and notices index are also emitted as assets. `SHA256SUMS` records present regular distribution files, excluding itself. The later signature bundle is independently verified and receives its own provenance. Checksums establish integrity, not publisher identity. Do not distribute private node state or generic-looking fixtures that reveal personal context.

The route source includes a modified internal Tailcat component. When it is in a target's runtime package closure, packaging retains its original license, provenance metadata and [adaptation record](../internal/routecat/UPSTREAM.md), and records upstream and adapted input hashes separately. This does not change the normal single-executable package or establish route acceptance. No strict-egress helper executable is distributed.

## Reproducibility and provenance

The packager pins the toolchain, required tags and target, disables cgo and workspace overrides, uses read-only module resolution, trims build paths, disables automatic VCS embedding, clears the linker build ID and sets an explicit version. Archives use sorted entries, normalized owners/modes and `SOURCE_DATE_EPOCH`; pre-1980 ZIP dates normalize to 1980. Symlinks and special archive entries are rejected.

The release workflow builds each native target twice and compares output digests. Locked frontend reproduction is a separate input check. This establishes repeatability of the selected inputs on that runner, not independent-builder reproduction, OS code signing/notarization or live application compatibility.

The CycloneDX SBOM and notice index preserve module/package provenance and copied notice hashes without guessing a legal classification. Go replacements, missing provenance and missing notices fail packaging. Source-only terms outside conventional notice filenames still require review before distribution.

Trusted-main Go caches bind runner, OS, architecture, toolchain, dependency manifests and source commit. Only successful canonical main CI saves that namespace. Prerelease restores the exact tested-commit key without fallback or saving. Cache misses build normally; caching never skips tests, package reproduction or signature checks.

Native CI also has a separate development namespace for canonical same-repository pull requests and explicit non-main branch dispatches, with the canonical default branch required to remain `main`. Each PR or hashed branch ref has its own boundary, plus the same runner, toolchain, manifests and checked-out source dimensions. Fallback stays within that boundary; only successful native checks save. Forks, other events and main runs cannot write development caches. Logs distinguish exact hits, fallback hits, misses and skipped restores; a fallback can succeed with `cache-hit: false`. The manifest and browser jobs do not use development caches. Development entries share the repository cache capacity and can evict trusted-main entries; eviction only causes a cold build. Same-source cold/warm CI measurement, including confirmed saves and restores, is still required before claiming a speedup.

## LAN native acceptance

The source includes a stock Tailcat two-peer test using a loopback TLS DERP fixture, no external relay and no UDP underlay. It exercises denied-key admission, bidirectional TCP held for 130 seconds across a real two-minute relay lease, UDP, active revocation and cleanup; the native suite also covers Core two-peer message/file/share operations. See the [verification record](VERIFICATION.en.md#recorded-source-evidence) for completed source-specific results. Each changed source and its browser flows need their own run. The restricted test topology is not a production promise of zero external traffic or general TCP continuity. Run the opt-in test on a native environment with socket support and no unsupported proxy/Tailscale environment overrides. The extra `lanlink_integration` and `ts_omit_udptransport` tags are for this isolated fixture only; ordinary production builds retain direct UDP support:

```sh
SOBALINK_RUN_LAN_INTEGRATION=1 go test -tags lanlink_integration,ts_omit_portmapper,ts_omit_captiveportal,ts_omit_useproxy,ts_omit_udptransport -count=1 -v -timeout=5m ./internal/lanlink -run '^TestTrustedRelayTwoPeerIntegration$'
```

The new route tests must additionally exercise two independent processes with actual sockets, controlled loss/recovery and externally unavailable LAN cold start while retaining the same pair and service entrance. Fixture-only `ts_omit_udptransport` is not the product configuration: native tests must also cover the normal direct-enabled build. Prepared-route unit tests, an internal transport prototype or an old alpha.2 run do not establish final Core/UI integration. [Route acceptance](VERIFICATION.en.md#route-recovery-gate)

## Publication gates

The prerelease workflow starts only through an explicit manual `workflow_dispatch` with a candidate version and exact tested commit. It requires that commit to be the current main with successful canonical CI. Publication must:

1. Select a new explicit prerelease version and exact reviewed main commit, with successful CI for that commit
2. Execute the four native race/vet/package jobs and the actual Go-backed local-browser acceptance job; record failures and unperformed real-network tests accurately
3. Reproduce locked frontend assets, build every package twice, compare digests, and execute the packaged CLI with Japanese/English, native locale fallback and stable JSON checks
4. Include build metadata, complete runtime dependency/SBOM/notice inventories, checksums and verified GitHub artifact provenance
5. Sign the Packslip bundle using the constrained release workflow identity and short-lived OIDC, verify issuer, workflow identity, artifact digests and transparency evidence, and attest the bundle itself
6. Recheck the source and CI before creating a tag and unpublished prerelease; never replace a published version or move an existing tag
7. Verify every staged digest, publish only as a prerelease, independently retrieve public bytes without download authentication, and verify their signatures and provenance
8. Install that exact published version through mise on all four native targets with signature, identity and digest checks enabled; execute the actual installed binary and verify its source metadata

A release must have the signed `packslip.sigstore.json` and all matching assets; a tag or archive alone is insufficient. Ordinary CI's disposable offline signing fixture is not a release and must not be accepted as production identity.

sobalink release verification uses `https://github.com/webkaz-labs/sobalink/.github/workflows/prerelease.yml@refs/heads/main`, with issuer `https://token.actions.githubusercontent.com`. Earlier tsnet-bridge signatures retain their original project and workflow identities; they are not rewritten. Packslip/mise do not themselves establish linked GitHub build provenance: the workflow separately verifies it with the repository, source, workflow, ref and runner restrictions.

Check the selected release's linked run for signed publication and installed-binary results under this workflow identity. Historical artifacts must still be checked against their original identities. OS warnings must be resolved through supported signing/distribution work, not bypassed.

## Updating a development build

Stop `soba`, keep any needed state backup private, rebuild the intended source and frontend, check `soba version`, then inspect saved state before explicitly starting services. A fresh directory requires new enrollment and trust. The new product does not promise migration from legacy commands or configuration, and an installation check does not prove rollback compatibility.

[User workflow](GENERIC.en.md#stop-revoke-and-upgrade) · [Remaining acceptance](VERIFICATION.en.md)
