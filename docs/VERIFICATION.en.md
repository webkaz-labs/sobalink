# sobalink verification

[日本語](VERIFICATION.md) · [User guide](GENERIC.en.md) · [Distribution](DISTRIBUTION.md)

**Verification applies to an exact source and version.** Device/service connectivity, transfers, saved workflows, capacity controls, private sign-in, the optional proxy and diagnostics are implemented. Implementation, local logic tests, native CI, browser execution, signed distribution and actual-device acceptance are separate results. For a published prerelease, also inspect its linked release workflow results.

## Current integration and published baseline

The latest published release is [0.3.0-alpha.2](https://github.com/webkaz-labs/sobalink/releases/tag/v0.3.0-alpha.2), exact source `00cc6a99809df77bf1754936ea7bf5ca4c5d0741`. Prepared multi-route recovery is newer unreleased source; it is not covered by that release's results. No next version is assigned here.

| Source/category | Result and boundary |
| --- | --- |
| Published alpha.2 | [Release run 37178488713](https://github.com/webkaz-labs/sobalink/actions/runs/37178488713) passed all 15 jobs: four native race/vet/package jobs, signing/provenance, unauthenticated public retrieval and actual mise-installed binaries on Linux x64/ARM64, macOS ARM64 and Windows x64. Its exact-main successful CI gate passed. Physical-device/application acceptance is separate |
| Published alpha.2 resource observation; merged [PR #7](https://github.com/webkaz-labs/sobalink/pull/7), merge `11cd8285607c220962c97db725f36fdd00d653d7` | [Recorded report](https://github.com/webkaz-labs/sobalink/pull/7#issuecomment-5977603260): [observation 37183542583](https://github.com/webkaz-labs/sobalink/actions/runs/37183542583) tested the exact released Linux amd64 binary through 15-minute offline idle, ten normal restarts and forced idle termination/recovery. [CI 37183542586 attempt 2](https://github.com/webkaz-labs/sobalink/actions/runs/37183542586/attempts/2) passed four native targets, browser and manifest after an unchanged-code retry of one intermittent Windows frontend assertion. No active-transfer crash, packet capture, power-loss or later route acceptance |
| New route implementation | [Draft PR #8](https://github.com/webkaz-labs/sobalink/pull/8). Helper, native prototype and final integration evidence are separated [below](#route-recovery-gate). Later working-tree changes are not the tested prototype SHA |

## Recorded source evidence

Earlier alpha.1 observations remain scoped to their source: [published-binary resources](RESOURCE_MEASUREMENT.en.md) and [isolated relay traffic](RELAY_TRAFFIC_MEASUREMENT.en.md).

<details>
<summary>Historical source results (not the current release gate)</summary>

| Source/category | Result and boundary |
| --- | --- |
| Source `20dac52` | [Run 37104195732](https://github.com/webkaz-labs/sobalink/actions/runs/37104195732) completed with Linux x64/ARM64 and macOS ARM64 native jobs passing, while the Windows x64 and browser jobs failed. The browser suite passed 36 of 45 cases. This is a failed run overall; it establishes neither acceptance of later fixes nor signed-release or actual-device acceptance |
| Historical public `1027f04a` | [Run 37095653635](https://github.com/webkaz-labs/sobalink/actions/runs/37095653635): 23 formal Playwright cases executed, 20 passed and 3 failed. Two failures concern positional relay-input test locators; one exposed empty-string/default-folder autosave fallback. Later fixes require their own source-specific evidence. All four native target jobs (Linux x64/ARM64, macOS ARM64 and Windows x64), including race/vet, isolated relay fixtures and packages, plus the manifest job passed. The browser failure keeps this run from passing overall |
| Historical public `1c5c195` | [Run 37085369977](https://github.com/webkaz-labs/sobalink/actions/runs/37085369977): Linux x64/ARM64, macOS ARM64, Windows x64 and manifest passed. The older agent-script browser job failed before local login. This run is not fully green |
| Earlier `5dd6b8c99b8e0167924b38411889e3a0092ac4af` | [Run 37046723268](https://github.com/webkaz-labs/sobalink/actions/runs/37046723268) passed all six jobs, including that snapshot's Go-backed Chromium smoke. This does not verify later forms or restored workflows |
| Later service/capacity/profile changes | Local focused race tests cover logical choices, finite storage/flow budgets, lifetime and revocation, saved definitions, groups, leased tasks, rollback, offline metadata and reviewed deletion/import. Native CI must run on the final integrated source |
| Private sign-in CLI `7076dcb` | Local mocked and in-memory tests cover official URL validation, browser/link/QR modes, current-flow reuse, pending approval, waiting/cancellation and QR rendering/restoration. No real enrollment or phone scanning was performed |
| Restored UI snapshot `1c977c9` | 274 DOM tests across 14 files, typecheck/build and seven fixture-safety tests passed locally; two final production-asset builds had identical SHA digests. This includes path-policy consumers. DOM tests do not prove actual Core/browser flows, native IME, real authentication or actual-device behavior |
| Restored Web workflows `c9a30e7` / assets `04085f1` | 389 DOM tests, 8 capture-safety checks, typecheck and reproducible production builds passed locally. Covers client helpers, invocation lifetimes and refreshed discovery; new browser cases were authored and syntax-checked, not executed. Final Go-backed browser and native gates remain open |
| Later local Playwright Test suite | 28 production Go-process browser cases were enumerated for the later UI snapshot, not executed there. The 23-case execution above belongs to `1027f04a`; it does not establish acceptance of later UI or fixes |
| Proxy and diagnostics | Focused race tests, vet and Windows cross-compilation passed locally. Mock/in-memory transports exercise authenticated TCP-only scopes, current identities, teardown, redaction and explicit diagnostics. Native proxy sockets and actual applications remain unverified |
| Explicit startup/private profiles `c148f85` / `50d0f00` | Focused synthetic Core/CLI race tests and vet passed for frozen outbound selections, stale approval, offline suppression, private save/generate/reveal, durable revoke/failure handling, bounded storage and recovery without expiry renewal. This does not establish native installed ACLs, actual OS sign-in/suspend, enrollment or real proxy clients |
| Prerelease preparation `d74d815` | Prepared the manual exact-main CI gate, four native packages, repeatability, Packslip signature/provenance and four-target mise installation checks for candidate `0.3.0-alpha.1`. Workflow preparation alone is not evidence of publication or successful execution |

A failed server launch or pre-login browser failure is a failed gate, not acceptance of the screens that follow. Do not carry results across changed source without rerunning affected checks. Cross-compilation is not native execution. Native loopback fixtures do not establish actual-device, direct LAN/WAN/NAT, sleep/wake, OS sign-in, native IME or application compatibility.

The [feature parity matrix](FEATURE_PARITY.en.md) maps every fixed alpha.2 capability row to its current route and remaining gate. Later restored CLI `7263310` passed local synthetic workflow and Linux PTY checks; actual macOS/Windows terminal input, native IME and enrollment remain unverified. Synthetic hosted graph preview `d52a975` passed nine layout captures; those fixtures do not establish actual backend or Go-process browser behavior. New route changes require their own final integrated-source aggregate/native/browser checks and review.

</details>

## Native and browser acceptance

Retain all four native race/vet/package jobs, IPC/socket cleanup and language checks. Hosted Windows execution does not establish standard-user enrollment or installed startup behavior.

The browser gate must exercise the actual Go process and embedded production assets. Required flows include local code entry and expiry/reuse rejection; sessions and CSRF/Origin; private Tailnet sign-in and approval states; network setup and repair; peer selection; services, lifetimes, mapping and capacity review; saved definitions/groups; transfer and receive controls; stop/revoke; and optional advanced command flows where a dedicated form is absent.

Cover back/cancel, repeated actions, stale revisions, interrupted requests, network errors, keyboard/focus, Japanese/English, light/dark themes and narrow layouts. Check actual fonts/glyphs, graph/detail layout and screenshots from that build. QR pixels in a mocked terminal do not establish phone scanning. Browser composition events do not establish native IME acceptance.

Negative API tests must reject wrong Host/Origin, missing CSRF, foreign sessions, oversized requests and peer attempts to operate management. Codes, auth URLs, proxy credentials and pairing capabilities must not appear in routine state or distributed screenshots. A larger logical setting must work through Core, local IPC, Web request/response, CLI and browser consumers, subject to its separate finite resource budget.

## Real two-peer acceptance

Use generic fixtures and the necessary account/network permissions. Record source, versions, OS, backend, result and the first failing stage without publishing private identity state.

1. Start a fresh profile with no network selected, enroll a separate Tailnet node or explicitly pair through the chosen trusted relay, and verify current peer identities
2. Connect to SSH/SFTP, a web app and another chosen service; ordinary Tailnet targets need no sobalink. Check application authentication, TLS/host keys and an actual operation
3. Share narrow TCP/UDP scopes with explicit peers. Check same-port ranges, single-port mapping, exclusions, internal ports, unauthorized peers, later-started applications, finite expiry and explicit until-revoked
4. Verify until-stopped outbound connections, listener/flow capacity, lower admission settings, collisions, stop-shares, individual stop, trust revocation and cleanup without silent remapping or renewed permission
5. Save/import stopped definitions while offline, review groups, test partial-start rollback, run a local task, cancel it and verify owned cleanup/lease expiry without disrupting unrelated work
6. Trust a precise sender; send text explicitly and copy it manually. Check reviewed message-history cleanup, full storage and acknowledged-delivery/local-save errors
7. Offer an image, several files and a folder together. Decline one batch, accept another into an explicit destination, then compare sizes, hashes and empty-folder behavior
8. Test invalid paths/links, name collisions, custom/unlimited logical choices with finite metadata/spool budgets, staging cancellation/timeouts, storage errors and no overwrite/open/execute
9. Enable autosave for one exact peer/destination; check pause, disable, identity replacement and renewed trust without silently reviving the old grant
10. Interrupt a file and retry from its beginning. Verify saved-file acknowledgements; after restart require a new batch and report possible unique-name copies. There is no durable offline outbox
11. Test the authenticated optional SOCKS5 listener with allowed and denied TCP targets, credentials, reserved endpoints, identity changes and stop; verify no BIND, UDP or OS dial/DNS fallback. Separately test private save/generate/reveal, revision conflicts, disable/delete and durable revoke; verify no credentials in ordinary output or portable exports
12. Inspect diagnostics before/after a one-target TCP probe. Preserve runtime last-failure information while keeping application health unverified
13. Test direct/relay telemetry where observed, network interruption, sleep/wake and optional per-user OS sign-in startup. Keep unknown paths unknown and verify application reconnection separately. Test separately approved outbound/proxy launch, changed-scope refusal, offline suppression, finite lifetime and explicit stop without revival; inbound shares must stay stopped

Transport readiness, discovered metadata and local file staging are not application success or remote delivery. Stopping transport does not recall data or cancel remote jobs.

## Tailcat gate

The implementation covers explicit relay selection, distinct server/client role keys bound to paired identity, sealed bootstrap admission, atomic secret-state persistence before acknowledgement, UDP multiplexing and durable revocation. [LAN commands and recovery](LAN.en.md) keep pairing separate from application trust, autosave and service grants. Paired-peer admission follows the selected logical choice; private LAN state has a separate finite storage budget and existing pairs survive lower count choices.

Dependencies pin Tailscale `v1.104.0` and Tailcat `v0.7.1-0.20260929145319-b4dc28e8aa89`. The recorded alpha.2 native fixture used real stock Tailcat/WireGuard with a TLS-pinned loopback DERP relay. Recorded four-target CI covers invalid invitations/unknown keys, bidirectional TCP/UDP, 130 seconds of existing TCP across an actual two-minute relay lease with re-admission, active revoke and cleanup, plus Core two-peer messages/files/shares. It omits UDP underlay and uses no external relay.

This fixture does not establish real direct UDP, LAN/WAN/NAT migration, cross-relay migration, throughput or general TCP continuity. Production permits direct peer traffic and encrypted payload plus HTTPS/ICMP diagnostics to the explicit trusted relay endpoint. Loopback-only fixture restrictions are not a zero-egress promise.

Verify no default public relay map/DNS bootstrap, required omission tags and rejected mismatched capabilities or unsupported proxy/backend overrides. Discovery must rotate bounded passes through large peer sets; graph edges and rates must come from actual evidence.

Application revoke closes its authorization and tracked flows immediately. The embedded relay's two-minute lease is a different boundary: an admitted relay session can remain until expiry, or about four minutes from initial bootstrap when temporary admission overlaps a lease. Verify both application closure and later relay re-admission rejection.

Actual devices still need explicit setup/identity verification, consented service/file operations, canceled-send reselection after pause, offline repair, certificate expiry, interrupted/uncertain pairing, durable revocation and network recovery. No arbitrary public fallback or automatic backend exchange is allowed.

## Route recovery gate

The new source includes the adapted transport, authenticated offers, durable per-pair route state, independent explicit-lifetime approvals, Core/CLI and local UI. Implementation is not integrated acceptance. [Contract](ROUTE_RECOVERY_DESIGN.en.md) · [Commands](LAN.en.md#prepare-another-route-unreleased)

| Evidence tier | Current result and boundary |
| --- | --- |
| Pure helpers / synthetic state and coordinator tests | Earlier finite-only checks covered candidate validation, pair binding, tamper/replay/expiry, exact review, persistence faults, cancellation and bounded attempts. Historical focused CLI/Core passes do not verify the new explicit-lifetime v2 changes or two independent application processes |
| Initial native route prototype, `967fa58` | [Run 37218261408](https://github.com/webkaz-labs/sobalink/actions/runs/37218261408) failed cross-relay recovery by timeout. Its passing baseline browser and legacy relay tests do not make the run pass |
| Corrected native prototype, `52b72b22868fe52261118675ba46555ea02cc756` | [Run 37219113173](https://github.com/webkaz-labs/sobalink/actions/runs/37219113173) completed successfully on 2026-10-04. This is native prototype evidence for that exact source, not the later full Core/CLI/UI/route-state integration |
| Earlier integrated snapshot, `10e836cadf827cb9bf433c8faacd5f5198c04127` | [Run 37250381716](https://github.com/webkaz-labs/sobalink/actions/runs/37250381716) failed overall: four native jobs and manifest passed; the actual Go-backed browser suite had 67 passes and two timed-out route-candidate selector cases out of 69. A local selector fix needs its own CI. This snapshot does not include the later explicit-lifetime redesign or new two-Core-process fixture |
| Explicit-lifetime integration `15d878eaf78ffab13875e3282fc23c71d7a575cf` | [CI 37252690553](https://github.com/webkaz-labs/sobalink/actions/runs/37252690553) passed all six jobs: four native targets, 69/69 Go-backed Playwright cases and manifest. The [two-Core-process fixture](../internal/core/route_process_integration_test.go) ran in UDP-omitted and ordinary direct-enabled builds on all four targets: protected pair/route-state restart, LAN-local startup with the alternative unavailable, real echo traffic through the same localhost service entry, primary relay removal and local revocation. Direct UDP may remain healthy after a relay disappears; this is not proof of physical WAN migration. Subsequent stronger exact-grant/service-identity assertions require their own run |
| Final application integration | Still requires exact-source two-process/socket proof of prepared-LAN cold start with external services unavailable, same-pair route loss/recovery, stable local application entrance, revoke/expiry, migration and cleanup; then four native targets and actual Go-backed route browser flows |
| Physical devices | LAN/WAN/NAT changes, physical offline-LAN cold start, real application authentication/reconnect and OS suspend/wake are unperformed |

The product remains a normal direct-enabled single binary. An isolated no-UDP fixture is narrower evidence and cannot verify normal direct transport or a strict-egress product. Native acceptance must exercise the actual product configuration as well as controlled relay fixtures. No strict LAN/no-external-egress mode is included. Local relay classification does not prevent public direct peer paths.

Before a clearly labeled prerelease, pass the integrated automated gates, authentication/permission and privacy review, final-source native/browser checks and signed installed-binary distribution checks. Physical-device acceptance may remain explicitly pending so that users can install the prerelease through mise for those tests. Do not advertise established-TCP preservation, arbitrary request replay or byte-offset/restart file resume. Listener readiness, authenticated path evidence, application success and remote-job completion remain distinct.

## Release boundary

[Distribution](DISTRIBUTION.md) requires successful CI for the exact main commit selected for publication, real-browser acceptance, repeatable packages, signatures/provenance, independent public download and actual installed-binary checks on four native targets. Check [sobalink Releases](https://github.com/webkaz-labs/sobalink/releases) for a selected version's assets, source and linked workflow results. A prepared workflow, draft PR, conditional installation example or candidate version is not evidence of a release.

Historical [tsnet-bridge releases](https://github.com/webkaz-labs/tsnet-bridge/releases) retain their original executable, configuration and signature identities. Their installation and application-specific results do not verify this product.
