# Use sobalink

[日本語](GENERIC.ja.md) · [Overview](../README.en.md) · [Security](../SECURITY.md) · [Verification](VERIFICATION.en.md)

This guide covers `soba` in published [0.3.0-alpha.5](https://github.com/webkaz-labs/sobalink/releases/tag/v0.3.0-alpha.5). Its [15 release gates](https://github.com/webkaz-labs/sobalink/actions/runs/37423908236), including signed public retrieval and four-target mise installation, passed for the exact source recorded in [verification](VERIFICATION.en.md#current-integration-and-published-baseline). Real-device enrollment, application compatibility, native IME, ordinary-user Windows process behavior, OS sign-in/suspend and actual power-loss recovery remain separate checks.

The local Web UI and guided CLI support normal service workflows; explicit CLI commands support repeated actions and automation. Both use the same Go authorization and storage boundaries. Device connectivity and application services are the main workflow; messages and files are additional operations. Start with [sharing](#share-a-local-service) or [connecting](#connect-to-a-peers-service), and use the transfer steps when needed. Legacy `tsnet-bridge` releases use different commands and configuration.

[Guided CLI](CLI_GUIDE.en.md) · [Application settings and RustDesk](CLIENT_HELPERS.en.md) · [Local Web controls](WEB_CONTROLS.en.md) · [Feature parity and acceptance](FEATURE_PARITY.en.md) · [All guides](README.en.md)

Source-build addition: [local transfer settings resource](RESOURCE_SETTINGS.en.md) supports local inspection, preview, explicitly bound apply and operation status. It adds no remote management or peer grant and is not part of the published alpha.5 acceptance above. It uses the existing authenticated local IPC and local Web command dispatcher, with no new Web UI controls. See [the local operation workflow](#local-transfer-settings-operations-source-build).

## Start and open the local UI

Use mise **2026.9.18** to select the published alpha.5 prerelease, verify the version, then start it. Keep signature verification enabled:

```sh
mise use -g "packslip:github.com/webkaz-labs/sobalink[prerelease=true]@0.3.0-alpha.5"
mise exec -- soba version
mise exec -- soba
```

`mise use -g` changes the globally selected version. Keep signature and identity checks enabled. The examples below use `soba` directly when mise is activated; otherwise prefix each command with `mise exec --`. A [source build](DISTRIBUTION.md#build-this-checkout) is also available; use its `./bin/soba` executable (`./bin/soba.exe` on Windows).

`soba start` is equivalent. It runs in the foreground. Keep that terminal open; Ctrl+C closes its active connections and shares. From another terminal:

```sh
soba ui
soba status
```

Open the exact printed numeric-loopback URL on the same device. Enter the separately printed one-time code within five minutes. `soba ui` returns the URL and a fresh code as JSON; issuing another code replaces the previous unused code. Do not place it in a bookmark, URL, screenshot or shared log. The Web UI does not have a network-facing management address.

The first terminal prints a code only when its output is an interactive terminal. Use `soba ui` when needed and handle its response as secret local sign-in information. If the browser session expires, request a new code and sign in again. Normal state responses never contain the sign-in code.

The default private state directory is the operating system's user configuration directory under `sobalink`. To use another directory, include the same global option in every command:

```sh
soba --state-dir ./sample-state
soba --state-dir ./sample-state status
```

A fresh profile starts with no network selected. UI preferences, trust and service definitions are saved. Ordinary saved definitions remain stopped after a process restart; transfer progress never resumes. Separately reviewed [outbound startup and private proxy profiles](STARTUP.en.md) can opt into a future online launch. Inbound shares remain manual.

## Choose one network

| Choice | Setup | Important boundary |
| --- | --- | --- |
| Existing Tailnet | Activate the embedded node, then use the official interactive Tailscale sign-in flow | It is a separate node in the chosen Tailnet; the OS Tailscale app's session is not imported |
| Tailcat / explicit trusted relay | Explicitly select a numeric relay endpoint and certificate pin, then pair specific peers | Local UI/CLI and exact-source loopback gates are recorded for alpha.5. Physical topology remains unverified. No arbitrary public fallback or zero-external-traffic claim |
| No network | Leave the agent local | No peer transfer or service connection |

For the Tailnet path:

```sh
soba setup --network tailnet
soba login --browser
soba peers
```

`--browser` opens the official sign-in page and waits; `--qr` offers a locally generated QR in a private terminal, and `--link` explicitly prints the private link. Plain `soba login` retains machine JSON. Complete enrollment or wait for device approval; see [sign-in and cancellation](SIGN_IN.en.md). Treat enrollment URLs as secrets. The tool does not accept an auth key in command arguments, and it does not change OS routes or DNS. Tailnet policy and service authentication still apply.

Selecting a mode is explicit. Stop the agent and use `soba start --offline` to open management without reconnecting the saved network when changing modes or repairing saved LAN state. An existing TCP session does not transfer between Tailnet and Tailcat. The local UI and CLI support explicit relay selection and pairing; follow the [LAN guide](LAN.en.md). [Verification](VERIFICATION.en.md) separates published CI from later local checks.

For foreground/background startup, optional reviewed startup at user sign-in, saved autosave behavior and Tailnet logout, see [startup and logout](LIFECYCLE.en.md).

Tailcat permits direct peer traffic, encrypted payload and HTTPS/ICMP diagnostics to explicitly configured/approved relay endpoints. The normal direct-enabled binary can use public peer paths; `local` is a relay classification, not a LAN egress sandbox. The [prepared-route workflow](LAN.en.md#prepare-another-route) keeps the same pair across reviewed candidates, with an explicitly reviewed finite or until-revoked local approval on both devices. It was already included in alpha.4 and remains included in alpha.5. [Transport detail](ARCHITECTURE.md#network-boundaries)

## Trust the peer you mean

Choose a current peer in the UI and compare its identity before trusting it. Names are labels, not credentials. Reusing a name or address does not transfer permission to another identity.

```sh
soba peers
soba trust PEER_ID
```

Trust permits incoming messages and transfer offers from that exact identity. It does not grant permission inside the peer's applications. The receiving side must also trust the sender; local trust alone cannot authorize receipt on another device. Service sharing is a separate explicit peer-and-port grant.

Revoking trust invalidates that peer's transfer authorization and autosave generation, closes its tracked work and stops affected shares. Trusting it again does not restore the previous autosave grant. Check active services after any trust change.

```sh
soba revoke PEER_ID
```

For Tailcat, pairing establishes the verified key relationship using an explicit invitation. Treat invitation tokens and Tailcat capability addresses as secrets. Pairing, local message/file trust, autosave and service sharing are distinct decisions.

## Share a local service

Start the real application first, with its own authentication. Select Share, the current peers, TCP or UDP, ports and exclusions, and a lifetime. Review the effective scope before starting.

```sh
soba share --ports 3000-3003 --peers PEER_ID
```

Omitting the name chooses an unused one; set `--name preview` to choose explicitly. Existing names are never overwritten. On a name conflict, choose another name or use the saved-service commands below. TCP and a one-hour lifetime are the defaults.

For common SSH or web ports, choose an editable preset:

```sh
soba --dry-run share --preset ssh --peers PEER_ID
soba share --preset ssh --peers PEER_ID
```

`ssh` suggests TCP 22 (SSH/SFTP); `web` suggests TCP 8080. `postgres` and `local-ai` are additional editable examples; inspect `soba connect --help` for their ports. A local AI preset is an API example, not a universal port. Explicit `--ports` or `--network` values take precedence. Presets do not discover, configure or validate the actual application.

This makes the selected embedded-node ports available to the selected peers and maps each port to the same numeric loopback port by default. It does not publish an internet URL or configure the application. A recipient with ordinary Tailscale access can use the provider's embedded-node address and permitted port without installing sobalink.

- Select current peers explicitly; the initial `sharePeers` policy is 32, adjustable or explicitly unlimited. There is no implicit “all peers” grant
- Shares default to a finite hour. Choose another positive whole-second duration, including over 24 hours, with `--ttl 72h`; choose `--lifetime until-revoked` explicitly to remove the share deadline
- TCP shares retain compact ranges across ports 1–65535, with exclusions and reserved internal endpoints removed, instead of opening one OS listener for every port
- A range also covers a service started later on an allowed port while the grant remains active. Prefer the narrowest useful range
- Use `--exclude`, for example `--ports 3000-3010 --exclude 3005-3007`, to remove ports
- Discovery `54543`, peer API `54544`, pairing `54545` and backend-internal endpoints cannot be exposed through a service share
- UDP requires individual sockets and shares an adjustable finite `materializedListeners` budget with local connections and optional proxy listeners; the initial budget is 64. Narrow the plan, stop unused work, or review a larger budget if capacity is exhausted

Optional `--discoverable` exposes only minimal active service metadata to the selected peers. It is off by default in the CLI. A discovery result says a grant is available, not that the application works. The receiving UI rechecks a selected discovered service before starting the connection.

A port conflict fails the operation; the tool does not silently switch ports or widen scope. Shared ranges retain same-port mapping. For a single shared port, `--local-port` can explicitly select the application port, for example `soba share --ports 8080 --local-port 3000 --peers PEER_ID`. `--loopback-host` selects exactly `127.0.0.1` or `::1`. Review this target separately from a client's local entry port.

## Connect to a peer's service

Choose an available share in the UI, or enter a current Tailnet peer and the intended service manually. An ordinary Tailscale target needs no sobalink. Its application must listen on the destination address and port and allow access through Tailnet and application policy.

```sh
soba connect --ports 3000 --peer PEER_ID
```

Copy the actual local endpoint from status into your client. Local ports default to the target port numbers. For targets below 1024, explicitly choose an unprivileged local port or use a preset: `ssh` suggests TCP 22/local 2222 and `web` suggests TCP 8080/local 8080. If occupied, choose another entry with `--local-port`. These are separate examples:

```sh
soba connect --preset ssh --peer PEER_ID
soba connect --preset web --ports 3000 --local-port 8081 --peer PEER_ID
```

Connections default to `until-stopped`; use `--ttl` for a finite lifetime. Connect listeners bind numeric loopback, use local ports 1024–65535 and consume the adjustable finite listener budget, initially 64. `--local-port` maps sorted effective remote ports to consecutive local ports; review the displayed mapping. It does not change the provider's application-target mapping.

A ready listener is only transport readiness. Verify authentication and an actual operation in the target app. Preserve TLS certificate names, SSH host-key verification, origin rules and the app's own authorization. A tunnel does not adapt stdio protocols into HTTP or run remote jobs.

## Reuse saved service settings

Save stopped definitions without starting listeners, including while offline, then group or export them and explicitly start reviewed selections. `soba --offline service save ...` and `soba --offline profile ...` work while the agent is stopped; `soba start --offline` instead starts local management. Groups start with rollback of newly started members on failure. `soba task` owns temporary services with a renewable 30-second lease and cleans them up after the local command exits. See [saved services, groups and tasks](SAVED_SERVICES.en.md) for complete commands, private exports, revision checks and cancellation boundaries.

Find the current ID in `soba status`, then inspect its complete saved configuration. These details are available only to authenticated local management, never as peer discovery metadata:

```sh
soba service show SERVICE_ID
soba --dry-run service copy SERVICE_ID
soba service copy SERVICE_ID
```

`copy` chooses an unused name by default and starts a new service. It retains ports, exclusions, peers, local ports, discovery visibility and permission lifetime; override selected fields with `--name`, `--ports`, `--ttl` and related flags. This starts a fresh permission lifetime when applied, rather than merely saving a definition.

To restart the original service, explicitly stop it first if it is active:

```sh
soba stop-service SERVICE_ID
soba --dry-run service restart SERVICE_ID
soba service restart SERVICE_ID
```

`restart` replaces and starts the stopped saved ID after checking the full configuration revision it just read and the reviewed backend. A conflict leaves saved settings unchanged; display the latest configuration and review again. It cannot change a known backend, silently stop an active service or overwrite another name. Only an unknown-backend entry needs an explicit reviewed `--backend tailnet|lan`. A connection following an advertised service must retain that service's peer, protocol and complete ports. Use a fresh `soba connect` for another target.

Use `--help` after `copy` or `restart` for override flags. The agent checks peer, network and port conditions when applying. A dry-run does not pin the later command to its preview: rerunning reads the saved settings again.

## Optional proxy and diagnostics

Use `soba connect` for an ordinary service entry point. For a SOCKS5-capable application that needs several reviewed TCP targets, an optional authenticated proxy is available. Review its peer/port scope and lifetime; provide credentials only through private input. Ordinary starts are ephemeral; explicit [private saved profiles](STARTUP.en.md) support generation, reviewed reveal and optional future startup. There is no OS DNS or target-dial fallback. `soba doctor` shows observations; `soba doctor --service SERVICE_ID --tcp` explicitly checks one TCP connection. Application authentication and operation still need separate checks. [Proxy and diagnostics guide](PROXY_DIAGNOSTICS.en.md)

## Read connection state

| State or label | Meaning |
| --- | --- |
| Saved | A definition exists; it does not prove any listener is running |
| Ready / active | The local transport is prepared within its grant |
| Direct | The backend has evidence of a direct peer path |
| Relay | The backend has evidence of a relayed encrypted path |
| Unknown | A current route has not been verified; do not infer it from latency |
| Reconnecting | Contact is unavailable or being re-established; application sessions may need reconnecting |
| Application unverified | The actual target application has not been validated |

Backend status may remain Unknown until a reliable path observation exists. A same-backend direct/relay route change is different from selecting another network. Do not assume that a path change, sleep/wake or reconnect preserves an established TCP stream. After interruption, confirm the peer identity and grant, then reconnect or retry the unfinished file as appropriate.

```sh
soba reconnect PEER_ID
soba status
```

`reconnect` probes the current peer again. It does not renew trust, autosave or service lifetimes, or resume canceled transfers or application sessions.

## Send text, images and files

### Text and images

Type or paste text, review it, then press Send. The Copy action copies selected text only when requested. There is no background clipboard reading or synchronization.

```sh
soba message PEER_ID "Hello from sample-node"
```

If message history cannot be saved, the receiver may already have the text. The Web UI keeps the editable draft and warns you to check the receiver and storage. It blocks the same text to the same peer for the current page session, even after a refresh or another successful message. This protection is not persistent across page reloads or application restarts; check the outcome before resending. [Delivery uncertainty and its memory budget](CAPACITY.en.md#web-delivery-uncertainty)

A pasted image enters the same reviewable file batch flow as a chosen image file. It is not sent merely because it was pasted. Images are transferred as files; received payloads are not automatically opened or executed.

### Several files or a folder

Select or drop files or folders into the UI. Review the peer, relative names, count and total size, remove unwanted items, then send the batch. Browser directory APIs may omit empty folders; verify them or use the CLI when needed.

```sh
soba send PEER_ID ./image.png ./notes.txt ./sample-folder
```

The CLI resolves paths relative to its current working directory. It rejects links and unsafe or unsupported entries instead of following them into other paths. Folder contents use relative paths; unrelated source paths are not exposed to the receiver.

Initial logical defaults are 256 manifest entries, including directories, and 1 GiB per file and per batch. Each can be set to a custom positive limit or explicitly unlimited; separately adjustable finite metadata, staging inventory, pending-offer, stream and byte budgets still constrain actual work. Local staging defaults to 600 seconds and can also use a custom duration or no total policy deadline. Browser upload requests also have a rolling 30-second no-progress read deadline, refreshed only when bytes arrive. The total staging deadline starts after reservation and is not extended by normal progress; unlimited staging removes only that total deadline. [Capacity and history](CAPACITY.en.md) explains the distinct choices. Large files stream through bounded buffers. Browser uploads first stage into the local agent: **100% local upload is not remote delivery**. Wait for receiver acceptance and the remote saved/completed state.

### Receive a batch

The default is to review each batch and accept it into a selected directory. Acceptance covers that batch's files and subfolders; it is not a standing permission for the sender.

```sh
soba accept TRANSFER_ID ./received
```

The CLI resolves relative paths from its working directory. Choose an appropriate destination on the receiving host; on Windows, quote a path such as `C:\Downloads\sobalink`. Files with an existing name are saved under a unique name without replacing the existing file. The receiver checks the declared size and SHA-256 before finalizing a file. Empty folders are preserved when present in the manifest. File permissions are restricted; executable attributes and links are not imported.

Decline a batch in the UI to refuse it. Cancel to stop ongoing work. Once the sender confirms receiver rejection or cancellation, the batch is terminal: its sending copies are removed and staging capacity is released. It cannot be retried; choose the files again to offer a new batch. A temporary communication failure remains retryable and retains its sending copies. If cleanup fails, the capacity stays reserved until cleanup succeeds. Canceling or revoking does not delete already saved files or retrieve data already sent.

### Opt into autosave for one peer

Choose a destination in the UI, or set the CLI default receive folder and enable autosave for an already trusted peer:

```sh
soba receive-dir ./received
soba autosave PEER_ID --on
```

`receive-dir` alone shows the current setting; `receive-dir --clear` clears the default. This setting grants no autosave permission and does not move existing per-peer destinations. `autosave --on` uses the peer's saved folder, or the default if it has none. Use `soba autosave PEER_ID --on --directory ./received-from-peer` to choose a different folder explicitly.

The autosave permission binds the selected backend, exact verified peer, its current trust generation and that directory. It does not apply to peers with the same display name. Persisted autosave is loaded only for its matching trusted identity; it can accept future batches without a new per-batch approval while enabled.

Disable autosave to restore per-batch acceptance. Peer Pause blocks messages and files and cancels active sends; after unpausing, select the files again for a new batch. It does not stop separately granted services. Changing the destination is another explicit choice. Revocation or an identity change invalidates the old grant; review and enable it again if needed. Autosave never enables overwrite, automatic opening, execution or clipboard sync.

```sh
soba autosave PEER_ID --off
soba pause PEER_ID
soba resume PEER_ID
```

Toggling autosave preserves pause; pause/resume preserves autosave and its directory. An enable, directory-change or resume error before file replacement leaves the prior settings intact. If the file was replaced but durability is uncertain, saved and effective peer settings reflect that replacement despite the error; inspect the current autosave, directory and pause state before retrying. If saving a disable fails, runtime auto-accept still stops, but durable disable is not confirmed. Repair storage and retry disabling before restarting the agent. After a successful disable, enable autosave explicitly if wanted again.

### Retry and cleanup

```sh
soba retry TRANSFER_ID
soba cancel TRANSFER_ID
soba forget TRANSFER_ID
```

Retry sends unfinished files from their beginning while both agents retain the same batch state. Successfully acknowledged files are not rewritten. After a lost acknowledgement, a repeated file request returns the existing saved acknowledgement. This is per-file retry, not byte-level resume.

Batch progress and acknowledgements are process-local. After either agent restarts, send a new batch and review what was already saved. A new batch can create a uniquely named copy. Forget removes terminal history and any retained sender staging copies, so that batch can no longer be retried. Original source files and files already saved by the receiver stay in place.

**Known crash-recovery limitation:** after an unexpected process exit, old sending copies in the private state directory’s `outgoing` subdirectory are inventoried and still count against the finite staging budget. The application does not automatically delete these orphan copies, and CLI/Web history clearing cannot remove batches from a previous process. Repeated interruptions can therefore exhaust the budget and block new sends even though growth remains bounded. Stop the matching instance, review and remove only confirmed orphan staging inside that `outgoing` directory, then restart so usage is inventoried again. Keep uncertain copies private; do not remove the identity/configuration files, original source files or received files. This is separate from a retryable batch retained by the currently running instance. A visible, reviewed orphan-cleanup control remains a usability follow-up.

Receiver-owned unfinished staging is restored into the receive quota through its private index and bounded startup inventory. Missing legacy records require an explicit review; damaged records, changed bindings or uncertain persistence keep receiving blocked. Sending and local management remain available. See [capacity and recovery](CAPACITY.en.md#receiver-recovery). Recovery never claims ambiguous roots, scans arbitrary folders, deletes saved output, or resumes a transfer. The [transfer free-space reserve](CAPACITY.en.md#transfer-free-space-margin) checks actual available disk space separately; it does not replace receive quota recovery.

## Stop, revoke and upgrade

```sh
soba status
soba stop-service SERVICE_ID
soba stop-shares
soba revoke PEER_ID
soba stop
```

Use current IDs from state. `soba stop-shares` stops every inbound share, including task-owned shares, while leaving the network node and outbound connections running. `soba revoke PEER_ID` removes application trust; [LAN pair revocation](LAN.en.md#revoke-recover-and-stop) also removes the transport pairing. Individual stop closes that service's active connections. Stop or Ctrl+C shuts down the agent, network and active work; private settings and identity remain. Expiry stops the grant and its tracked connections but does not recall sent data or cancel a remote application job.

Updates are explicit. Stop the matching instance first and keep any state backup private because it contains identity and peer information. Check the alpha.5 release's signed assets and successful verification before using this pin; do not assume prerelease state is compatible with every upgrade or downgrade:

```sh
mise use -g "packslip:github.com/webkaz-labs/sobalink[prerelease=true]@0.3.0-alpha.5"
mise exec -- soba version
mise exec -- soba start --offline
```

Inspect retained settings and startup approvals in the local UI. A profile from alpha.1 without a receive recovery index blocks new file reception until you review its prior receive locations, unfinished staging and saved files. Open [Preferences → File receiving → Review receiving](WEB_CONTROLS.en.md#review-blocked-file-receiving), or use `soba receive recovery confirm` with local management running. Confirm the review in the UI, or add CLI `--reviewed`, only after completing the [legacy receive review](CAPACITY.en.md#receiver-recovery). It initializes a missing index, does not repair damaged records and does not delete saved files or resume transfers.

Stop this offline instance before starting normally. Explicit outbound startup approvals may run again; inbound shares require a separate start. Keep the same `--state-dir` in every command when using a custom directory. Source-build users can rebuild the frontend and executable from a reviewed source instead. Use `soba help upgrade` for a short reminder and the signed-install instructions above for a published prerelease.

Legacy `tsnet-bridge` 0.2.x commands and configuration are incompatible with sobalink, and there is no automatic migration of that schema. Keep the old state separately and use a sobalink state directory. A fresh directory requires its own enrollment and trust decisions.

Saved-network startup failure can be recovered with `soba start --offline`. It keeps local management available, labels saved peers unverified/offline, and allows explicit pair revocation or relay reconfiguration without starting that network.

## Language, automation and troubleshooting

Global options precede the command:

```sh
soba --locale ja help
soba --locale en share --help
soba --state-dir ./sample-state status
soba --json-errors service show SERVICE_ID
```

Language follows `LC_ALL`, `LC_MESSAGES`, `LANG`, then the OS preference; Japanese uses Japanese messages and other locales fall back to English. The UI has language and theme controls. Command names, IDs, endpoint values and machine JSON are stable across languages. `status`, `peers`, groups, service workflows, guided `connect`/`share`, saved `rules` and application `settings` provide localized human views. Use `--json` for structured output; advanced typed actions retain structured results. Explicit login display modes provide private human-oriented output.

Normal typed commands need no JSON editing. Use `soba help examples`, `soba lan --help` and `soba service --help` for short recipes.

`--dry-run` returns JSON containing `applied: false`, the command, payload and `validation: "local-input-only"` without applying an action. It may read a running agent to choose unused names or reuse saved settings. To check inputs without an agent, supply an explicit name, for example `soba --dry-run share --name review-ssh --preset ssh --peers PEER_ID`. This does not verify identity, network reachability, free ports or later execution success. Invitation contents are redacted. It is unavailable for `start`, `ui` and `stop`.

`--json-errors` writes a stable `code` and explanatory `error` to stderr on failure, retaining a failing exit status. Success stdout stays unchanged. Automation should use `code`, not the potentially localized explanation.

Advanced `soba command NAME JSON_PAYLOAD` accepts nonsecret literal JSON. For invitations or other secret payloads, use `soba command NAME --json-file PATH` or pipe a JSON object into `soba command NAME --stdin`; invitations retain a separate 48 KiB input envelope, while other local command envelopes follow the selected finite resource budgets. Do not put secrets in literal arguments. Each sends a typed request through the same local core; it is not a bypass of trust, CSRF/session boundaries or filesystem policy. [API contract](../web/API.md)

| Problem | Next action |
| --- | --- |
| Local sign-in fails | Use the exact printed URL, run `soba ui`, and enter its new code |
| No peers | Check the selected network and complete its enrollment or pairing |
| Transfer waits | Have the receiving peer inspect and accept its batch or review its autosave policy |
| Partial transfer | Keep both agents running and retry the unfinished files; after a restart, create a new batch |
| Service not discovered | Check explicit share scope, active lifetime and current peer contact, or connect manually to a known service |
| `response_too_large` | Inspect current data and the relevant finite page/storage budget in the local UI; review a larger budget or explicit terminal-history cleanup, then retry. Cleanup does not delete received files |
| Local port conflict or listener budget | Stop unused work, narrow the ports, review the finite capacity budget, or explicitly select another local starting port |
| Message/history capacity | Inspect [capacity and reviewed cleanup](CAPACITY.en.md); increasing a count does not increase its separate storage budget |
| Socket operation not permitted | Run native/socket checks in an environment that permits listeners; baseline CI passed, but a local mock result does not establish live LAN connectivity |
| Unknown route or reconnect | Inspect current state and retry the app connection; do not assume a relay or uninterrupted TCP |

Report errors with secrets, local identity state, pairing capabilities and private endpoints removed. [Acceptance gates](VERIFICATION.en.md) distinguish source checks from real-device results.

### Direct LAN endpoint update delivery

Applying a reviewed local endpoint move saves and activates the local change separately from delivering its signed update to the selected peers. During that same action, delivery may make up to eight sequential attempts using the identical saved proof and exact reviewed destination. The waits are 250 ms, 500 ms, 1 s, 2 s, 4 s, 4 s and 4 s. The existing 30-second operation limit and original proof/approval deadlines can stop the action earlier; retries never extend them or renew consent. Every attempt checks current authority, and a changed receipt or destination stops delivery.

A recipient may temporarily refuse an update while another operation is running. Temporary refusal or a lost response may be retried after the previous connection is fully cleaned up. A substantive reply, including “review required,” stops automatic retries. Connection refusal and timeouts are not automatically retried. A recipient may still be restarting its listener, so delivery can end unconfirmed even after an earlier attempt was accepted. The optional delivery attempt count includes local admission checks that may stop before opening a connection.

“Saved” and “active” describe the local result. “Unconfirmed” does not prove that the recipient accepted or rejected the update. If the bounded attempts are exhausted, review the existing proof and current destination through the separate delivery preview/apply action before retrying. That explicit retry reuses the proof without changing its sequence or lifetime. It never discovers a new address automatically.

## Local transfer settings operations (source build)

Start the local agent first. Use the opaque resource ID returned by `list`, then review both settings together:

```sh
soba resource list --json
soba resource inspect --id RESOURCE_ID --json
soba resource preview --id RESOURCE_ID --concurrent-files default --concurrent-per-peer 2 --json
```

Review the preview and retain its exact `operationId`, `baseRevision`, `revision` and both choices. Substitute those values below; `apply` never performs an implicit new preview:

```sh
soba resource apply --id RESOURCE_ID --operation-id OPERATION_ID --base-revision BASE_REVISION --revision REVIEW_REVISION --concurrent-files default --concurrent-per-peer 2 --json
soba resource status --id RESOURCE_ID --operation-id OPERATION_ID --json
```

`default` and positive finite integers are supported; both choices are mandatory. Changing either choice requires a new preview. A stale review returns `resource_revision_conflict`; review the new preview before applying it. All output keys, IDs and status enums are stable JSON across locales. Global `--dry-run` validates inputs and prints the request without contacting the agent, reserving an operation, validating a review against current state or applying anything. `--offline` is unsupported.

An operation's `outcome` is historical evidence. Its separately returned `current` descriptor is the current settings observation; a successful older operation does not mean its settings are still current. `evidenceDurable` distinguishes durably recorded outcomes from uncertain evidence. `saved_not_applied` means configuration was saved but a runtime stage failed; `unknown` means success or rollback cannot be asserted. Inspect the current descriptor and stage fields before deciding what to do next.

Retries with the same operation ID and identical bound request return retained evidence without replaying the provider. Different content with the same ID is rejected. The bounded journal pins unfinished and unknown operations; they are not evicted or automatically replayed. Older evicted evidence is reported as `resource_operation_not_retained`, not executed again. Missing evidence is not proof of success or failure. Journal uncertainty blocks new applies until a later owned startup validates and durably republishes the evidence; startup does not replay the settings operation. Full pinned journals reject new operations. This is not an exactly-once guarantee, and local counters do not prevent whole-profile rollback.

Downgrading to the earlier PR1 read-only resource implementation makes the resource interface unavailable when the operation journal is populated; legacy settings commands continue. Do not delete private state to restore resource availability. These are source-level implementation and mock-test boundaries, not evidence of real power-loss durability or published-release acceptance.
