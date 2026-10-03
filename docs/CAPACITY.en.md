# Capacity, lifetimes and history

[日本語](CAPACITY.ja.md) · [User guide](GENERIC.en.md) · [Security](../SECURITY.md) · [API contract](../web/API.md)

Limits describe different things. Removing a logical limit does not allocate unlimited memory, disk, sockets or time in the underlying operating system. Review the requested choice, effective value and current usage together.

| Kind | Choices | Meaning |
| --- | --- | --- |
| Logical policy | `default`, positive `limited`, explicit `unlimited` | A selected count, size, retention recommendation or operation deadline |
| Resource budget | `default` or positive finite `limited` | Storage, metadata, listeners, active flows, queues and response pages remain bounded; `unlimited` is rejected |
| Service lifetime | Finite duration, outbound `until-stopped`, inbound `until-revoked` | Authorization lifetime is explicit and independent of capacity |
| Protocol and safety invariant | Not a capacity preference | Valid port/identifier/path syntax, identity and scope, reserved endpoints, private management and non-overwrite rules always apply |

`default` resolves to the build's visible catalog value. It is not zero or unlimited. Finite values use positive JSON-safe integers; durations use whole seconds within the time representation. Omitted keys return to their defaults when a replacement policy is applied. The policy version is `1`; unknown keys, duplicate fields, nulls and ambiguous choices are rejected. Check `policy.config` for the exact build's `requested`, `effective`, `catalog`, `adjustable` and `usage` values.

## Initial values are not product maxima

| Logical choice | Initial default |
| --- | --- |
| `savedServices`, `trustedPeers`, `sharePeers` | 64 saved definitions, 128 trusted/paired peers, 32 peers per share |
| `rangePolicies`, `portIntervals` | 64 range policies, 256 configured intervals |
| `groups`, `groupMembers` | 32 groups, 64 members per group |
| `batchEntries`, `fileBytes`, `batchBytes` | 256 entries including directories, 1 GiB per file and per batch |
| `pathDepth`, `pathBytes` | 16 levels and 4096 UTF-8 bytes per portable relative path |
| `messageBytes` | 16 KiB of decoded text |
| `transferHistoryEntries` | 32 retained transfer records per direction |
| `messageHistoryEntries`, `messageHistoryBytes`, `messageHistoryAgeSeconds` | Cleanup recommendations: 128 messages, 48 KiB encoded history, 30 days |
| `stagingSeconds`, `receiveWaitSeconds`, `fileTransferSeconds` | 600 seconds for each operation |

All listed logical choices support a custom positive value or explicit `unlimited`. A lower choice governs new admission without evicting saved records or canceling already admitted work. Retention recommendations only select candidates for reviewed cleanup; applying them never silently prunes history. A task lease and a service lifetime remain separate from operation deadlines.

Resource budgets are independently adjustable and always finite. Representative defaults are:

| Resource choice | Initial default |
| --- | --- |
| `profileBytes`, `lanStateBytes`, `messageStorageBytes` | 4 MiB profile, 2 MiB private LAN state, 4 MiB message storage |
| `messageTextBytes` | 16 KiB decoded message storage/framing budget |
| `materializedListeners` | 64 shared across UDP service sockets, local forwards and optional proxy listeners |
| `tcpConnections`, `tcpPerPolicy`, `tcpPerPeer` | 512 total, 128 per policy, 64 per peer |
| `udpSessions`, `udpPerPolicy` | 512 total, 256 per policy |
| `udpQueuedBytes`, `udpPolicyQueuedBytes`, `udpQueuePackets` | 16 MiB total, 1 MiB per policy, 64 packets per queue |
| `diskReserveBytes` | 512 MiB of observed free space retained as a transfer safety margin |
| `transferSpoolBytes`, `receiveReservedBytes` | 4 GiB sender staging and 4 GiB receiver reservations |
| `transferManifestBytes`, `transferMetadataBytes` | 256 KiB manifest accounting, 1 MiB retained transfer metadata |
| `transferPending`, `transferPendingPerPeer` | 32 pending transfers, 8 per peer |
| `transferConcurrentFiles`, `transferConcurrentPerPeer` | 4 active file streams, 2 per peer |
| `stagingInventoryEntries`, `stagingInventoryDepth` | 100,000 inspected staging entries, 64 inventory levels |
| `discoveryBytes`, `pageBytes`, `pageEntries` | 256 KiB discovery response, 1 MiB / 128 entries per local page |

Actual available disk, memory, descriptors and platform limits can reject work below a chosen budget. These are accounted resource bounds, not a promise to cap the process's total heap or RSS. Path depth and total UTF-8 length are logical choices, with defaults of 16 levels and 4096 bytes. Their unlimited modes still fit within the finite manifest budget: effective path bytes cannot exceed `transferManifestBytes`, and effective depth also cannot exceed the number of components that fit those bytes. The whole manifest and retained metadata need their own space. Portable components remain limited to 255 bytes and reject unsafe/reserved names, rooting, traversal, NUL, backslashes and links. These safety rules do not change with capacity. Native OS/filesystem limits may still reject a path accepted by policy; larger-policy tests do not prove every platform accepts long paths. The local absolute receive-destination field currently has a separate 4096-byte validation limit; `pathBytes` applies to offered relative paths, not that destination. The staging inventory depth is a separate finite traversal budget.

`profileBytes` also bounds each private startup-approval, saved-proxy and revocation file separately. Usage reports their individual byte counts without serializing secrets. A policy change cannot shrink this limit below any retained file; it is not one aggregate directory-size limit.

Examples of interacting limits:

- A 2 GiB file needs suitable `fileBytes` and `batchBytes` choices plus sufficient sender spool and receiver reserved-byte budgets on the respective devices
- Unlimited `batchEntries` still needs finite manifest and retained metadata capacity; directory entries count too. Source enumeration accounts for live ancestor/path metadata against `transferManifestBytes` as well
- Unlimited `messageBytes` still uses `messageTextBytes`. Increasing text allowance may also require more message storage; JSON escaping is included in transport framing
- Unlimited peers or services still require enough serialized profile and LAN-state storage. Lowering a count retains existing pairs and records; lowering storage below existing saved data is refused
- A compact TCP share can cover ports 1–65535 except exclusions and reserved endpoints without allocating a listener per port. UDP and local forwards materialize listeners, so their finite listener budget still applies
- Discovery checks peers in rotating bounded passes. A large peer set is not fully probed in one refresh, and a saved peer is not proof of online availability

## Transfer free-space margin

`diskReserveBytes` is a positive, finite, adjustable free-space reserve, not a file-count limit or a lifetime byte counter. Raising it leaves more room and can stop new writes sooner; lowering it leaves less room. It applies immediately to subsequent receiving and outgoing staging checks, including active transfers. Review the value in Capacity and history or through the policy preview/apply flow below.

The receiver checks before preparing a destination, each new directory, each temporary file, and the final no-replace hard link; browser and CLI sending check before staging and each temporary file. Each payload write rechecks the actual destination filesystem in chunks of at most 32 KiB. Linux and macOS use available blocks from the open file descriptor; Windows uses available-to-caller bytes on the open handle's volume GUID. An unavailable/unsupported space probe fails closed. In particular, Windows destinations without volume-GUID resolution, such as some network filesystems, cannot be used for guarded transfer writes.

Each admitted payload write charges a 64 KiB allowance against other in-flight writes. A process-wide 1 MiB allowance budget bounds overlapping checks/writes across receiving and sending, even across different volumes. A short admission lock is released before the disk write; waiting for an allowance respects cancellation. Existing finite manifest, staging, reservation, and concurrency settings continue to apply.

Low space or an inspection failure stops the affected operation with a retryable error. Free space or check the volume/access permissions; review the reserve if appropriate, then retry. A failed outgoing staging attempt must be selected again. A failed receiving item restarts from byte zero; saved files retain their acknowledgements and are never overwritten. An automatic-accept failure leaves a pending batch that may need explicit acceptance after space is restored. The app removes only its active failed temporary copies under the existing cleanup rules and never silently deletes older files to make room.

This is a practical guard based on observed free space, not an exact disk quota. Other processes, filesystem metadata/allocation granularity, quotas, and delayed allocation can still cause a write to fail or consume the margin between checks. The allowance is not a filesystem allocation guarantee. Separate app processes do not share the allowance. Receiver temporary quota accounting is restored separately through the private index and bounded startup inventory described in [receiver recovery](#receiver-recovery); the free-space reserve does not replace that accounting. No automatic orphan cleanup or restart-resume guarantee is provided. [Background log bound and diagnostic limits](LIFECYCLE.en.md)

## Review and apply

The local UI exposes capacity choices and review. Advanced CLI use goes through the same Core commands:

```sh
soba command policy.config '{}'
soba command policy.preview --json-file capacity-change.json
soba command policy.apply --json-file capacity-apply.json
```

Start from the current requested policy, preserve the choices you still want, then put that complete policy under `policy` in the change file. For example, this replacement resets omitted choices to default and raises only the shown values:

```json
{"policy":{"version":1,"logical":{"sharePeers":{"mode":"limited","value":64}},"resources":{"profileBytes":{"mode":"limited","value":8388608}}}}
```

Review the returned effective values and usage. The apply file contains that same `policy` and an `expectedRevision` copied from the preview's `revision`. A changed policy, saved profile or proposal requires a fresh preview. Preview changes nothing; successful apply persists the selection before reporting it. New admission uses the chosen budgets; occupied resources remain accounted. No additional permissions, listeners or transfers start merely because capacity grows.

## Lifetimes and cancellation

Shares default to a finite hour. `--ttl 72h` is a supported finite example; there is no fixed 24-hour ceiling. `--lifetime until-revoked` explicitly creates a share without an expiry. Outbound connections default to `until-stopped`, with finite `--ttl` available. Saved definitions preserve the chosen mode but alone do not authorize restart. A separately reviewed [outbound startup approval](STARTUP.en.md) can start a fresh finite lifetime on a future online process launch; inbound shares remain manual. Reconnect does not renew a grant.

Local staging defaults to 600 seconds in both browser and CLI paths. Its explicit unlimited choice removes that operation's policy deadline while cancellation, process shutdown, storage limits and network failures remain effective. Receive waiting and file transfer have separate choices. Unlimited staging is not a durable offline outbox or a guarantee that a disconnected browser/request will resume.

## Receiver recovery

Recovery accounts only staging whose ownership can be confirmed. Before creating a recovery record, recovery removes only the earlier temporary state-save file whose ownership is confirmed and prevents competing saves until this operation finishes. Other saved settings retain their existing size limits. A failed or uncertain recovery keeps receiving blocked during that attempt; an error alone does not establish whether the recovery record remains, so inspect recovery status before assuming completion. Sending and local management remain available. Uncertain saves or changed bindings keep receiving blocked across restart. Recovery does not claim or delete ambiguous roots, scan arbitrary destinations, or remove saved output. Zero-payload internal staging is safely retired or remains accounted, without per-transfer metadata accumulation. A missing legacy index still requires the explicit review described below; recovery does not discover unrecorded leftovers. See [bounded private-state persistence](ARCHITECTURE.md#bounded-private-state-persistence) for persistence and recovery details, and [security boundaries](../SECURITY.md#trust-and-receiving) for the cross-filesystem limitation.

At startup, an unavailable per-peer autosave destination blocks receiving while local management and sending remain available. Its autosave grant is disabled; restoring the folder or confirming recovery does not enable it again. Review and explicitly enable autosave if wanted. If the disable cannot be saved, receiving stays blocked until confirmation can persist it; repair storage before restarting. A default folder alone grants no autosave, and manual destinations are checked through their recorded unfinished staging.

An existing profile without an index is blocked from receiving until local review. Run `soba receive recovery confirm` to preview. Review all previous default, per-peer and manually selected receive folders, unfinished staging and previously saved output; resolve unfinished old receives so no untracked partial data remains, keeping saved files; then run `soba receive recovery confirm --reviewed` to persist the review and initialize the missing index. This explicit legacy path cannot discover arbitrary old leftovers that were never recorded. Check old locations yourself. Unknown or unavailable old locations must be reviewed before confirmation. --reviewed attests that review and cleanup are complete; it does not delete files. Do not confirm while untracked partial data remains. If you remove confirmed leftovers manually, delete only exact known unfinished staging files; do not broadly remove ordinary files or saved output. A missing legacy index does not erase or recalculate previous files, and confirmation adds no partial-byte or restart resume. See [security boundaries](../SECURITY.md#trust-and-receiving).

## Reviewed history cleanup

Messages are not silently removed when a new one arrives or a retention choice changes. Use the UI cleanup preview or the advanced commands:

```sh
soba command message.history.preview '{}'
soba command message.history.cleanup '{"expectedRevision":"REVISION"}'
```

Replace `REVISION` only after reviewing the returned exact candidates and counts. The revision binds current messages, policy and candidates; a change requires another preview. If cleanup fails before replacing the history file, memory stays unchanged. If replacement occurs but durability cannot be confirmed, memory follows the replaced file and the error reports that uncertainty; do not assume cleanup was rolled back. If storage fills first, sending/receiving can fail with a storage error rather than erasing earlier messages. A peer acknowledgement followed by a local history-save error does not mean the peer failed to receive the message; inspect the reported stage before retrying. If an incoming message's history was replaced with uncertain durability, retry only with the same message ID: the receiver reconciles the uncertain history before a durable acknowledgement, and ordinary duplicates do not rewrite history. Do not resend that message with a new ID.

Transfer history is separate. `soba forget TRANSFER_ID` explicitly removes a terminal transfer record; it does not delete received files. Released staging no longer consumes payload reservation, but failed cleanup remains accounted until successfully removed. Process restart does not resume transfer progress. [Transfer behavior](GENERIC.en.md#retry-and-cleanup)
