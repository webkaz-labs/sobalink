# Connection routing foundation

This internal package is an integration foundation, not an enabled connection mode.

A stable Ed25519 application key is proven with a fresh verifier-owned challenge on each exact authenticated transport identity. An observed identity must come from that transport's current identity API, never from a display name, DNS name or IP. Both peers independently verify the mapping. Persist only after explicit review. Binding identities does not grant service access.

The new-flow router accepts a reviewed ordered route set. Each attempt checks current transport authorization and exact resource permission; permission failure stops rather than trying another backend. Only an explicitly classified availability failure may fall back. Revocation closes tracked flows and rejects a connection that completes across a generation change. Existing streams are never replayed or migrated.

Strict LAN selection rejects WAN and Tailnet routes. The application must additionally avoid starting external backend workers in this mode. Healthy-flow behavior, stable application entrances, backend processes, persistence, CLI/Web controls and native integration remain application-level work.

Tests exercise signature/nonce binding, independent transport observations, expiry/replay, strict selection, denial and revocation races using socket-free fixtures. They are not real-device or NAT compatibility evidence.
