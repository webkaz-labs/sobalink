# Connection routing foundation

This internal package supplies identity binding and new-flow routing used by the Core mixed-backend integration. It does not independently start a connection mode. See [mixed connections](../../docs/MIXED_CONNECTIONS.en.md) for the product workflow.

A stable Ed25519 application key is proven with a fresh verifier-owned challenge on each exact authenticated transport identity. An observed identity must come from that transport's current identity API, never from a display name, DNS name or IP. Both peers independently verify the mapping. Persist only after explicit review. Binding identities does not grant service access.

The new-flow router accepts a reviewed ordered route set. Each attempt checks current transport authorization and exact resource permission; permission failure stops rather than trying another backend. Only an explicitly classified availability failure may fall back. Revocation closes tracked flows and rejects a connection that completes across a generation change. Existing streams are never replayed or migrated.

Strict LAN selection rejects WAN and Tailnet routes. The application must additionally avoid starting external backend workers in this mode. Healthy-flow behavior, stable application entrances, backend processes, persistence, CLI/Web controls and native integration belong to the application layer; this package alone does not establish their acceptance.

Tests exercise signature/nonce binding, independent transport observations, expiry/replay, strict selection, denial and revocation races using socket-free fixtures. They are not real-device or NAT compatibility evidence.

## Evaluate a new flow

1. Verify the current transport identity and reviewed same-peer binding.
2. Check the exact resource permission on the selected route.
3. Use a later approved route only for positively classified availability failure.
4. Stop on authorization failure or revocation; do not replay an existing stream.

The routing decision concerns a new flow. A matching display name, address or saved mapping cannot replace the live transport identity check.
