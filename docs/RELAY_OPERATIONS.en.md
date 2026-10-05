# Local relay setup and recovery

[日本語](RELAY_OPERATIONS.ja.md) · [User guide](GENERIC.en.md)

Choose this device's private address explicitly, start its relay, and privately pair the other devices. A saved configuration, a running listener, and successful peer/application traffic are different states. Native loopback tests do not establish physical LAN reachability, sleep/wake behavior, or application compatibility.

## Normal setup

1. Start `soba start --offline`, then inspect `soba lan addresses`. In the local Web UI, choose **Host a relay on this device**. An eligible address can belong to a VPN; review its interface rather than assuming it is the intended network.
2. Select an address and unused high TCP port. Run `soba setup --network lan --host ADDRESS:PORT`, or review and start the selected listener in the Web UI. The UI never automatically selects the first address.
3. Use `soba status` or the relay summary to distinguish saved configuration from listener readiness. A successful bind is not proof that another device can reach it. If bind fails, the saved identity remains; choose another port explicitly. Do not disable another program or change a firewall silently.
4. Privately inspect and exchange recipient-bound invitations using `soba lan --help`. Pairing and application trust require separate actions.
5. `soba stop`, or **Stop sobalink** in the UI, stops the whole app, its relay, and current service connections. Restarting with the same configuration preserves the identity; applications may need new connections.

When using a custom profile, pass the same `--state-dir PATH` before each command. These commands operate on that profile only. No remote installation, startup registration, firewall changes, or relay election occurs.

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
