# Tailcat LAN setup and recovery

[日本語](LAN.ja.md) · [Main guide](GENERIC.en.md) · [Security](../SECURITY.md) · [Verification](VERIFICATION.en.md#tailcat-gate)

This guide covers the sobalink development build. At `5dd6b8c9`, [all four native targets passed](https://github.com/webkaz-labs/sobalink/actions/runs/37046723268) a loopback relay test across an actual two-minute lease and Core two-peer text/file/share/revoke integration. Later UI, typed CLI and Core changes have local evidence only and are not covered by that CI. Actual devices, direct LAN/WAN/NAT, sleep/wake and native IME remain unverified. [Verification by source](VERIFICATION.en.md)

## Use the local UI

Open Network setup, choose LAN and create or display this device's public identity. Then choose the appropriate path:

1. To host, choose “Host a relay on this device”, select a private address/interface from the current list and an unused TCP port, review the listener, then start it. The default port is 48443 and can be changed. An empty list needs a private-network connection and refresh. Firewall settings are unchanged; a running listener does not prove another device can reach it
2. To join from a fresh profile, send this device's public ID to the inviter and paste its private invitation. Review the host identity, exact relay/pin and expiry, then choose “Connect and pair”. The UI configures that reviewed relay and pairs; an existing different network or relay requires explicit stop/offline recovery first
3. “Advanced: use an existing relay” remains available for a verified numeric endpoint and certificate pin. Once connected, invite the exact recipient key or join a received invitation. Pairing leaves application trust off until explicitly approved

“Stop sobalink” reviews active transfers and services before stopping the app, hosted relay and connections. The local control page disconnects. Reopen the app to continue. Ordinary definitions remain stopped; only separately reviewed [outbound startup approvals](STARTUP.en.md) can start fresh connections. Previous transfer progress never resumes.

Copying an invitation is explicit. Dismissing setup retains an outstanding invitation only in memory so it can still be canceled; consumed invitations lose their copy/cancel actions. Pause and revoke show their affected peer and scope before submission. For a running-backend change, stop and restart with `soba start --offline`, then return to setup.

The local SVG graph and accessible list show self-to-peer state only. Both remain available on narrow layouts, preserving the view explicitly selected. Click a device or edge to open its details and permission controls. Record actual font/metric and interaction acceptance for the latest source separately in [verification](VERIFICATION.en.md).

## Choose the relay explicitly

LAN mode uses Tailcat without account login. Choose either a relay hosted by this process on an exact private IP/high port, or an existing trusted relay identified by exact numeric IP/port and its TLS certificate SHA-256 pin. Both peers must select the same endpoint and pin. No public relay map, DNS bootstrap or arbitrary fallback is selected automatically.

The local relay binds only the chosen address; do not use a wildcard. A loopback address is useful only for same-host testing and is not reachable from another device. A hosted relay port must be at least 1024 and must not conflict with management, discovery, peer, pairing or backend-internal ports.

Stop an already running agent before changing backend or relay. Start management without reconnecting its saved backend:

```sh
soba start --offline
```

Leave that process running and use another terminal for commands. `--offline` is a startup option, not a promise that later explicit network commands are disabled.

On the hosting device, use `soba lan addresses` to list candidates and choose a private address. The IP below is fictional; replace it with an actual candidate. Listing a candidate does not prove reachability:

```sh
soba lan addresses
soba setup --network lan --host 192.168.50.10:54546
soba status
```

The host creates a private relay identity and certificate. State exposes only its public endpoint/pin and public node key. Verify those with the joining device through a trusted channel. Do not copy the private state file.

The other device selects the same relay. `RELAY_CERT_SHA256` is a placeholder for the verified 64-character lowercase certificate hash:

```sh
soba setup --network lan --relay 192.168.50.10:54546 --certificate RELAY_CERT_SHA256
```

`--relay` does not start a relay; its operator provides the listener and admission policy. Configuration validates and starts the selected backend, but listener readiness does not prove that the other device is reachable. `--host` and `--relay` are mutually exclusive. Use `soba setup --network lan` to reuse a saved selection; add `--name sample-node` only to change the node name.

## Pair the intended identity

With the agent running on each device, obtain its public identity. This also works with no selected network or under `start --offline`:

```sh
soba lan identity
```

This creates and saves a LAN identity if needed and returns only the public key in `publicKey`. Send the joining device's exact 64-character public key to the inviter through a trusted channel and compare it. Names are labels, not authentication.

On the inviter, after configuring the relay, issue a one-time invitation for that exact key. Replace `PUBLIC_ID` with the joining device's public key:

```sh
soba lan invite --to PUBLIC_ID
```

The default lifetime is five minutes. Override it with `--ttl`, in whole seconds from 1 second to 10 minutes, and set a display name with `--name sample-peer`. The result includes the secret `invitation` string, expiry and recipient public key. Save this output privately as `private-invitation.json` and share it only with the intended peer through an appropriate private channel. Keep secrets out of URLs, screenshots, shared logs, shell history and command arguments.

On the joining device, inspect the received file before joining:

```sh
soba lan inspect --json-file ./private-invitation.json
```

`inspect` checks the recipient against this device and validates the expiry, then returns the host public key/name and relay endpoint/pin without joining. Verify the intended host and configure that exact relay using `setup --network lan --relay ... --certificate ...` above before joining. `join` never changes the relay automatically:

```sh
soba lan join --json-file ./private-invitation.json
```

`inspect`, `join` and `cancel` accept either the full `lan invite` response or raw invitation JSON. No hand-written wrapper or manual JSON-string escaping is needed. A pipe or redirected input can replace the file option:

```sh
soba lan inspect --stdin < ./private-invitation.json
```

Input is limited to 48 KiB and interactive-terminal stdin is rejected. Protect the file and invitation-creation output, and remove unnecessary temporary copies. Reading a file does not change its permissions. `--dry-run` redacts invitation contents; use a real `inspect` call to validate the recipient, expiry and relay.

A successful join returns `paired: true` and `trusted: false`. Pairing commits private state before acknowledging success and establishes the transport relationship only. Each receiving device separately grants application trust to its sender:

```sh
soba peers
soba trust PEER_ID
```

Then use explicit messages, batch transfers and scoped services from the [main guide](GENERIC.en.md). Autosave remains a separate opt-in bound to backend, exact peer, trust generation and destination.

## Cancel or recover an invitation

The inviter can cancel its own outstanding invitation using the private file it issued:

```sh
soba lan cancel --json-file ./private-invitation.json
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

Add `--json-errors` before the CLI command to receive failures as `{code,error}` on stderr. Network failures also expose the stable `self.errorCode` in state. The UI localizes known recovery codes and keeps the unchanged technical explanation separate.

An uncertain reply is not success or proof that neither side changed. Reissuing the same command is not a safe substitute for inspecting both sides.

## Revoke, recover and stop

Application trust and transport pairing are separate:

```sh
soba revoke PEER_ID
soba lan revoke PEER_ID
soba stop
```

The first command removes application trust. `soba lan revoke` also removes the LAN transport pair, its application trust/autosave and affected services, and persists that removal. Run it on each side when ending the relationship completely. A storage failure causes the LAN backend to stop instead of claiming durable success.

If the saved network will not start, stop the agent and restart with `soba start --offline`. Saved peers remain visible as unverified/offline so you can explicitly revoke them. Changing the relay requires all current LAN pairs to be revoked and no active backend. The same recovery path is available after a saved local certificate expires; do not bypass certificate checks or silently reuse old pair permissions.

Peer Pause blocks messages/files and cancels active sends. After unpausing, select those files again; canceled sends do not automatically restart. Disabling autosave instead returns to batch-by-batch consent. Neither operation stops a separately granted service; stop that service or revoke the pair explicitly.

## Traffic and remaining limits

The mode permits direct peer traffic, encrypted payload through the selected relay and HTTPS/ICMP diagnostics to that relay endpoint. It is not strict LAN isolation or zero external traffic. Required build tags omit port mapping, captive-portal probes and system-proxy support; unsupported proxy/backend override environments are rejected.

The embedded relay authenticates a sealed HTTPS bootstrap before admitting the invited transport role. Unknown keys have no blanket exception. Its two-minute relay connection lease rechecks admission; a previously admitted relay session can persist for up to two minutes after removal, or about four minutes from initial bootstrap when temporary admission overlaps a lease. Application authorization and tracked application flows are revoked immediately.

Path state remains Unknown until measured. A saved pairing is not evidence of contact. The graph shows this device and observed peers only; it must not invent a full mesh, bandwidth or direct/relay paths. Direct/relay changes, sleep/wake and reconnection do not guarantee existing TCP continuity. The successful native fixture establishes one loopback relay scenario across a lease boundary, not real LAN/WAN/NAT migration or a production zero-egress policy.
