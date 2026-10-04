# Internal transport prototype

This package is an unused, bounded adaptation of Tailcat. It is not a released
transport or evidence of real-device, direct-path, migration, or strict-egress
acceptance.

Source: https://github.com/tailscale/tailcat/tree/b4dc28e8aa89

Pinned module: `github.com/tailscale/tailcat v0.7.1-0.20260929145319-b4dc28e8aa89`

Copied source files: `tailcat.go`, `wire.go`, `disco.go`, `listen.go`.
Their copyright notices and BSD-3-Clause terms are preserved in `LICENSE`.
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

Release integration must explicitly include this source-only component and its
license/provenance in the package's notices/SBOM. The current module inventory
skips main-module packages, so merely copying LICENSE here is not sufficient for
binary distribution.
