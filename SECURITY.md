# Security boundaries

[日本語](docs/SECURITY.ja.md) · [Architecture](docs/ARCHITECTURE.md) · [Verification](docs/VERIFICATION.en.md)

sobalink is experimental software. Its controls constrain local management, peer transfers and service grants. It is not an OS sandbox, a general VPN, a remote administration service or proof that a target application is safe. Published alpha.2 has completed signed distribution and four-target installation checks; the newer route implementation is still under verification. See the [exact-source record](docs/VERIFICATION.en.md) before relying on a claim.

## Local management stays local

- The React UI and management API bind only to an OS-selected ephemeral TCP port on **exactly `127.0.0.1`**. There is no configurable wildcard, LAN, Tailnet or hostname bind
- Requests require the exact printed Host and a loopback source. Origin and Fetch Metadata checks reject cross-origin management requests; mutations require the exact same Origin
- The local terminal supplies a cryptographically random one-time code, valid for five minutes. The code is never embedded in a URL. Successful use consumes it; a fresh `soba ui` code replaces the previous unused code
- The browser receives an HttpOnly, SameSite=Strict session cookie and uses a session-bound CSRF token for mutations. The UI uses in-memory session state and same-origin APIs
- The loopback URL is HTTP. These local checks do not provide HTTPS transport to another device and must not be exposed by a proxy or a service share
- Responses disable caching and framing and carry a restrictive content security policy. Production JavaScript, styles and fonts are embedded; there is no runtime CDN
- Bounded requests, streams, headers and concurrency limit resource use. Failure to create a socket is a failure, not a reason to broaden the bind address
- Local session and command JSON request bodies have a five-second native socket read deadline. Native browser upload bodies have a rolling 30-second no-progress read deadline, refreshed only when bytes arrive. The existing total staging deadline still begins after reservation and is not extended by progress; explicitly unlimited staging removes that total deadline but not the idle deadline
- Local IPC is restricted to the owning OS user and dispatches to the same application core. A process already running as that user can still access their files and authority; these controls do not isolate a compromised local account

The peer API is a different handler. A peer cannot use it to change networks, trust, receive paths, service grants, sessions, settings or arbitrary local files.

## Explicit network and identity

A new profile selects no network. Existing Tailnet mode enrolls a separate embedded tsnet node through the official interactive flow; it does not import a system Tailscale session, edit OS routes/DNS, create a kernel tunnel or install a privileged service.

Outbound service connections use the embedded userspace stack and current peer identity. The application does not fall back to ordinary OS service dialing or OS DNS resolution when a permitted peer is unavailable. Application target authorization, Tailnet grants/ACLs and application credentials remain separate controls.

Tailcat setup uses an exact numeric bootstrap relay and TLS certificate SHA-256 pin. Pairing rejects capabilities for a different bootstrap relay. The unreleased route implementation preserves that original anchor and the paired identity/role keys while adding up to three explicitly prepared candidates. Peer offers are recipient-encrypted and authenticated to the existing pair, including directional role-key bindings, sequence and lifetime/expiry. A received offer never grants local route permission: exact candidate approval is separate, with an explicitly reviewed finite or `until-revoked` lifetime, and is never automatically renewed. Version 2 removes the earlier 30-day policy ceiling; old version-1 finite proofs keep their exact deadlines and validation limits. [Protocol and persistence](docs/ROUTE_RECOVERY_DESIGN.en.md)

Tailcat permits peer direct traffic, encrypted payload through explicitly configured/approved relay candidates, and HTTPS/ICMP diagnostics to those exact relay endpoints. The normal single executable retains direct UDP transport, which may use public peer paths. `local` classifies a relay address; it is **not strict LAN-only traffic, zero external contact or an egress sandbox**. No strict-egress product mode or helper binary is included in this prerelease scope. No default public relay map, DNS bootstrap or arbitrary fallback is authorized. Required build tags omit port mapping, captive-portal probing and system-proxy support; unsupported proxy/backend environment overrides fail closed. [Integration gate](docs/VERIFICATION.en.md#route-recovery-gate)

Backend selection is explicit and process-scoped. A mode switch is not an automatic recovery mechanism. Direct, relayed and reconnecting are different observations; unknown path evidence must stay unknown. Neither a direct/relay transition nor restarting a backend guarantees survival of an existing TCP session.

Display names and addresses are not durable identity. Incoming identity is derived from the authenticated transport, then checked against current state. Peer removal, address reassignment, expiry or trust revocation must invalidate the relevant authorization. Tailcat pairing capabilities, pre-shared keys, invitation tokens and private state must never appear in status, logs, discovery or distributed examples.

### LAN pairing and relay admission

A pairing invitation is scoped to one recipient public key and lasts 1–600 seconds. The embedded relay uses a sealed HTTPS bootstrap to verify the invited server identity and one-time token before temporarily admitting the required transport role; unknown keys receive no blanket exception. Server/client transport role keys are distinct and bound to the paired identity. Secret pairing state is saved atomically before success is acknowledged. Pairing does not grant application trust or autosave.

Application revocation closes authorization and tracked flows immediately. The embedded relay separately rechecks admission through a two-minute connection lease: an already admitted relay session can remain for up to two minutes after removal, or up to about four minutes from an initial bootstrap when temporary role admission and an existing lease overlap. This residual relay session is not permission to reopen application flows. External relay admission policy remains that relay operator's responsibility.

Secret command payloads use bounded `--json-file` or non-terminal `--stdin` input rather than literal arguments. Protect the input file and explicit invitation-creation response; parsing errors do not echo the payload.

An uncertain pairing reply is not automatically retried; the other side may already have committed. Check and revoke that peer there before creating a new invitation. If remote pairing succeeded but the local save failed, revoke the remote approval first. A failed durable revocation stops the LAN backend rather than reporting success. `soba start --offline` keeps local management available for repair without starting the saved backend.

### Prepared-route state (unreleased)

Additional relay configuration is edited only with the backend stopped (`soba start --offline`), then used after restart. It does not revoke pairs or replace the original bootstrap relay. Prepared candidates require at least private LAN state version 2; the new explicit-lifetime route state uses version 3. Older binaries reject unsupported state versions. Authenticated route updates and their nested saved route records use protocol/state version 2; these numbers are separate from the enclosing LAN file version. A legacy pair without an applied offer keeps its original singleton behavior. Creating an offer does not silently migrate its outgoing permission.

Local review binds the exact received envelope and chosen candidate IDs. Higher sequence numbers cannot renew grants, change application permissions or re-enable revoked routes. Export/import uses the local management API and private files or copied text; it does not automatically send external messages, run commands or execute received content. Removal from this device's configured set does not revoke a peer's previously received offer; withdraw that peer's route approval explicitly as well when needed.

Durable sequence/proof records survive expiry and withdrawal. Uncertain private-state persistence blocks further route changes and outgoing reconnects rather than claiming rollback or success. Failed route-revocation persistence also latches Core recovery during offline management before any new node can reactivate saved authority. Do not restore an older profile backup as a downgrade workaround: local counters cannot detect every whole-profile rollback. Route expiry/revocation closes the affected outgoing generation; pair revocation remains the way to end the full relationship. Existing TCP and arbitrary application bytes are never replayed. [Recovery procedure](docs/LAN.en.md#prepare-another-route-unreleased)

Until-revoked route authority is not a perpetual-availability promise. Generated local relay certificates currently expire after 365 days; certificate renewal and changed pins require explicit review and remain separate from route permission. Issued/received protocol versions and counters cannot regress, and old finite deadlines cannot become indefinite merely through migration. A withdrawal export is pending delivery: remote approvals change only when the intended recipient verifies and applies it. Applied withdrawal, like local revoke, stops affected work and latches recovery if durable removal fails.

## Trust and receiving

Trust for messages/file offers binds an exact verified peer in the selected backend. Service sharing is a separate grant. The receiving peer must authorize the sender; one device's trust selection cannot grant authority on another device.

- Each incoming batch requires explicit receiver acceptance by default
- Optional autosave binds the backend, exact trusted identity, current trust generation and an explicit absolute destination. Reviewed receive settings are reflected after private profile-file replacement, including a replaced result whose durability is uncertain.
- Autosave can accept future batches while enabled. Disabling it restores batch consent. Peer Pause blocks messages/files and cancels active sends, which require reselecting files after unpausing; separate service grants are unaffected. Trust renewal or an identity change does not revive an earlier generation's grant
- Neither acceptance nor autosave permits overwriting existing files, automatic opening, execution, shell evaluation or clipboard synchronization
- Receiver destinations stay local. Peers provide portable relative paths, never trusted local absolute paths
- The receiver rejects traversal, absolute/drive/device paths, ambiguous separators/names, symlinks, reparse points and unsupported file types. It does not preserve executable attributes
- A file is finalized only after its size and SHA-256 match the accepted manifest. Exclusive creation and a unique final name prevent existing-file replacement. Temporary files and source paths are not exposed through the peer API
- Initial logical defaults are 256 entries per batch and 1 GiB per file/batch. They can be adjusted or explicitly unlimited; finite metadata, staging inventory, pending-offer, storage and concurrency budgets still bound admission. Local staging defaults to 600 seconds with an explicit unlimited option. [Capacity policy](docs/CAPACITY.en.md) separates those choices from immutable safety checks
- Browser upload progress describes local staging. Remote acceptance and saved acknowledgement are separate states

Retry is a whole-file operation for unfinished files in the current session. A saved file acknowledgement is idempotent while that batch exists. There is no partial-byte resume or durable restart-resume journal. After restart, a new offer may produce a uniquely named duplicate; users must review previously saved output.

The receiver accounts only identified owned staging in its private index (legacy v1 is readable; v2 is written). A separate bounded durable retirement guard protects transitions that discard preparation or root evidence. The absence of a retirement guard is normal; a missing index on an existing profile requires the explicit legacy review below. It is recorded before the index changes and remains until post-save identity checks pass. An uncertain save or changed binding keeps receiving blocked across restart; receiving alone is blocked. The guard is not ownership proof or deletion authority. Recovery does not guess ownership, broadly scan destinations, or delete saved output. Zero-payload internal staging is safely retired or remains accounted, and control metadata does not accumulate per transfer. The preparation intent and existing three index writes preserve compatibility and preparation ordering but are not by themselves the retirement safety guarantee. No atomic compare-and-delete is promised across profile and destination filesystems against same-account mutation. Unknown, corrupt, unavailable or replaced state/destinations, unsafe entries and incomplete or over-budget inventory block receiving. This private index and guard are not a peer/API/export contract; do not edit them manually.

For an existing profile with no index (for example, one created by an older version), receiving stays blocked until the user reviews previous default, per-peer and manually selected destinations, unfinished staging and saved output, resolves unfinished old receives so no untracked partial data remains, then explicitly confirms. Keep saved files; if an old location is unknown or unavailable, receiving must stay blocked until you can review it. `soba receive recovery confirm` is a preview; `soba receive recovery confirm --reviewed` records the review and initializes the missing index. The flag attests that this review and cleanup are complete; it does not delete files. It cannot discover arbitrary old staging that was never recorded, so do not confirm while those partials remain. If manually removing confirmed leftovers, restrict deletion to the exact known unfinished staging files; never broadly delete ordinary files or saved output. See [capacity and recovery](docs/CAPACITY.en.md#receiver-recovery).

Atomic private-state replacement can publish a new file while durability remains uncertain. Saved-state owners reconcile memory to that file and report the uncertainty; an error does not imply rollback. A new service start with uncertain persistence stops before activation. Saved startup approvals must match current revocation epochs and require explicit renewal after revocation. Incoming-message retry uses the same ID and reconciles uncertain history before durable acknowledgement; ordinary duplicates skip history I/O. Public errors preserve machine types and redact private paths.

Private writer ownership, recovery bounds and preservation of unknown legacy temporary files are described in [bounded private-state persistence](docs/ARCHITECTURE.md#bounded-private-state-persistence).

Cancellation, expiry and revocation close tracked work. They do not delete successfully saved files, retract sent content or cancel jobs launched inside a remote application. Forgetting history does not delete received files.

## Service sharing and discovery

A share requires a current embedded-node address, an exact numeric loopback target, explicit protocol and port ranges, explicit current peer IDs and a reviewed lifetime. Shares default to one finite hour; other positive whole-second durations and explicit `until-revoked` are supported. The initial share-peer choice is 32, adjustable or explicitly unlimited within separate finite storage/resource budgets. No wildcard audience, subnet route, arbitrary LAN gateway or internet forwarder is implicit.

TCP shares use one compact fallback dispatcher over ports 1–65535, excluding reserved/internal endpoints. Ranges use same-port mapping: shared port N targets loopback port N. One shared port can explicitly map to another application port. They do not allocate an OS listener for every port in a range. UDP shares, local connections and optional proxy listeners require individual resources and share an adjustable finite listener budget, initially 64. Local connection ports must be 1024–65535. Conflicts and exhausted capacity fail without silently remapping or widening permission.

**A range grants future use of all effective ports for its lifetime.** An application started later inside that range becomes reachable to the permitted peers. Narrow the range and use explicit exclusions. The discovery endpoint `54543`, peer API `54544`, pairing endpoint `54545`, current local management/control endpoints and backend-internal endpoints are not general service-share targets. Reserved endpoints cannot be made shareable by selecting a larger range.

Every accepted stream is authorized against current identity, scope and expiry before dialing its loopback target. Stop, expiry and revocation invalidate tracked connections. Saved definitions alone never authorize a restart. Separately reviewed startup approvals can start only their exact outbound definitions after an online process launch; inbound shares remain manual. Offline launch suppresses startup approvals for that process. Changed scopes or revoked targets require renewed review. See [explicit startup](docs/STARTUP.en.md).

Discovery is opt-in metadata for current, authorized, active shares, including explicitly non-expiring shares. Refreshes use rotating bounded peer passes; response/page budgets bound metadata without treating the first pass as the entire peer set. It must not expose unapproved peers, local destinations, filesystem paths or credentials. Discovery results are revalidated before use and always label application health as unverified. An ordinary Tailnet service can be connected manually without sobalink on the target.

Keep the target application's authentication, TLS/SNI, SSH host-key checks and origin controls. Local forwards may be used by other local processes; loopback is not per-process authentication. A remotely accessible application may see the bridge's loopback connection, so it must not treat that source as sufficient authorization.

## Capacity and optional local tools

Logical default/limited/unlimited choices never weaken current-identity checks, reserved-port exclusions, non-overwrite receiving, exact loopback management or netstack-only outward service transport. Finite profile, LAN-state, message, staging, metadata, listener, flow and queue budgets remain separately adjustable. Lower logical counts govern new admission without removing saved records. Storage budgets cannot be lowered below saved data. Message retention requires a separate revision-bound cleanup preview/apply; neither incoming messages nor a policy change silently delete history.

An optional authenticated SOCKS5 listener supports TCP CONNECT only, to reviewed current peer identities and exact allowed ports through the selected userspace backend. It has no BIND, UDP ASSOCIATE, OS DNS or OS target-dial fallback. Ordinary proxy starts use runtime-only credentials and scopes. Explicit save/generate operations can persist a reviewed scope and private credentials in a separate protected store; portable exports, routine state, logs and dry runs exclude them. Private-file reveal requires an explicit reviewed operation. Future launch is separately opted into; scope/identity changes and revocation invalidate approval. Lifecycle changes close tracked flows. The local listener and management endpoints cannot become share targets. `soba doctor --service ID --tcp` opens one explicitly approved TCP target without sending application data; its result is not an application, TLS or host-key check. See [proxy and diagnostics](docs/PROXY_DIAGNOSTICS.en.md).

## Private state and diagnostics

The private state directory contains node identity, trust and local configuration. Do not sync or publish it, use it as a shared attachment, or include it in packages. Backups inherit its sensitivity. A new state directory is a new identity, with separate enrollment and trust decisions.

Logs, issue reports, screenshots, sample configuration, README examples and distribution metadata must use generic fixtures. Remove codes, auth URLs, peer capabilities, private keys, local paths, private endpoints and unrelated personal context. Production packaging rejects provenance gaps and includes dependency notices rather than copying runtime state.

There is no unrequested application launch, remote administration/command endpoint, remote filesystem browser, broadcast send, durable offline outbox, OS-wide traffic interception or clipboard watcher. An explicit `soba task` runs the exact local command and arguments with owned service leases; it grants no remote shell API. This does not prevent a separately authorized remote application from running a job; control and cancellation of that job remain the application's responsibility.

## Verification and disclosure

Retain native Linux x64/ARM64, macOS ARM64 and Windows x64 race tests, vet, frontend tests and actual packaged-binary checks. Signed artifacts, provenance and repeatable builds are separate from OS code signing/notarization and real-device acceptance. Do not disable signature, identity, digest or OS security checks to make an installation pass.

Report security issues privately through the repository's available private reporting mechanism; do not put credentials or a working exploit against a private endpoint in a public issue. If private reporting is unavailable, ask for a private channel before sharing sensitive details. There is no claim of a completed external security audit or stable support for this draft.

### Route permission save failures

Route reductions stop the affected outgoing generation before persistence and block current-process recovery on failure; new grants wait for confirmed saving. Failed disk writes cannot guarantee durable revocation across a new process. Stop soba and inspect/reconcile saved approvals before restarting after a route recovery error. See [route recovery failure boundaries](docs/ROUTE_RECOVERY_DESIGN.en.md#persistence-revocation-and-failure).
