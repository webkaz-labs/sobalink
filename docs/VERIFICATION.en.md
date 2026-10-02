# sobalink verification

[日本語](VERIFICATION.md) · [User guide](GENERIC.en.md) · [Distribution](DISTRIBUTION.md)

**sobalink is an unreleased development draft.** Repository renaming and a successful draft-PR workflow do not create a signed release. The new LAN adapter and Core/CLI implementation are present; dedicated LAN UI and the connection graph are still being integrated.

## Verified baseline

[PR #2](https://github.com/webkaz-labs/sobalink/pull/2), commit `278a6e17`, passed [all six jobs in run 37036882061](https://github.com/webkaz-labs/sobalink/actions/runs/37036882061): four native target/package jobs, the actual Go-backed Chromium browser job, and the manifest job. Browser checks covered Japanese/English desktop and mobile layouts, with screenshot evidence from that build.

That result establishes the baseline Web/Tailnet/transfer/service implementation's automated checks. It does not establish real Tailnet enrollment, actual remote application health, or the later LAN code and new UI. Preserve that source boundary when reading CI history.

## Evidence categories for the new snapshot

| Category | Recorded result and limit |
| --- | --- |
| LAN adapter | Separate static reviews and 38 local adapter tests passed, with repeated race checks; TLS pin/redirect checks use real TLS over in-memory connections |
| LAN Core | 18 Core, 2 transfer-manager and 1 Web API cases passed locally, alongside race/vet and full compilation |
| Secret CLI payloads | File/stdin input and offline-start recovery are implemented; focused command race tests passed |
| Stock two-peer Tailcat | Native loopback TLS DERP harness is prepared and compiled, but its actual run is pending |
| LAN setup UI and graph | Being integrated separately; baseline screenshots do not verify these new controls |
| Real network/application acceptance | Real Tailnet/LAN enrollment, application behavior, network changes and sleep/wake remain unverified |
| Signed publication | No new version has been assigned; release preparation and signed publication remain pending |

Local socket restrictions (`operation not permitted`) prevent a local live-network result in this execution environment. They do not invalidate the successful baseline native/browser CI, and local logic tests do not substitute for the pending stock integration run. Every new committed snapshot needs its own affected checks and CI evidence.

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

The opt-in native harness uses real stock Tailcat/WireGuard and a TLS-pinned loopback DERP fixture. It is designed to reject invalid invitations and unknown keys, exchange bidirectional TCP/UDP, hold established TCP for 130 seconds across a real two-minute relay lease and require observed DERP re-admission, then test revoke and cleanup. Its build omits UDP underlay and uses no external relay. **This harness has not run yet.** Do not label the lease-continuity scenario successful until its exact-source CI passes.

Even a successful fixture result would not establish real direct UDP, LAN/WAN/NAT migration, cross-relay migration, throughput or general established-TCP preservation. The fixture's loopback-only restrictions are not a production zero-egress promise. Production permits peer direct traffic plus encrypted payload and HTTPS/ICMP diagnostics to the explicit trusted relay endpoint.

Verify no default public relay map/DNS bootstrap, required omission tags, rejected mismatched capabilities and unsupported proxy/backend overrides. Unknown paths remain Unknown. The upcoming graph must use actual self-to-peer observations and must not invent peer-to-peer links or rates.

Application revoke closes its authorization and flows immediately. The embedded relay's two-minute connection lease is a separate boundary: an admitted session can remain until lease expiry, or about four minutes from initial bootstrap if temporary role admission overlaps a lease. Test both application closure and later relay re-admission rejection; do not equate them.

Real-device acceptance still needs explicit two-peer setup, identity verification, consented file/service operations, pause/unpause with canceled-send reselection, offline repair, certificate expiry, durable revoke, interrupted/uncertain pairing and network recovery. No arbitrary public fallback or automatic backend exchange is allowed.

## Release and legacy results

Follow the exact-source native, browser, reproducibility, signature/provenance, public-download and installed-binary gates in [DISTRIBUTION.md](DISTRIBUTION.md). A draft PR is not a release.

The historical `tsnet-bridge` releases and their published workflow records remain on [Releases](https://github.com/webkaz-labs/tsnet-bridge/releases). They used an earlier executable and profile. This guide intentionally does not present those commands or old RustDesk-specific acceptance as the current product workflow.
