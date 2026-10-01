# Verification status

[日本語の導入・動作確認手順](VERIFICATION.md) · [English README](../README.en.md)

Updated 2026-10-01. **The experimental implementation and automated packaging checks pass. Real tailnet enrollment and RustDesk remote-control acceptance remain incomplete. The testing prerelease does not make this a supported end-to-end product.**

## Verified source and native CI

Source commit: [`0f7ec7909c3d95750842931c79b95ef0d410135c`](https://github.com/webkaz-labs/tsnet-bridge/commit/0f7ec7909c3d95750842931c79b95ef0d410135c)

[Complete successful CI run](https://github.com/webkaz-labs/tsnet-bridge/actions/runs/36857522576)

All five native jobs passed:

- Linux amd64 on ubuntu-24.04
- Linux arm64 on ubuntu-24.04-arm
- macOS arm64 on macos-26
- macOS amd64 on macos-15-intel
- Windows amd64 on windows-2025

Each job ran race-enabled tests, native local IPC/lifecycle checks, vet, formatting checks, two repeatable package builds, archive/notices/SBOM validation, and execution of the packaged binary's help/version commands. The aggregate Packslip 1.4.0 job signed a disposable example-identity fixture and verified all five archives plus five scoped SBOM resources. Test signing keys/bundles were removed, not published.

The run's development artifacts are historical CI outputs. The acceptance
installation path is now the explicitly pinned testing prerelease described in
[distribution](DISTRIBUTION.md), not a manually extracted CI artifact. Confirm
that [v0.1.0-alpha.1](https://github.com/webkaz-labs/tsnet-bridge/releases/tag/v0.1.0-alpha.1)
has been published and its release workflow has completed. Installation verification
and the real tailnet/RustDesk acceptance below are separate results.

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

## Minimum real acceptance session

The following is a procedure, not a passed result. It needs a reachable configured hbbs/hbbr server, its public key, a standard-user Windows session, and a macOS session for the initial bidirectional test. Linux controller/controlled roles need separate acceptance afterward.

1. Use mise 2026.9.18 in a normal, non-elevated terminal. Both PowerShell and
   Unix shells accept:

   ```sh
   mise use -g "packslip:github.com/webkaz-labs/tsnet-bridge[prerelease=true]@0.1.0-alpha.1"
   mise exec -- tsnet-bridge version
   ```

   Require `tsnet-bridge 0.1.0-alpha.1`. The full version pin is exempt from the
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

- [ ] Windows standard-user clean install, interactive enrollment, state save/reuse, stop and logout
- [ ] Linux nonroot real enrollment, state reuse, shutdown, and recovery on amd64/arm64
- [ ] macOS real enrollment and state lifecycle on supported architectures
- [ ] Real RustDesk Mac-to-Windows and Windows-to-Mac registration, screen, input, disconnect and reconnect
- [ ] Linux interoperability in each intended controller/controlled role
- [ ] Idle UDP registration and later incoming connection notifications
- [ ] Relay address propagation, common local relay port, server rewrite settings, and mixed-profile handling
- [ ] Direct Tailscale and DERP paths; blocked UDP and restrictive upstream network conditions
- [ ] Sleep/resume, network change, outages, node expiry/revocation, ACL refusal and process/application restart
- [ ] Review testing-prerelease native mise/Packslip installation results; separately test future upgrades and rollbacks
- [ ] Final dependency/security review, upstream private logging review and OS signing/notarization decision

No real tailnet credentials are required by the existing CI. Real enrollment creates persistent external access and requires explicit approval. Do not replace any unchecked item with a mock result.
