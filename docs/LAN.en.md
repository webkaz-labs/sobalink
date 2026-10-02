# Tailcat LAN setup and recovery

[日本語](LAN.ja.md) · [Main guide](GENERIC.en.md) · [Security](../SECURITY.md) · [Verification](VERIFICATION.en.md#tailcat-gate)

The local LAN setup UI, graph and shared Core/CLI implement explicit relay setup, recipient-bound invitations, pairing and durable revocation. At `14ee61f8`, [all four native targets passed](https://github.com/webkaz-labs/sobalink/actions/runs/37042721076) the real stock Tailcat loopback-relay scenario, including 130-second existing TCP across a real two-minute lease, bidirectional TCP/UDP, denied keys, revoke and cleanup. The newer UI has 117 passing local tests, strict TypeScript and two byte-identical builds; its actual-font/graph browser checks and new Core integration execution are pending on the next exact CI. Actual devices, direct LAN/WAN and sleep/wake remain unverified.

## Use the local UI

Open Network setup and choose LAN. Create or display this device's public identity, enter the trusted numeric relay endpoint and certificate pin, then activate it. The UI configures an existing relay; use the host command below to start an embedded relay. Invite the exact recipient key or paste a received invitation, review the target, and join. Pairing success leaves application trust off until explicitly approved.

Copying an invitation is explicit. Dismissing setup retains an outstanding invitation only in memory so it can still be canceled; consumed invitations lose their copy/cancel actions. Pause and revoke show their affected peer and scope before submission. For a running-backend change, stop and restart with `soba start --offline`, then return to setup.

The local SVG graph and its narrow-screen list show self-to-peer state only. Click a device or edge to open its existing details and permission controls. Japanese body text is configured at 15 px with secondary text at 13 px; actual Chromium font/metric evidence for this revision remains pending.

## Choose the relay explicitly

LAN mode uses Tailcat without account login. Choose either a relay hosted by this process on an exact private IP/high port, or an existing trusted relay identified by exact numeric IP/port and its TLS certificate SHA-256 pin. Both peers must select the same endpoint and pin. No public relay map, DNS bootstrap or arbitrary fallback is selected automatically.

The local relay binds only the chosen address; do not use a wildcard. A loopback address is useful only for same-host testing and is not reachable from another device. A hosted relay port must be at least 1024 and must not conflict with management, discovery, peer, pairing or backend-internal ports.

Stop an already running agent before changing backend or relay. Start management without reconnecting its saved backend:

```sh
soba start --offline
```

Leave that process running and use another terminal for commands. `--offline` is a startup option, not a promise that later explicit network commands are disabled.

To host on a chosen private address, replace the generic sample with an address assigned to this device:

```sh
soba command network.configure '{"mode":"lan","hostname":"sample-host","lan":{"kind":"host","address":"192.168.50.10:54430"}}'
soba status
```

The host creates a private relay identity and certificate. State exposes only its public endpoint/pin and public node key. Verify those with the joining device through a trusted channel. Do not copy the private state file.

The other device selects the same relay. `RELAY_CERT_SHA256` is a placeholder for the verified 64-character lowercase certificate hash:

```sh
soba command network.configure '{"mode":"lan","hostname":"sample-peer","lan":{"kind":"relay","address":"192.168.50.10:54430","certificateSHA256":"RELAY_CERT_SHA256"}}'
```

Selecting `kind: relay` does not start a relay. That operator is responsible for its listener and admission policy. Configuration validates and starts the selected backend; listener readiness is not proof that the other device is reachable. `soba setup --network lan` can reuse an existing selection, but cannot supply a first relay selection by itself.

## Pair the intended identity

Get each device's public identity without starting a network if needed:

```sh
soba command lan.identity '{}'
```

This creates and saves a LAN identity if one does not already exist and returns only its public key. Compare the intended recipient's key through a trusted channel. Names are labels, not authentication.

The inviter issues a one-time invitation for that exact key, with a lifetime of 1–600 seconds:

```sh
soba command lan.invite '{"recipientPublicKey":"RECIPIENT_PUBLIC_KEY","name":"sample-peer","ttlSeconds":300}'
```

The result includes an `invitation` string, expiry and recipient public key. The invitation string contains a secret capability. Share it only with that intended peer through an appropriate private channel. Keep it out of URLs, screenshots, routine status, logs, shell history and literal command arguments. Do not publish an actual invitation as a configuration example.

The payload for `lan.join` is one JSON object whose `invitation` value is that complete string. The shape below is a placeholder, not a working invitation:

```json
{"invitation":"COMPLETE_INVITATION_JSON_STRING"}
```

Use a private regular file or a pipe for secret payloads. These bounded input paths avoid putting the secret itself into command arguments:

```sh
soba command lan.join --json-file ./private-join.json
soba command lan.join --stdin < ./private-join.json
```

The file contains the wrapper object above, not the entire invitation-creation response and not a raw unwrapped invitation. JSON-encode the string; do not concatenate or manually escape secret content into a shell command. Input is limited to 48 KiB, and interactive-terminal stdin is rejected. Protect the input file and any invitation-creation output; remove temporary copies when no longer needed. The CLI does not make a file private merely by reading it.

A successful join returns `paired: true` and `trusted: false`. Pairing commits private state before acknowledging success and establishes the transport relationship only. Each receiving device separately grants application trust:

```sh
soba peers
soba trust PEER_ID
```

You can then use explicit messages, batch transfers and scoped services from the [main guide](GENERIC.en.md). Autosave remains a separate opt-in bound to backend, exact peer, trust generation and destination.

## Cancel or recover an invitation

The inviter can cancel its own outstanding invitation with the same wrapper shape and private payload input:

```sh
soba command lan.cancel --json-file ./private-invitation.json
```

| Result | Next action |
| --- | --- |
| `lan_environment_proxy` | Remove unsupported proxy variables for this process, then restart LAN |
| `lan_environment_override` | Remove unsupported Tailscale overrides for this process, then restart LAN |
| `lan_relay_mismatch` | Compare the selected numeric endpoint and exact certificate pin with the invitation |
| `lan_certificate_expired` | Check the clock; otherwise use offline management to revoke old pairs and reconfigure the relay |
| `network_restart_required` | Stop the agent, run `soba start --offline`, then change the network, name or relay |
| `lan_cancel_invite_first` | Cancel the invitation you issued to this peer before joining the peer's invitation |
| `lan_pair_reply_uncertain` | Do not retry blindly. The other side may have committed; inspect and revoke its completed pair before creating a fresh invitation |
| `lan_remote_paired_local_save` | The other side saved the pair but this side did not. Revoke the remote approval before retrying |
| `lan_revoke_not_persisted` | LAN has stopped because durable revocation could not be confirmed. Repair private-state storage before restarting |
| Expired, canceled or invalid invitation | Verify the intended identity and selected relay, then issue a fresh invitation if still wanted |

Network failures also expose the stable `self.errorCode` in state. The UI localizes known recovery codes and keeps the unchanged technical explanation separate.

An uncertain reply is not success or proof that neither side changed. Reissuing the same command is not a safe substitute for inspecting both sides.

## Revoke, recover and stop

Application trust and transport pairing are separate:

```sh
soba revoke PEER_ID
soba command lan.revoke '{"peerId":"PEER_ID"}'
soba stop
```

The first command removes application trust. `lan.revoke` also removes the LAN transport pair, its application trust/autosave and affected services, and persists that removal. Run it on each side when ending the relationship completely. A storage failure causes the LAN backend to stop instead of claiming durable success.

If the saved network will not start, stop the agent and restart with `soba start --offline`. Saved peers remain visible as unverified/offline so you can explicitly revoke them. Changing the relay requires all current LAN pairs to be revoked and no active backend. The same recovery path is available after a saved local certificate expires; do not bypass certificate checks or silently reuse old pair permissions.

Peer Pause blocks messages/files and cancels active sends. After unpausing, select those files again; canceled sends do not automatically restart. Disabling autosave instead returns to batch-by-batch consent. Neither operation stops a separately granted service; stop that service or revoke the pair explicitly.

## Traffic and remaining limits

The mode permits direct peer traffic, encrypted payload through the selected relay and HTTPS/ICMP diagnostics to that relay endpoint. It is not strict LAN isolation or zero external traffic. Required build tags omit port mapping, captive-portal probes and system-proxy support; unsupported proxy/backend override environments are rejected.

The embedded relay authenticates a sealed HTTPS bootstrap before admitting the invited transport role. Unknown keys have no blanket exception. Its two-minute relay connection lease rechecks admission; a previously admitted relay session can persist for up to two minutes after removal, or about four minutes from initial bootstrap when temporary admission overlaps a lease. Application authorization and tracked application flows are revoked immediately.

Path state remains Unknown until measured. A saved pairing is not evidence of contact. The graph shows this device and observed peers only; it must not invent a full mesh, bandwidth or direct/relay paths. Direct/relay changes, sleep/wake and reconnection do not guarantee existing TCP continuity. The successful native fixture establishes one loopback relay scenario across a lease boundary, not real LAN/WAN/NAT migration or a production zero-egress policy.
