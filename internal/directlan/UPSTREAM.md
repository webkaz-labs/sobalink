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
one-use pairing exchange and bounded session-activation control. Pairing binds
both peers' distinct WireGuard keys to their mutually authenticated Ed25519
identities. This source uses the opt-in owned WireGuard facade and owned
gVisor forwarder lifecycle. Their separately pinned source adaptations,
preparation, module selection and distribution provenance are reviewed together. The deterministic lower-key initiator gates new application
dials, with generation checks and separately bounded control work. Independent
engine rekey/retransmission timers still govern established flows. Earlier experimental
TLS-stream/framed-UDP test results do not establish this implementation's
acceptance. The initial generation-ownership source has compiled and passed
limited fixed-endpoint Linux race regressions, including IPv4/IPv6 cold sessions.
The detached retirement/staging controller has also compiled; its execution and
full acceptance remain pending. It has no replacement publication entrypoint. Full native/application
acceptance and installed-release checks remain separate from these limited results.
Real-device LAN, firewall reachability and suspend/resume also remain unverified.

Capacity defaults are configurable finite resources, not an extra fixed logical
peer ceiling. The caller supplies the selected flow, listener, invitation/control and
packet-queue limits; a lock-free logical-peer limit callback supports current
Core policy. The pinned WireGuard engine's own 65,536-peer table is a distinct
implementation boundary. Control-frame bounds do not limit application stream
or file length. Interface readiness reads local metadata only, and unknown
inspection failures remain distinct from proven absence of the exact local IP.

`creator.go` adapts the explicit TCP/UDP endpoint construction sequence from:

- Module: `gvisor.dev/gvisor v0.0.0-20260915211658-a6f909f08a72`
- Source file: `pkg/tcpip/adapters/gonet/gonet.go`
- Source SHA-256: `700235f948cc7e363057e61ebf7d5186ea9fbfcc08d5abdea3b03f16879e809d`
- Source: https://github.com/google/gvisor/blob/a6f909f08a72/pkg/tcpip/adapters/gonet/gonet.go
- License: Apache-2.0; the original copyright/license header is retained and
  the unchanged license is copied to `GVISOR_LICENSE`
- License SHA-256: `0fbab5c58efbdf6d31e8085214f2dd821659c03d73cff3ed2b08e98826ea1cd9`

The adaptation reserves capacity before creation, publishes the exact endpoint
before bind/connect/wait, checks cancellation and transfers ownership only under
the terminal admission gate. Creator failures retain one cleanup owner through
endpoint terminal observation. The independent stack and WG joins remain
required. Retained outer application/bridge/HTTP participants join through
exact generation origins and leases. Local resource cleanup alone does not
revalidate current application authority or authorize a replacement.
`RetireTransport` remains explicitly incomplete; the detached controller has
no replacement-publication path or Core request caller.

`packetListener` preserves an exact association capability in each returned
address. Adapters must propagate that address object unchanged for reverse
writes; reconstructing an address from its string cannot select another
association. Owned WG session/decryption callbacks carry a non-reused peer
registration token. Registration is installed before WG peer publication, and
callbacks verify it against the exact session; same-key re-pair cannot retarget
an old callback to the new peer state.
