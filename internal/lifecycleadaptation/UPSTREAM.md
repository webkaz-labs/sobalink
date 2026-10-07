# Pinned transport dependency adaptations

Sobalink prepares three exact local dependency trees with `go run ./cmd/prepare-engine`:

| Upstream module | Version | Generated directory |
| --- | --- | --- |
| tailscale.com | v1.104.0 | .sobalink-deps/tailscale |
| github.com/tailscale/wireguard-go | v0.0.0-20260928213032-417aef361226 | .sobalink-deps/wireguard |
| gvisor.dev/gvisor | v0.0.0-20260915211658-a6f909f08a72 | .sobalink-deps/gvisor |

The version-qualified replacements in `go.mod` are a fixed set. Other modules,
versions, output paths or extra replacements are rejected. Preparation imports
only the standard library and local preparation packages, so it can start before
any generated dependency directory exists. It does not compile or start transport
code. The low-level Tailscale-only validator remains available for its own source
fixtures; the product command always selects the three-module configuration.

## Source and license identity

WireGuard comes from revision `417aef361226c869ab29e15fe3539b01173c4719` at
<https://github.com/tailscale/wireguard-go/tree/417aef361226c869ab29e15fe3539b01173c4719>
and retains its MIT license. gVisor comes from revision
`a6f909f08a7250c080b5b70f3513e8b27b75f64a` at
<https://github.com/google/gvisor/tree/a6f909f08a7250c080b5b70f3513e8b27b75f64a>
and retains Apache-2.0. The existing Tailscale record is described in
[engineadaptation/UPSTREAM.md](../engineadaptation/UPSTREAM.md).

`wireguard.json` and `gvisor.json` bind each original module/archive checksum,
go.mod checksum, immutable revision, original file hashes, complete original
source-tree digest, overlay hashes and complete adapted-tree digest. `pin.go`
independently authenticates the manifest bytes. `sources/` contains only changed
or added files, with `.txt` appended so upstream packages are not compiled as
part of the preparation tool. Original notices remain in modified files; added
files carry the corresponding license and modification notices.

The complete pinned gVisor license is also retained at
`licenses/gvisor/LICENSE` for standalone source delivery. Its SHA-256 is
`0fbab5c58efbdf6d31e8085214f2dd821659c03d73cff3ed2b08e98826ea1cd9`, matching the
manifest's original LICENSE entry. Distribution retains each complete original
module notice inventory, all overlays, manifests, preparation sources and
explanations. Source components use distinct identities
`internal/lifecycleadaptation/wireguard` and `internal/lifecycleadaptation/gvisor`;
these identify the two manifest groups, not separate source checkouts.

## Preparation and verification

The preparer checks every selected checkout input before creating output. It
obtains only the original pinned Go archives and verifies their canonical Go h1
checksums and full source trees. `PrepareArchive` applies overlays in memory;
`MaterializePinned` writes a fresh staging tree outside the module cache, checks
it again, and renames it into its exact generated location. Upstream extracted
source is never patched in place. Existing output is fully rehashed; links,
reparse points, unknown files and source drift are rejected rather than silently
repaired. Inspect and explicitly remove a changed generated directory before
preparing it again.

`go run ./cmd/prepare-engine --verify` performs read-only verification of all three
input sets and generated trees. With the pinned toolchain and original archives
already cached, `GOPROXY=off go run ./cmd/prepare-engine` uses the same preparation
path offline. A partially completed preparation leaves only individually verified
output trees; a later invocation resumes and the final verification still requires
all three. No build should treat partial preparation as complete.

Distribution checks exact package/module resolution and complete generated trees
before and after compilation. Its build metadata records actual target-linked
inputs as well as the retained adaptation recipes. The SBOM identifies each
adapted component separately and records the original module as an ancestor;
it does not label a modified runtime as the unmodified upstream module. Package
and installation verification independently checks retained manifests, overlays,
licenses, recipes and source identities.

## Lifecycle boundaries

The additive gVisor forwarder API admits a request before launching its handler,
publishes its endpoint before waiting for the handshake, wakes on cancellation,
and transfers ownership through the generation's final handoff check. Only the
creator cleans an endpoint before handoff. Request completion and handler return
are separate obligations. Creator completion does not establish stack or dispatcher
completion; outgoing creators and ingress must follow their own generation fences.

The opt-in WireGuard facade restricts configuration to one initial bind/start,
non-lazy peers, zero persistent keepalive and the documented nonblocking callbacks.
It seals/wakes the exact socket and TUN before application cancellation bookkeeping,
then joins API/producers, handshake consumers, timer arms, retained peer workers,
crypto queues/consumers and limiter cleanup. Ordinary empty bind Close remains
distinct from terminal SealAndClose. BeginSetup must span initial configuration
and be released before waiting. A wait timeout retains the owner; it does not
manufacture terminal completion. Retained peer admission is capped at MaxPeers.

Peer callbacks include a nonzero immutable registration token scoped to one WG
owner. Values never repeat; explicit exhaustion rejects rather than wrapping.
Same-key re-pair receives a new identity. PeerRegistered runs synchronously before
publication/start, outside owner.mu, and must bind the exact session selected for
that configuration transaction. Later callbacks reject mismatched tokens; owned
receive never falls back to key-only attribution.

The shared Admit bookkeeping closure has the strict lock order application mutex
-> WG admission gate -> generation mutex. No path holding the generation mutex
may enter the WG facade or request stop, and the closure must not perform I/O,
cancellation, resource operations or waits. Bind/TUN/callback implementations must
satisfy their documented behavior; Go interfaces alone do not prove those
properties. The joint lifecycle and caller lock graph requires its own review.

The additive `device/owned_lifecycle_testing.go` overlay is compiled only when
both `directlan_integration` and `directlan_lifecycle` are selected. Its fixture
stats copy only the existing atomic handshake timestamps; no UAPI serialization,
peer identity, endpoint or key material is exposed. Three named test operations
invoke the existing expiry, flush and scheduled-on-next-send methods without
changing their behavior or protocol thresholds. Each takes an API completion
lease, holds the existing configuration serializer, rejects terminal stop and
stale/non-running registrations, and invokes the operation after the admission
gate and peer lookup lock are released. The lease lasts through synchronous
session-state notifications. No raw handle or arbitrary callback escapes, and
the production facade has no added surface when either fixture tag is absent.

Source identity, compilation, lifecycle correctness, application compatibility and
installed-binary verification are separate results. A successful source verification
does not demonstrate worker termination, retained-listener behavior or network
compatibility. Platform, race, lifecycle, reproducibility, signing and distribution
checks remain necessary for the final integrated source.
