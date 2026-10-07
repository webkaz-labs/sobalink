# Local relay setup and recovery

[日本語](RELAY_OPERATIONS.ja.md) · [User guide](GENERIC.en.md)

Choose this device's private address explicitly, start its relay, and privately pair the other devices. A saved configuration, a running listener, and successful peer/application traffic are different states. Native loopback tests do not establish physical LAN reachability, sleep/wake behavior, or application compatibility.

## LAN and outside-LAN use

Use Tailscale for the supported outside-LAN workflow. This setup hosts a relay only on a selected private or loopback address and an unprivileged TCP port. It supplies no standalone public-relay daemon or external deployment workflow. Existing advanced trusted-relay settings can name a reachable compatible numeric endpoint with a certificate pin; that capability does not provide the server or its bootstrap authorization. A LAN relay must be reachable from both peers over the existing network; no router, firewall or administrator changes are required or performed.

Direct application traffic can bypass the relay after establishment, but Tailcat still uses it for initial pairing/presence and endpoint-change notifications. A successful already-established loopback path after relay shutdown does not establish relay-free cold start or recovery after network changes. For a relay-free LAN connection, use the separately paired direct-LAN mode, whose fixed control TCP and WireGuard UDP endpoints both need reachability.

## Normal setup

1. Start `soba start --offline`, then inspect `soba lan addresses`. In the local Web UI, choose **Host a relay on this device**. An eligible address can belong to a VPN; review its interface rather than assuming it is the intended network.
2. Select an address and unused high TCP port. Run `soba setup --network lan --host ADDRESS:PORT`, or review and start the selected listener in the Web UI. The UI never automatically selects the first address.
3. Use `soba status` or the relay summary to distinguish saved configuration from listener readiness. A successful bind is not proof that another device can reach it. If bind fails, the saved identity remains. Follow the reported cause before retrying; any different endpoint needs explicit review. Do not disable another program or change a firewall silently.
4. Privately inspect and exchange recipient-bound invitations using `soba lan --help`. Pairing and application trust require separate actions.
5. `soba stop`, or **Stop sobalink** in the UI, stops the whole app, its relay, and current service connections. Restarting with the same configuration preserves the identity; applications may need new connections.

When using a custom profile, pass the same `--state-dir PATH` before each command. These commands operate on that profile only. No remote installation, startup registration, firewall changes, or relay election occurs.

## Listener health and startup diagnosis

The current source observes the owned relay's serving-loop termination. If either its relay listener or private admission listener ends, both are shut down and status stops reporting the host as ready. It does not restart the relay or change the endpoint. Stop and reopen sobalink with the saved configuration to recover; stop still affects the whole app.

In mixed mode, LAN status comes from the exact LAN worker, independently of another backend being ready. `readinessKnown: false` means its current observation failed: CLI and Web show an unconfirmed status rather than claiming that the listener stopped. Existing readiness booleans remain false in that response; consumers should check `readinessKnown` first. The field is additive, and older responses without it retain their previous interpretation. No status read performs a reachability probe.

These typed startup diagnoses currently cover standalone LAN mode; a mixed-mode LAN worker that fails before its control channel starts can still report a generic startup failure.

Startup reports port conflict, unavailable address/address family, denied bind permissions, or exhausted listener resources only when the operating system supplies that typed local-listen failure. Other failures remain unclassified. Raw error details are not exposed. A port conflict does not authorize stopping another program or choosing another port, and an unavailable address does not authorize changing a certificate. Saved keys, pins, pairs and policies remain intact.

## Certificate and address changes

The host certificate is bound to its IP. Ordinary repeat setup keeps a usable certificate. Changing only the port keeps the certificate and fingerprint, but a changed endpoint still requires removing saved pairs first. Status shows the certificate expiry and warns within 30 days. Check the device's clock before replacing an expired or not-yet-valid certificate.

Replacing an existing certificate requires a separate explicit choice. An IP change or invalid certificate cannot silently generate a new pin:

1. Revoke saved LAN pairs explicitly on the affected devices. Record any application permissions you intend to reapprove; do not assume old trust remains appropriate.
2. Stop the host and reopen with `soba start --offline`.
3. Run `soba setup --network lan --host ADDRESS:PORT --rotate-certificate`. In the Web UI's host review, select **Replace the saved relay certificate and fingerprint** before starting. Cancellation changes nothing. Saved pairs and a running engine block replacement.
4. Verify the new address and fingerprint privately. Configure the other devices with that exact selection, create new invitations, pair again, and approve application trust separately. Old pins continue to fail; there is no automatic overwrite or fallback.

Replacement preserves this device's public identity but creates a new relay identity. A failed save does not publish a replacement in memory. If durability is reported as uncertain, stop and inspect the saved state before retrying. Do not delete private state to hide the failure.

## Verification boundary

Synthetic tests cover explicit replacement, unchanged repeats, same-IP port reuse, IP changes, refusal while pairs/engines remain, failed save, certificate status, bilingual human output, and UI review/cancellation. These are product-level automated checks, not completed real-device enrollment or network-isolation acceptance. Use only explicitly approved devices and networks for that acceptance, and publish generic outcomes rather than real addresses, interface inventories, identities, or invitations.

## Opt in to allowed LAN destinations before connecting

The default `trusted-relay` behavior permits ordinary direct peer destinations and selected-relay diagnostics, including explicitly configured external relay addresses. To restrict underlay destinations from the first connection, choose the policy in the same initial setup:

```sh
soba setup --network lan --host ADDRESS:PORT --policy-mode allowed-lan-destinations --prefix PRIVATE_CIDR
```

Use `--relay ADDRESS:PORT --certificate SHA256` instead of `--host` on a joining device. Repeat `--prefix` for additional reviewed prefixes. The Web UI offers **Advanced: allowed destinations** before the host or invitation review. Address-list prefixes are suggestions only; none are automatically permitted. The selected and prepared relay addresses must be inside the allowed prefixes. The policy and relay are saved together before creating the transport.

For an existing configuration, stop and reopen offline, inspect `soba lan policy show`, then use `soba lan policy set --mode allowed-lan-destinations --prefix PRIVATE_CIDR`. Saving this command does not start the network. The Web UI reviews the same offline change before saving. To widen deliberately, choose `--mode trusted-relay` without prefixes; its review explains the broader destination behavior. Peer trust and route grants remain separate.

Prefixes describe destination addresses, not physical interface binding. A matching address may route through a VPN, a tunnel, or another network. This mode is not evidence of physical LAN isolation or whole-machine zero external egress. A failed policy save stops further changes in that process until recovery; a fresh process reads the actual file, which may still contain the prior policy. Do not treat a failed request as a durable restriction.

## Candidate metadata and adjustable relay resources

There is no fixed four-candidate permission limit. More than four exact candidates may be saved, exchanged and explicitly approved. Saved metadata must fit `lanStateBytes`; the unchanged signed exchange format supports 24 KiB of plaintext within a 64 KiB envelope, and DERP uses nonzero 16-bit region IDs. These are storage/protocol bounds, not statements about what NATs support. Older releases still reject offers containing more than four candidates; upgrade both ends before using larger offers. They also do not recognize explicitly saved new relay-resource keys; review compatibility before downgrading that state. Existing pairs, finite/permanent lifetimes, pins and explicit grants do not change when loading old state.

The server keeps a presence connection for every configured relay. Its active relay map also bounds relay-diagnostic destinations. Starting more relays than the selected presence budget fails before networking starts, with saved metadata and grants intact. Raise the budget explicitly rather than treating it as permission to contact new destinations.

```sh
soba lan resources show
soba lan resources set --presence-connections 8 --candidate-attempts 8
soba lan resources set --tls-connections 128 --admission-connections 32
```

First stop networking and reopen with `soba start --offline`. Add the same `--state-dir PATH` before each command when using a selected profile. `set` reviews the current capacity revision and preserves all unrelated choices. `--dry-run` shows the requested edits without contacting the agent. `default` resets an individual value. The local Web UI exposes the same budgets under **Capacity and history → More limits and resource budgets**, with next-start guidance. Active network engines must be stopped before relay-budget changes.

| Resource key | Safe default | Effect |
| --- | ---: | --- |
| `relayPresenceConnections` | 4 | Maximum simultaneous configured server relay-presence connections and relay-map diagnostic targets; increase explicitly for a larger active map |
| `relayCandidateAttempts` | 4 | Maximum sequential approved candidates attempted by one managed dial; the cursor advances fairly after typed availability failures so later candidates can be tried on another call |
| `relayTLSConnections` | 64 | Maximum accepted TLS connections to this device's embedded relay |
| `relayAdmissionConnections` | 16 | Maximum accepted connections to its private loopback admission controller |

Budgets require finite positive integers. Presence and attempt budgets cannot exceed the nonzero 16-bit DERP identifier space (65535); connection counts must fit the platform counter and JSON integer representation. An exhausted inbound slot closes only the excess connection, and closure returns its slot. Existing handshake/header timeouts and the two-minute relay-connection lease remain in force. Raising a budget can increase socket, memory and CPU use; it does not enable startup installation, new relay endpoints, OS routes, or external discovery.

Recovery is availability-only, sequential and still bounded by the existing caller deadlines. Explicit TCP refusal/unreachable/reset errors may select another approved candidate. A generic timeout, unknown proof error, cancellation, pin/authentication/permission error, or a joined error containing any of those is terminal. The maintained engine reports typed dial, TLS and protocol failures for the current connection epoch. Only positively classified dial failures permit another candidate; TLS, admission and protocol failures stay terminal. Stale callbacks cannot replace current state, and an observed terminal failure survives internal reconnects until actual relay admission succeeds. Without a classified cause, recovery stops rather than guessing. Native failure-delivery and final-source recovery tests remain required release gates.
