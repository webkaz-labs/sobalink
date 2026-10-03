# Explicit outbound startup and private SOCKS profiles

[日本語](STARTUP.ja.md) · [User guide](GENERIC.en.md)

Saved definitions remain inert by default. Optional startup selections authorize
only the exact reviewed outbound connections on future online process launches.
No inbound shares or file transfers start automatically. These controls are
implemented and covered by synthetic tests; installed-device, enrollment and
application acceptance still require real-device verification.

## Saved outbound connections

1. Save the desired outbound services or group, then inspect them:
   `soba startup preview --name example --group example`
2. Review the complete returned service scopes and lifetimes. Enable that exact
   selection with its `revision` and `storeRevision`:
   `soba startup save --name example --group example --review REVISION --store-review STORE_REVISION`
3. Inspect approvals with `soba startup list`. To prevent future automatic starts:
   `soba startup disable --name example --store-review STORE_REVISION`

Use `--ids ID,ID` instead of `--group` to select individual saved services. Saving
an approval does not start anything immediately. Editing or importing changed
definitions, changing a group's members, changing the selected node hostname/network, or
revoking a target makes old approvals invalid. Review again explicitly to enable
the changed scope. An imported profile never supplies a startup approval.

The online process captures enabled approvals at launch and attempts each once
after network readiness. Startup uses the same reviewed multi-service admission,
listener readiness checks and rollback as an explicit group start. A failed
attempt stays failed until an explicit action or a new process launch. An explicit
stop does not immediately restart it. `soba start --offline` suppresses all
startup work, even if a network is subsequently configured in that process.

A finite lifetime begins anew on an explicitly requested process launch. Ordinary
transport recovery preserves the existing absolute expiry; it never renews an
expired or explicitly stopped permission. Starting the application at OS login
is a separate optional setting: `soba autostart --help`.

## Private SOCKS profiles

The ordinary `soba proxy start` command remains ephemeral. Optional saving uses
only private input files or a pipe:

- `soba proxy preview --name example --peer PEER_ID --ports 443,8443`
- `soba proxy saved`
- `soba proxy save --json-file PRIVATE_FILE` or `soba proxy save --stdin`
- `soba proxy generate --json-file PRIVATE_FILE` for explicit strong generation

The save payload contains the reviewed `scope`, `expectedRevision` from preview,
`expectedStoreRevision` from `proxy saved`, `username`, `password`, and an explicit
`startOnLaunch: true` or `false`. Generation accepts the same fields except
username/password, and creates a 144-bit random username and 256-bit random
password. It persists them only through this explicit action and never returns
credentials in the normal response. Saving and generation do not start a proxy.

Manage an entry using its own `revision` from `proxy saved`:

- `soba proxy saved-start NAME --review REVISION`
- `soba proxy saved-disable NAME --review REVISION`
- `soba proxy saved-delete NAME --review REVISION`
- `soba proxy reveal NAME --review REVISION --private-file PATH`

Reveal writes credentials directly to the chosen private file. Generic command
output cannot bypass this destination requirement. Keep revealed files private. Delete also stops the session associated with that entry;
disable prevents future automatic starts and recovery from that stored approval.
An already running listener can be stopped with `soba proxy stop ID`.

Saved scopes retain exact peer identities, TCP ports, loopback endpoint, network,
node hostname and lifetime. All current-identity/netstack/loopback restrictions still
apply. Revocation invalidates old approvals across restarts; re-trusting does not
restore them. If durable revocation cannot be confirmed, relevant active work is
stopped and startup is suppressed in the current process. The error requires
repairing private storage before restarting; no successful durable revocation is
claimed when storage fails.

Startup approvals, revocation metadata and SOCKS secrets are stored separately
from portable profile exports, using `profileBytes` as a finite per-file storage budget and
atomic private writes (current-user ACL on Windows). Normal status, history,
logs, errors and dry-run output omit credentials. Do not put credentials in argv,
shared configuration, source code or exported profiles.

## Verification boundary

Synthetic Core/CLI tests cover frozen selections, stale reviews, changed groups,
inbound rejection, offline suppression, exactly-once launch admission, disabling,
explicit stops, durable peer revoke, failure handling, credential generation,
private reveal, redaction, and saved-proxy recovery without expiry renewal. They
do not establish real enrollment, OS-login/suspend behavior, installed ACLs or
application compatibility. Native Windows CI remains necessary for ACL checks.
