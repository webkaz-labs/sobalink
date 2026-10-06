# Explicit LAN destination admission

[日本語](LAN_DESTINATIONS.ja.md) · [LAN setup](LAN.en.md) · [Relay operations](RELAY_OPERATIONS.en.md)

**Published in alpha.5, with exact-source automated and distribution evidence in [verification](VERIFICATION.en.md). Physical-device network acceptance remains pending.** The optional `allowed-lan-destinations` policy restricts this LAN transport's UDP writes to explicitly selected private/ULA/loopback CIDRs and its DERP TCP connections to exact configured numeric relay endpoints within those CIDRs. It does not bind a physical interface or prove same-link, VPN isolation, or whole-host/process zero egress. A matching destination can still be routed through another interface or VPN.

Administrator rights and LAN-router changes are not required or performed. The feature uses ordinary userspace sockets; it does not install a TUN driver, modify routes/firewalls, enable UPnP/NAT mappings or bypass a network policy. If the selected network blocks the needed traffic, report the failure and choose another explicitly allowed configuration.

## Choose before connecting

In the local Web setup, review the destination policy with the relay address and certificate pin. Select only the network prefixes you intend to use; interface-derived suggestions are never enabled automatically. The relay and policy are saved together before starting the transport. Invitations do not grant permission to broaden your local destination policy.

For example, with a reviewed synthetic network:

```sh
soba setup --network lan --host 192.168.50.10:48443 --policy-mode allowed-lan-destinations --prefix 192.168.50.0/24
```

Use `soba setup --help` for the exact host/relay flags supported by the current CLI. A prefix applies to destinations, not the source interface. IPv6 ULA prefixes are supported; link-local addresses with zone identifiers and IPv4-mapped IPv6 addresses are rejected. A loopback prefix is useful for same-device testing only.

Inspect or change saved policy while the backend is stopped:

```sh
soba lan policy show
soba start --offline
soba lan policy set --mode allowed-lan-destinations --prefix 192.168.50.0/24 --prefix fd50::/64
```

Stop the running process before the offline launch. Restart normally after saving the reviewed policy. The selected relay and all locally prepared relay candidates must fit. Existing remote route grants are intersected with the policy: an out-of-policy route cannot become fallback. Choosing `trusted-relay` explicitly restores ordinary direct peer destinations and selected-relay diagnostics; it does not select a public relay automatically.

## Enforcement and lifetime

- UDP single writes, batches, fallbacks, retry attempts, discovery and cookie responses use the same low-level boundary
- DERP TCP uses numeric exact-endpoint admission, with no DNS fallback, environment proxy or custom dialer bypass
- Guarded engines disable netcheck HTTP/HTTPS/ICMP, standalone probes, raw discovery and the separate WireGuard ICMP pinger before those paths create sockets
- Port mapping, captive-portal probes and proxy support remain omitted by the existing production build tags
- Policy is immutable for an engine generation. Retirement revokes admissions and closes tracked UDP/TCP sockets; a new generation receives a fresh policy. Already delivered packets cannot be recalled
- Existing Tailnet and ordinary trusted-relay modes retain their behavior. There is no automatic switch between these network backends

The original pinned relay is still needed for bootstrap and discovery. This feature does not add relay-free pairing, public rendezvous, or general NAT traversal. Pair trust, service grants and file-receive permissions stay independent.

A failed policy save is not a durable change. The current process requires recovery rather than starting with uncertain authority; a new process reads the actual saved file, which may still contain the earlier policy. Inspect saved configuration before restarting after a storage error. State carrying this policy uses LAN file version 4 and cannot be loaded by older binaries.

## Build and evidence

The pinned Tailscale module is copied to a generated directory and changed by a reviewed, hash-verified patch. `go run ./cmd/prepare-engine` prepares it; `mise` and CI include this step. The downloaded module cache is never edited. Missing preparation or an unadapted module fails compilation. Releases retain the adapted source identity, upstream checksums, patch/source hashes and licenses in their inventories.

See [verification](VERIFICATION.en.md) for exact-source results. Automated loopback tests and synthetic denial tests do not establish physical-LAN/VPN containment. Physical devices, multiple NICs, VPN overlap, routing changes, sleep/resume and IP renewal are post-release acceptance work requiring separately selected devices/networks and permissions.
