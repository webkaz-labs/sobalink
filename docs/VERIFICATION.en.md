# sobalink verification

[日本語](VERIFICATION.md) · [User guide](GENERIC.en.md) · [Distribution](DISTRIBUTION.md)

**sobalink is an unreleased development draft.** LAN, the local UI and typed CLI are implemented, but published CI and later local verification cover different source snapshots.

## Verified native snapshot

Published [PR #2](https://github.com/webkaz-labs/sobalink/pull/2) commit `5dd6b8c99b8e0167924b38411889e3a0092ac4af` passed [all six jobs in run 37046723268](https://github.com/webkaz-labs/sobalink/actions/runs/37046723268). Linux x64/ARM64, macOS ARM64 and Windows x64 passed the real stock Tailcat loopback TLS DERP test: 130 seconds of existing TCP across an actual two-minute relay lease, bidirectional TCP/UDP, denied-key admission, active revocation and cleanup. Core two-peer text/file/share/revoke integration, native packages, that snapshot's Go-backed Chromium checks and the manifest also passed.

These are real native CI processes using isolated loopback peers. They do not establish actual-device, direct LAN/WAN/NAT, sleep/wake, native IME or release acceptance. Do not apply those results to later UI, CLI or Core changes.

## Later local verification

| Source/category | Recorded result and limit |
| --- | --- |
| Graph/navigation `76648f48` | 121 UI tests and five browser-QA safety tests passed locally; no published CI for this source |
| Guided UI `8c49e4f6` | Integrated from `0d9d3a3`: 216 UI tests, strict TypeScript/build, five privacy/preflight tests and two byte-identical clean builds passed locally. Real browser execution of the new forms and published CI remain pending |
| Typed CLI `7b69e606` / `9cfcc958` | LAN setup, invitation input, receive settings, presets, service reuse, previews and structured errors are implemented. Pure-function, mock CLI and in-memory IPC tests cover these changes; actual native IPC awaits new CI |
| Local IPC `e85c3ece` | 13 focused in-memory/shutdown race tests and vet passed locally. A validated six-batch history exceeds the old 256 KiB allowance and now fits the 16 MiB encoded response limit; oversized replies return `response_too_large` with local-UI history recovery. This is a wire-size limit, not a heap-memory limit. New native IPC CI remains pending |
| Receive settings/Core `321da1e` | Reviewed changes persist autosave/directory/pause once, then publish them together. Core and transfer race tests passed locally; published CI for this change has not run |
| Additional real-browser acceptance | Verify actual fonts/glyphs/metrics, graph/detail screenshots and complete flows against the latest source. Unit-test counts do not establish real-browser or native IME acceptance |
| Actual-device acceptance | Real device setup, direct LAN/WAN/NAT changes, application compatibility and sleep/wake remain unverified |
| Signed publication | No new version is assigned; release preparation and signed publication remain pending |

Later source is not yet covered by published CI. Separate source inspection, mocks, real-process tests and actual-device acceptance. Bind each later result and screenshot to the exact source that produced it.

## Required native and browser checks

Retain Linux x64/ARM64, macOS ARM64 and Windows x64 native race tests, vet, IPC/socket cleanup, package execution and language checks. Cross-compilation alone does not satisfy native acceptance. Hosted Windows execution does not establish standard-user enrollment.

The local browser gate must exercise the actual Go process and embedded production assets, not only synthetic state. Verify code entry, expired/reused codes, session expiry, navigation/back/cancel, repeated actions, stale state, network errors, keyboard/focus, Japanese/English, light/dark themes and narrow layouts. Record screenshot evidence from that exact build. A failed server launch is a blocker, not a browser pass.

Negative API tests must reject wrong Host/Origin, missing CSRF, foreign sessions, oversized requests and attempts to access administration through the peer API. Check that codes, auth URLs and capabilities do not leak into URLs or routine state.

## Real two-peer acceptance

Use generic fixtures and obtain the necessary network/account permissions. Record source commit, versions, OS, selected network, result and the first failing stage without publishing private identity state.

1. Start a fresh profile, confirm no network is selected, enroll a separate Tailnet node, and verify the intended peers
2. Trust the precise sender identity on the receiver, send explicit text, copy it manually, and confirm there is no automatic clipboard synchronization
3. Offer an image, several files and a folder together; decline one batch, accept another into an explicit directory, and compare saved sizes/hashes and empty-folder behavior
4. Test name collisions, invalid paths/links, unsupported entries, size/concurrency limits, storage failure and cancellation without existing-file overwrite or automatic opening/execution
5. Enable autosave for one exact peer and destination; test pause, disable, identity replacement and trust renewal without silently reviving the old grant
6. Interrupt a file, retry it from the start, and verify completed-file acknowledgements do not rewrite saved files; after restart, require a new batch and expose possible unique-name copies
7. Share a narrow TCP range to explicit peers, test an excluded/reserved port and an unauthorized peer, start another app inside the permitted range, and confirm the grant covers that later app only until expiry
8. Exercise UDP and local-listener caps, collisions, explicit local-port mapping, stop, expiry, revocation and cleanup
9. Connect to an ordinary Tailnet service without sobalink on the target; check the actual app's authentication, TLS or host-key handling and application operation
10. Test direct/relay observations only where supported by real telemetry, network interruption and sleep/wake. Treat current Unknown as Unknown and do not promise preserved TCP sessions

Transport readiness, discovered metadata and file staging are not application or delivery completion. Stop does not recall data or cancel remote jobs.

## Tailcat gate

The adapter and Core/CLI now cover explicit relay selection, distinct server/client role keys bound to the paired identity, sealed bootstrap admission, atomic secret-state persistence before acknowledgement, UDP multiplexing and durable revocation. The command contract and recovery errors are documented in the [LAN guide](LAN.en.md). Pairing remains separate from application trust, autosave and service grants.

Source dependencies use Tailscale `v1.104.0` and Tailcat `v0.7.1-0.20260929145319-b4dc28e8aa89`, which supplies the required identity and revocation APIs. That pin and successful compilation are not evidence of working native pairing.

The opt-in native harness passed on all four targets at `5dd6b8c99b8e0167924b38411889e3a0092ac4af` in [run 37046723268](https://github.com/webkaz-labs/sobalink/actions/runs/37046723268). It uses real stock Tailcat/WireGuard and a TLS-pinned loopback DERP fixture, rejects invalid invitations and unknown keys, exchanges bidirectional TCP/UDP, holds existing TCP for 130 seconds across an actual two-minute relay lease with observed DERP re-admission, and verifies active revoke and cleanup. This fixture omits UDP underlay and uses no external relay.

This successful fixture result does not establish real direct UDP, LAN/WAN/NAT migration, cross-relay migration, throughput or general established-TCP preservation. The fixture's loopback-only restrictions are not a production zero-egress promise. Production permits peer direct traffic plus encrypted payload and HTTPS/ICMP diagnostics to the explicit trusted relay endpoint.

Verify no default public relay map/DNS bootstrap, required omission tags, rejected mismatched capabilities and unsupported proxy/backend overrides. Unknown paths remain Unknown. The integrated graph uses actual self-to-peer observations and must not invent peer-to-peer links or rates.

Application revoke closes its authorization and flows immediately. The embedded relay's two-minute connection lease is a separate boundary: an admitted session can remain until lease expiry, or about four minutes from initial bootstrap if temporary role admission overlaps a lease. Test both application closure and later relay re-admission rejection; do not equate them.

Real-device acceptance still needs explicit two-peer setup, identity verification, consented file/service operations, pause/unpause with canceled-send reselection, offline repair, certificate expiry, durable revoke, interrupted/uncertain pairing and network recovery. No arbitrary public fallback or automatic backend exchange is allowed.

## Release and legacy results

Follow the exact-source native, browser, reproducibility, signature/provenance, public-download and installed-binary gates in [DISTRIBUTION.md](DISTRIBUTION.md). A draft PR is not a release.

The historical `tsnet-bridge` releases and their published workflow records remain on [Releases](https://github.com/webkaz-labs/tsnet-bridge/releases). They used an earlier executable and profile. This guide intentionally does not present those commands or old RustDesk-specific acceptance as the current product workflow.
