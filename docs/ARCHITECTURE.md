# Architecture and compatibility

## Scope

The same Go CLI runs as an unprivileged foreground or detached process on Linux, macOS, and Windows. It embeds a separate Tailscale identity. It never configures an OS TUN, global proxy, OS DNS, subnet router, exit node, or automatic startup service.

Two forwarding modes share a small policy layer:

- Fixed profile: TCP ID-port minus one, TCP/UDP ID-port, TCP relay-port
- SOCKS profile: authenticated SOCKS5 CONNECT only, restricted to the configured TCP peers/ports

All exposed application listeners bind IPv4 loopback. Control commands use a 0600 Unix socket inside a 0700 directory, or a Windows current-user-only named pipe. The Windows client verifies the server process user before sending commands, preventing another user from impersonating a server by pre-creating its predictable name. Local control is not an HTTP endpoint.

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
