# sobalink architecture

[日本語ガイド](GENERIC.ja.md) · [English guide](GENERIC.en.md) · [Security](../SECURITY.md) · [Development principles](DEVELOPMENT_PRINCIPLES.en.md)

The local development draft combines an embedded React UI and a Go agent. Human interaction and agent CLI requests share one application boundary. A peer can exchange authorized messages, batches and service metadata; it cannot operate the local administration API.

```mermaid
flowchart LR
    U["Browser on this device"] --> W["127.0.0.1 ephemeral Web API"]
    C["soba CLI"] --> I["Owner-only local IPC"]
    W --> K["Shared Go core"]
    I --> K
    K --> P["Identity and permission checks"]
    P --> N["Explicit network backend"]
    N --> R["Authenticated peer API"]
    P --> F["Bounded file receiver"]
    P --> S["Scoped service dispatcher"]
    S --> L["Exact local loopback service"]
```

## Source map

| Source | Responsibility |
| --- | --- |
| `cmd/soba` | Locale selection, foreground lifecycle, local command interface |
| `web` | React source, pinned npm lockfile, generated assets embedded with Go |
| `internal/webui` | Exact loopback management listener, code/session/Origin/CSRF checks |
| `internal/control` | Owner-restricted Unix socket or Windows named pipe |
| `internal/core` | Shared commands, current state, profile, trust, peers, transfers and service grants |
| `internal/identity` | Embedded tsnet adapter and current Tailnet identity |
| Tailcat integration work | Trusted-relay adapter and pairing are excluded from the initial snapshot; the integration gate remains open |
| `internal/transfer` | Manifest validation, receive policy, bounded streaming, storage and whole-file retry |
| `internal/ranges` | Compact range/exclusion sets, immutable plans and TCP fallback admission |
| `internal/policy`, `internal/transport` | Current-identity authorization, forwarding and bounded TCP/UDP lifetimes |
| `internal/distribution` | Deterministic package layout, source/dependency metadata, frontend assets and notices |

The legacy `cmd/tsnet-bridge` implementation may remain in the tree. It does not define the new UI, CLI or profile compatibility contract. Product names and executable names change independently of the current repository/module locator.

## Network boundaries

The application selects one backend explicitly. A new profile selects none. A saved selected backend may reconnect when the agent starts; that does not restart service grants or transfer progress. Runtime switching cannot silently exchange backend identities or continue an existing TCP session.

Existing Tailnet mode uses a separate embedded tsnet node. Enrollment uses the official interactive login flow. Current peer identity, Tailnet grants/ACLs and the application grant are checked independently. Service dialing uses the embedded stack instead of an OS-network fallback. OS routes and DNS remain outside this product's management surface.

Tailcat mode is unavailable in the initial snapshot and remains under integration. The stock adapter uses an explicit numeric relay endpoint with a TLS certificate SHA-256 pin and matching peer capabilities. It has no default public relay map or DNS bootstrap. Build tags `ts_omit_portmapper,ts_omit_captiveportal,ts_omit_useproxy` exclude port mapping, captive-portal probes and system-proxy fallback; proxy/unsupported backend environment overrides are rejected.

The selected relay may be self-hosted or another endpoint explicitly trusted by the user. Its permitted traffic includes encrypted relayed payloads and HTTPS/ICMP diagnostics to that same endpoint, plus peer direct traffic. This boundary is not strict LAN isolation or zero external traffic. Pairing key ownership, durable acknowledgement, UDP multiplexing and native continuity tests must pass before claiming a working end-to-end Tailcat mode. [Current gate](VERIFICATION.en.md#tailcat-gate)

```mermaid
flowchart TD
    A["Explicit network selection"] --> B["Existing Tailnet"]
    A --> C["Tailcat with selected trusted relay"]
    B --> D["Current authenticated peer identity"]
    C --> E["Explicit pairing and verified peer key"]
    D --> F["Transfer or service grant"]
    E --> F
    F --> G["Direct or encrypted relay path when observed"]
    G --> H["Application result checked separately"]
```

Direct and Relay are path observations; Reconnecting is a lifecycle state. Unknown means the backend has not supplied reliable path evidence. A route can change inside a backend, but the product makes no established-TCP preservation promise. An unavailable backend does not trigger automatic exchange to another mode.

## Management and peer surfaces

The management listener is always `127.0.0.1:0` at allocation and then the exact resulting host/port. It serves embedded assets and `/api` only. A separately delivered one-time terminal code creates a session; mutating requests require the matching Origin and session CSRF token. URL-based secrets, wildcard listening and remote administration are excluded.

The current UI contract is [web/API.md](../web/API.md), backed by TypeScript in `web/src/api.ts`. The UI cannot make its own trust decision authoritative: Go validates identity, expiry, range, receive policy and path safety on every relevant operation. Request IDs deduplicate repeated commands within bounded process-local history; they do not claim durable transaction recovery.

The peer API has separate routes for hello, explicit messages, transfer offers/status/content and minimal service discovery. Transport-derived identity is rechecked against current peer state. Peer routes do not accept local management commands, filesystem destinations or arbitrary URLs. Reserved endpoints are discovery `54543`, peer API `54544` and pairing `54545`; local management/control and backend-internal endpoints are also excluded from generic service sharing.

## Trust, batches and storage

Trust, pairing, autosave and service sharing are distinct scopes. A display name never supplies identity. A receive policy binds an exact peer and trust generation to an absolute local destination. Revocation closes work and invalidates the previous generation. Persisted receive policy becomes effective only after its atomic configuration write succeeds.

```mermaid
stateDiagram-v2
    [*] --> Pending: Valid offer from trusted peer
    Pending --> Accepted: Batch consent or exact autosave grant
    Pending --> Rejected: Receiver declines
    Accepted --> Receiving: Stream a file
    Receiving --> Completed: All files verified and saved
    Receiving --> Partial: An unfinished file fails
    Partial --> Receiving: Retry unfinished whole files
    Pending --> Cancelled: Cancel or revoke
    Accepted --> Cancelled: Cancel or revoke
    Receiving --> Cancelled: Cancel or revoke
    Partial --> Cancelled: Cancel or revoke
```

A manifest includes relative paths, kinds, sizes and SHA-256 values. Directories explicitly represent empty folders. The receiver bounds metadata, path depth, reservations, outstanding batches and concurrent file streams, rejects unsafe paths/links and creates files without overwrite. Final acknowledgement follows size/hash verification and save completion. Completed-file acknowledgements are idempotent while their batch exists.

The current core limits each batch to 256 entries and 1 GiB. Browser uploads stage into private local storage before offering a batch to the peer. Local upload progress, remote acceptance, file saving, partial completion and completed delivery remain separate states. No automatic file opening, execution or clipboard synchronization follows receipt.

Retry starts each unfinished file again from byte zero. There is no byte-offset resume, durable progress journal or restart resume. Saved data survives cancellation and process shutdown; active batch state does not. A new batch after restart may create unique-name duplicates, which the user must review.

## Compact service plans

TCP sharing uses a compact inclusive range/exclusion plan with one userspace fallback dispatcher. Shared port N maps to the same exact loopback port N. A broad range is a real future grant: applications started later on effective ports are reachable to its permitted peers until expiry.

Plans require 1–32 current peer IDs, a current embedded-node address, a specific protocol, a valid range and an explicit lifetime of 1 second–24 hours. System reservations and active internal endpoints are removed from effective scope. Overlap, collision, stale identity and exhausted capacity fail before an unsafe plan becomes usable.

UDP shares and local connection listeners require per-port resources and share a total 64-listener budget. TCP shares do not consume one listener per covered port. Client connections may explicitly remap sorted effective remote ports to consecutive local ports, but shares retain same-port targets. Each accepted flow checks current identity, grant and deadline before loopback dialing.

Discovered services carry bounded metadata only for eligible peers and unexpired active shares. A selected observation is revalidated before connecting. Ordinary Tailnet services can be connected manually without a peer sobalink API. All service state labels application health `unverified`.

Expiry, stop and peer revocation close tracked flows. They do not cancel a remote application job, retrieve delivered content or extend a deadline. Saved definitions are inert after restart until explicitly started.

## Evidence and release boundary

Unit tests and mock backends establish specific logic properties. A DOM test checks component behavior. A real local browser checks rendering and navigation. Native socket tests check OS behavior. Two real peers establish enrollment, delivery and path behavior. None substitutes for another.

The current execution environment denies required socket creation with `operation not permitted`, so complete native and actual-browser acceptance remain open. Preserve the four native OS/architecture CI targets, a Go-backed browser acceptance path, exact-source results and separate live-network testing. Cross-compilation is not native execution.

Production packages embed generated frontend assets and include the pinned dependency inventory, copied notices, build metadata and SBOM. Repeatability, signature/provenance verification and actual installed-binary checks remain publication gates. No new release is claimed from this local draft. [Distribution](DISTRIBUTION.md) · [Verification](VERIFICATION.en.md)
