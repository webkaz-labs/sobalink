# Application settings and RustDesk helper

[日本語](CLIENT_HELPERS.ja.md) · [User guide](GENERIC.en.md) · [Development principles](DEVELOPMENT_PRINCIPLES.en.md)

`soba settings` returns the saved application endpoints, exact port mappings, current listener state and application-specific hints. The RustDesk helper saves four forwards and the server's public key as one group. It does not change application settings or start listeners. Automated tests cover metadata and command behavior; RustDesk screen sharing and input control remain unverified.

## Copy ordinary application settings

```sh
soba settings
soba settings --service SERVICE_ID
soba settings --group GROUP_NAME --json
```

For a stopped agent, add the global `--offline` flag before the command. Add `--state-dir PATH` before the command when selecting a different profile, and retain that selection in follow-up commands.

For a single-port SSH forward, the shared Core API returns an SSH command with a `HostKeyAlias` derived from the backend, immutable peer ID and remote SSH port. Replace `USER` with the SSH username. Independently verify the server's host key; this alias can require a new `known_hosts` entry. Keep host-key checking enabled.

For a single-port Web forward, the API returns an HTTP candidate. It does not test HTTP or HTTPS. HTTPS may need the original hostname, SNI and origin; a loopback URL can fail certificate name validation. Preserve certificate verification. UDP services and multi-port mappings do not receive a fabricated single-port HTTP or SSH command.

`mappings` contains inclusive local/remote port pairs. It remains compact for ranges and accounts for excluded ports. A mapped forward allocates local ports consecutively across the remaining remote ports. `localEndpoint` appears only when the mapping has one port. `remoteEndpoints` contains copyable scalar endpoints only; `remoteHosts` supplies the current addresses for multi-port mappings. Both use current addresses only when the selected backend and immutable peer identity are available. Offline output retains the saved peer IDs and port mappings without guessing an address.

A saved endpoint is usable only after the relevant service is explicitly started. A ready listener proves transport readiness, not application success. These application endpoints are not SOCKS proxy addresses.

## Save a RustDesk group

Obtain the RustDesk server's existing public key and the immutable peer IDs from the selected network. These examples use placeholders; the helper does not generate a server key or configure a remote server.

```sh
soba --offline rustdesk setup --backend tailnet --id-peer ID_PEER --key PUBLIC_KEY
```

Review all four mappings and the public key. Repeat the same setup options with `--apply --review REVISION`, using the revision from the preview. Use `--json` for the stable local API representation.

Defaults are:

| Role | Protocol | Local endpoint | Remote destination |
| --- | --- | --- | --- |
| NAT | TCP | `127.0.0.1:32115` | ID peer, port `21115` |
| ID | TCP | `127.0.0.1:32116` | ID peer, port `21116` |
| Heartbeat | UDP | `127.0.0.1:32116` | ID peer, port `21116` |
| Relay | TCP | `127.0.0.1:32117` | Relay peer, port `21117` |

The relay peer defaults to the ID peer. Override it with `--relay-peer RELAY_PEER`. `--id-port`, `--relay-port`, `--local-id-port` and `--local-relay-port` select custom ports. NAT always uses the ID port minus one on each side. ID ports must be 1025–65535; relay ports must be 1024–65535. The local relay TCP port must differ from local ID and NAT ports. When ID and relay use one peer, their remote TCP ports must also differ.

`--name NAME` selects a new group; `--backend lan` selects the LAN backend. `--loopback-host ::1` uses IPv6 loopback. The default lifetime is `until-stopped`; `--ttl 72h` selects a finite duration for every rule. Saving a group does not enroll a node, create inbound permissions, start a proxy, or edit the RustDesk application.

Saving is atomic: validation, revision, capacity or file-write errors cannot leave a partially saved helper. The existing profile's service, group and byte budgets apply. Repeating the same setup preserves the saved group and does not restart it. A different setup with an existing group name is rejected; choose a different name or explicitly review and edit the existing definitions.

## Review, start, use and stop

After starting and connecting soba through the ordinary network workflow, review the saved group and start it as a separate action:

```sh
soba --dry-run group start rustdesk
soba group start rustdesk
soba rustdesk settings --group rustdesk
```

With a custom profile, use the same `--state-dir PATH` for all commands. The start path revalidates the exact saved services, current immutable peers, permissions and listener conflicts. A stopped-agent preview cannot establish live port availability. Required remote services and permissions must already exist.

Before manually changing RustDesk, back up its existing server and proxy settings. Copy the helper's exact ID server, relay server and public key. Keep the proxy field blank and UDP enabled; connect using `remote-ID/r`. All participating endpoints must use the same local relay address and port. Mixed profiles remain unverified.

```sh
soba group stop rustdesk
```

Stopping soba or its group does not restore RustDesk's previous application settings. SOCKS CONNECT-only mode cannot register a controlled RustDesk 1.4.9 endpoint with OSS server 1.1.16; other client roles require separate validation. This forward helper does not configure SOCKS credentials.

The public-key metadata travels with the four-role group through local profile export/import. Individual rule or group edits that break the role mapping are rejected. To deliberately detach the helper metadata, review the group and send `group.save` with its current revision, a group without `rustdesk`, and `removeRustDesk: true`; the forwards remain saved. Profile import uses its existing whole-bundle preview/revision review. No helper metadata is advertised through peer service discovery.

## Shared local API

CLI and Web use the same authenticated local Core commands:

- `rustdesk.preview`: `{configuration: RustDeskSetup}`; returns the canonical setup, four service definitions, group, review revision and client settings
- `rustdesk.save`: the same configuration plus `expectedRevision`; atomically saves metadata without starting services
- `rustdesk.settings`: `{group: NAME}`; returns the exact ID/relay settings and four-role states
- `client.settings`: optional `ids` or `group`; returns exact mappings, current addresses where available, SSH/HTTP hints and bilingual notices

The same commands are available through the metadata-only offline path. They do not load transport identities, create credentials, probe applications or grant network access. `application` remains `unverified`, including when listeners are ready.
