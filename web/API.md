# Local browser API

The React client uses only same-origin `/api` requests. The Go host is responsible for numeric-loopback binding, session cookies, Origin/Host checks, CSRF enforcement, authorization, identity pinning, deadlines, request deduplication and all filesystem/network policy. No successful browser preview proves a remote application works.

`src/api.ts` is the authoritative frontend TypeScript contract. Machine field names are stable across Japanese and English.

## Session and state

- `GET /api/state` returns `State`: `csrfToken`, `self`, `peers`, `messages`, `transfers`, local `services`, local `shares`, optional discovered `availableServices`, and `settings`
- `POST /api/session` accepts `{code}`; a successful response may return `{csrfToken}`. The client then fetches state. Codes are not retained in browser storage
- `401 {code:"unauthenticated"}` returns the client to local-code entry and clears in-memory drafts
- All responses are JSON. Errors use `{code,message?}` or `{error:{code,message}}`. The client localizes recognized codes and displays an unrecognized backend explanation without inventing its cause
- Mutation success is `{ok:true,result?:{...}}`; no state is optimistically marked sent, received, connected or saved
- State is polled every three seconds while visible, with stale-request cancellation and generation guards. Loss of contact retains clearly stale information and disables new sends/starts

## Commands

`POST /api/command` accepts `{requestId,name,payload}` with `X-CSRF-Token`. The exact typed payloads are in `CommandPayloads`.

- `message.send`: `{peerId,text}`; text is explicit and limited to 16,384 UTF-8 bytes, matching the backend
- `peer.trust`: `{peerId,trusted}`; this is local permission for the exact verified identity, not permission granted by the remote device
- `peer.reconnect`: `{peerId}`
- `peer.autosave`: `{peerId,enabled,paused,directory}`; the designated directory is on the host. The current backend pause is peer-wide for messages and file transfers in both directions, and the UI explicitly labels that scope
- `transfer.accept`: `{transferId,destination}`; explicit per-batch directory required when no configured receive directory exists
- `transfer.decline`, `transfer.cancel`, `transfer.retry`, `transfer.forget`: `{transferId}`. Forgetting terminal history does not delete saved files
- `service.connect`: `{name,peerId,serviceId?,network,ports,excludePorts?,localPort?,ttlSeconds,purpose,discoverable:false}`
- `service.share`: `{name,peerIds,network,ports,excludePorts?,ttlSeconds,purpose,discoverable}`; shares use the same local and exposed port numbers
- `service.stop`: `{id}`
- `network.configure`: `{mode:"none"|"tailnet"|"lan",hostname?}`
- `network.login`: `{}`. An explicit response may include `result.authUrl`; only HTTPS `login.tailscale.com` links are rendered, never loaded automatically or persisted
- `settings.update`: `{locale?:"auto"|"en"|"ja",theme?:"system"|"light"|"dark",receiveDirectory?}`

Discovered `availableServices` carry opaque IDs and compact port ranges. A discovered connection sends the chosen `serviceId`; the backend must revalidate the exact current observation before starting. Manual connections omit it and remain available for ordinary Tailscale nodes. Local listener previews cap connections at 64 ports. Sharing previews retain compact ranges, and the backend remains authoritative about exclusions, collisions, mapping and supported protocols.

## Upload staging

`POST /api/upload` is multipart with `peerId`, `requestId`, `manifest` (JSON `TransferEntry[]`), and repeated `files` entries. A file entry’s multipart filename is its relative manifest path. Only empty directories appear as explicit directory entries, with no file part; nonempty parent directories are implicit in child paths. Treat browser-supplied paths as untrusted and validate independently on the host.

The client caps batches at 256 entries and 1 GiB; lower state-advertised limits are honored. XHR progress is explicitly local staging progress. Remote acceptance, transfer, saving, partial failure and completion come only from state. Cancellation aborts staging, then refreshes to discover whether an offer was already created. The failed draft keeps its request identity to support an idempotent manual retry.

File picker relative paths and drag/drop directory entries are supported. Empty folders can be preserved only when the browser’s directory-entry API exposes them. File-selection UI explains this limitation. Pasted images enter the same reviewable batch flow; pasted text stays in the composer. There is no automatic clipboard sync, file opening or execution.

## Build and verification

Run `npm ci`, `npm test`, and `npm run build` from `web/`. The production `dist/` is embedded by Go; Node is needed only for development/builds. Runtime assets, fonts and code are local, with no CDN dependency. Dependencies and the lockfile are pinned.

For synthetic-state browser checks, run `npm run dev -- --port 4177`, then `npm run test:browser`. Set `CHROMIUM_PATH` if Chromium is installed elsewhere, `SOBA_WEB_URL` for another local URL, and `SOBA_SCREENSHOT_DIR` for screenshot output. Vite has no proxy that rewrites the management server’s Origin/Host protections. For live API testing, rebuild the Go-embedded production assets. These checks verify rendering, responsive navigation, dialog focus and explicit-send behavior. They do not establish real LAN/Tailnet enrollment, remote delivery, application compatibility, native IME behavior or OS suspend behavior.

The CI browser job starts `cmd/soba-e2e` with its explicit test build tag and uses `SOBA_E2E_SESSION_FILE` (or `--session-file`) to supply a private `{url,code}` file. This mode uses the real Go management server, session cookie, CSRF checks, production CSP, embedded assets and two in-process fictional peers. It performs no browser API route mocking. Install the pinned browser with `node node_modules/playwright-core/cli.js install --with-deps chromium`. Screenshots are captured only after login, in Japanese/English and desktop/mobile layouts; credentials, cookies, traces and the private session file are not published. In-process peers are integration evidence, not real-device network acceptance.
