# Direct LAN userspace transport provenance

`stack.go` is a narrowly scoped adaptation of the in-memory TUN and numeric
TCP/UDP helper portions of:

- Module: `github.com/tailscale/wireguard-go v0.0.0-20260928213032-417aef361226`
- Source file: `tun/netstack/tun.go`
- Source SHA-256: `dc8bdff07b29630c2e0867c0b2b56d6e4de1035049be89c5979c6cffe4b7623b`
- Source: https://github.com/tailscale/wireguard-go/blob/417aef361226/tun/netstack/tun.go
- License: MIT; the copyright/SPDX header is retained and terms are copied
  unchanged to `WIREGUARD_LICENSE`

Changes retain only IPv6 userspace addresses and numeric stack entrypoints,
remove all resolver/DNS and generic dialing APIs, add a post-decryption exact
source/destination authorization check, use bounded channel notifications with
shutdown-safe packet reads, expose the stack only inside this package for the
compact scoped TCP/UDP dispatcher, and match the repository's pinned gVisor
PacketBuffer pointer API. The in-memory default NIC route leads only to
WireGuard. No kernel TUN, host route, forwarding, interface policy or firewall
configuration exists here.

`bind.go` is project code implementing the pinned WireGuard `conn.Bind`
interface. It opens only the configured private/loopback numeric UDP address and
port. Both directions require membership in the global current exact approved endpoint
set inside
the selected prefixes. Endpoint parsing never resolves names. Nonzero socket
marks are rejected; no interface, namespace or network-policy operation exists.
The pinned engine keeps each configured outbound endpoint. Received ciphertext
from another approved endpoint retains the WireGuard key identity, never the
outer address identity; cross-key source spoofing is rejected. IP scopes do not
prove physical NIC or VPN isolation. This adapter rejects any unapproved
send/receive endpoint. Incoming decrypted packets must have the
peer's exact /128 source and this node's exact destination before stack delivery.

Application TCP and native UDP use WireGuard plus gVisor. TLS carries only the
one-use pairing/control exchange, binding both peers' distinct WireGuard keys
to their mutually authenticated Ed25519 identities. Earlier experimental
TLS-stream/framed-UDP test results do not establish this implementation's
acceptance. Native localhost tests validate this adapter; real-device LAN,
firewall reachability, application compatibility, suspend/resume and installed
release acceptance remain separate gates.

Capacity defaults are configurable finite resources, not an extra fixed logical
peer ceiling. The caller supplies the selected flow, listener, invitation and
packet-queue limits; a lock-free logical-peer limit callback supports current
Core policy. The pinned WireGuard engine's own 65,536-peer table is a distinct
implementation boundary. Control-frame bounds do not limit application stream
or file length. Interface readiness reads local metadata only, and unknown
inspection failures remain distinct from proven absence of the exact local IP.
