# Saved services, groups, and tasks

[日本語](SAVED_SERVICES.ja.md) · [User guide](GENERIC.en.md)

Saved definitions work with the agent stopped or with its selected network offline. Saving or importing never opens a listener or restores an old permission. Use the global `--offline` flag to edit only saved metadata under an exclusive profile lock, without starting the agent, generating network credentials, or loading transfer and pairing state. If the agent is already running, omit that flag. `soba start --offline` instead opens the local management UI without connecting a network.

```sh
soba --offline service save share --backend tailnet --name example-web --ports 8080 --peers PEER_ID
soba --offline service save connect --backend tailnet --name example-ssh --preset ssh --peer PEER_ID
soba --offline service show SERVICE_ID
```

The returned ID identifies the saved definition. `service save ... --replace SERVICE_ID` explicitly replaces a stopped definition after checking its current revision. To start one saved definition, use `soba service restart SERVICE_ID`; it rechecks the current network and peer identities. `soba service copy SERVICE_ID` creates and starts a separate definition with an unused name.


Use `soba --offline rules` to list saved definitions with the agent stopped. The [global Web catalog](WEB_CONTROLS.en.md) also supports save-only creation, editing, copying and reviewed removal when a saved peer is unavailable. [Application settings and RustDesk](CLIENT_HELPERS.en.md) explain the shared endpoint helpers.

## Groups and readiness

```sh
soba --offline group save example SERVICE_ID_1 SERVICE_ID_2
soba --offline group list
soba group start example --confirm
soba services wait SERVICE_ID_1 SERVICE_ID_2
soba group stop example
```

Saving a group does not start anything. Replacing a saved group requires `--replace`. A start reviews the complete selected definitions and uses their saved lifetimes by default. `services start`, `group start` and `task` accept a positive whole-second `--ttl` for this invocation only; saved definitions and their revisions stay unchanged. Shares require review confirmation; `--confirm` is the noninteractive form. If any new service cannot start, every service newly started by that operation is stopped. Services already active with the same owner and scope remain untouched and their lifetimes are not extended.

Readiness means the authorized listeners are ready. It does not prove application compatibility, a successful remote login, or completion of a remote job. `services start`, `services stop`, and `services wait` also accept `--group NAME` or individual IDs. `wait-ready` is an alias for `services wait`. Explicit `--owner NAME` on `services`/`group` start, stop and wait scopes those operations to the same owner; it does not create a renewable task lease. Use the [guided CLI guide](CLI_GUIDE.en.md) for examples.

## Temporary services owned by a command

```sh
soba task --group example --confirm -- APPLICATION ARGUMENT...
```

The task starts the reviewed selection, waits for listener readiness, then runs the exact local command and arguments directly. It owns only those services, renews a 30-second lease every 10 seconds, and attempts owned cleanup when the command exits, fails, or is canceled. If the wrapper disappears or cleanup cannot reach the agent, successfully leased permissions expire no later than 30 seconds after their last renewal. The invocation's permission lifetime also remains in effect, using the saved lifetime unless `--ttl` overrides it; renewing a task lease does not extend that permission.

Unix cancellation terminates the local process group. Windows cancellation follows direct-child process semantics. Stopping transport does not promise to cancel a job already submitted remotely; use that application's own cancellation mechanism.

## Stop or remove

`soba stop-shares` revokes every active share, including task-owned shares, while retaining the node and outbound connections. `soba stop-service SERVICE_ID` stops an ordinary manually owned service. Task cleanup uses its owner to avoid stopping another task's work.

```sh
soba service delete SERVICE_ID
soba service delete SERVICE_ID --apply --review REVISION
```

Deletion first shows active state and affected groups. Add `--stop-active` to explicitly stop an active permission, and `--remove-from-groups` to remove group references. Groups left empty by that deletion are removed. A changed definition or group profile invalidates the review. A failed durable save leaves the live permission untouched.

## Private export and import

```sh
soba --offline profile export --output definitions.json
soba --offline profile import definitions.json
soba --offline profile import definitions.json --apply --review REVISION
```

Exports contain only saved service scopes and groups. They exclude local node identity, credentials, login state, trusted-peer state, LAN pairing capabilities, receive destinations, messages, and task owners or leases. Service names, peer references, and ports remain part of the definitions: review and redact them before sharing. Export creates a new private local file outside the application's state directory; it never uploads the file or overwrites an existing output.

Import previews replacement of saved definitions. Its review binds both the destination's current profile and the incoming definitions, so changing either requires another preview. Stop active services before applying an import. The existing local identity, network settings, trust, and received data are preserved, and every imported service remains stopped.

Groups and group membership obey the configured `groups` and `groupMembers` admission choices. Lower choices retain saved groups and members; they constrain growth. Native tests cover disabled persistence, revision guards, rollback, ownership, lease expiry, and process cancellation. Real enrollment and application compatibility still require real-device acceptance.
