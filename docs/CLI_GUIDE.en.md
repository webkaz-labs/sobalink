# Guided and automated CLI workflows

[日本語](CLI_GUIDE.ja.md) · [Principles](DEVELOPMENT_PRINCIPLES.en.md)

`soba connect` and `soba share` offer guided terminal selection, editing and review. `status`, `peers`, saved-service workflows and group operations use readable output by default; add `--json` for stable machine output. Explicit connect/share configuration flags and dry-run previews retain their JSON results. The checks below cover synthetic peers and service metadata; they do not establish actual enrollment, real-device application compatibility or OS lifecycle behavior.

## Start with the shortest path

```sh
soba init
soba start --background
soba setup --network tailnet
soba login --link --wait
soba connect
```

`init` creates inert local metadata only. Repeating it preserves the existing profile; it does not enroll a network or create credentials. Use `init --name example-node --json` for an explicit name and stable output.

The connect picker first refreshes services advertised to this node through authenticated peer responses. Select a service by number, refresh with `r`, choose `m` for a known service's manual configuration, or cancel with `q`. An ordinary Tailscale application can use manual configuration without installing sobalink on its host.

For a share, start the target application, run `soba share`, and choose the allowed peers by displayed numbers or exact names. Comma-separated selection permits multiple distinct peers. Ambiguous names are rejected. Discovery is an explicit choice, and minimal metadata is exposed only to those peers.

The wizard shows the peer, purpose, complete port scope, local entry or application port, lifetime, discovery scope and saved name before applying anything. Presets are editable examples. Names may contain Japanese or other Unicode letters, digits, hyphens and underscores. Review actions edit the name, ports, lifetime or purpose, return to target selection, or cancel. A failure offers another reviewed retry or editing; a changed advertised grant requires selecting it again.

On supported terminals, Up/Down selects choices and Left/Right edits text. Numbers, names and comma-separated values also work. Redirected output and `TERM=dumb` use plain prompts. `--interactive` explicitly enables the guided flow for supplied input; `--json` never prompts and cannot be combined with interactive mode. Guided input refuses `TEA_TRACE`, stores no input history, rejects invalid UTF-8/control sequences, and restores terminal modes after exit.

### Choose the output before automating

| Need | Mode | What to expect |
| --- | --- | --- |
| Help choosing a service | `soba connect` | Guided selection and review in a terminal |
| Repeat a known action | Explicit flags | Exact target and scope; review before starting |
| Parse a result | `--json` | Stable machine output; no interactive approval |
| Validate input only | Global `--dry-run` | No start and no proof that a port is available |

Use the same global `--state-dir PATH` throughout a workflow. A preview is not permission for a later changed action.

## Nearby LAN setup

For a nearby-device connection, start with `soba init`, `soba start --offline --background`, then `soba nearby` on both devices. Have the receiving device explicitly open requests and review the requester’s immutable plan before approval. Review [request-and-accept setup](CONNECTION_SETUP.en.md#request-and-accept-v2), including TOFU versus full-fingerprint verification; message/file trust remains separate. The legacy full-fingerprint flow and its relay-role choices are documented separately there.

## Explicit agent commands

```sh
soba --dry-run connect --json --name api --peer PEER_ID --ports 8080 --local-port 18080
soba connect --json --name api --peer PEER_ID --ports 8080 --local-port 18080
soba share --json --name ssh --preset ssh --peers PEER_ID --ttl 2h --discoverable
soba --json-errors status --json
soba peers --json
```

`--peer-name example-node` or `--peer-names example-node,second-node` resolves exact current authenticated names and rejects missing, repeated or ambiguous matches. Use the explicit ID flags `--peer` and `--peers` for offline saved definitions and previews that need no network query.

A dry run validates local input and prints its exact action payload; it does not prove listener availability or remote application success. Names and all JSON keys, identifiers and values remain unchanged by locale. `--locale auto|ja|en` selects human help and errors; `--json-errors` provides stable error codes.

For an advertised connection, take `peerId`, `id`, `revision`, `network` and `ports` from the same refreshed service row and pass the binding explicitly:

```sh
soba discover --json
soba connect --json --name advertised-api --peer PEER_ID --network tcp --ports 8080 --purpose web --local-port 18080 --service-id GRANT_ID --service-revision REVIEWED_REVISION
```

The revision is opaque review metadata. See [when advertised information changes](GENERIC.en.md#when-advertised-information-changes) for the freshness decision. Scripts must compare every reviewed field before accepting a newer revision.

## Saved definitions, endpoints and stopping

```sh
soba --offline rules
soba --offline rules --json
soba --offline settings api
soba --offline service show api
soba status
soba stop
soba stop
```

`rules` lists saved definitions. Service show/copy/restart/delete, settings, stop-service, service selection, task selection and group members accept exact saved names or IDs. Ambiguous names/IDs and selecting the same service twice are rejected. Name resolution remains pinned to the resolved ID and revision; it never changes scope or extends a lifetime. Global `--offline` permits listing and settings inspection while the agent is stopped, without opening the network. Saved state is not a readiness claim. `settings` supplies local/remote mappings, SSH HostKeyAlias and HTTP candidates. Keep SSH host-key and TLS certificate verification enabled; preserve the original TLS name and origin for HTTPS. These service endpoints are not SOCKS proxy addresses.

`status` shows the peer scope, reported endpoint, state, effective expiry, owner and lease with a next step. It reports `state: stopped` in JSON when no local process is reachable, and repeated `stop` succeeds in that state. Permission, protocol and other errors remain errors. Use the same `--state-dir` on follow-up commands when selecting another profile.

## Groups and task ownership

```sh
soba group save demo api
soba --dry-run group start demo --ttl 2h --owner session-example
soba group start demo --ttl 2h --owner session-example --confirm
soba group wait demo --owner session-example
soba group stop demo --owner session-example
soba task --services api --ttl 45m --confirm -- PROGRAM ARG
```

Group lists and share confirmations show service names and scope as readable text. `--json` service/group starts with shares still require explicit `--confirm` and never prompt.

`--ttl` changes this invocation only, using positive whole seconds; saved definitions and revisions stay unchanged. `--owner` scopes start/stop/wait to the same owner without creating a renewable lease. See [task ownership and the two deadlines](SAVED_SERVICES.en.md#temporary-services-owned-by-a-command) for cleanup, lease renewal and the remote-job boundary.

## Review startup effects

`soba autostart --json` shows the exact user-level sign-in registration and its public launch effects. Saved mode may reconnect the selected network, approved autosave receivers, valid explicitly enabled outbound startup selections and saved proxies. It lists their scopes and validity without credentials. Changed scope, stored credential revision or revocation requires a new review token. `--startup offline` suppresses those effects. Inbound shares and ordinary immediate transfers do not resume. Explicitly approved durable outbox requests have their own bounded restart authority; see [durable outbox](DURABLE_OUTBOX.en.md).

See the [startup guide](STARTUP.en.md) for reviewing and managing those separate approvals. Registration preview does not change the OS or start the application.

## Recovery and private inputs

Port conflicts do not silently choose another port. For a stopped saved outbound connection, `soba service ports NAME_OR_ID` explicitly checks alternatives without changing its fixed port. Review the full definition and lifetime, then separately choose `soba service restart NAME_OR_ID --local-port PORT --expected-revision REVISION`. Proposals are not reservations; actual start rechecks every bind. See [alternate-port checks](PORT_PROPOSALS.en.md). You can also edit the reviewed local port or stop the conflicting listener deliberately. Refresh discovery after a grant or network change and select the current service again. Keep the agent and the provider application running for endpoint use.

Credentials and private invitations do not belong in command arguments, shell history or logs. Use the existing private file/pipe input commands and explicit runtime approvals. No example above contains a credential or configures a real device.

## Opt-in durable file outbox

`soba send --queue --peer PEER_ID --expires-in 24h --json PATH` prepares a queue review without sending. The Web UI has the same explicit mode and deadline review. Queued means local acceptance, not delivered; unknown outcomes must be looked up by their original request ID. See [durable outbox](DURABLE_OUTBOX.en.md) for confirmation, restart, cancellation and the additive API contract.

See the [actual CLI save-versus-start output](SAVED_SERVICES.en.md#distinguish-save-from-start-in-actual-cli-output).
