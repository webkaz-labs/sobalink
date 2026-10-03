# sobalink architecture

[日本語ガイド](GENERIC.ja.md) · [English guide](GENERIC.en.md) · [Security](../SECURITY.md) · [Development principles](DEVELOPMENT_PRINCIPLES.en.md)

The development build connects devices to their selected application services, with explicit text and file transfer as additional operations. It combines an embedded React UI and a Go agent. Human interaction and agent CLI requests share one application boundary. A peer can exchange authorized messages, batches and service metadata; it cannot operate the local administration API.

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
| `cmd/soba` | Locale selection, foreground/background lifecycle, reviewed per-user startup, local command interface |
| `web` | React source, pinned npm lockfile, generated assets embedded with Go |
| `internal/webui` | Exact loopback management listener, code/session/Origin/CSRF checks |
| `internal/control` | Owner-restricted Unix socket or Windows named pipe |
| `internal/core` | Shared commands, current state, stopped definitions/groups, trust, peers, transfers, service grants, proxy and diagnostics |
| `internal/capacity` | Versioned logical choices and finite resource-budget catalog |
| `internal/identity` | Embedded tsnet adapter and current Tailnet identity |
| `internal/lanlink`, `internal/core/lan.go` | Tailcat adapter, explicit relay setup, pairing, protected state and offline recovery; stock loopback-relay and Core integration have recorded native results; later changes require exact-source acceptance |
| `internal/transfer` | Manifest validation, receive policy, bounded streaming, storage and whole-file retry |
| `internal/ranges` | Compact range/exclusion sets, immutable plans and TCP fallback admission |
| `internal/policy`, `internal/transport` | Current-identity authorization, forwarding and bounded TCP/UDP lifetimes |
| `internal/distribution` | Deterministic package layout, source/dependency metadata, frontend assets and notices |

The legacy `cmd/tsnet-bridge` implementation may remain in the tree. It does not define the new UI, CLI or profile compatibility contract. The repository and module are now `github.com/webkaz-labs/sobalink`; earlier release signatures retain their historical identities.

## Network boundaries

The application selects one backend explicitly. A new profile selects none. A saved selected backend may reconnect when the agent starts. Saved definitions alone do not restart services, and transfer progress never resumes. Separately reviewed outbound startup approvals have the bounded behavior described below. Runtime switching cannot silently exchange backend identities or continue an existing TCP session.

Existing Tailnet mode uses a separate embedded tsnet node. Enrollment uses the official interactive login flow. Current peer identity, Tailnet grants/ACLs and the application grant are checked independently. Service dialing uses the embedded stack instead of an OS-network fallback. OS routes and DNS remain outside this product's management surface.

The Tailcat adapter and Core/CLI commands are implemented. Dedicated LAN UI and the connection graph are integrated locally. Stock loopback-relay and Core two-peer integration have passed four-target native CI. Historical public source `1c5c195` passed the four native jobs and manifest but failed its older browser job before login. The newer `1027f04a` baseline executed formal browser cases with three failures; final-source acceptance is tracked separately. The stock adapter uses an explicit numeric relay endpoint with a TLS certificate SHA-256 pin and matching peer capabilities. It has no default public relay map or DNS bootstrap. Build tags `ts_omit_portmapper,ts_omit_captiveportal,ts_omit_useproxy` exclude port mapping, captive-portal probes and system-proxy fallback; proxy/unsupported backend environment overrides are rejected.

The selected relay may be self-hosted or another endpoint explicitly trusted by the user. Its permitted traffic includes encrypted relayed payloads and HTTPS/ICMP diagnostics to that same endpoint, plus peer direct traffic. This boundary is not strict LAN isolation or zero external traffic. Independent static review and local logic/race tests cover pairing key ownership, durable acknowledgement and UDP multiplexing. The stock Tailcat loopback-relay continuity test passed for the recorded four-platform CI scenario; direct LAN/WAN, actual devices and sleep/wake are separate unverified cases. [Current gate](VERIFICATION.en.md#tailcat-gate)

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

Startup restores the explicitly saved network and saved receive approvals by default, including enabled, unpaused per-peer file autosave. Ordinary saved definitions remain stopped and previous transfers never resume. Separate exact outbound startup selections and private saved-proxy approvals may opt into a future online launch, once after readiness; offline launch suppresses those attempts for the entire process. Changed definitions/groups/network/hostname or target revocation invalidate approval. A finite permission begins afresh on a new process launch, while transport recovery preserves the existing expiry. [Explicit startup and private profiles](STARTUP.en.md) Optional per-user sign-in startup previews and binds the exact OS plan and saved profile to a review token; local-only startup is an explicit alternative. Tailnet logout closes application traffic before contacting the identity backend, distinguishes acknowledged and unconfirmed logout, and then exits. See [startup and logout](LIFECYCLE.en.md) ([日本語](LIFECYCLE.ja.md)).

## Management and peer surfaces

The management listener is always `127.0.0.1:0` at allocation and then the exact resulting host/port. It serves embedded assets and `/api` only. A separately delivered one-time terminal code creates a session; mutating requests require the matching Origin and session CSRF token. URL-based secrets, wildcard listening and remote administration are excluded.

Network failures carry a stable `self.errorCode`; the UI localizes recovery guidance while preserving technical details. The current UI contract is [web/API.md](../web/API.md), backed by TypeScript in `web/src/api.ts`. The UI cannot make its own trust decision authoritative: Go validates identity, expiry, range, receive policy and path safety on every relevant operation. Request IDs deduplicate repeated commands within bounded process-local history; they do not claim durable transaction recovery.

The peer API has separate routes for hello, explicit messages, transfer offers/status/content and minimal service discovery. Transport-derived identity is rechecked against current peer state. Peer routes do not accept local management commands, filesystem destinations or arbitrary URLs. Reserved endpoints are discovery `54543`, peer API `54544` and pairing `54545`; local management/control and backend-internal endpoints are also excluded from generic service sharing.

## Trust, batches and storage

Trust, pairing, autosave and service sharing are distinct scopes. LAN invitations last 1–600 seconds and are bound to one recipient key. Sealed bootstrap authorizes only the verified transport role; server and client role keys stay distinct. Atomic private-state persistence precedes pairing success. See the [LAN command and recovery guide](LAN.en.md). A display name never supplies identity. A receive policy binds a backend, exact peer and trust generation to an absolute local destination. Revocation closes work and invalidates the previous generation. Persisted receive policy becomes effective only after its atomic configuration write succeeds.

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

The initial logical defaults are 256 entries per batch and 1 GiB per file/batch, adjustable or explicitly unlimited within finite storage/metadata budgets. Local staging defaults to 600 seconds or an explicitly selected alternative. Browser uploads stage into private local storage before offering a batch to the peer. Local upload progress, remote acceptance, file saving, partial completion and completed delivery remain separate states. No automatic file opening, execution or clipboard synchronization follows receipt.

Retry starts each unfinished file again from byte zero. There is no byte-offset resume, durable progress journal or restart resume. Saved data survives cancellation and process shutdown; active batch state does not. A new batch after restart may create unique-name duplicates, which the user must review.

The embedded relay forces a fresh admission check through a two-minute connection lease. Removed relay sessions may remain for that lease, and temporary bootstrap admission plus a lease can last about four minutes from initial bootstrap. Application revocation closes its own flows immediately. The native fixture passed its 130-second existing-TCP test across a real relay lease on all four targets at the recorded commit; this is a bounded scenario, not a general TCP continuity guarantee.

Peer Pause stops messages/files and cancels active sends; resuming requires reselecting those files. It does not revoke separate service grants. `soba start --offline` opens management without reconnecting a saved backend, so pairing state can be revoked or repaired after startup failure.

## Compact service plans

TCP sharing uses a compact inclusive range/exclusion plan with one userspace fallback dispatcher. Shared range port N maps to the same exact loopback port N; one shared port can explicitly map to a different local application port. A broad range is a real future grant: applications started later on effective ports are reachable while the permission remains active.

Plans require explicit current peer IDs, a current embedded-node address, a specific protocol, valid ports in 1–65535 and a reviewed lifetime. Shares default to a finite hour, support other positive whole-second durations, and allow explicit `until-revoked`. Outbound connections default to `until-stopped`. The logical share-peer default is 32, adjustable or explicitly unlimited; it is not a hard protocol maximum. System reservations and active internal endpoints are removed from effective scope. Overlap, collision, stale identity and exhausted capacity fail before an unsafe plan becomes usable.

UDP shares, local connections and optional proxy listeners require materialized resources and share an adjustable finite budget, initially 64 listeners. TCP shares do not consume one listener per covered port. Client connections may explicitly remap sorted effective remote ports to consecutive local ports, but shared ranges retain same-port targets. Each accepted flow checks current identity, grant and deadline before loopback dialing.

Discovered services carry bounded metadata only for eligible peers and active grants, including explicit until-revoked shares. Rotating bounded refresh passes avoid permanently ignoring peers after an arbitrary first group. Discovery and local pages retain separate finite byte/entry budgets. A selected observation is revalidated before connecting. Ordinary Tailnet services can be connected manually without a peer sobalink API. All service state labels application health `unverified`.

Expiry, stop and peer revocation close tracked flows. They do not cancel a remote application job, retrieve delivered content or extend a deadline. Saved definitions are inert until explicitly started or covered by a separately reviewed outbound-only startup approval; inbound shares cannot opt into startup.

## Capacity, saved workflows and local tools

The versioned capacity policy separates logical `default`/`limited`/`unlimited` choices from adjustable finite resource budgets. The policy file has its own bounded envelope so saved-profile growth cannot require an unbounded read to discover its limit. Profile, LAN pairing, messages, transfer metadata/spool, listeners, TCP/UDP flows, queues, discovery and pages use the corresponding budgets. Lower counts do not remove records or existing pairs; finite storage cannot be lowered below current saved data. Message retention is only a cleanup recommendation until an explicit revision-bound preview/apply succeeds. [Capacity contract](CAPACITY.en.md)

Stopped service definitions and groups are separate from live grants. Saving and importing can run under an exclusive offline profile lock without starting a network or loading credentials/transfer state. Private exports contain service/group definitions only. Review tokens bind replacement/deletion to current data. Group start rolls back only newly started members on failure; task ownership and renewable 30-second leases limit abandoned local command scopes without stopping unrelated work. `stop-shares` explicitly stops all inbound shares, including task-owned ones, while preserving outbound connections and node connectivity. [Saved workflow contract](SAVED_SERVICES.en.md)

The optional SOCKS5 tool authenticates clients and supports only TCP CONNECT to reviewed peer/port targets through the chosen userspace backend. Each dial rechecks current identity. Ordinary starts are ephemeral; explicit saved/generated profiles use a separate protected, revision-bound store, excluded from portable exports and routine state. Private reveal is an explicit operation. Durable peer-revocation epochs prevent saved approvals from reviving after restart; failed persistence stops related work and suppresses startup until storage is repaired. It has no OS target dialing/DNS fallback, BIND or UDP ASSOCIATE. Its listener shares resource accounting and cannot be exposed through a service share. Diagnostics optionally open one explicit approved TCP connection, preserve the last failure code/time in runtime, and leave application/TLS/host-key health unverified. [Proxy and diagnostics](PROXY_DIAGNOSTICS.en.md)

Bulk remote administration, remote filesystem browsing, broadcast delivery and a durable offline outbox are not implemented. A broader resource-management foundation belongs to a separate milestone; ordinary service transport does not imply those capabilities.

## Connection graph scope

The lightweight SVG connection view is integrated locally. Synthetic hosted preview `d52a975` passed nine layout captures; final Go-backed browser acceptance remains pending. It depicts this device and its known peers from actual state observations, with observed contact and trust kept separate from path type. It must not invent peer-to-peer full-mesh links, rates or direct/relay telemetry. A missing observation is Unknown, and saved pairing metadata is not proof that a peer is online.

## Evidence and release boundary

Unit tests and mock backends establish specific logic properties. A DOM test checks component behavior. A real local browser checks rendering and navigation. Native socket tests check OS behavior. Two real peers establish enrollment, delivery and path behavior. None substitutes for another.

Public baseline `1027f04a` executed 23 formal browser cases in [run 37095653635](https://github.com/webkaz-labs/sobalink/actions/runs/37095653635): 20 passed and three failed. All four native target jobs, including race/vet, isolated relay fixtures and packages, and the manifest job passed; the browser failure keeps the run from passing overall. Historical `1c5c195` passed all four native jobs and manifest but failed its older browser job before login. Earlier UI snapshot `1c977c9` passed 274 DOM tests and build checks locally, with 28 browser cases enumerated only. Restored Web workflows `c9a30e7` and assets `04085f1` passed 389 DOM tests, eight capture-safety checks, typecheck and reproducible builds; their new browser cases were authored and syntax-checked, not executed. Each source needs its own completed native/browser evidence; none of those partial or earlier results establishes final-source acceptance. See [verification by source](VERIFICATION.en.md). Cross-compilation is not native execution, and loopback fixtures do not establish actual-device, direct LAN/WAN/NAT, native IME or sleep/wake acceptance.

Production packages embed generated frontend assets and include the pinned dependency inventory, copied notices, build metadata and SBOM. Repeatability, signature/provenance verification and actual installed-binary checks remain publication gates. No new release is claimed from this local draft. [Distribution](DISTRIBUTION.md) · [Verification](VERIFICATION.en.md)
