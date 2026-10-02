# sobalink verification

[日本語](VERIFICATION.md) · [User guide](GENERIC.en.md) · [Distribution](DISTRIBUTION.md)

**This is the new sobalink development draft, not a verified new release.** Its initial snapshot includes the embedded Web UI, shared CLI/core, Tailnet adapter, peer messaging/batch transfers and scoped service ranges. Tailcat is unavailable in this snapshot and remains under integration. No legacy release result proves this source.

## Evidence categories

| Category | Current meaning |
| --- | --- |
| Source implementation | The initial draft contains Tailnet, Web, CLI, transfer and service-range code |
| Unit/mock/race checks | Focused checks exercise authorization, storage, limits, range dispatch and UI behavior; report each actual run separately |
| Complete native tests | The cloud execution environment denies socket creation with `operation not permitted`; a blocked local run is not a full pass |
| Actual browser acceptance | The Go-backed browser CI harness is prepared; exact-source native results and screenshots are required before marking it passed |
| Real network/application acceptance | Enrollment, two-peer delivery, ACL behavior, direct/relay changes and actual applications remain unverified |
| Signed sobalink publication | No version has been assigned and no new signed release is claimed |

The draft's local frontend component suite passed 44 tests during implementation. That result checks components in a test environment, not an actual browser against the Go server. Later changes require rerunning affected checks. Native CI must bind results to the exact source commit, and a new commit invalidates claims that refer only to an older run.

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

**Unavailable in the initial draft.** Integration must first resolve distinct server/client key ownership, authenticated pairing, atomic secret-state persistence before success acknowledgement, revocation and UDP multiplexing. The stock adapter work uses a pinned upstream revision with the required identity/revocation APIs; compile or pure tests alone do not establish working pairing.

Then verify an explicit numeric trusted relay and certificate pin, rejection of mismatched capabilities, no default public relay map/DNS bootstrap, the required omission build tags, and rejection of unsupported proxy/override environments. Permitted traffic includes peer direct traffic and encrypted payload plus HTTPS/ICMP diagnostics to the selected relay endpoint. Do not claim strict LAN isolation or zero external traffic.

Real native tests must demonstrate two peers, approved pairing, reliable peer identity, transfer/service operation, unauthorized-peer rejection, revoke/close, relay failure and direct/relay/reconnect behavior. No arbitrary public relay fallback or automatic backend exchange is allowed. Existing TCP continuity remains unpromised until specifically demonstrated, and even such a result is scoped to its scenario.

## Release and legacy results

Follow the exact-source native, browser, reproducibility, signature/provenance, public-download and installed-binary gates in [DISTRIBUTION.md](DISTRIBUTION.md). A draft PR is not a release.

The historical `tsnet-bridge` releases and their published workflow records remain on [Releases](https://github.com/webkaz-labs/tsnet-bridge/releases). They used an earlier executable and profile. This guide intentionally does not present those commands or old RustDesk-specific acceptance as the current product workflow.
