# Verification status

[日本語の検証概要・旧 RustDesk 手順](VERIFICATION.md) · [Named connection guide](GENERIC.en.md) · [English README](../README.en.md)

Updated 2026-10-01. **The exact-source automated tests for `0.2.0-alpha.1` passed on all four native targets. Public distribution and actual mise installation are recorded separately below. Real enrollment and application acceptance remain incomplete; this is an experimental testing prerelease.**

## 0.2.0-alpha.1: current source and release evidence

Source [`236bd8e217f213a93b667f3d8d0509811d4f5464`](https://github.com/webkaz-labs/tsnet-bridge/commit/236bd8e217f213a93b667f3d8d0509811d4f5464) adds named forward and inbound TCP/UDP rules, pinned peer selection, presets, grouped start/stop with partial-start rollback, TTL and task leases, per-rule JSON, wait-ready, local migration/import/export, and opt-in idle-node user startup. Human output defaults to automatic Japanese/English locale selection. [Current guide](GENERIC.en.md)

The [exact-source ordinary CI run](https://github.com/webkaz-labs/tsnet-bridge/actions/runs/36910805666) passed all five jobs:

- Native race suites, real Unix-socket/Windows-pipe service and control IPC, vet and formatting on Linux x64/ARM64, macOS ARM64 and Windows x64
- Native package construction, archive execution, metadata, SBOM and license-notice checks on each target
- Packaged offline v2 initialization/settings/group/import/export/startup-planning workflows, explicit Japanese/English and automatic locale selection, read-only native OS-locale fallback and exact machine-JSON equality
- Aggregate Packslip fixture signing/verification, distinct from public OIDC signing

The [release workflow](https://github.com/webkaz-labs/tsnet-bridge/actions/runs/36911703369) completed all 15 jobs successfully for `0.2.0-alpha.1` and this exact source, including its source gate, four native release jobs, provenance, signing, publication, public verification and four actual mise-install jobs. Each native release job ran 25 repeated real IPC regressions, two identical package builds and native archive/language/locale/JSON smoke checks. All four restored their exact-source trusted-main Go caches. Tests still ran; cached compilation does not mean cached test results or independent-builder reproducibility.

- Publication: [v0.2.0-alpha.1](https://github.com/webkaz-labs/tsnet-bridge/releases/tag/v0.2.0-alpha.1) was published on 2026-10-01 at 19:11:08 UTC as a prerelease with 19 assets. The tag resolves to the exact source above. Packslip signing, artifact and bundle provenance, staging and publication jobs passed
- Independent unauthenticated public downloads, signature and provenance verification: [Passed](https://github.com/webkaz-labs/tsnet-bridge/actions/runs/36911703369): all 19 public assets downloaded without authentication, with Packslip signature, provenance and content verification
- Actual mise installation and installed-binary checks on all four targets: [Passed on all four targets](https://github.com/webkaz-labs/tsnet-bridge/actions/runs/36911703369): actual exact-pin installation with mise 2026.9.18, version/source metadata, SBOM/notices, Japanese/English output, actual OS-locale fallback and exact JSON equality; no signature bypass or release-age override

The native mise jobs finished between 19:12:34 and 19:12:48 UTC on 2026-10-01, about 1½ minutes after the 19:11:08 publication. The full workflow completed at 19:12:54 UTC. Signature and release-age settings stayed at their verified defaults.

An additional local non-root Linux check used fresh isolated HOME/XDG/mise directories and the exact public Packslip pin. Download/install took 50.4 seconds. Global activation within the isolated state, version `0.2.0-alpha.1`, source `236bd8e…`, native-binary digest, SBOM/notices and the complete Japanese/English/native-locale/exact JSON and v2 offline checks passed. No real node was started. This local result is distinct from the four native release-install jobs.

These successful distribution and offline checks do not establish real enrollment, application compatibility or Windows standard-user operation. [Distribution details](DISTRIBUTION.md)

### Coverage and limits

Current source tests and recorded local review cover:

- Japanese/English onboarding, help, confirmations, status, errors and next actions; locale precedence/overrides, preserved user values and machine JSON
- In-place typing retries, edit/back/cancel, narrow-terminal QR fallback, interrupted and repeated operations, and unchanged scope/expiry on repeated active starts
- Browser/QR/manual-link login states, trusted-URL rejection, terminal-redirection protection and terminal QR pixel reconstruction; a separate installed libzbar decoder recovered a synthetic noncredential URL
- Config, CLI, identity, policy, transport, autostart and distribution; native runners complete the real service/control IPC checks blocked by the local sandbox
- Independent security review of confirmation-scope binding, repeated lifetime changes, observed revocation latching and process-wide stream/datagram resource budgets
- Twenty repeated local race runs of new forwarding/inbound/lifecycle cases
- Numeric-loopback target rejection, source denial before local dial, TCP half-close, revoked replies and source-ID reassignment
- Separate UDP source mappings and delayed/out-of-order delivery, idle/queue/capacity/cancellation tests
- No start on save/import/restart; partial-group rollback preserving existing work; owner mismatch and idempotent stop; TTL/lease expiry and no reconnect resurrection
- Five deliberate negative controls that rejected broken peer pins, source reassignment, expiration, independent grant cancellation and loopback-only targets

The earlier core source [d481e0e](https://github.com/webkaz-labs/tsnet-bridge/commit/d481e0e54888edea892879ac2866da151ba7ca0a) passed [four native jobs and the Packslip fixture](https://github.com/webkaz-labs/tsnet-bridge/actions/runs/36902789788). That is historical intermediate evidence; the exact-source run above covers the subsequent usability/localization changes. No real tailnet node was enrolled for these automated checks.

Still unverified: real enrollment and phone QR authentication, real-tailnet inbound acceptance and ACL behavior, SSH/SFTP/HTTP/HTTPS/DB/AI/MCP applications, mobile clients, direct/DERP performance, OS suspend/network handoff, user-login startup registration/removal, Windows standard-user enrollment, and RustDesk bidirectional screen/input. These are separate acceptance gates, not passed by local mocks, native CI or distribution verification.

## Historical 0.1.0-alpha.2 release and real installation verification

[v0.1.0-alpha.2](https://github.com/webkaz-labs/tsnet-bridge/releases/tag/v0.1.0-alpha.2) was published on 2026-10-01 at 16:07:59 UTC from
[`0069e38732227c8913ee6ceb0ec21784ae03d862`](https://github.com/webkaz-labs/tsnet-bridge/commit/0069e38732227c8913ee6ceb0ec21784ae03d862).
The [complete release workflow](https://github.com/webkaz-labs/tsnet-bridge/actions/runs/36888899407) succeeded, including:

- Native race tests and 25 repeated IPC regression runs on Linux amd64/arm64, macOS arm64, and Windows amd64
- Two identical package builds per target, native archive execution, SBOMs and license notices
- Real GitHub OIDC Packslip signing, provenance verification, and unauthenticated download of all 19 public assets
- Actual mise 2026.9.18 installation, global activation, version/help execution, source metadata, SBOM and retained notice checks on all four targets

Installation completed between 16:09:42 and 16:10:12 UTC, about two minutes after
publication, using the exact prerelease pin with no age override or signature
bypass. This proves distribution and native executable startup, not enrollment
or real application compatibility. Windows hosted runners are administrator
sessions; standard-user authentication remains unverified.

These alpha.2 results do not establish publication or installation of `0.2.0-alpha.1`. The [legacy RustDesk procedure](#legacy-rustdesk-acceptance-procedure) below deliberately remains pinned to the historical version.

The IPC regressions retain real sockets/pipes and explicitly cover connect/close
overlap. Exact drain, timeout, cancellation, and forwarding deadlines use
virtual-time tests. The Windows adapter recovers a lost-close-notification path
in pinned go-winio; the successful native repetitions do not establish a blanket
OS scheduling or shutdown-latency guarantee.

## Historical source and native CI

Source commit: [`0f7ec7909c3d95750842931c79b95ef0d410135c`](https://github.com/webkaz-labs/tsnet-bridge/commit/0f7ec7909c3d95750842931c79b95ef0d410135c)

[Complete successful CI run](https://github.com/webkaz-labs/tsnet-bridge/actions/runs/36857522576)

All five native jobs passed:

- Linux amd64 on ubuntu-24.04
- Linux arm64 on ubuntu-24.04-arm
- macOS arm64 on macos-26
- macOS amd64 on macos-15-intel
- Windows amd64 on windows-2025

Each job ran race-enabled tests, native local IPC/lifecycle checks, vet, formatting checks, two repeatable package builds, archive/notices/SBOM validation, and execution of the packaged binary's help/version commands. The aggregate Packslip 1.4.0 job signed a disposable example-identity fixture and verified all five archives plus five scoped SBOM resources. Test signing keys/bundles were removed, not published.

The run's development artifacts are historical CI outputs, not the current installation path. Use the version-specific [distribution record](DISTRIBUTION.md) and [named connection guide](GENERIC.en.md) for `0.2.0-alpha.1`. The legacy procedure below remains pinned to `0.1.0-alpha.2`. Installation verification and real tailnet/application acceptance are separate results.

The current prerelease matrix is four targets: macOS ARM64, Windows x64, and
Linux x64/ARM64. The five-target results above are historical; Intel macOS is
not included in the current distribution.

Current ordinary CI now builds each target once, preserving all tests, packaged
archive checks and the Packslip fixture. The prerelease still builds twice and
compares digests. Trusted-main module/compilation caches reduce repeated work;
test results are never reused. [Cache boundaries](DISTRIBUTION.md#ci-and-prerelease-caches)

## Local checks and review

- TCP forwarding, half-close, timeout/cancellation, shutdown, connection caps and loopback binding
- Authenticated SOCKS CONNECT, failed authentication, malformed input, BIND/UDP rejection
- Persistent per-source UDP mappings, asynchronous replies, idle expiry, bounds and per-datagram authorization
- Strict profile parsing, private persistence, single-instance lock, peer/port allowlists and netstack-only dispatch
- Active peer-ID/address pinning, including hostname reuse and IP reassignment
- App lifecycle regression checks: short request context isolation, revocation, dead-listener recovery, partial bind rollback and logout failure
- Repeated race runs and independent security review; no remaining blocker found for explicitly experimental source publication
- govulncheck 1.8.0 on the Linux build found no reached vulnerabilities or vulnerable imported packages after updating x/crypto to 0.56.0. The module-level unmaintained openpgp advisory remains in an unused part of x/crypto; this is not a blanket claim that all dependencies are vulnerability-free

A restricted development sandbox denied Unix-domain socket syscalls; those local tests were recorded as blocked rather than counted as passed. Native CI subsequently passed those tests on all targets.

Windows CI caught and corrected three platform-specific issues: textual SID aliases in the ACL assertion, CRLF checkout formatting, and Go toolcache junction traversal during notice collection. ACLs are now compared by binary SID; checkout text is LF-stable; only the explicitly supplied notice root is link-followed, and interior links/reparse points are rejected.

## What Windows CI does not prove

[GitHub's Windows hosted runners run as administrators with UAC disabled](https://docs.github.com/en/actions/reference/runners/github-hosted-runners#administrative-privileges). Passing native CI therefore does not establish standard-user startup or enrollment. Cross-compilation never establishes native runtime behavior either.

A future credential-free test can launch a child with a restricted token, confirm that the administrator SID/privileges are disabled, and exercise an isolated local-control test harness. [CreateRestrictedToken](https://learn.microsoft.com/en-us/windows/win32/api/securitybaseapi/nf-securitybaseapi-createrestrictedtoken) supports reducing an existing token without creating a persistent account. A controlled child still differs from a clean standard-user Windows installation and cannot establish real interactive login behavior.

That additional harness is **not implemented or passed**. Tailscale's [pinned tsnet tests](https://github.com/tailscale/tailscale/blob/v1.102.5/tsnet/tsnet_test.go) demonstrate local test-control patterns, but all control/DERP/bootstrap traffic would need to be isolated before calling such a test offline.

The current production `run` command is not an offline initialization test: tsnet starts coordination traffic even before browser approval. `version`, `setup`, `settings` and an inactive `status` can be checked without enrollment; they do not test actual tsnet startup.

## Legacy RustDesk acceptance procedure

The following is the experimental `0.1.0-alpha.2` fixed-forwarding procedure, preserved for reproducible historical comparison. For new named connections and scoped shares use [the 0.2.0-alpha.1 guide](GENERIC.en.md). This procedure is not a passed result and does not cover the new generic workflow. It needs a reachable configured hbbs/hbbr server, its public key, a standard-user Windows session, and a macOS session for the initial bidirectional test. Linux controller/controlled roles need separate acceptance afterward.

1. Use mise 2026.9.18 in a normal, non-elevated terminal. Both PowerShell and
   Unix shells accept:

   ```sh
   mise use -g "packslip:github.com/webkaz-labs/tsnet-bridge[prerelease=true]@0.1.0-alpha.2"
   mise exec -- tsnet-bridge version
   ```

   Require `tsnet-bridge 0.1.0-alpha.2`. The full version pin is exempt from the
   default 24-hour discovery delay; verification stays enabled. Stop on signature
   errors or OS security warnings. A prerelease is not a stable support claim.
2. In terminal A, create the profile and run in the foreground:

   ```sh
   mise exec -- tsnet-bridge setup
   mise exec -- tsnet-bridge run
   ```

   Setup requests the server's tailnet name/IP and RustDesk **public** key (the
   contents of `id_ed25519.pub`, never `id_ed25519`). A separate relay uses
   `setup --relay-host` with the confirmed host instead of plain setup. Keep A
   open. Run starts real Tailscale coordination traffic before browser approval.
   In another non-elevated terminal B under the same user:

   ```sh
   mise exec -- tsnet-bridge login --no-browser
   mise exec -- tsnet-bridge status --json
   mise exec -- tsnet-bridge doctor
   mise exec -- tsnet-bridge settings
   ```

   The person running the test opens the private authorization URL and approves
   this specific node. Do not send auth keys, passwords, tokens or private URLs to
   chat, issues or CI logs. Require `state: ready`, `tailnet_state: Running`,
   `mode: forward`, and the expected loopback listeners. `doctor` exiting zero is
   insufficient. `rustdesk: unverified` remains expected.
3. Repeat on the other endpoint. Each creates its own tsnet identity and requires
   separate approval. Windows standard-user acceptance requires a genuinely
   non-administrator account; merely opening a non-elevated shell from an admin
   account does not establish that condition.
4. Back up existing RustDesk settings before entering the displayed values. Use the same loopback relay address/port on every participant, keep the proxy blank and UDP enabled, and select relay with `remote-ID/r`. Check server relay-address rewrite settings first. Authenticate through RustDesk's normal UI.
5. Test Windows as the controlled endpoint first, then reverse direction. Record cold and idle registration, actual screen display/input, disconnect/reconnect, and observed relay address. A `ready` status or TCP connect is not this evidence.
6. Test `stop` and restart for saved-state reuse, process/application restarts, port conflicts and network interruption. Restore the backed-up RustDesk settings afterward. If a test node is no longer needed, explicitly log out while the helper is running; removal from the admin console is a separate approved action.

Remote testing requires access to the chosen test endpoints and approval for each real enrollment. Credentials should remain in the service's own sign-in flow. Without the necessary endpoints and enrollment approvals, real E2E cannot be marked as passed.

## Required before a supported release

The following real-device checks remain open for `0.2.0-alpha.1`. Distribution completion is a separate gate recorded above; it must not close these items.

- [ ] Browser/manual-link/phone-QR enrollment on actual devices, with MFA and node approval where required
- [ ] Named TCP/UDP forward and scoped inbound shares against real tailnet ACLs, including denied peers, identity changes, stop and TTL expiry
- [ ] SSH/SFTP, HTTP/HTTPS, database, AI/API and applicable HTTP MCP clients with normal authentication and certificate/host-key checks
- [ ] User-level autostart registration/removal at real OS login, remaining idle and preserving stopped/expired rules
- [ ] Real sleep/network-change recovery, including expiry during sleep, with no unintended share restart
- [ ] Windows standard-user clean install, interactive enrollment, state save/reuse, stop and logout
- [ ] Linux nonroot real enrollment, state reuse, shutdown, and recovery on amd64/arm64
- [ ] macOS real enrollment and state lifecycle on supported architectures
- [ ] Real RustDesk Mac-to-Windows and Windows-to-Mac registration, screen, input, disconnect and reconnect
- [ ] Linux interoperability in each intended controller/controlled role
- [ ] Idle UDP registration and later incoming connection notifications
- [ ] Relay address propagation, common local relay port, server rewrite settings, and mixed-profile handling
- [ ] Direct Tailscale and DERP paths; blocked UDP and restrictive upstream network conditions
- [ ] Sleep/resume, network change, outages, node expiry/revocation, ACL refusal and process/application restart
- [ ] Future upgrades/rollbacks and legacy-profile migration in the intended real environment; keep these separate from first-install CI
- [ ] Final dependency/security review, upstream private logging review and OS signing/notarization decision

No real tailnet credentials are required by the existing CI. Real enrollment creates persistent external access and requires explicit approval. Do not replace any unchecked item with a mock result.
