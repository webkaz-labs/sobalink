# Experimental LAN write boundaries

[日本語](README.ja.md) · [Relay setup experiment](../relay-setup/README.en.md)

This isolated research fixture tests selected send boundaries in a patched copy of
`tailscale.com v1.104.0`. It is **not a product feature**, physical-LAN/VPN containment,
or whole-process zero-egress proof. Application dependencies and releases are unchanged.
The allowlisted CI artifact records which platform actually passed; this document is
not a pass record. Earlier Linux-only UDP evidence remains in PR #9's history.

## What is exercised

- UDP single, batch, disco transport, netcheck send and lazy-endpoint cookie-style
  entrypoints; fast batching/fallback, missing policy and retry-time revocation
- IPv4 and IPv6 fake writers: selected private/ULA destinations, outside prefix,
  global/documentation addresses, mapped IPv4, zones, missing policy and wrong ports
- Real native IPv4 and IPv6 loopback UDP: seven allowed datagrams, five denied
  entrypoints and one revocation **per address family**. Separate ephemeral receiver
  ports on the same loopback address avoid configuring loopback aliases. Cross-address
  rejection remains fake-writer coverage; old Linux evidence used separate addresses.
- DERP URL and region TCP paths: exact numeric selected relay, explicit family checks,
  hostname/DNS fallback refusal, proxy refusal, custom URL-dialer bypass prevention,
  and revocation before a subsequent dial. Native loopback TCP accepts four selected
  connections, rejects four alternate-port attempts, and checks two revocations
  across IPv4/IPv6. This is TCP connect evidence, not a TLS or DERP handshake.
- Netcheck HTTPS, HTTP-only, ICMP entrypoints and standalone UDP initialization are
  disabled before client/resolver/pinger/socket setup. DNS fallback rejects hostnames
  for cached and uncached cases. Numeric STUN addresses still require the guarded
  magicsock sender. Linux raw-discovery startup is disabled before raw socket creation.
- Guard-removal negative controls must fail both UDP and TCP fake-boundary assertions.
  Negative controls cannot use real sockets. No ICMP packets or raw sockets are used.

The workflow runs Linux x64/ARM64, macOS ARM64 and Windows x64 with the race detector.
A skipped or missing required native case is a failure. IPv6 unavailability is reported
as failure, never silently interpreted as IPv6 success. There are no namespace, route,
firewall, capability, startup, account or host-security changes.

## Isolation and reproduction

`prepare_engine.py` checks the SHA-256 of each modified upstream source, copies the
full pinned module to a new directory, and injects bounded experiment hooks. It removes
upstream tests from the three tested packages and adds the fixtures. All their production
code and real dependencies compile; this is not extracted source. The complete upstream
regression suite is not run. Hooks are not wired to production constructors: they are a
feasibility experiment, not a shippable enforcement API. TCP revocation here prevents
new dials only: it does not close previously admitted TCP streams. Policy replacement,
existing-connection shutdown and route-change synchronization still need a unified lifecycle.

Use Go 1.27.1 and Python 3:

```sh
go test -race ./...
go vet ./...
python -m unittest -v test_tools.py
go mod download tailscale.com@v1.104.0
python prepare_engine.py --source "$(go env GOMODCACHE)/tailscale.com@v1.104.0" --destination /tmp/lan-engine-experiment
cd /tmp/lan-engine-experiment
go test -race -run '^TestLAN(Remaining.*(Fake|Disabled|Denied)|ExperimentFake)' ./wgengine/magicsock ./derp/derphttp ./net/netcheck
```

Native socket tests require `LAN_GUARD_REAL_SOCKETS=1`; use the hosted CI workflow for
the reviewed native run. `run_native.py` consumes three compiled test binaries and emits
only hashes, synthetic test names, fixed platform labels and aggregate outcomes. No host
inventory, actual network endpoints, credentials, user settings or raw output is uploaded.

## Remaining-path source audit

The relevant origin boundaries in the pinned engine are:

| Path | Boundary and experiment treatment |
| --- | --- |
| WireGuard/disco/STUN/cookie UDP | `wgengine/magicsock/rebinding_conn.go` single and batch retry loops guarded; `magicsock.go` binds netcheck SendPacket to that writer |
| DERP TCP URL | `derp/derphttp/derphttp_client.go:dialURL`; exact numeric dial replaces DNS cache/default/custom-dialer path in strict experiment |
| DERP region IPv4/IPv6 | `dialNode`/`dialContext`; each attempted address passes the exact relay boundary |
| HTTP CONNECT proxy | `dialNodeUsingProxy` rejected before proxy socket or TLS; production build already omits proxy feature for trusted-relay mode |
| Netcheck HTTPS and HTTP-only | `net/netcheck/netcheck.go:measureHTTPSLatency`/`runHTTPOnlyChecks` disabled |
| Netcheck ICMP | Both aggregate setup and individual measure entrypoints disabled before Pinger creation/use |
| Netcheck DNS and standalone UDP | `nodeAddrPort` refuses hostname fallback; `Standalone` disabled, so it cannot substitute an independent writer |
| Raw discovery self-test | Linux `listenRawDisco` disabled before AF_PACKET setup and its separate loopback UDP self-test |
| Port mapping / captive portal | Trusted-relay product build requires omit tags via `ValidateBuild`; this fixture is not their runtime proof |
| Log upload | Application `lanlink.New` disables logtail; this fixture does not establish whole-process suppression |
| Application bootstrap, overlay requests, loopback service bridges, local management | See the separate [application-origin inventory](../relay-setup/README.en.md#application-origin-socket-inventory) and bootstrap fixture |
| Separate tailnet backend | Not automatically selected by LAN operation; outside this underlay fixture |

An inventory is not a completeness proof of all dependencies, dynamic callbacks or future
upstream changes. A shipping design must wire a unified immutable policy into every
constructor, audit dependency updates and observe the complete process, not only these
selected receivers. ICMP/HTTPS denial tests establish an early-return branch, not packet
capture evidence of every possible diagnostic origin.

## Interface, VPN and real-device acceptance gates

CIDR membership and numeric address identity cannot establish the outgoing interface.
The socket-free model explicitly demonstrates that the same address can still satisfy a
prefix while being routed over a VPN. The current experimental sockets are not bound to
an approved NIC, and a route lookup alone has a check/use race. IPv6 ULA does not mean
same-link; link-local scope IDs require a separate reviewed design and are rejected here.

Before claiming strict LAN containment, a disposable topology or explicitly approved test
devices must exercise: selected NIC versus another NIC; on-link versus routed prefix;
overlapping VPN routes; route replacement between admission and write; interface removal,
address renewal and policy revocation; IPv4/IPv6 parity; link-local zones; suspend/resume;
and intended relay unavailability. Observe each egress interface and the peer at the same
time with a synthetic payload, including negative receivers and a positive capture control.

The next real-device step requires explicit device/network/port selection and permission
to run temporary binaries and services. Packet-capture privileges, route/VPN/firewall changes
and login startup each need separate approval. No such changes are made by this PR. Keep
raw captures and real machine/network data private; publish only reviewed generic outcomes.
The [two-device relay checklist](../relay-setup/README.en.md#minimal-two-device-acceptance-not-yet-executed)
is useful operational acceptance, but does not prove guard integration or cross-NIC isolation.
