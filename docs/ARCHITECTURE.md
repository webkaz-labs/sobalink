# Architecture and compatibility

## Scope

The same Go CLI runs as an unprivileged foreground or detached process on Linux, macOS, and Windows. It embeds a separate Tailscale identity. It never configures an OS TUN, global proxy, OS DNS, subnet router, exit node, or system-wide startup service. Optional user-level registration starts only an idle v2 node; `run --idle` rejects legacy automatic-forwarding profiles.

Legacy forwarding modes share a small policy layer:

- Fixed profile: TCP ID-port minus one, TCP/UDP ID-port, TCP relay-port
- SOCKS profile: authenticated SOCKS5 CONNECT only, restricted to the configured TCP peers/ports

Outward and legacy application listeners bind IPv4 loopback. Restricted inbound listeners bind the embedded node’s explicit tailnet address. Control commands (64 KiB request / 256 KiB response bounds, 16 concurrent handlers) use a 0600 Unix socket inside a 0700 directory, or a Windows current-user-only named pipe. The Windows client verifies the server process user before sending commands, preventing another user from impersonating a server by pre-creating its predictable name. Local control is not an HTTP endpoint.

## Named rules and restricted inbound connections

Version 2 persists named TCP/UDP rules and groups. Every save/import disables rules; startup and restart never reconstruct runtime grants. A start request carries a digest of the exact reviewed rule definitions; the daemon compares the current atomic selection before changing state. A group start is transactional for newly started members. Other owners' active listeners are preserved. Peer IDs are pinned in saved forward rules and inbound source lists, preventing same-name replacement from retargeting a connection.

Forward listeners stay numeric IPv4 loopback; remote service ports span 1..65535 independently from unprivileged local ports 1024..65535. Inbound listeners use only `tsnet.Server.Listen` / `ListenPacket` on an explicitly current self tailnet address. Local targets are exactly `127.0.0.1` or `::1`. The OS dialer is used only for that explicitly selected loopback service, never for outward tailnet traffic.

Incoming source addresses are checked against current, nonexpired peer IDs and their start-time numeric mapping. Even an address reassigned to another allowed peer cannot inherit the previous stream/datagram mapping. TCP authorization is checked around data I/O and by a periodic watcher; UDP checks both directions. Listener/flow shutdown and application authentication warnings accompany sharing. Limits are 128 streams per listener / 512 process-wide, 256 UDP source mappings per rule / 512 process-wide, and 64 queued packets per source. UDP queued payload has additional 1 MiB per-rule / 16 MiB process-wide budgets, reserved before copying and released on send, error or close. Read buffers and upstream stack overhead are separate from queue budgets.

Every share requires a TTL of at most 24 hours. Grant guards check both monotonic elapsed time and wall-clock expiry before data forwarding. An independent grant canceler closes existing streams and mappings even while the manager is busy; per-I/O guards reject post-expiry traffic. A daemon pass updates observed status. Restart cannot revive a grant. Identical repeated starts retain the original expiry; changed TTL/lease values require explicit stop and a reviewed restart. A task has its own cleanup owner and renewable short lease. `task` renews a 30-second lease every 10 seconds and stops only its rules on command exit; absent renewals expire. This is not an OS security boundary or remote-job cancellation.

A common connection outage closes listeners while preserving the original grant within its lifetime. Recovery checks the same pinned identities. Observed identity disappearance/change latches a terminal failure requiring explicit restart. Per-rule state/JSON distinguishes listener readiness and TCP diagnostic reachability from unverified application behavior. No application request is replayed.

## Login presentation

The explicit login command supports local browser launch, a private copyable URL, or locally generated terminal QR for a trusted phone. Only the official HTTPS login.tailscale.com authorization URL is accepted. QR generation uses the same pinned skip2/go-qrcode version as Tailscale, with no remote image service or saved QR file. Redirected QR output is rejected. Login completion and machine approval remain distinct; local wait timeout is not advertised as server-side link expiry. The process-wide upstream logtail kill switch runs before tsnet construction to prevent auth URLs entering new disk-buffered diagnostics, in addition to disabling upload and quiet callbacks. Existing buffers are retained privately rather than automatically erased. No alternative authentication credentials or automatic approval are introduced.

## Identity and fail-closed policy

A generated generic node name avoids automatically copying OS account or host names. The profile, SOCKS credentials, tsnet state, and startup log have separate files. Unix flock / Windows LockFileEx prevents simultaneous owners. Failed state reads preserve the state; there is no automatic delete/reset or repeated identity creation.

Peer names are resolved exclusively from the current tsnet peer snapshot. Short names must be unambiguous; FQDNs and numeric tailnet addresses are accepted. A request must map to a configured peer, network, and port. Public/LAN/loopback destinations, the Tailscale DNS service address, arbitrary DNS, and subnet routes are rejected.

The pinned adapter calls only tsnet's initialized netstack TCP/UDP functions. It deliberately does not call `Server.Dial` / `UserDial`: their normal routing logic can select the system network when a peer disappears between validation and dialing. The adapter uses the explicitly unstable `Server.Sys()` API, isolated under `internal/identity`; upgrades require regression tests and a source review.

Active flows pin the peer identity and numeric endpoint. UDP validates that identity before sending and before delivering datagrams. Health checks revalidate existing TCP/UDP flows and close changed/revoked identities. This complements Tailscale ACL enforcement; control-plane propagation and existing-session behavior are not instantaneous guarantees.

## UDP forwarding

A mapping is keyed by the local application's UDP source socket. It maintains one connected upstream UDP socket until idle expiry, error, revocation, or shutdown. A dedicated receive loop forwards replies and later asynchronous server messages to the same source. Bounded mapping counts and queues limit resource use. Production idle timeout defaults to five minutes; regular registration traffic keeps mappings alive. A new local source address/port receives a new mapping. If a restarted application reuses the same source tuple, it may reuse the existing mapping until expiry or shutdown.

Fixed forwarding cannot add SOCKS authentication without changing the application protocol. Other local processes can reach loopback ports. Restrict tailnet policy and require application authentication.

## Startup and recovery

Setup validates a strict versioned profile, checks fixed local ports, and atomically saves it without networking. Start acquires the OS lock, exposes protected IPC, starts tsnet, waits for authentication and peer visibility, checks allowed TCP ports, then binds forwarding listeners. Partial listener startup rolls back all listeners.

Health checks distinguish login required, machine approval, peer policy, server reachability, and bind failures. Forwarding closes when health fails. Checks retry with increasing intervals, bounded jitter, and a 30-second cap, without deleting login state. Extended sleep/network-change measurements remain follow-up work.

Stop retains login. Reconnect recreates forwarding using the existing node and login state; it does not claim to restart tsnet itself. Logout closes forwarding first and calls the upstream logout API. Failure reports local stop with server-side logout unconfirmed. Node deletion is separate.

IPC shutdown stops admission, gives an in-flight response a 200 ms drain window,
then cancels handlers and closes connections outside the registry lock. Native
listener/connection closure runs concurrently, within a five-second total wait
budget. An incomplete join returns `ErrShutdownTimeout` with the pending stages;
cleanup may continue in the background, and the service exit propagates the error.
This is an IPC wait policy, not a real-time OS scheduling guarantee or a bound on
upstream tsnet shutdown. Virtual-time tests check exact policy boundaries; real
named-pipe/Unix-socket tests use readiness barriers and diagnostic watchdogs.

The Windows listener also handles go-winio 0.6.2's lost-close-notification path:
an unexpected accept error during shutdown triggers exactly one additional close
notification. Accept errors before shutdown and the upstream's exact closed
sentinel do not trigger recovery. The original error remains observable and the same total
shutdown budget still applies; protected pipe permissions are unchanged.

The CLI never treats TCP reachability as a successful RustDesk session. Status has separate `rustdesk: unverified`. There is no claimed direct/DERP status without a measured per-peer observation.

## RustDesk proof requirements

RustDesk 1.4.9 proxy mode switches endpoint registration to TCP. OSS server 1.1.16 returns `NOT_SUPPORT` for TCP `RegisterPk`. Other TCP messages are supported; the incompatibility is registration, not every TCP operation.

The experimental fixed profile keeps normal UDP registration by leaving the application's proxy blank. ID port P forwards TCP/UDP to hbbs:21116, P-1 forwards TCP to hbbs:21115, and relay port R forwards TCP to hbbr:21117.

A relay address is sent to the opposite peer, so all participating endpoints must use the same loopback R. Arbitrarily changing each endpoint's port or mixing ordinary tailnet and loopback profiles cannot be assumed compatible. hbbs may rewrite relay addresses. `/r` selects relay mode but does not establish zero direct probes, correct NAT classification, or successful remote control.

Required real proof: distinct tsnet identities, cold registration, idle registration, both control directions, screen/input, relay-address propagation, direct/DERP conditions, process restart, application restart, and port-conflict recovery. Local fake tests do not satisfy this gate.

## Primary sources

- [tsnet overview](https://tailscale.com/docs/features/tsnet)
- [Tailscale 1.102.5 release](https://github.com/tailscale/tailscale/releases/tag/v1.102.5)
- [Pinned netstack selection and dials](https://github.com/tailscale/tailscale/blob/v1.102.5/tsnet/tsnet.go)
- [UserDial routing and system fallback](https://github.com/tailscale/tailscale/blob/v1.102.5/net/tsdial/tsdial.go#L587-L616)
- [RustDesk 1.4.9 TCP registration](https://github.com/rustdesk/rustdesk/blob/1.4.9/src/rendezvous_mediator.rs#L481-L492)
- [OSS server TCP RegisterPk unsupported](https://github.com/rustdesk/rustdesk-server/blob/73523b31cfd25d77dee862e6fc9f5e1fb5e485ef/src/rendezvous_server.rs#L548-L555)
- [UDP registration address refresh](https://github.com/rustdesk/rustdesk-server/blob/1.1.16/src/rendezvous_server.rs#L564-L581)
- [Relay address selection](https://github.com/rustdesk/rustdesk/blob/1.4.9/src/rendezvous_mediator.rs#L825-L833)
- [Received relay address handling](https://github.com/rustdesk/rustdesk/blob/1.4.9/src/client.rs#L536-L560)
- [Server relay-address rewrite](https://github.com/rustdesk/rustdesk-server/blob/1.1.16/src/rendezvous_server.rs#L507-L525)
- [Force-relay FAQ](https://github.com/rustdesk/rustdesk/wiki/FAQ#force-relay)
- [Windows tsnet non-admin issue](https://github.com/tailscale/tailscale/issues/20031)
