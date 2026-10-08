# Local transfer settings resource: source build

[日本語](RESOURCE_SETTINGS.ja.md) · [User guide](GENERIC.en.md)

This checkout provides local resource inspection, preview, explicitly bound apply and durable operation evidence for two transfer-admission settings. This is source-build functionality, not a new published release or a release commitment. Remote management and Web UI resource controls are not added.

## Review, apply and check

Start the local agent and use the same profile selection in another terminal. Follow the [command workflow](GENERIC.en.md#local-transfer-settings-operations-source-build): list the resource, preview both choices, review the response, apply its exact binding, then check status if needed.

`list`, `inspect`, `preview`, `apply` and `status` return language-independent JSON; `--json` is accepted explicitly. CLI `status` sends `resource.operation.status`. Human help and errors follow `--locale auto|ja|en`. Use the exact opaque resource ID from `list`. Both settings require `default` or a positive finite integer no greater than 9007199254740991. `unlimited`, zero and negative values are rejected. The provider also validates the complete proposed capacity policy. `requested` retains the choices and `effective` shows resolved values. Lower limits govern admission rather than canceling active transfers.

Preview changes no settings, starts no transfers and reserves no operation slot. Global `--dry-run` validates local input and prints the request without contacting the agent; it neither validates current state nor produces an authoritative review. Global `--offline` definition editing does not support these commands. A running agent started with `start --offline` can manage local settings without starting its saved network.

## Review expiry and retry contract

- Apply requires the preview's exact `operationId`, `baseRevision`, `revision` and both reviewed choices. There is no implicit re-preview or substitution of current revisions. Changing a choice requires reviewing a new preview.
- Reviews bind the current policy/profile, exact choices, local actor/action, process authority revision and next operation slot. Authority write attempts, including failed writes or changes later reverted, conservatively invalidate unused reviews. Another admitted operation consumes the slot. Restart requires a new review for a new operation; expiry is state-based, not a wall-clock timeout.
- Multiple previews may name the same unreserved slot. Only the admitted binding can own that ID. A different retained request returns `resource_operation_mismatch`; stale current-state/slot bindings return `resource_revision_conflict`. Inspect and review anew before attempting a new operation.
- Retained evidence is checked before boot/revision checks. Repeating the same operation ID with identical bound content returns its historical evidence without provider replay, including after restart. Check `status` after a lost response; do not treat a missing response as permission to silently create a replacement operation.
- An evicted/consumed operation returns `resource_operation_not_retained` and cannot execute again. `resource_operation_not_found` means no retained execution outcome is known. Neither code proves success or failure. The request ID used by local control is separate from this durable operation ID.

## Outcome and current state

The operation response contains `operationId`, historical `outcome`, `evidenceDurable`, a separate `current` descriptor and `journal` usage. The current descriptor reflects observation at response time; it may differ from the historical request because legacy commands or later operations changed settings. A previous success is not a claim that those settings remain current.

| `outcome.status` | Meaning |
| --- | --- |
| `applied` | Configuration was saved durably and required accounting/transfer stages succeeded |
| `saved_not_applied` | Configuration was saved durably, but accounting or transfer-runtime application failed |
| `failed` | Configuration was not attempted or was not published; runtime stages were not attempted |
| `canceled` | Cancellation was observed after durable intent but before configuration application |
| `unknown` | The result cannot be certified, including uncertain configuration durability or unfinished intent |

`outcome.configuration` is `durable`, `not_attempted`, `not_published`, `uncertain` or `unobserved`. Each of `outcome.accounting` and `outcome.transfer` is `succeeded`, `not_required`, `failed`, `not_attempted` or `unobserved`. These fixed enums do not include raw provider errors. A returned `unknown` is not success or rollback. `evidenceDurable: true` describes a durably recorded result, which may itself record uncertainty; it does not prove application success. Cancellation before intent is admitted can instead return a canceled command error with no operation record.

## Bounded evidence and recovery

The private journal has fixed bounds of 128 records and 256 KiB (262144 bytes) for its serialized envelope. Admission reserves room for the completed record before invoking the provider; the byte bound can limit admission before 128 records. `journal` reports `records`, `bytes`, `maxRecords`, `maxBytes` and `writable`. `writable` reports whether uncertainty has frozen admission, not a guarantee that storage or space is available.

Intent, canonical configuration and result are separate writes, not a multi-file transaction. A durably saved intent precedes the provider call, and a result write follows it. Completed known outcomes may be evicted oldest-first within the bounds; unfinished intents and unknown outcomes are pinned. No automatic replay or forced eviction resolves them. If only pinned records remain and capacity is exhausted, new operations return `resource_journal_full`.

An intent write that did not publish returns `resource_journal_write_failed` without attempting settings changes. Uncertain intent publication or a failed result write returns unknown evidence and freezes new applies. A later owned startup validates and durably republishes available disk evidence before enabling the resource; it never replays settings operations. If startup cannot certify that evidence, the resource remains unavailable. Status/current observations do not automatically repair an unknown outcome.

This provides bounded evidence and no-replay behavior, not an exactly-once guarantee, a multi-file rollback guarantee or proof of real power-loss recovery. Local high-water counters do not prevent whole-profile rollback. Do not delete private state to recover a resource identity or free pinned records.

## Identity, compatibility and scope

- The one resource has type `transfer-admission-settings`, local authority/provider scope and an opaque persistent ID. It is not a device name, path, address or peer identity. Copying the complete private state directory copies the identity; no remote clone-resolution behavior is claimed.
- Only `transferConcurrentFiles` and `transferConcurrentPerPeer` are represented. Their canonical owner remains the existing capacity policy. Unrelated settings and legacy profile/capacity file formats are preserved.
- Owned startup validates and republishes the bounded private resource envelope. Invalid, unsupported or uncertain state disables the resource feature rather than silently replacing its identity; legacy settings remain available. Downgrading to the earlier PR1 read-only implementation makes the resource interface unavailable with a populated operation journal, while legacy settings continue.
- Responses expose no private paths, peer lists, transport identities or full profiles. The fixed local actor is not an individual user's identity. Existing authenticated local control is required: the CLI uses local IPC, and the existing local Web `/api/command` dispatcher can reach the same commands. Its authentication and Fetch Metadata protections are unchanged. The fixed `local-control` actor covers both front doors without individual attribution. No new Web UI controls, peer-management endpoint, management grant or discovery advertisement is added. Pairing grants no management permission.

Mock tests establish bounded logic behavior only. Physical-device acceptance, actual power-loss durability and published distribution acceptance remain separate. See the [development principles](DEVELOPMENT_PRINCIPLES.en.md) and [security boundary](../SECURITY.md).
