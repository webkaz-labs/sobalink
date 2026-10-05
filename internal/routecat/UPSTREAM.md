# Adapted internal transport source

This package is a bounded adaptation of Tailcat used by the LAN transport
implementation currently under verification. Implementation and packaging
inventory do not establish release, real-device, direct-path, migration, or
strict-egress acceptance.

Source: https://github.com/tailscale/tailcat/tree/b4dc28e8aa89

Pinned module: `github.com/tailscale/tailcat v0.7.1-0.20260929145319-b4dc28e8aa89`

Copied source files: `tailcat.go`, `wire.go`, `disco.go`, `listen.go`.
Their copyright/SPDX headers are retained in the adapted files, and the original
BSD-3-Clause terms are preserved verbatim in `LICENSE`. The locally added
`regions.go` has its own explicit MIT header; this package is not presented as an
unmodified upstream module.
The executable dependency remains the repository's pinned `tailscale.com v1.104.0`.
No module replacement is used.

Changes: remove network map fetching and implicit region defaults; validate and
canonicalize up to four numeric certificate-pinned one-node regions; keep all
server regions present in one engine; update each peer's relay from the DERP
connection carrying its key-checked meow; keep clients single-candidate; bound
presence work by server lifetime. A meow is relay-authenticated discovery, not
end-to-end application authentication. WireGuard plus the outer pairing/trust
coordinator remains responsible for that boundary.

PrivateOnly rejects builds that retain UDP underlay transport. It confines maps
to private/loopback literals, disables region diagnostics, and never fetches a
public/default map. Public/direct-enabled product builds cannot silently turn
this into a runtime egress sandbox. The stricter build still needs captured
traffic acceptance and packaging review before use.

There is intentionally no live UpdateRegions method: candidate removal, policy
changes, key admission, active flow cancellation and durable configuration need
one outer transaction. Restarting with unchanged identities is the bounded first
implementation. No established-TCP preservation is promised.

Release packaging inventories this adaptation separately from module
dependencies when it appears in the target's runtime package closure. It retains
`LICENSE`, `UPSTREAM.json` and this document under the archive's
`share/sobalink/licenses/source/internal/routecat/` directory. Notice JSON and
build metadata record the original upstream hashes separately from the hashes
of selected adapted source/build inputs. CycloneDX records this main-module
component as modified and identifies Tailcat only as its upstream ancestor.

The reviewed upstream pin and exact license bytes are checked independently of
`UPSTREAM.json`; changing upstream requires a corresponding packaging review.
Selected compile/embed inputs, `go.mod`, `go.sum`, and retained provenance are
hashed deterministically. Target-filtered source inventory does not itself prove
which functions survive linking or that any network behavior has been accepted.
The product archive still contains only its existing executable; no additional
helper executable is packaged by this source-component inventory.
