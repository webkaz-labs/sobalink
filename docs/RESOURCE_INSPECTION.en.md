# Inspect one peer's transfer settings

[日本語](RESOURCE_INSPECTION.ja.md)

This source version permits one managed DirectLAN peer to read exactly two transfer settings and their effective values. It does not expose a catalog, operation history, arbitrary commands, or remote changes. Normal agent startup enables the implementation, but creates no permission or inspection listener without an eligible confirmed grant. This guide describes source development after the published alpha.5 baseline.

## Review and grant on the target

1. Start the owning local agent. Both devices must have the same current managed DirectLAN relationship.
2. Use `soba resource list` to obtain the target's resource ID.
3. Run `soba resource grant preview --id RESOURCE_ID --peer PEER_KEY --expires-at RFC3339_EXPIRY` with a finite future expiry, including its timezone.
4. Review the target and peer keys, relationship, both transfer fields, inspect-only action, original expiry, and `initializesState`. Store the entire unchanged JSON in a private file. The CLI rejects nonregular, oversized, or ambiguous JSON; it does not enforce the file's access permissions.
5. Run `soba resource grant confirm --review-file REVIEW.json --confirm`.

Preview does not change saved state. It may close unsafe in-memory permission when it observes expiry or clock uncertainty. A stale review is rejected rather than silently replaced. A second active grant requires explicit revocation first.

`initializesState: true` explicitly includes creation of the private grant-state directory. Missing whole state cannot establish that older grants never existed. Do not delete state as a recovery method.

## Read from the permitted peer

Use the exact resource ID, grant ID, and revision from the target's `soba resource grant inspect --id RESOURCE_ID` output:

`soba resource remote inspect --peer TARGET_PEER_KEY --id RESOURCE_ID --grant-id GRANT_ID --grant-revision REVISION`

Output contains only `transferConcurrentFiles` and `transferConcurrentPerPeer`, their requested choices, and effective values. JSON names and values are language-independent. There is no automatic protocol/backend fallback. An unavailable result does not disclose whether a requested resource or grant exists.

Each exchange has one absolute 15-second deadline. Its single application dial uses a fixed 10-second budget, including authenticated managed-session recovery and at most one rate-limited wakeup. It does not retry the inspection request or extend the grant expiry.

The fixed, selector-free hello checks protocol compatibility on an authenticated managed connection. It does not attest to a particular service process. Resource and grant selectors are sent only after a supported hello.

## Lifetime and readiness

- Saved permission survives restart only until its original absolute expiry, with the same target identity, resource, peer, and immutable pair context. Restart and reconnect never renew it.
- Owned private-state recertification and current relationship validation precede runtime activation. Each boot retains a monotonic cutoff; retries cannot extend it.
- `activation` and `listenerReady` distinguish saved permission from an owned listener. A ready listener does not prove successful remote access.
- The userspace inspection port is 54546. It remains available to ordinary services when inspection is inactive. An existing owner is preserved; a collision reports saved-but-inactive permission instead of replacing that service.
- Revocation, expiry, uncertain state, or detected clock rollback denies new disclosure. One bounded response admitted before revocation may finish afterward; bytes already written cannot be retracted.

Persisted lifetime relies on a trustworthy target wall clock across downtime. Durable observations detect some rollback, but cannot prove elapsed downtime or detect rollback of the entire private state. A crash before a terminal observation is durably recorded can lose that observation. These limits are not a trusted-time guarantee.

## Permission scopes

Inspection is independently confirmed. General message/file Trust does not create or restore it. The broad peer permission removal (`soba revoke PEER_ID` or the Web Revoke permission action) also revokes the matching inspection grant, even when ordinary Trust is already absent. Message/file pause and stopping service shares do not revoke this separate permission. Removing the managed pair denies inspection even if its retained grant record still says active. Re-pairing does not transfer the old grant to a new pair context.

## Revoke and recover visibility

Run `soba resource grant revoke --id RESOURCE_ID --grant-id GRANT_ID --grant-revision REVISION --confirm` using the current local inspection result. Revocation remains available after peer loss or expiry, and after detected time uncertainty when stored ownership remains valid.

A failed persistence operation is not durable success. In particular, a failed revoke may not survive a crash. Invalid or substituted private state freezes inspection rather than resetting authority or silently repairing it.

Isolated synthetic two-Core tests cover first inspection, both transport-role restart directions through original expiry, durable revoke, and preserving an existing port owner. These are same-process Core.Close/Open tests; ordinary maintenance may already have established sessions. They do not establish physical-device, firewall traversal, suspend/resume, OS-process restart, or OS-login acceptance.
