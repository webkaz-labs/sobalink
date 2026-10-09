# Review and manage one peer's transfer settings

[日本語](RESOURCE_MANAGEMENT.ja.md) · [Read-only inspection](RESOURCE_INSPECTION.en.md)

This source-development slice adds explicitly permitted management of exactly `transferConcurrentFiles` and `transferConcurrentPerPeer` on one paired DirectLAN peer. It is under integration review. The pure fence/epoch tests do not establish end-to-end management, distribution, physical-device, application, OS-login or suspend acceptance. Published-release claims remain governed by the [verification record](VERIFICATION.en.md).

## Authorize the exact scope on the target

1. Start the owning agent and use `soba resource list` to obtain the target resource ID.
2. If an active inspection or management grant exists, explicitly revoke it first using its current ID/revision. Changing a grant scope never happens implicitly.
3. Run `soba resource grant preview --management --id RESOURCE_ID --peer PEER_KEY --expires-at RFC3339_EXPIRY`.
4. Review the exact target and peer keys, immutable paired relationship, both transfer fields, complete `inspect`, `preview`, `apply`, `operation.status` scope, finite expiry, `initializesState` and `upgradesFormat`. The complete scope permits both finite positive choices and selecting defaults. Save the unchanged review JSON in a private file.
5. Run `soba resource grant confirm --management --review-file REVIEW.json --confirm`.
6. Check `soba resource grant inspect --id RESOURCE_ID`. Saved permission, `activation`, and `listenerReady` are different observations; a listener does not establish successful remote use.

Management confirmation upgrades the grant store to version 3 when necessary, preserving retained records and original expiry. Older version-2-only agents cannot read it afterward. Preview and restart do not perform this upgrade. Revoke does not downgrade it; never delete private state to downgrade or recover authority.

Management requires protocol v2 on both ends. Its dedicated listener explicitly rejects v1 before reading selectors. After an explicit revoke/regrant upgrade, an old inspection client cannot use this listener. Use `resource remote manage`; no fallback occurs. Existing inspect-only grants and `resource remote inspect` retain their v1 behavior.

## Review before applying from the permitted peer

Use the exact target resource ID, management grant ID and current grant revision supplied by the target:

```text
soba resource remote manage inspect --peer TARGET_PEER_KEY --id RESOURCE_ID --grant-id GRANT_ID --grant-revision REVISION
soba resource remote manage preview --peer TARGET_PEER_KEY --id RESOURCE_ID --grant-id GRANT_ID --grant-revision REVISION --concurrent-files default --concurrent-per-peer 2
```

Inspect returns only requested settings and effective values. Preview is read-only and reserves no slot. Review both choices and effective values. It returns opaque `operationId`, `baseRevision` and `reviewRevision`; these are bound to this scope, current state and authenticated generation, not a particular TCP connection.

```text
soba resource remote manage apply --peer TARGET_PEER_KEY --id RESOURCE_ID --grant-id GRANT_ID --grant-revision REVISION --operation-id OPERATION_ID --base-revision BASE_REVISION --revision REVIEW_REVISION --concurrent-files default --concurrent-per-peer 2 --confirm
```

Supply the exact preview values and choices. `--revision` means `reviewRevision`. Apply never silently creates a new preview or substitutes current revisions. Another operation, restart, or relevant authenticated-generation change can invalidate an unused review. A changed choice requires a new review. `--dry-run` performs local input validation only; it does not establish remote authorization or readiness. Commands retain language-independent JSON with English/Japanese help and errors.

The first accepted fresh remote apply converts the operation journal from v1 to v2, preserving all retained local records and high-water. This is separate from grant-store migration. The conversion is durably published before the new intent; conversion alone is not evidence that settings changed. Older v1-only agents cannot read that journal. Ordinary local operations, inspect, preview and status never trigger this conversion.

## Check an uncertain reply

```text
soba resource remote manage status --peer TARGET_PEER_KEY --id RESOURCE_ID --grant-id GRANT_ID --grant-revision REVISION --operation-id OPERATION_ID
```

Use the same opaque operation ID after a lost or uncertain apply reply. Do not infer failure, silently create a replacement operation, or blindly retry. An identical retained request returns historical evidence without executing the provider again. A different payload with that ID is rejected. Retained original authorizing identity is never rewritten by a current status selector.

An operation response contains only the historical outcome and `evidenceDurable`. This flag describes durable terminal evidence, not whether configuration application or a file transfer succeeded. `unknown` is neither success nor rollback. Status does not add current settings; inspect them separately. Missing, evicted, local-owned and other-scope records all produce the same unavailable status. A replacement grant or changed pair cannot inherit older scope history.

The private journal shares one sequence and fixed bounds across local and remote evidence. Unresolved intent/unknown records are pinned. Storage uncertainty freezes new applies; do not delete state or treat an unavailable response as permission to recreate an operation.

## Lifetime, cancellation and boundaries

- General message/file trust never creates management permission. Broad peer revoke also revokes this permission; pausing messages/files or stopping a service share does not.
- Original finite expiry and the boot monotonic cutoff survive reconnects. Re-pairing cannot reuse the grant. Detected clock rollback or uncertain/replaced private state denies new work and disclosure. Across downtime, expiry still depends on the target clock; complete state rollback cannot reliably be detected.
- Durable intent precedes one-shot provider admission. Revocation or cancellation before admission denies execution. Already admitted work may finish and synchronously record its outcome after revocation/disconnect. A separately checked response may then be withheld.
- Each exchange sends at most one application request within a fixed 15-second budget, including bounded managed-session recovery. No automatic apply retransmission or OS-network fallback occurs.
- The owned userspace listener shares port 54546 with inspection, preserves an existing port owner, and does not expose the local management API. No arbitrary commands, paths, credentials, actor claims, catalogs, global journal counts, or local operation IDs are accepted/disclosed.
- A failed revoke is not durable success and may not survive a crash. Check private state before restarting when revocation persistence is uncertain.
