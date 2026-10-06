# Local browser API

The React client uses only same-origin `/api` requests. The Go host is responsible for numeric-loopback binding, session cookies, Origin/Host checks, CSRF enforcement, authorization, identity pinning, deadlines, request deduplication and all filesystem/network policy. No successful browser preview proves a remote application works.

`src/api.ts` is the authoritative frontend TypeScript contract. Machine field names are stable across Japanese and English.

## Session and state

- `GET /api/state` returns `State`: `csrfToken`, `self`, `peers`, `messages`, `transfers`, local `services`, local `shares`, optional discovered `availableServices`, `servicePresets`, effective `limits`, `settings`, and optional `lan` readiness/selected-relay state
- `POST /api/session` accepts `{code}`; a successful response may return `{csrfToken}`. The client then fetches state. Codes are not retained in browser storage
- `401 {code:"unauthenticated"}` returns the client to local-code entry and clears in-memory drafts
- All responses are JSON. Errors use `{code,message?}` or `{error:{code,message}}`. The client localizes recognized codes. Unknown failures receive localized next-step guidance, with the unchanged backend explanation available under Technical details
- Mutation success is `{ok:true,result?:{...}}`; no state is optimistically marked sent, received, connected or saved
- State arrays remain complete; standalone list endpoints do not truncate the snapshot. State is polled every three seconds while visible, with stale-request cancellation and generation guards. Loss of contact retains clearly stale information and disables new sends/starts

[Local Web paths and lifecycle guide](../docs/WEB_CONTROLS.en.md) ([日本語](../docs/WEB_CONTROLS.ja.md)).

## Commands

`POST /api/command` accepts `{requestId,name,payload}` with `X-CSRF-Token`. The exact typed payloads are in `CommandPayloads`.

- `message.send`: `{peerId,text}`; text is explicit; its UTF-8 byte limit is the lower of effective `logical.messageBytes` and finite `resources.messageTextBytes`
- `peer.trust`: `{peerId,trusted}`; this is local permission for the exact verified identity, not permission granted by the remote device
- `discovery.refresh`: `{peerId?}` returns `{services,observations,partial}` and queries service metadata only. A specified peer rechecks its current identity; this does not restart transports. The Web **Check shared services** action uses this command
- `peer.reconnect`: `{peerId}`; explicitly rebuilds transports for active services with that peer. It does not renew trust, autosave or service grants
- `peer.autosave`: `{peerId,enabled?,paused?,directory?}`; omitted fields preserve independent settings under the Core command lock. Enabling without a directory uses the saved peer directory or configured receive directory. Policy, directory and pause are persisted once before publication. A failed enable/directory/unpause save preserves prior state; failed disable still removes runtime auto-accept, which a later partial save must not restore. Durable disable remains unconfirmed after a save error and must be retried before restart. Pause stops messages/file offers, cancels outgoing batches and removes staging copies; original files must be selected again after resume. Separate service grants remain active
- `transfer.accept`: `{transferId,destination?}`; explicit per-batch directory required when no configured receive directory exists
- `transfer.decline`, `transfer.cancel`, `transfer.retry`, `transfer.forget`: `{transferId}`. Forgetting terminal history preserves original and received files. For outgoing batches it also deletes retained temporary copies and removes retry capability; the UI reviews this consequence before discarding failed or declined outgoing batches
- `service.connect`: `{name,peerId,serviceId?,serviceRevision?,network,ports,excludePorts?,localPort?,loopbackHost?,lifetime?,ttlSeconds,purpose,discoverable:false,backend?,replaceId?,expectedRevision?}`
- `service.share`: `{name,peerIds,network,ports,excludePorts?,localPort?,loopbackHost?,lifetime?,ttlSeconds,purpose,discoverable,backend?,replaceId?,expectedRevision?}`; one exposed port may map to a different application port. Shared ranges retain same-port mapping; reserved control ports are excluded
- `service.config`: `{id}` returns `{configuration,revision,active}` to authenticated local management only. The full saved configuration is not peer discovery metadata. Create rejects existing names; copy starts a new name. Replacement requires a stopped `replaceId`, its complete `expectedRevision` and the reviewed `backend`; stale revisions, another existing name and backend changes are rejected before changing saved settings
- `service.ports`: `{id,expectedRevision,fromPort?,count?,attempts?}` explicitly checks a stopped saved outbound connection's loopback binds. The saved backend, full revision, protocol, address family, ports and exclusions remain fixed. Only a real OS address-in-use result permits searching alternatives; permission, family, capacity and unknown failures stop separately. `fromPort` defaults to 49152 and accepts 1024–65535. Zero/omitted count and attempts follow finite `portProposalResults`/`portProposalAttempts` budgets; positive requests must fit them. Total bind work uses `portProposalBinds`, the checking deadline uses `portProposalSeconds` (5 seconds by default, independent of service lifetime), and simultaneous sockets fit remaining `materializedListeners`. Candidate windows skip application/backend reserved ports. All transient sockets close before returning; no forwarding, grant or persistence occurs. This observation bypasses request-result caching; even a repeated request ID performs a fresh check, so raised result budgets cannot accumulate in the mutation deduplication cache. Results contain the complete `configuration`, `revision`, `conflictPort`, `effectivePorts`, `checkedAt`, `fromPort`, requested budgets, performed `attempts`/`bindChecks`, `stopReason`, `reservation:false`, bilingual `nextSteps`, and `proposals:[{localPort,localEnd}]`. Consecutive local ports map to ascending effective remote ports after exclusions, matching `service.connect`. An empty successful observation has `code:"listener_proposals_exhausted"` and `proposals:[]`, not a claim that every high port is occupied. The Web action is available for stopped saved forwards: opening reads source data only; an explicit check uses a fresh request ID and an abort signal outside mutation retry guards. It validates complete results, shows bounded exhaustion without usable candidates, and pages large candidate lists locally. A selected port enters the existing stopped-definition editor only after a same-revision reread; full reviewed `service.save` and reviewed start remain separate. Reload discards the selected candidate; late results after close/source change/stale/active state are ignored. Proposals are observations, not reservations. A direct start applies one through a separate explicit `service.connect` replacement using its saved revision; the Web may instead save the stopped definition before its separate reviewed start. Actual binding rechecks every port. CLI: `service ports NAME_OR_ID`, then `service restart NAME_OR_ID --local-port PORT --expected-revision REVISION`. See [English](../docs/PORT_PROPOSALS.en.md) / [日本語](../docs/PORT_PROPOSALS.ja.md) for taxonomy and check budgets.
- `service.stop`: `{id}`
- `network.configure`: `{mode:"none"|"tailnet"|"lan",hostname?,lan?,rotateCertificate?,lanPolicy?:{mode,prefixes}}`; LAN is either `{kind:"host",address}` for an exact private local listener or `{kind:"relay",address,certificateSHA256}` for a trusted numeric relay. Changing a running network/hostname requires stopping and restarting the process, which the UI explains
- Host setup preserves a valid saved certificate for an unchanged IP, including a port-only change. IP change or unusable certificate requires explicit `rotateCertificate:true`; saved pairs and active engines block replacement. Public `lan.certificate` exposes `{state,notBefore,notAfter}` only, with `valid`, `expiring` (within 30 days), `expired`, or `not-yet-valid`. No private certificate material is returned
- `lan.policy.get`: `{}` returns `{mode,prefixes,editable,restartRequired}`. `lan.policy.set`: `{mode:"trusted-relay"|"allowed-lan-destinations",prefixes:string[]}` requires an offline engine. Allowed mode requires explicit canonical private/ULA/loopback prefixes covering selected and prepared relays; trusted mode requires no prefixes. Initial `network.configure.lanPolicy` saves the same policy atomically with relay setup before starting. Status exposes `lan.policy`. A failed policy save does not establish a durable restriction and latches recovery for an existing store
- `lan.addresses` includes optional `prefix` on each read-only `{interface,address}` choice. Suggestions never automatically enable a destination range; prefix membership is not physical interface/VPN isolation
- `network.logout`: `{}` is exposed through an explicit Tailnet impact review. It stops local traffic and requests backend logout before application exit; the Web result distinguishes acknowledgement, cleanup uncertainty and an unconfirmed result without claiming process exit or administrator node removal. It uses the authenticated command boundary directly so the response is not obscured by an immediate state refresh. Startup registration has no Core command: the Web guide provides the existing CLI preview/apply instructions and truthfully reports OS registration status unavailable
- `application.stop`: `{}` returns `{state:"stopping"}` before the agent closes the local UI, hosted relay and active work. The UI reviews impact and does not claim shutdown is complete from this acknowledgement
- `network.login`: `{refresh?,qr?}`; `network.login.status`: `{qr?}`. Both return `{state:"waiting"|"connected"|"approval-required",authUrl?,qr?}` only on explicit local requests. The client polls status only after an explicit waiting result and stops on dismissal, completion or an error. It aborts pending reads when closed. Existing links are reused unless the user selects a new-link request. Connected or approval-pending state does not restart login. Only bounded official HTTPS `login.tailscale.com/a/...` links are rendered; links are never opened automatically or persisted. Optional QR data is a server-generated boolean matrix rendered locally, without a third-party image service
- `settings.update`: `{locale?:"auto"|"en"|"ja",theme?:"system"|"light"|"dark",receiveDirectory?}`

Discovered `availableServices` carry activation-scoped opaque IDs, purpose, compact port ranges, checkedAt, expiry/lifetime and a metadata revision. Selection preserves `serviceId` and `serviceRevision`; the latter encodes the reviewed metadata and is not a bearer credential. Immediately before a new live connection, the Web performs a targeted `discovery.refresh`. It uses the fresh revision only when the originally reviewed grant ID, purpose, protocol, ports and lifetime match and expiry has not shortened. A long-open form can therefore proceed without an arbitrary UI deadline. A changed scope, missing grant or failed refresh stops before connect and requires review/recovery. Observation-time refresh and equal/later expiry within the same lifetime do not change the original reviewed scope; different grants, purpose, protocol, ports, shorter expiry or changed lifetime require explicit reselection. Offline saves/copies may retain historical metadata without starting. Bound purpose/ports are read-only, and manual mode explicitly clears both reference fields. A discovered connection sends the chosen `serviceId`; the backend must revalidate the exact current observation before starting. Manual connections omit it and remain available for ordinary Tailscale nodes. Live-start previews use the remaining effective `resources.materializedListeners` budget. Saving a stopped definition can exceed current listener availability because it starts no listeners. The compatibility fallback for a snapshot without effective limits is 64 listeners. TCP sharing previews retain compact ranges; UDP shares consume listeners. The backend remains authoritative about exclusions, collisions, mapping and supported protocols. Outbound connections default to `until-stopped`; shares default to a finite hour and offer an explicit `until-revoked` choice. Finite durations use positive integer `ttlSeconds`; no-expiry choices use zero. Saving definitions alone never authorizes startup; optional launch behavior requires a separate reviewed startup approval.

## Explicit TCP diagnostics and optional scoped proxies

Each active local TCP service offers an expandable **TCP transport check**. `diagnostics.run {serviceId,probeTCP:true,port}` opens and closes one connection to one effective approved port. A range requires an explicit single-port choice. Results show the stable code, UTC time, TCP transport result, `application:"unverified"`, and localized next steps. Local `lastFailure` code/time/next steps stay visible after recovery or stop until the process ends or the definition is deleted. Viewing a service performs no probe.

**Preferences → Advanced connections** contains the optional SOCKS5 controls. `proxy.list {}` shows current runtime proxies; `proxy.stop {id}` closes a proxy and its tracked connections without claiming to cancel work already accepted by an application. Authoritative `state.proxies` updates remove expired or stopped listeners as the normal state poll observes them; the list also has an explicit refresh.

`proxy.preview {scope}` takes `{name,backend,loopbackHost,localPort,lifetime,ttlSeconds,targets:[{peerId,port}]}`. Target controls select current peer IDs and exact TCP ports. The review shows the normalized name, backend, loopback endpoint, lifetime, every peer ID and the host/port the application may use. Preview opens no listener. Only after a separate review acknowledgement does the UI reveal private runtime username/password inputs. `proxy.start {scope,expectedRevision,username,password}` uses that exact review and the existing local Host/Origin/session/CSRF boundary. It never starts automatically.

The ordinary ephemeral path requires both credentials to contain 1–255 UTF-8 bytes. They are held only in private input controls until serialization, then cleared before awaiting the request. They never enter the shared command-retry cache, browser local storage, history, logs or profile exports. Optional private persistence is a separate reviewed path below. Back, close, unmount, scope change, successful submission and failed submission erase the private inputs. A changed scope or failed start requires fresh review and credentials; an uncertain start also requires a current proxy-list refresh before another review. Errors display stable localized guidance, never response text that could echo a credential.

Only numeric loopback listeners and SOCKS5 TCP CONNECT with username/password authentication are supported. BIND and UDP are unsupported. For ordinary ephemeral proxies, network loss, peer revocation or identity change, expiry, logout, listener failure and process exit stop the runtime; restarting requires a fresh review and runtime credentials. Saved proxy recovery follows its separately reviewed scope and original expiry. TCP reachability and listener readiness do not prove application compatibility or authentication.

## Capacity and explicit history cleanup

`policy.config {}` returns `{version:1,requested,effective,catalog,adjustable,usage,revision}`. Both policy objects have `{version:1,logical,resources}`. Each choice is `{mode:"default"}`, `{mode:"limited",value:positiveInteger}`, or logical-only `{mode:"unlimited"}`. Resources stay finite. Catalog values expose defaults and units; only keys marked adjustable are offered. Common file/message choices appear first, with retention and advanced budgets expandable.

`policy.preview {policy}` returns the proposed effective settings, candidate-bound revision, usage, and `destructive:false`. `policy.apply {policy,expectedRevision}` requires that exact review and returns the updated config. Editing invalidates the review. Lower limits affect new admission and do not evict existing records or cancel active work; storage cannot be lowered below retained data.

`message.history.preview {}` returns `{version:1,revision,messageIds,retained,remove,destructive:true}`. The UI shows exact keep/remove counts and permanent-removal consequences. Only a separate explicit `message.history.cleanup {expectedRevision}` removes reviewed local history. A changed policy/history invalidates the revision and requires a new review. Saving retention settings or appending messages never silently prunes history.

`service.list {direction?:"share"|"forward",cursor?,revision?}`, `message.list {cursor?,revision?}`, and `transfer.list {cursor?,revision?}` return `{version:1,items,revision,nextCursor?,total}`. Continuation requires the original revision; `page_changed` requires a fresh first page. History list IDs are page identities; message/transfer action IDs remain in `messageId`/`transferId`. These standalone pages do not replace complete Web snapshots in this version.

## Saved definitions, groups and private portability

- `service.save {configuration,expectedRevision?}` saves a stopped definition; creation omits its ID, replacement supplies the saved ID and its exact revision. It does not grant access or open listeners
- `service.delete {id,expectedRevision,expectedProfileRevision,stopActive,removeFromGroups}` requires explicit review of active-service stop and named group effects. Empty groups are removed only with reviewed membership removal. Definition/profile changes invalidate the review
- `service.stop-shares {}` stops every active local share, including task-owned shares, while retaining the node, outbound connections and saved settings. The UI reviews its global scope
- `group.list {}` returns `{groups,revision}`. `group.save {group:{name,serviceIds},expectedRevision}` saves references only; replacing an existing name requires explicit UI confirmation
- `service.selection {ids|group}` returns complete selected definitions, their revision, current states, readiness and `application:"unverified"`. `services.start` and `services.stop` use that same selector plus `expectedRevision`. Start alone accepts optional `lifetime` and `ttlSeconds` for that invocation; the default uses each saved lifetime. The UI offers finite/custom duration for mixed selections, until-stopped for all-forward selections and until-revoked for all-share selections. It shows the effective reviewed lifetimes and leaves saved definitions unchanged. A different active runtime lifetime requires explicit stop then a reviewed start; retries never extend an active grant. The UI reviews devices, network, ports, exact mappings and lifetimes before either action. Selection edits discard a review. Starting does not prove application success; stopping does not claim to cancel previously submitted remote jobs
- `profile.export {}` returns `{profile:{version:1,services,groups},revision,disabled:true}`. The browser downloads only the explicit bundle. Null collection values represent empty collections. The export excludes credentials, identity, trust, pairing capabilities, receive destinations, messages and task runtime state; service names, peer references and ports remain
- `profile.import.preview {profile}` returns the normalized bundle, candidate-bound revision, `disabled:true`, `preservesIdentity:true`, and `replacesServices`, plus `removesRustDeskMetadata` naming helper groups whose public key or role metadata is removed or changed; the UI displays these names before import. `profile.import {profile,expectedRevision}` replaces saved definitions after review and requires active services to be stopped. Imported services stay stopped. Choosing a different file invalidates the review; unsupported or over-budget files remain local

The global saved-service catalog also creates, edits, copies and deletes stopped definitions independently of current peers or an active network. It accepts explicit exact missing-peer IDs as saved references, uses authoritative revisions for edits/removal, and never starts a saved definition automatically. Task owner and supplied lease data are visible in service rows and selection reviews; manual actions cannot claim task ownership.

**Save a share → Start from saved share** is a save-only copy-to-draft shortcut.
The choices come from the existing catalog, while each selection reads the current
`service.config {id}`. Only definition fields are copied; IDs, replacement
revisions, runtime ownership and startup authority are not. The full audience,
network, target/mapping, protocol, ports/exclusions, discovery and lifetime stay
editable and require review before `service.save {configuration}`. Missing peer
references remain visible and inert. Selection changes, lost contact, reload,
close and editing invalidate the appropriate pending read/review; late reads and
repeated save clicks cannot apply another selection. A selected template is a
detached draft, not authority or a live reference to later source changes. Agents
can express the same path with these existing commands; there is no new template
store or API. A later start uses the ordinary current selection/permission checks.

The initial device screen emphasizes services and connectivity. Files/messages remain directly reachable, and unsent messages, reviewed file batches and in-progress uploads survive switching views. The separate saved-service manager can manage persisted definitions and groups without treating them as active permissions.

## LAN identities and pairing

The LAN relay mode uses an explicitly selected numeric IP/port and pinned certificate, with paired direct paths when available. Listener readiness does not prove relay reachability. `lan` carries `configured`, `publicKey`, optional `relay`, `pairingReady`, `listenerReady`, `relayReady`, and observed-or-unknown `path`. Saved relay settings, relay listener readiness and pairing readiness are distinct; none proves the other device is reachable. A LAN peer ID is its exact lowercase 64-hex public key.

- `lan.addresses {}` returns `{addresses:[{interface,address}]}` with eligible private local address candidates; it starts no listener
- `lan.identity {}` explicitly creates/returns the local server public identity; no private role keys are shown
- `lan.inspect {invitation}` validates the current recipient and expiry without joining, returning `{recipientPublicKey,recipientMatches:true,hostPublicKey,hostName,expires,relay}`. It does not establish remote availability or prove that an invitation has not been canceled
- `lan.invite {recipientPublicKey,name,ttlSeconds:300}` returns `{invitation,expires,recipientPublicKey}`. Names are limited to 80 UTF-8 bytes
- `lan.cancel {invitation}` cancels the exact pending capability; it does not undo an established pair
- `lan.join {invitation}` returns `{peerId,paired:true,trusted:false}`. Pairing alone does not grant local messages/file permission or automatic receiving
- `lan.revoke {peerId}` explicitly reviews the exact target and app/service impact before submission; offline saved pairs remain revocable

The guided host UI reads current address candidates, requires an explicit selection and reviews the exact listener before configuration. The fresh-profile join UI inspects an invitation, reviews its relay and impact, then explicitly configures that exact relay and pairs. It rechecks refreshed state and expiry before joining and never replaces an already selected different backend or relay silently.

The opaque invitation remains in memory across setup dismissal so it can be cancelled. It is never placed in URLs, persistent browser storage, ordinary status or logs. Copy is explicit; a manual selection field appears only if that operation is unavailable. A consumed invitation loses its copy/cancel actions and retained capability. Expiry, reciprocal-invitation conflicts, uncertain replies and persistence failures require explicit recovery; there is no automatic re-pairing or trust.

The network view uses only self-to-peer relationships from state. It separates discovery/registration, network online status, verified app readiness, ready listeners and file-transfer progress. It does not estimate total network traffic or infer paths. Device and edge buttons open the same local details and permission controls. Both the diagram and accessible list remain available on narrow layouts, and the explicit selected view is preserved.

## Upload staging

`POST /api/upload` is multipart with `peerId`, `requestId`, `manifest` (JSON `TransferEntry[]`), and repeated `files` entries. A file entry’s multipart filename is its relative manifest path. Only empty directories appear as explicit directory entries, with no file part; nonempty parent directories are implicit in child paths. Treat browser-supplied paths as untrusted and validate independently on the host.

The client derives effective admission limits from `state.limits`: batch entries are bounded by `logical.batchEntries` and the finite manifest budget; per-file and total bytes use `logical.fileBytes`/`logical.batchBytes` and finite outgoing spool capacity. Path bytes use the lower of `logical.pathBytes` and `resources.transferManifestBytes`; path depth uses `logical.pathDepth` bounded by the number of minimum-size components that fit that finite path budget. Portable per-component limits (255 UTF-8 bytes), relative-root rules, reserved names and collision checks remain in force. It honors increased choices instead of retaining the former 256-entry/1-GiB browser maxima or fixed 16-level/4096-byte path ceilings, while older snapshots retain those defaults. A logical unlimited choice does not remove finite memory/storage budgets. The peer may impose lower receive limits. Core owns operation deadlines; the client does not impose an unrelated 30-second command deadline. XHR progress is explicitly local staging progress. Remote acceptance, transfer, saving, partial failure and completion come only from state. Cancellation aborts staging, then refreshes to discover whether an offer was already created. The failed draft keeps its request identity to support an idempotent manual retry.

File picker relative paths and drag/drop directory entries are supported. Empty folders can be preserved only when the browser’s directory-entry API exposes them. File-selection UI explains this limitation. Pasted images enter the same reviewable batch flow; pasted text stays in the composer. There is no automatic clipboard sync, file opening or execution.

## Build and verification

Run `npm ci`, `npm test`, and `npm run build` from `web/`. The production `dist/` is embedded by Go; Node is needed only for development/builds. Runtime assets, fonts and code are local, with no CDN dependency. Dependencies and the lockfile are pinned.

For deterministic browser checks, follow [Browser acceptance](BROWSER_ACCEPTANCE.md). Playwright Test starts a fresh isolated Go fixture for each named test and uses production HTTP/session/CSRF/CSP and rebuilt embedded assets. Set `SOBA_E2E_BINARY` to the compiled fixture and `SOBA_SCREENSHOT_DIR` to a separate sanitized artifact directory. Vite has no proxy that rewrites the management server’s Origin/Host protections. These checks do not establish real LAN/Tailnet enrollment, real-device delivery, external application compatibility, native IME behavior or OS suspend behavior.

The CI browser job starts `cmd/soba-e2e` with its explicit test build tag and uses `SOBA_E2E_SESSION_FILE` (or `--session-file`) to supply a private `{url,code}` file. This mode uses the real Go management server, session cookie, CSRF checks, production CSP, embedded assets and two in-process fictional peers. It performs no browser API route mocking. Install the pinned browser with `node node_modules/playwright-core/cli.js install --with-deps chromium`. Screenshots are captured only after login, in Japanese/English and desktop/mobile layouts; credentials, cookies, traces and the private session file are not published. Capture guards also reject visible private sign-in links and QR containers (`.auth-private`, `.auth-qr`); only synthetic auth data belongs in test assertions. In-process peers are integration evidence, not real-device network acceptance.

The browser harness also records actual Chromium glyph font families and computed text metrics for Japanese static controls, verifies readable type sizes, accepts a real incoming batch and waits for its saved state, and captures graph desktop/mobile/detail views. Font reports contain only static selectors and font metadata. They contain no access codes, input values, cookies or invitations.

## Per-peer discovery observations

Optional `peer.discovery` carries `{state,checkedAt?,code?,services}`. Device details and connection setup distinguish pending, confirmed, unconfirmed, unsupported, limited and stale observations. Confirmed zero means no services were advertised; it does not mean no applications are running. An unavailable or unsupported reply does not establish installation status or that the peer/application is offline. Limited means the configured framing budget was exceeded. The explicit **Check shared services** action uses `discovery.refresh {peerId}` without restarting active transports; viewing the notice sends no request. Ordinary peers remain available for manual connections and explicit share selection. Failed discovery clears current selectable advertisements in Core without converting a saved binding to manual ports.


## Client setup helpers

**Saved services → Connection helpers → Save a RustDesk group** uses `rustdesk.preview {configuration}` with explicit name/network, immutable ID/relay peer IDs, server public key, separate remote/local ID and relay ports, numeric loopback and lifetime. Current peer pickers are available; exact offline IDs may be saved as unverified references. The preview returns `{configuration,group,services,revision,saved,applied,clientSettings}`. The UI verifies and displays all four roles: NAT TCP at ID-minus-one, ID TCP, heartbeat UDP and relay TCP, their exact peers/port mappings, public key and saved lifetime. Unsaved roles are planned, not ready. Edit/back invalidates the preview; cancel does not save.

`rustdesk.save {configuration,expectedRevision}` atomically saves the reviewed four definitions and public metadata without starting. An identical saved group is idempotent; a differing same-name group requires another name. Profile changes require another preview. A successful result links back to the existing selected group manager for a separate start review. RustDesk metadata cannot be silently detached by ordinary group replacement. Full-profile imports separately review lost metadata names.

`client.settings {ids?|group?}` supplies read-only general hints and helper groups; `rustdesk.settings {group}` is the corresponding Core helper settings contract. **Client settings** appears on service rows and saved entries, and selected groups have one settings action. Scalar local/remote endpoints remain copyable text, ranges retain exact inclusive mappings and `remoteHosts`, SSH uses the supplied peer-specific HostKeyAlias/command, and HTTP is only a candidate with the original TLS hostname/SNI/origin/certificate caveat. Active lifetime fields describe the runtime override when present. No external URL or application is launched or configured. Refresh is explicit; failures preserve manual copy/retry paths.

RustDesk settings show actual ID/relay endpoints, server public key, blank proxy, UDP enabled, `/r` suffix, all four readiness states and Core's bilingual notices. Every endpoint needs the same local relay address and port, approved remote server access and all four forwards. Saving or listener readiness does not verify RustDesk screen sharing, input control, SSH authentication or HTTP/HTTPS compatibility. Only public application configuration belongs in these helpers; never paste private keys or passwords.


## Optional startup approvals and private saved proxies

The defaults remain off. **Saved services → Connection helpers → Review automatic startup** lists approvals and previews the selected outbound definitions. `startup.preview {name,ids?|group?}` returns the full exact selection, network/hostname, `selectionRevision`, `revision`, `storeRevision` and `enabled:false`. The review shows every peer, protocol, effective mapping, purpose, saved lifetime and advertised reference. Shares are rejected. Existing-name replacement requires its own checkbox. `startup.save` submits the same selector with `expectedRevision` and `expectedStoreRevision`; success saves a future approval without starting the current process. `startup.list {}` and `state.startup` return sanitized `{entries,revision,suppressed}`. Entry `state` is the launch attempt result, not current listener readiness. `startup.disable {name,expectedStoreRevision}` requires impact review, cancels pending work and keeps existing services running.

At a future online process Open, exact enabled outbound approvals are captured and attempted once after readiness. A finite lifetime starts anew at that explicit process launch. Definition/group/hostname/network/revocation changes invalidate the approval. Offline Open suppresses it, including later network configuration within that process. A stop, failed attempt or expired permission never silently retries. OS sign-in registration remains a separate CLI operation.

The exact ordinary proxy review additionally offers **Save this reviewed proxy**. `proxy.saved.list {}` supplies its sanitized private-store revision; `proxy.save {scope,expectedRevision,expectedStoreRevision,username,password,startOnLaunch}` or `proxy.generate` without credentials persist only after explicit review. `startOnLaunch` is always an explicit boolean and defaults false in the UI. Generation stores strong credentials and never reveals them automatically. Replacement of an existing name is explicit; same-name active listeners must first be stopped. Save/generate opens no listener. Failed or uncertain saves clear supplied credentials and require a fresh scope review; the user reloads the saved list to establish the outcome.

**Advanced connections → Saved proxy credentials → Manage saved proxies** uses sanitized `proxy.saved.list` / `state.savedProxies`: `{entries:[{name,scope,revision,startOnLaunch,credentialsSaved,valid,state}],revision,suppressed}`. Separate reviewed `proxy.saved.start`, `proxy.saved.disable` and `proxy.saved.delete` take `{name,expectedRevision}` for the exact record. Start performs a current Core scope preview and then starts a new runtime lifetime. Disable prevents future startup/resume while preserving any running listener and stored credentials. Delete permanently removes the stored credentials and approval and stops associated runtime work. Ordinary runtime Stop ends that run; any enabled future-process approval must be disabled separately. A saved proxy may recover transient loss only within the original absolute expiry; explicit stop, expiry or invalid scope never silently restarts it.

`proxy.reveal {name,expectedRevision}` is requested only by **Reveal credentials temporarily**. Its response bypasses the shared request retry cache and is copied directly into private uncontrolled fields; the returned object is erased immediately. Secret values never enter React state, normal snapshot, history, URL, logs or export. Automated browser evidence capture rejects the whole private view. Hide, close, unmount, scope/revision change and late-response cancellation clear the fields. Copy is separate and explicit; the system clipboard retains the selected credential until replaced by the user. The existing capture guard excludes the whole credential/reveal view, even while empty, and all nonempty private values. Only fictional credentials are used in DOM/browser test inputs.

Both new paths show localized failure recovery and distinguish an acknowledgement from an unconfirmed persistence result. Private settings are stored outside portable exports; this is intentional durable storage only after explicit opt-in, not browser persistence. Native ACL, installed startup, suspend/recovery and external-app verification remain separate acceptance gates.

## Direct LAN and explicit mixed modes (development)

`Network` additionally accepts `direct-lan` and `mixed`. Direct mode keeps
application traffic in WireGuard/gVisor userspace TCP/native UDP; the separate
mutual-Ed25519 TLS listener carries only pairing/control. Never infer a current
identity from an IP, display name or saved configuration alone.

- `network.configure`: `{mode:"direct-lan",hostname?,directLAN:{listen,prefixes}}`.
  `listen` is an exact private/loopback numeric IP with an unreserved high port;
  `prefixes` are explicitly selected canonical private/loopback CIDRs. TCP
  pairing/session control and UDP WireGuard use the same port. No DNS/relay/STUN fallback exists
- `direct-lan.status`, `direct-lan.identity`: `{}`. These reads do not generate
  identity; explicit configure does. Snapshot `directLAN` includes `configured`,
  `listenerReady`, `publicKey`, `endpoint`, `prefixes`, `recoveryRequired`, and
  optional `resourceRestartRequired`. Local readiness does not prove remote
  reachability. A known missing/down exact local interface is unavailable;
  inspection errors are unknown and cannot authorize fallback
- `direct-lan.invite`: `{recipientPublicKey,name,ttlSeconds,qr?}` returns private
  `{invitation,expires,recipientPublicKey,qr?}`. Lifetime is 1–600 whole seconds;
  Web defaults to 300. Optional `qr` is a locally generated boolean pixel matrix
  for exactly that invitation. It is subject to the same private-display and
  expiry/consumption cleanup as the invitation text
- `direct-lan.inspect`: `{invitation}` returns token-free
  `{hostPublicKey,hostName,endpoint,recipientPublicKey,recipientMatches:true,expires}`
  only after recipient and selected endpoint scope validation. Inspection does
  not prove the host is online or the token is unused
- `direct-lan.join`, `direct-lan.cancel`: `{invitation}`. Join returns
  `{paired:true,trusted:false,peerId}` after the protected state commit. Cancel
  applies only to the issuing node's current invitation
- `direct-lan.revoke`: `{peerId}` closes affected streams and application scopes.
  Ambiguous pairing/save errors are distinct from ordinary rejection; never
  claim the remote pair was rolled back
- `wan.candidates.get`: `{}` returns explicit trusted-relay WAN settings and
  `editable`/`restartRequired`. `wan.candidates.set` enables with
  `{enabled:true,stunEndpoints:[numeric IP:port...],advertiseIPv6,probeBudget}`
  or disables with exactly `{enabled:false}`. The positive finite probe budget
  bounds each discovery pass; it is not a permanent four-endpoint metadata cap.
  An empty STUN list is valid when `advertiseIPv6:true`; `probeBudget` is
  an integer from 1 to 65535. There is no implicit STUN server. Saving does not start discovery. Strict LAN
  destination permission is incompatible; approved encrypted relay fallback
  remains available in this separate `lan` mode
- `network.configure`: `{mode:"mixed",mixed:{backends:[...]}}` selects two or
  three separately prepared backends in explicit order, from `direct-lan`,
  `tailnet`, `lan`. Their external traffic permissions must be reviewed. Strict
  LAN-only permission cannot be silently broadened into mixed mode
- `mixed.status`: `{}` and snapshot `mixed` report configured backends, the
  device identity/public key, bindings, eligible route metadata and backend
  states. `backendReady` means backend readiness, never application health or
  measured traffic. Active status also reports `workerResources:{frameBytes,requests,handles}`
  and `resourceRestartRequired`; effective budgets remain in use until restart.
  No authentication URL or invitation is present in status
- `mixed.bind`: `{peers:[current unbound peer IDs from distinct backend routes]}`
  requires fresh cryptographic proof on every selected route. Matching names or
  IP addresses are insufficient. Old route approvals pause; the logical peer
  requires a new application approval. `mixed.unbind:{peerId}` removes only the
  specified binding and does not reactivate former approvals

Mixed route selection concerns new connections/reconnects only. There is no
established-TCP migration, byte replay or automatic scope expansion. A positively
unavailable backend may be skipped for a bound peer; unknown, permission,
identity, expired/revoked binding and authentication failures remain terminal.
An arbitrary dial timeout is not reclassified as safe backend unavailability.
The Web does not expose standalone direct pairing controls while mixed is active.

New direct application dials wait for the pinned WireGuard session. A bounded
mutually authenticated TLS control request can ask the deterministic lower-key
initiator to activate it; responder readiness requires authenticated WireGuard
transport confirmation. No application bytes use the control connection.
Cancellation, revocation and current-generation checks remain authoritative.
Independent WireGuard timers still govern existing flows and do not guarantee
TCP continuity across suspend, network loss or long idle.

### Relay resource settings

`policy.config` additionally lists `restartRequiredResources` and `relayResourceEditable`. The four relay budgets are `resources.relayPresenceConnections` (default 4), `relayCandidateAttempts` (4), `relayTLSConnections` (64), and `relayAdmissionConnections` (16), each using the existing `default` / positive `limited` capacity choice. `unlimited` is invalid for finite resource budgets. `policy.preview.restartRequired` reports an effective relay-budget change. Such edits are rejected while a network backend exists and take effect at the next start. The same effective values and edit/start boundary appear under `lan.resources` in status.

Candidate views and route approvals no longer reject a fifth item. Their metadata remains bounded by private-state and signed-exchange sizes, and relay-map IDs use the nonzero 16-bit DERP namespace. An insufficient `relayPresenceConnections` budget raises `lan_relay_presence_capacity` before startup without truncating saved candidates or grants. `lan_route_envelope_capacity` reports an offer exceeding the existing 24 KiB plaintext exchange envelope. See [relay operations](../docs/RELAY_OPERATIONS.en.md) ([日本語](../docs/RELAY_OPERATIONS.ja.md)) for defaults, restart and old-version compatibility.

## Passive network guidance

Optional `self.guidance` and passive `diagnostics.run {}` response
`networkGuidance` contain the same local-state interpretation:
`{code,category,action,summary:{en,ja},nextSteps:{en,ja}}`. They use only the
reported state/error code and do not probe, change settings, generate identity or
establish route/application success. Unknown errors retain their code and use an
unknown-cause explanation. A normal ready state does not create a route result.

`action` is one of `review_network`, `review_capacity`, `refresh_state`, or `wait`.
The Web opens a review surface or refreshes local status only after a click;
`wait` has no retry action. Stale notices are suppressed. Human `soba status`
uses the same localized next step, while its JSON preserves the original fields.
Technical codes/details remain expandable. Offline saved direct-LAN peers expose
`path:"unknown"`; their saved endpoint is configuration, not a path observation.

## Read-only public device cards

`device-card.export {mode,name,includeEndpointHint?,qr?}` reads an existing
`lan` or `direct-lan` component identity using an explicit public-field
allowlist. `device-card.inspect {card,expectedMode}` parses the bounded canonical
card without local setup or any network operation. Neither changes pairing,
trust, endpoints or settings. Both bypass command-result retention to read current
state on deliberate retries. Optional export QR is a local bitmap only; no image
reader or new dependency is included.

Both return `verification:"unverified"` and `freshness:"unknown"`. An inspection
content digest is not identity proof. Address/pin hints are opt-in on export,
shape-validated only on inspection, and never applied. See [device cards](../docs/DEVICE_CARDS.en.md)
([日本語](../docs/DEVICE_CARDS.ja.md)) for exact fields, bounds and remaining UI acceptance.

## Inert favorites

`favorites.list {}` returns
`{version:1,revision,entries,durabilityUncertain}`. Each entry is exactly
`{kind:"service",serviceId,available}` or `{kind:"group",groupName,available}`.
Availability describes a current saved reference, not an online peer or service.
`favorites.add` and `favorites.remove` accept
`{reference:{kind,serviceId|groupName},expectedRevision}` and return the same view.
Adding requires a current target; removing may clear a missing reference.

Preferences are separately stored, byte-bounded and lazily read; they do not
change the strict profile format, group membership, service revisions, startup
approvals or active deadlines. Missing references are not automatically pruned.
Every edit checks the current preference revision. Uncertain published writes
reconcile the actual file and return an error; `durabilityUncertain` is retained
in the current Core instance until a confirmed write, not across restart.

The lazy Web panel uses fresh request identities and leaves ordinary saved
navigation available after preference failure. Favorite operations never retain
or replay `useServer` uncertain-request entries. After an ambiguous response the
user must reload and review current marks before a new explicit mutation; other
commands retain their existing uncertainty/deduplication behavior. Favorite
selection still uses the existing full service-scope review before starting.
