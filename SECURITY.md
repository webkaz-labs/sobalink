# Security

This is an experimental pre-release tool. No production-security or RustDesk compatibility claim is made.

## Intended boundary

- Application listeners bind loopback only, never LAN or tailnet interfaces
- SOCKS requires random username/password and accepts TCP CONNECT only
- Fixed forwarding has no additional transport password: the application protocol cannot carry it
- Destinations must be explicitly configured, current tailnet peers and permitted ports
- The identity adapter dials only the userspace netstack; it never falls back to OS DNS/routing
- OS-backed locking prevents concurrent use of one state directory
- IPC is private to the current user; Windows also checks the connected server process identity
- Unix directories/files use 0700/0600; Windows uses protected current-user DACLs

State and SOCKS passwords are not encrypted at rest. Same-user processes and privileged users are outside the isolation boundary. Use a private profile directory on a local filesystem, narrowly scoped tailnet policy, and application authentication. Network-mounted or shared state directories are unsupported. A user who can alter the profile can authorize a different peer.

## Authentication and diagnostics

Enrollment is interactive through the official Tailscale control plane. The program does not accept auth keys, OAuth client secrets, or workload tokens from flags/profile/environment. The CLI retrieves private sign-in URLs over protected IPC and displays them only for the explicit login command, never in status or startup stdout/stderr. The pinned upstream tsnet logger can still buffer those URLs inside the private identity directory. The browser opener accepts only HTTPS authorization paths at login.tailscale.com.

Application analytics are absent. The adapter sets Tailscale's `TS_NO_LOGS_NO_SUPPORT` knob before starting tsnet to disable upstream diagnostic upload; ordinary Tailscale control, coordination, relay, DNS/bootstrap, and connection traffic still occurs. User/backend log callbacks suppress console output. They do not disable upstream private filch/logtail buffers, which must be treated as sensitive identity material. This is not an offline tool and does not promise to hide node metadata from the tailnet administrator or Tailscale service.

Do not publish state, profile data, private login URLs, SOCKS credentials, or startup logs without inspecting them. A future diagnostic bundle must redact and provide a preview; no automatic upload mechanism is included.

## Limitations

Revocation depends on current control-plane knowledge. UDP validates before forwarding datagrams, and health checks close stale active flows; already transmitted bytes cannot be recalled. Tailscale ACL enforcement remains authoritative. Fixed forwarding distorts application-visible source/NAT information, and its RustDesk interoperability has not passed real end-to-end acceptance.

A successful build or mocked test is not evidence of standard-user Windows enrollment, OS-level sandbox behavior, or signed/notarized distribution. Unsigned development binaries may trigger platform warnings. Do not bypass an OS security warning merely because CI passed.

## Reporting

Report reproducible issues through the repository's security reporting feature when available. Otherwise open a minimal issue asking for a private reporting channel, without secrets, identity state, passwords, or exploitable sensitive details. Do not post sensitive diagnostics publicly.

## Dependency changes

Go and Tailscale are pinned. The netstack-only adapter relies on an unstable upstream API; review it and rerun policy, lifecycle, and native tests before upgrading. CI never enrolls a node or uses real tailnet secrets. Ordinary CI does not publish releases. The separately dispatched prerelease workflow can publish explicitly requested testing releases, using short-lived GitHub OIDC signing and genuine build attestations without a persistent signing key. Packslip signatures do not replace OS code signing or real application acceptance.
