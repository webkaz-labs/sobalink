# Use sobalink

[日本語](GENERIC.ja.md) · [Overview](../README.en.md) · [Security](../SECURITY.md) · [Verification](VERIFICATION.en.md)

This guide describes the local sobalink development draft and its `soba` executable. It is not an installation guide for the legacy `tsnet-bridge` releases. The local Web UI is the normal workflow; CLI commands are useful for repeated actions and automation. Both use the same Go authorization and storage boundaries.

## Start and open the local UI

[Build this checkout](DISTRIBUTION.md#build-this-checkout), then run:

```sh
soba
```

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

A fresh profile starts with no network selected. UI preferences, trust and service definitions are saved. Active service grants and transfer progress are not automatically restarted or renewed after a process restart.

## Choose one network

| Choice | Setup | Important boundary |
| --- | --- | --- |
| Existing Tailnet | Activate the embedded node, then use the official interactive Tailscale sign-in flow | It is a separate node in the chosen Tailnet; the OS Tailscale app's session is not imported |
| Tailcat / explicit trusted relay | Explicitly select a numeric relay endpoint and certificate pin, then pair specific peers | Integration is in progress; arbitrary public fallback and a strict zero-external-traffic claim are excluded |
| No network | Leave the agent local | No peer transfer or service connection |

For the Tailnet path:

```sh
soba setup --network tailnet --name sample-node
soba login
soba peers
```

Review the returned official sign-in URL and complete enrollment. Treat enrollment URLs as secrets. The tool does not accept an auth key in command arguments, and it does not change OS routes or DNS. Tailnet policy and service authentication still apply.

Selecting a mode is explicit. Backend changes require stopping the agent; do not expect an existing TCP session to transfer between Tailnet and Tailcat. Tailcat is unavailable in the initial snapshot; setup and pairing controls are still being integrated, so no unverified copy-and-paste pairing command is prescribed here. Follow the available UI and `soba setup --help`; an unavailable-mode error means that path is not ready.

Tailcat's permitted traffic includes direct peer traffic, encrypted payload via the explicitly selected relay, and HTTPS/ICMP diagnostics to that relay endpoint. A relay may be self-hosted or another endpoint explicitly trusted by the user. This is not a LAN egress sandbox. [Transport detail](ARCHITECTURE.md#network-boundaries)

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

## Send text, images and files

### Text and images

Type or paste text, review it, then press Send. The Copy action copies selected text only when requested. There is no background clipboard reading or synchronization.

```sh
soba message PEER_ID "Hello from sample-node"
```

A pasted image enters the same reviewable file batch flow as a chosen image file. It is not sent merely because it was pasted. Images are transferred as files; received payloads are not automatically opened or executed.

### Several files or a folder

Select or drop files or folders into the UI. Review the peer, relative names, count and total size, remove unwanted items, then send the batch. Browser directory APIs may omit empty folders; verify them or use the CLI when needed.

```sh
soba send PEER_ID ./image.png ./notes.txt ./sample-folder
```

The CLI resolves paths relative to its current working directory. It rejects links and unsafe or unsupported entries instead of following them into other paths. Folder contents use relative paths; unrelated source paths are not exposed to the receiver.

The current product limit is 256 manifest entries and 1 GiB per batch, including directory entries. Metadata, depth, pending offers, concurrent streams and reserved bytes have additional limits. Large files stream through bounded buffers. Browser uploads first stage into the local agent: **100% local upload is not remote delivery**. Wait for receiver acceptance and the remote saved/completed state.

### Receive a batch

The default is to review each batch and accept it into a selected directory. Acceptance covers that batch's files and subfolders; it is not a standing permission for the sender.

```sh
soba accept TRANSFER_ID /absolute/receive-directory
```

Use an appropriate absolute path on the receiving host; on Windows, quote a path such as `C:\Downloads\sobalink`. Files with an existing name are saved under a unique name without replacing the existing file. The receiver checks the declared size and SHA-256 before finalizing a file. Empty folders are preserved when present in the manifest. File permissions are restricted; executable attributes and links are not imported.

Decline a batch in the UI to refuse it. Cancel to stop ongoing work. Canceling or revoking does not delete already saved files or retrieve data already sent.

### Opt into autosave for one peer

In that peer's settings, select an absolute directory and explicitly enable autosave. The permission binds the exact verified peer, its current trust generation and that directory. It does not apply to peers with the same display name. Persisted autosave is loaded only for its matching trusted identity; it can accept future batches without a new per-batch approval while enabled.

Pause or disable autosave from the peer controls. A peer pause also pauses its transfers. Changing the destination is another explicit choice. Revocation or an identity change invalidates the old grant; review and enable it again if needed. Autosave never enables overwrite, automatic opening, execution or clipboard sync.

### Retry and cleanup

```sh
soba retry TRANSFER_ID
soba cancel TRANSFER_ID
soba forget TRANSFER_ID
```

Retry sends unfinished files from their beginning while both agents retain the same batch state. Successfully acknowledged files are not rewritten. After a lost acknowledgement, a repeated file request returns the existing saved acknowledgement. This is per-file retry, not byte-level resume.

Batch progress and acknowledgements are process-local. After either agent restarts, send a new batch and review what was already saved. A new batch can create a uniquely named copy. Forget removes terminal history; it does not delete received files.

## Share a local service

Start the real application first, with its own authentication. Select Share, the current peers, TCP or UDP, ports and exclusions, and an expiry. Review the effective scope before starting.

```sh
soba share --name preview --network tcp --ports 3000-3003 --peers PEER_ID --ttl 1h
```

This makes the selected embedded-node ports available to the selected peers and maps each port to the same numeric loopback port. It does not publish an internet URL or configure the application. A recipient with ordinary Tailscale access can use the provider's embedded-node address and permitted port without installing sobalink.

- Select 1–32 current peers explicitly; there is no implicit “all peers” grant
- Lifetime is a whole number of seconds from 1 second to 24 hours; the default CLI lifetime is 1 hour
- TCP shares retain compact ranges instead of opening one OS listener for every port
- A range also covers a service started later on an allowed port before expiry. Prefer the narrowest useful range
- Use `--exclude`, for example `--ports 3000-3010 --exclude 3005-3007`, to remove ports
- Discovery `54543`, peer API `54544`, pairing `54545` and backend-internal endpoints cannot be exposed through a service share
- UDP requires individual sockets and shares a total 64-listener budget with local connection listeners. Narrow the plan if it exceeds capacity

Optional `--discoverable` exposes only minimal active service metadata to the selected peers. It is off by default in the CLI. A discovery result says a grant is available, not that the application works. The receiving UI rechecks a selected discovered service before starting the connection.

A port conflict fails the operation; the tool does not silently switch ports or widen scope. Same-port TCP sharing is separate from a client's optional local-port remapping.

## Connect to a peer's service

Choose an available share in the UI, or enter a current Tailnet peer and the intended service manually. An ordinary Tailscale target needs no sobalink. Its application must listen on the destination address and port and allow access through Tailnet and application policy.

```sh
soba connect --name preview --network tcp --ports 3000 --peer PEER_ID --ttl 1h
```

Copy the actual local endpoint from status into your client. A single port defaults to the same local port. For a privileged or occupied destination port, explicitly choose another local starting port:

```sh
soba connect --name ssh --network tcp --ports 22 --local-port 10022 --peer PEER_ID --ttl 1h
```

Connect listeners bind numeric loopback, use local ports 1024–65535 and consume the shared 64-listener budget. `--local-port` maps sorted effective remote ports to consecutive local ports; review the displayed mapping. It does not change a share's same-port loopback target.

A ready listener is only transport readiness. Verify authentication and an actual operation in the target app. Preserve TLS certificate names, SSH host-key verification, origin rules and the app's own authorization. A tunnel does not adapt stdio protocols into HTTP or run remote jobs.

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

## Stop, revoke and upgrade

```sh
soba status
soba stop-service SERVICE_ID
soba revoke PEER_ID
soba stop
```

Use current IDs from state. Individual stop closes that service's active connections. Stop or Ctrl+C shuts down the agent, network and active work; private settings and identity remain. Expiry stops the grant and its tracked connections but does not recall sent data or cancel a remote application job.

No released sobalink upgrade path exists yet. For a new development build, stop the process, keep a private backup of state if needed, rebuild the frontend and binary from the intended source, run `soba version`, then inspect state before explicitly restarting services. Keep backups private because they contain identity and peer information. A fresh state directory requires its own enrollment and trust decisions. Legacy `tsnet-bridge` commands and configuration are not compatibility requirements for this new product.

## Language, automation and troubleshooting

Global options precede the command:

```sh
soba --locale ja help
soba --locale en share --help
soba --state-dir ./sample-state status
```

Language follows `LC_ALL`, `LC_MESSAGES`, `LANG`, then the OS preference; Japanese uses Japanese messages and other locales fall back to English. The UI has language and theme controls. Command names, IDs, endpoint values and machine JSON are stable across languages. CLI status and action responses use JSON rather than interactive prompts.

Advanced `soba command NAME JSON_PAYLOAD` sends a typed request through the same local core; it is not a bypass of trust, CSRF/session boundaries or filesystem policy. [API contract](../web/API.md)

| Problem | Next action |
| --- | --- |
| Local sign-in fails | Use the exact printed URL, run `soba ui`, and enter its new code |
| No peers | Check the selected network and complete its enrollment or pairing |
| Transfer waits | Have the receiving peer inspect and accept its batch or review its autosave policy |
| Partial transfer | Keep both agents running and retry the unfinished files; after a restart, create a new batch |
| Service not discovered | Check explicit share scope and expiry, or use a manual connection to a known service |
| Local port conflict or listener cap | Stop an unused connection, narrow the ports, or explicitly select another local starting port |
| Socket operation not permitted | Run in an environment that permits the required listeners; a mock test cannot establish live connectivity |
| Unknown route or reconnect | Inspect current state and retry the app connection; do not assume a relay or uninterrupted TCP |

Report errors with secrets, local identity state, pairing capabilities and private endpoints removed. [Acceptance gates](VERIFICATION.en.md) distinguish source checks from real-device results.
