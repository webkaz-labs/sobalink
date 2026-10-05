# Direct LAN without a relay

[日本語](DIRECT_LAN.ja.md) · [Development principles](DEVELOPMENT_PRINCIPLES.en.md)

The development build implements `direct-lan`: recipient-authenticated pairing
at an exact numeric private-network address, followed by WireGuard-encrypted TCP
and native UDP through an embedded gVisor userspace stack. No relay, DNS lookup,
STUN, public discovery, kernel TUN, administrator permission, router change or
firewall change is used by this mode. Existing network policy may still block
reachability; the application reports failure rather than changing that policy.

Native automated loopback tests cover cold-start pairing, both directions of
identity proof, TCP streaming/half-close, native UDP, restart, message delivery,
scoped application forwarding and revocation. These do **not** establish
real-device LAN reachability, application compatibility, suspend/resume,
installation, or publication of this development source. The additional browser
review tests are authored; their current local run is blocked before page load
by Chromium's process-singleton socket permission, so visual acceptance remains
open. An earlier concurrent race run exposed simultaneous initial WireGuard
handshakes: only 9/10 first messages and 7/10 first HTTP probes connected before
the unchanged eight-second application deadline. A deterministic session gate
now serializes initial activation, and the broader Core order subsequently
passed ten repetitions concurrently with two full adapter repetitions. Immediate
responder restart also passes without extending production deadlines. The separate
native lifecycle gate passes bidirectional TCP and UDP through 125 seconds of
active traffic, with a later completed handshake observed on both peers, and
fresh TCP/UDP dials after 185 seconds idle. Synthetic key expiry, flushing and
scheduled rekey also pass. These are bounded localhost results, not real-device
or installed-release acceptance.

## Shortest path in the local Web UI

1. Start each device offline and open its local control page. Choose **Set up
   network → Direct LAN**
2. Choose **Show this device’s LAN addresses** to fill an assigned address and
   prefix, or enter them manually. Select an unused port from
   1024–65535, plus the exact private subnet prefix to permit. Review and start
   it. The same number is used for the TCP pairing/session-control listener and UDP tunnel
3. Share the recipient device's **public ID** with the inviting device. On the
   inviting device, enter that ID and a recognizable name, then create the
   five-minute invitation. The optional private QR is generated locally; text
   copying remains available
4. Transfer the invitation privately to the intended recipient. Paste the
   complete text, review the inviter's pinned public ID, exact endpoint and
   expiry, then pair. A QR scanner may return the same text for this review
5. Approve messages/file offers separately on each device. Create each service
   share with its exact peers, loopback target, ports and lifetime

The display distinguishes saved configuration, local tunnel readiness and
confirmed peer/application response. A public ID is not a secret. An invitation
is a one-use secret and must stay out of logs, public screenshots, command-line
arguments and shared documents. Consumed/expired invitations are removed from
the visible copy/QR controls. Pairing never grants application permissions.

## CLI alternative

Run the agent first with `soba start --offline`. The following addresses are
fictional private-network examples; select addresses actually assigned to the
intended devices and the intended subnet.

Inviting device:

```sh
soba direct-lan configure --listen 192.168.50.10:48444 --prefix 192.168.50.0/24
soba direct-lan identity
soba direct-lan status
```

Recipient device uses its own address, for example
`--listen 192.168.50.11:48444`, and shares the public ID returned by its identity
command. The inviter creates an invitation for that exact ID:

```sh
# POSIX: ensure the private output file is owner-only before writing it.
umask 077
soba direct-lan invite --to RECIPIENT_PUBLIC_ID --name device --ttl 5m > invitation.json
```

On systems without `umask`, use an owner-private file location and permissions.
Transfer that file only through the selected private channel. On the recipient:

```sh
soba direct-lan inspect --json-file invitation.json
soba direct-lan join --json-file invitation.json
```

`--stdin` accepts the same JSON envelope. The private invitation itself is never
a positional CLI argument. JSON fields and command names are unchanged across
Japanese and English. `status --json` provides machine-readable status.

Cancel an unused invitation on its issuer with
`soba direct-lan cancel --json-file invitation.json`. Revoke a pair with
`soba direct-lan revoke PEER_ID`. Revocation closes currently tracked work and
invalidates application approvals for that peer. It does not retract delivered
content or stop work already launched inside a remote application.

## Identity, persistence and network boundaries

- Mutual Ed25519 TLS proves the identity of both pairing participants. The
  invitation pins the intended recipient and host; both sides authenticate
  separate WireGuard public keys and exact tunnel endpoints in that exchange
- The private WireGuard key is domain-separated from the protected Ed25519
  seed. A name or unauthenticated IP never supplies identity
- Every application connection uses the userspace stack and exact paired
  WireGuard key. WireGuard cryptokey routing and a post-decryption check permit
  only the paired `/128` source and this device's exact overlay destination
- The UDP bind uses a global set of current exact approved endpoints inside the
  selected private/loopback prefixes. Received ciphertext from another approved
  endpoint still has the identity and `/128` source authorized by its WireGuard
  key; the physical source address never grants another peer’s identity. Each
  peer’s outbound endpoint stays pinned. Hostname resolution, wildcard endpoints,
  IPv4-mapped aliases and nonzero socket marks are rejected. IP scope and passive
  interface observation do not prove a physical NIC or isolation from OS VPN
  routing; this application does not change that routing
- Before a new application dial, the lower Ed25519 public key initiates
  WireGuard. The other side can request this through the already pinned mutual
  TLS control connection and waits for authenticated WireGuard transport
  confirmation. Concurrent dials share activation. An immediate responder
  restart can require the existing five-second WireGuard send-rate window;
  control retries remain inside the original caller deadline. Both devices must
  retain their selected TCP control and UDP tunnel reachability
- This gate addresses initial/new-dial establishment. WireGuard's independent
  rekey and retransmission timers still operate for existing flows; it does not
  promise uninterrupted TCP across network loss, long idle or suspend/resume
- Pairing/control TLS never becomes an application proxy. Only explicit
  virtual listeners and the existing scoped TCP dispatcher receive application
  streams; existing service authorization controls the permitted loopback target
- Paired state is durably saved through the protected Core store before
  activation. The private WireGuard state is version 2; comparison-only version-1
  state is rejected, including unpaired state. Uncertain writes fail closed and require saved-state recovery.
  Lost pairing replies may leave only the remote side paired; inspect/revoke
  that remote approval before retrying. If revocation cannot be written, the old
  disk pair can still be read after restart even though current work is closed;
  stop and reconcile protected state before restarting
- A passive local-interface check reports when the exact selected IP is no
  longer assigned to an up interface. It never scans peers, probes the network,
  changes prefixes or binds a replacement address. An inspection failure is
  **unknown**, not proof that another backend is safe to select

Repeated identical configuration preserves identity. Changing the endpoint or
prefixes requires stopping, reopening offline, explicitly removing affected
pairs and pairing again; an invitation does not silently broaden local policy.

## Capacity and evidence

Logical peer admission follows the selected `trustedPeers` policy, including its
explicit unlimited setting; lowering it does not delete saved pairs. Private
state remains bounded by `lanStateBytes`. The pinned WireGuard implementation
has a separate 65,536-entry peer-table resource boundary.

Flow, listener, pending-invitation/control and packet-queue resource limits are supplied
from the existing configurable Core resource budgets. Runtime allocation changes
apply after restart. TLS control work has a separate finite allowance derived
from the pending-invitation budget, so app waiters cannot consume its slots. Status reports `resourceRestartRequired`; logical peer
admission and private-state budgets update on successful policy publication.
Small control-message, invitation-lifetime and datagram protocol bounds do not
truncate file transfers or application TCP streams.

Focused native checks:

```sh
go test -race -tags directlan_integration ./internal/directlan -count=2 -timeout 150s
go test -race -tags directlan_integration,directlan_lifecycle ./internal/directlan -run '^TestNativeSessionLifecycle$' -count=1 -timeout 8m
go test -race -tags ts_omit_portmapper,ts_omit_captiveportal,ts_omit_useproxy,directlan_integration ./internal/core -run '^TestDirectLAN' -count=10 -timeout 180s
```

See [transport provenance](../internal/directlan/UPSTREAM.md). Earlier
TLS-stream/framed-UDP prototype results do not establish the final
WireGuard/native-UDP implementation's acceptance. Ordinary `lan` trusted-relay
mode and [mixed connections](MIXED_CONNECTIONS.en.md) have different external
traffic permissions.
