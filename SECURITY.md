# Security

This is an experimental pre-release tool. No production-security or RustDesk compatibility claim is made.

## Intended boundary

- Outward forwarding and SOCKS listeners bind numeric loopback only
- Explicit v2 shares bind only this embedded node’s tailnet address; their local targets are exactly 127.0.0.1 or ::1
- Sharing requires pinned source peers and TTL, in addition to tailnet policy; localhost-only trust must not replace application authentication
- SOCKS requires random username/password and accepts TCP CONNECT only
- Fixed forwarding has no additional transport password: the application protocol cannot carry it
- Starts are bound to the exact reviewed rule definitions; concurrent changes require a fresh review
- Stop, expiry and observed peer-identity revocation cancel existing flows; process restart restores no v2 grants
- Destinations must be explicitly configured, current tailnet peers and permitted ports
- The identity adapter dials only the userspace netstack; it never falls back to OS DNS/routing
- OS-backed locking prevents concurrent use of one state directory
- IPC is private to the current user; Windows also checks the connected server process identity
- Unix directories/files use 0700/0600; Windows uses protected current-user DACLs

State and SOCKS passwords are not encrypted at rest. Same-user processes and privileged users are outside the isolation boundary. Use a private profile directory on a local filesystem, narrowly scoped tailnet policy, and application authentication. Network-mounted or shared state directories are unsupported. A user who can alter the profile and issue control commands can authorize a different peer. Task owner IDs partition cleanup only; they are not authentication credentials.

## Optional peer-scoped service metadata

The current source adds a read-only discovery endpoint; it is not part of the published `0.2.0-alpha.1` binary. A missing `discoverable` field means false, including existing profiles. New interactive shares preview discovery scope before confirmation, `--no-discovery` disables it, and existing `--confirm` scripts keep it disabled unless `--discoverable` is explicit. Saving/importing a discoverable rule does not start advertising it. Allowed recipients remain an explicit current-peer selection.

TCP 54543 at `/.well-known/tsnet-bridge/services/v1` listens only on the embedded node's current tailnet address while an opted-in share is active. It is not a public/LAN listener, local-control API or general proxy. Every request checks Tailscale `WhoIs` for the real socket source, current stable ID/source-IP mapping, each grant's start-time pins, allowed caller and unexpired lifetime. Headers are not identity evidence. A valid tailnet caller can receive only its own permitted current shares; no service entries are returned for other callers. The fixed endpoint's existence and an empty response may still reveal that a bridge discovery endpoint is running to a peer permitted by tailnet policy.

Only protocol version, opaque service ID, fixed purpose, TCP/UDP network, shared port, expiry and `application: unverified` leave the provider. Rule names, local target address/port, owner, path, allowed-peer list and free-form context do not. Service metadata remains information shared with approved recipients, not a secret-storage mechanism. Do not opt in if those recipients should not learn that metadata.

Discovery permission and service-port permission are separate. Tailnet ACLs must allow the chosen caller to reach TCP 54543 for discovery and independently allow its selected application port/protocol. The bridge never changes tailnet ACLs to make discovery work. A blocked or unsupported endpoint preserves manual connection to a known service, subject to the same service authorization.

Reads are bounded to 128 current peers, four concurrent requests, two seconds per peer and eight seconds overall, without application-port scanning, a central registry or disk cache. Metadata observations expire locally after at most 15 seconds and are rechecked before saving/starting. HTTP admission, sizes, counts and timeouts are bounded; redirects and connection reuse are disabled. All client traffic stays identity-pinned and netstack-only. These bounds are not denial-of-service immunity. A confirmed observation proves neither application health nor completion of a remote action.

## Authentication and diagnostics

Enrollment is interactive through the official Tailscale control plane. The program does not accept auth keys, OAuth client secrets, or workload tokens from flags/profile/environment. The CLI retrieves private sign-in URLs over protected IPC and displays them only for the explicit login command, never in status or startup stdout/stderr. Before tsnet construction, the process-wide upstream logtail kill switch disables new buffer writes, including automatic auth-URL messages. Old files from earlier versions may remain and must stay private. The browser opener and terminal QR accept only HTTPS authorization paths at login.tailscale.com. QR is generated in memory only for an explicit login command and refuses file/pipe redirection; it is as sensitive as the authorization URL. Terminal scrollback/screenshots can still retain it. Neither local wait timeout nor Ctrl+C is claimed to revoke the server link.

Application analytics are absent. The adapter sets Tailscale's `TS_NO_LOGS_NO_SUPPORT` knob before starting tsnet to disable upstream diagnostic upload; ordinary Tailscale control, coordination, relay, DNS/bootstrap, and connection traffic still occurs. User/backend log callbacks suppress console output. The upstream logtail kill switch also prevents new buffered diagnostic entries; upstream may still create empty buffer/configuration files. Existing private files and identity state remain sensitive and are not automatically deleted. This is not an offline tool and does not promise to hide node metadata from the tailnet administrator or Tailscale service.

Do not publish state, profile data, private login URLs, SOCKS credentials, or startup logs without inspecting them. A future diagnostic bundle must redact and provide a preview; no automatic upload mechanism is included.

## Resource bounds and lifetimes

TCP/SOCKS admission is bounded at 128 connections per listener and 512 process-wide. UDP allows at most 256 mappings per rule and 512 process-wide. Queued UDP payload is limited to 1 MiB per rule and 16 MiB process-wide, in addition to the 64-packet per-source queue. Excess traffic is dropped; these controls are not bandwidth fairness or denial-of-service immunity. Read buffers, upstream stack memory and fixed metadata are additional overhead.

Sharing expires after at most 24 hours. Independent grant cancellation and per-I/O guards use elapsed-time and wall-clock checks; exact physical suspend scheduling is not guaranteed. Repeating an identical start retains the original expiry; changing an active TTL/lease requires stop and a reviewed restart. A task’s abandoned lease expires without relying on caller cleanup. TTL closes transport only, never guarantees remote-job cancellation.

## Limitations

Revocation depends on current control-plane knowledge. UDP validates before forwarding datagrams, and health checks close stale active flows; already transmitted bytes cannot be recalled. Tailscale ACL enforcement remains authoritative. Fixed forwarding distorts application-visible source/NAT information, and its RustDesk interoperability has not passed real end-to-end acceptance.

A successful build or mocked test is not evidence of standard-user Windows enrollment, OS-level sandbox behavior, or signed/notarized distribution. Unsigned development binaries may trigger platform warnings. Do not bypass an OS security warning merely because CI passed.

## Reporting

Report reproducible issues through the repository's security reporting feature when available. Otherwise open a minimal issue asking for a private reporting channel, without secrets, identity state, passwords, or exploitable sensitive details. Do not post sensitive diagnostics publicly.

## Dependency changes

Go and Tailscale are pinned. The netstack-only adapter relies on an unstable upstream API; review it and rerun policy, lifecycle, and native tests before upgrading. CI never enrolls a node or uses real tailnet secrets. Ordinary CI does not publish releases. The separately dispatched prerelease workflow can publish explicitly requested testing releases, using short-lived GitHub OIDC signing and genuine build attestations without a persistent signing key. Packslip signatures do not replace OS code signing or real application acceptance.
