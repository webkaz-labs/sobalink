# Local browser API

The React client uses only same-origin `/api` requests. The Go host is responsible for numeric-loopback binding, session cookies, Origin/Host checks, CSRF enforcement, authorization, identity pinning, deadlines, request deduplication and all filesystem/network policy. No successful browser preview proves a remote application works.

`src/api.ts` is the authoritative frontend TypeScript contract. Machine field names are stable across Japanese and English.

## Session and state

- `GET /api/state` returns `State`: `csrfToken`, `self`, `peers`, `messages`, `transfers`, local `services`, local `shares`, optional discovered `availableServices`, `settings`, and optional `lan` readiness/selected-relay state
- `POST /api/session` accepts `{code}`; a successful response may return `{csrfToken}`. The client then fetches state. Codes are not retained in browser storage
- `401 {code:"unauthenticated"}` returns the client to local-code entry and clears in-memory drafts
- All responses are JSON. Errors use `{code,message?}` or `{error:{code,message}}`. The client localizes recognized codes. Unknown failures receive localized next-step guidance, with the unchanged backend explanation available under Technical details
- Mutation success is `{ok:true,result?:{...}}`; no state is optimistically marked sent, received, connected or saved
- State is polled every three seconds while visible, with stale-request cancellation and generation guards. Loss of contact retains clearly stale information and disables new sends/starts

## Commands

`POST /api/command` accepts `{requestId,name,payload}` with `X-CSRF-Token`. The exact typed payloads are in `CommandPayloads`.

- `message.send`: `{peerId,text}`; text is explicit and limited to 16,384 UTF-8 bytes, matching the backend
- `peer.trust`: `{peerId,trusted}`; this is local permission for the exact verified identity, not permission granted by the remote device
- `peer.reconnect`: `{peerId}`
- `peer.autosave`: `{peerId,enabled,paused,directory}`; the designated directory is on the host. Pause requires a review of its impact: it stops messages/file offers, cancels outgoing batches and removes their staging copies. Original files remain; they must be selected again after resume. Separate service grants remain active
- `transfer.accept`: `{transferId,destination}`; explicit per-batch directory required when no configured receive directory exists
- `transfer.decline`, `transfer.cancel`, `transfer.retry`, `transfer.forget`: `{transferId}`. Forgetting terminal history does not delete saved files
- `service.connect`: `{name,peerId,serviceId?,network,ports,excludePorts?,localPort?,ttlSeconds,purpose,discoverable:false}`
- `service.share`: `{name,peerIds,network,ports,excludePorts?,ttlSeconds,purpose,discoverable}`; shares use the same local and exposed port numbers
- `service.stop`: `{id}`
- `network.configure`: `{mode:"none"|"tailnet"|"lan",hostname?,lan?:{kind:"relay",address,certificateSHA256}}`; changing a running network/hostname requires stopping and restarting the process, which the UI explains
- `network.login`: `{}`. An explicit response may include `result.authUrl`; only HTTPS `login.tailscale.com` links are rendered, never loaded automatically or persisted
- `settings.update`: `{locale?:"auto"|"en"|"ja",theme?:"system"|"light"|"dark",receiveDirectory?}`

Discovered `availableServices` carry opaque IDs and compact port ranges. A discovered connection sends the chosen `serviceId`; the backend must revalidate the exact current observation before starting. Manual connections omit it and remain available for ordinary Tailscale nodes. Local listener previews cap connections at 64 ports. Sharing previews retain compact ranges, and the backend remains authoritative about exclusions, collisions, mapping and supported protocols.

## LAN identities and pairing

The LAN relay mode uses an explicitly selected numeric IP/port and pinned certificate, with paired direct paths when available. Listener readiness does not prove relay reachability. `lan` carries `configured`, `publicKey`, optional `relay`, `pairingReady`, and observed-or-unknown `path`. A LAN peer ID is its exact lowercase 64-hex public key.

- `lan.identity {}` explicitly creates/returns the local server public identity; no private role keys are shown
- `lan.invite {recipientPublicKey,name,ttlSeconds:300}` returns `{invitation,expires,recipientPublicKey}`. Names are limited to 80 UTF-8 bytes
- `lan.cancel {invitation}` cancels the exact pending capability; it does not undo an established pair
- `lan.join {invitation}` returns `{peerId,paired:true,trusted:false}`. Pairing alone does not grant local messages/file permission or automatic receiving
- `lan.revoke {peerId}` explicitly reviews the exact target and app/service impact before submission; offline saved pairs remain revocable

The opaque invitation remains in memory across setup dismissal so it can be cancelled. It is never placed in URLs, persistent browser storage, ordinary status or logs. Copy is explicit; a manual selection field appears only if that operation is unavailable. A consumed invitation loses its copy/cancel actions and retained capability. Expiry, reciprocal-invitation conflicts, uncertain replies and persistence failures require explicit recovery; there is no automatic re-pairing or trust.

The network view uses only self-to-peer relationships from state. It separates discovery/registration, network online status, verified app readiness, ready listeners and file-transfer progress. It does not estimate total network traffic or infer paths. Device and edge buttons open the same local details and permission controls. Narrow layouts use an equivalent accessible list.

## Upload staging

`POST /api/upload` is multipart with `peerId`, `requestId`, `manifest` (JSON `TransferEntry[]`), and repeated `files` entries. A file entry’s multipart filename is its relative manifest path. Only empty directories appear as explicit directory entries, with no file part; nonempty parent directories are implicit in child paths. Treat browser-supplied paths as untrusted and validate independently on the host.

The client caps batches at 256 entries and 1 GiB; lower state-advertised limits are honored. XHR progress is explicitly local staging progress. Remote acceptance, transfer, saving, partial failure and completion come only from state. Cancellation aborts staging, then refreshes to discover whether an offer was already created. The failed draft keeps its request identity to support an idempotent manual retry.

File picker relative paths and drag/drop directory entries are supported. Empty folders can be preserved only when the browser’s directory-entry API exposes them. File-selection UI explains this limitation. Pasted images enter the same reviewable batch flow; pasted text stays in the composer. There is no automatic clipboard sync, file opening or execution.

## Build and verification

Run `npm ci`, `npm test`, and `npm run build` from `web/`. The production `dist/` is embedded by Go; Node is needed only for development/builds. Runtime assets, fonts and code are local, with no CDN dependency. Dependencies and the lockfile are pinned.

For synthetic-state browser checks, run `npm run dev -- --port 4177`, then `npm run test:browser`. Set `CHROMIUM_PATH` if Chromium is installed elsewhere, `SOBA_WEB_URL` for another local URL, and `SOBA_SCREENSHOT_DIR` for screenshot output. Vite has no proxy that rewrites the management server’s Origin/Host protections. For live API testing, rebuild the Go-embedded production assets. These checks verify rendering, responsive navigation, dialog focus and explicit-send behavior. They do not establish real LAN/Tailnet enrollment, remote delivery, application compatibility, native IME behavior or OS suspend behavior.

The CI browser job starts `cmd/soba-e2e` with its explicit test build tag and uses `SOBA_E2E_SESSION_FILE` (or `--session-file`) to supply a private `{url,code}` file. This mode uses the real Go management server, session cookie, CSRF checks, production CSP, embedded assets and two in-process fictional peers. It performs no browser API route mocking. Install the pinned browser with `node node_modules/playwright-core/cli.js install --with-deps chromium`. Screenshots are captured only after login, in Japanese/English and desktop/mobile layouts; credentials, cookies, traces and the private session file are not published. In-process peers are integration evidence, not real-device network acceptance.

The browser harness also records actual Chromium glyph font families and computed text metrics for Japanese static controls, verifies readable type sizes, accepts a real incoming batch and waits for its saved state, and captures graph desktop/mobile/detail views. Font reports contain only static selectors and font metadata. They contain no access codes, input values, cookies or invitations.
