# Relay setup and operation experiment

[日本語](README.ja.md)

The smallest useful path is explicit **“use this device as the relay”**, selecting an existing local address and a high port, then privately pairing the other device. Production already has address listing, saved identity, pinned TLS, invitation inspection, and whole-application stop. This experiment adds tests, not automatic installation, host election, or release behavior.

## Evidence and limits

- The native lifecycle test creates only ephemeral loopback listeners. It checks occupied-port failure, mandatory authenticated bootstrap, cancelled start, reserved admission port, TLS readiness, stop, closed listener, and restart with the same identity. A changed address rejects the old certificate; explicitly replacing identity changes its pin.
- The bootstrap fixture exercises the production function through in-memory TLS: exact numeric destination, certificate pin rejection before sending proof, redirect rejection, and synthetic proxy variables having no effect. Authentication/cancel tests confirm provisional relay admission never grants application trust. The short lease/slot test is simulated.
- The existing opt-in integration test uses real stock Tailcat/WireGuard and loopback TLS DERP, bidirectional TCP/UDP, denied-key admission, revocation, clean stop, and ordered traffic over the real two-minute relay connection lease. Direct UDP underlay is compiled out for this test.
- Native runs belong in the dedicated, unprivileged hosted CI job on four platforms. A successful job proves that runner's loopback cases, not physical LAN reachability, sleeping hardware, OS login behavior, native installation, failover to another physical relay, or whole-process zero external egress. Read the allowlisted artifact for actual results; these documents are not a pass record.

## Smallest setup flow

1. Read available local interface/address choices. Never silently pick the first address: a private address can belong to a VPN or the wrong LAN. Existing enumeration keeps up/non-loopback private addresses and excludes link-local IPv6; it does not classify physical versus VPN NICs or bind later routing to a selected interface.
2. Explicitly choose the local relay address and high port. Save identity once. Repeating the same setup preserves it. Report saved configuration separately from a ready listener; if bind fails, retain a recoverable configured state. Offer another port rather than altering firewalls or disabling another program.
3. On the joining device, explicitly obtain its public identity. Issue a short-lived recipient-bound invitation, privately inspect the host, recipient, expiry, endpoint and pin, configure the inspected relay, and join. Application trust remains a separate approval. This experiment does not implement LAN invitation QR rendering; JSON/file/stdin remains the tested path. Treat any invitation JSON or future QR as a secret capability: never publish it or include it in logs.
4. Show configured/listening/reachable states separately. Stop the whole application to stop its embedded relay. Restart with saved identity, and let applications reconnect when a connection has been interrupted. A two-minute socket lease is a connection resource limit, not a host-election lease.
5. Offer login startup only as a separate opt-in with the selected OS action reviewed. Do not install remotely, copy credentials, change privileged firewall rules, or enable startup during this experiment.

## Address change, certificate renewal and failover

The saved host certificate is tied to a single IP and expires after one year. Reusing it at another IP fails. Existing selection changes are constrained when an engine or saved pairs exist; explicit replacement is not transparent renewal. A practical product design needs expiry warning, authenticated pin/address update, rollback, and a recovery path when the old endpoint is unreachable. Never weaken pin checks to hide this failure.

Authorized route candidates and reconnect behavior can be reused, but this experiment does not establish that a second host is provisioned or that both peers can meet on it. If the only relay sleeps or goes offline, report unavailability; do not automatically select an unapproved public relay. Election/remote placement additionally need explicit target management authorization, independent per-host private keys, authenticated membership, conflict resolution, and bounded withdrawal/health rules. They are a later feature.

## Minimal two-device acceptance (not yet executed)

Use two explicitly approved test devices on an approved isolated network, with no production credentials or data. Review the exact devices, selected NICs/addresses, ports and temporary synthetic service before changes. Firewall changes, startup installation, packet capture permissions and network changes require their own approval. Keep raw network captures and private addresses private; publish only synthetic/aggregate outcomes.

1. Start A in offline setup mode, inspect `soba lan addresses`, then use `soba setup --network lan --host ADDRESS:PORT`. Confirm the listener is ready and the chosen address belongs to the intended NIC. On B get `soba lan identity`; on A issue `soba lan invite --to PUBLIC_ID --ttl 5m`. Transfer the invitation privately.
2. On B run `soba lan inspect --json-file INVITATION_FILE`; compare the displayed peer and relay. Configure exactly that endpoint/pin with `soba setup --network lan --relay ADDRESS:PORT --certificate SHA256`, then `soba lan join --json-file INVITATION_FILE`. Approve the intended application identity/scope separately. Confirm expired, cancelled, wrong-recipient and wrong-pin invitations fail without replacing trust.
3. Share a temporary synthetic loopback TCP echo/file service with only B and a short expiry. Transfer a known payload and compare hashes. Repeat after 130 seconds; record connection survival versus reconnection separately. Confirm a port conflict yields a recoverable error and does not change unrelated settings.
4. Stop A, confirm B becomes unavailable without an unapproved fallback, restart A at the same address with saved state, and retry a fresh application connection. Then, only with approval, sleep/wake A and repeat. Measure time to detection/recovery; do not call an old TCP connection preserved unless observed.
5. With a separately approved address change or certificate rotation, confirm stale pin/address failure, then follow the explicit authorized update or re-pairing path. Preserve generic failure reasons and outcomes only. A two-device test cannot prove independent two-client failover while the sole relay host is asleep: that acceptance needs a third approved always-on relay or a separate fixture.
6. Stop both test applications, remove temporary shares/invitations, and confirm no listener remains. Report OS/architecture, synthetic case names, counts and timings, never hostnames, interface inventory, private endpoints, public identity codes, invitations or raw packet captures.

## Application-origin socket inventory

In addition to engine underlay sockets, maintain coverage of:

- `internal/lanlink/relay_bootstrap.go`: explicit numeric selected-relay TLS request, no proxy/DNS/redirect. Local relay has a selected TCP listener and private loopback admission HTTP connection.
- `internal/core/peers.go`: peer HTTP/file exchange uses the approved backend dialer and rejects redirects, with no proxy. `internal/discovery/discovery.go` sends its bounded HTTP request through the supplied policy dialer, not a default HTTP transport.
- `internal/transport/inbound.go`, `internal/ranges/engine.go`: authorized incoming connections bridge only to validated numeric loopback service targets. These are intentional OS loopback dials, not an underlay escape route.
- `internal/core/diagnostics.go`: forwarding checks use the backend; shared-service checks dial the approved numeric loopback service. `internal/app/rules.go` has legacy rule diagnostics, whose share target validation in `internal/config/rules.go` restricts targets to `127.0.0.1`/`::1`.
- `internal/control`, `internal/webui`, `internal/transport/{tcp,udp,socks}.go`: local IPC or loopback management/application listeners. Generic strict LAN policy must explicitly preserve these required local paths.
- `internal/identity/tsnet.go` is a distinct tailnet backend. Trusted-relay/LAN operation must not silently fall back to it. This inventory is source evidence, not a dynamic whole-process egress proof.
