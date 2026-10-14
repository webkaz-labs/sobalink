# Durable file outbox

[日本語](DURABLE_OUTBOX.ja.md) · [CLI](CLI_GUIDE.en.md) · [Web controls](WEB_CONTROLS.en.md)

The published 0.4.0-alpha.4 baseline includes an opt-in file/folder outbox. A successful queue operation means **accepted on this sending device**, not delivered. Only durable receiver evidence confirms a save. Source tests and synthetic checks do not establish release-package, physical-device, power-loss, enrollment or OS-login acceptance.

## Fresh profile format

Published 0.4.0-alpha.4 uses profile format 2. The current unreleased checkout uses format 3, which also requires the message store for the [durable text queue](DURABLE_TEXT.en.md). Both initialize their required stores under the agent's lifecycle lock. Format 3 rejects alpha.4 and older profiles; preserve them and select a separate empty directory. There is no migration or feature-enable wizard. Earlier profiles are rejected without being rewritten or erased: preserve them and choose a fresh profile directory. Normal receiving approvals remain in force; each queued send still needs explicit review and consent.

An interrupted first initialization may remain blocked if empty-state ownership cannot be certified. A ready profile never recreates missing delivery ledgers. Process-restart recovery excludes restored/rolled-back private snapshots, manual state replacement/deletion, and concurrent private-namespace modification by another actor with the same filesystem access. Detected substitution or unknown inventory fails closed; filesystem checks are not inode-conditional deletion or protection against an adversarial simultaneous rename. Do not delete private evidence to clear a recovery warning.

## Shortest path

1. In a device conversation, keep **Send now** for an ordinary immediate send, or explicitly choose **Queue for later**. Trusted offline recipients may be eligible for the file queue while immediate text is unavailable. The unreleased development build separately offers explicit [text queueing](DURABLE_TEXT.en.md).
2. Use the existing file/folder picker and select each exact recipient. Choose a deadline, then **Prepare queue review**. Review the returned files, identities, local staged-copy use and absolute deadline with timezone. Preparation alone authorizes no delivery.
3. Choose **Queue until {time}**. The exact immutable content and recipients may be attempted automatically, including after an application restart, only until that deadline. Both devices must run Sobalink and become reachable. Browser closure after confirmed enqueue is safe while the sender agent remains running.
4. Open **Outbox** from the persistent navigation or conversation to check each recipient. It fetches authoritative server records after reopening or reconnecting.

The default deadline is 24 hours and the maximum is 7 days. The Web picker also offers 1 hour; CLI accepts durations from 1 minute through 168 hours. The absolute reviewed deadline is stored and never extended by a restart. A changed payload, recipient or deadline needs a new explicit review. Text-message queueing is a separate, unreleased [feature](DURABLE_TEXT.en.md). Scheduled start times remain outside both queue modes.

## CLI

```sh
soba send --queue --peer PEER_A --peer PEER_B --expires-in 24h --json ./sample.txt
# Use the exact fields returned by preparation:
soba outbox commit OUTBOX_ID --process PROCESS_ID --revision REVIEW_REVISION --confirm --json
soba outbox list --json
soba outbox show OUTBOX_ID --json
soba outbox show --request REQUEST_ID --json
soba outbox cancel OUTBOX_ID --json
soba outbox discard OUTBOX_ID --process PROCESS_ID --json
```

`--json` without `--confirm` prepares only and never prompts. Human queue mode reviews the staged content before its confirmation. `--confirm` explicitly approves the exact selected recipients, staged contents and displayed absolute deadline. For a selected profile, retain the same `--state-dir` in follow-up commands; printed continuations preserve it.

`--dry-run` validates local command inputs without IPC, staging, capability checks or sending. Exit 0 means a verified command result, including local enqueue, blocked/expired status, or cancellation; it never means delivery by itself. Validation, control failure and an unconfirmed mutation fail nonzero. JSON uncertainty includes a stable error code and the original locator. No first-slice long-running wait command or manual replay-as-new shortcut is provided.

## Read state and evidence separately

- **Queued / Waiting for device:** local durable admission, without confirmed remote save.
- **Waiting to check compatibility:** no payload is transmitted until an authenticated peer confirms durable-receipt support. A known unsupported peer needs an upgrade. There is no automatic legacy fallback.
- **Checking delivery / Delivery needs review:** a file may already have arrived. Query the original request; do not prepare a replacement blindly.
- **Permission needs review:** inspect the exact recipient identity and current scope. Re-pairing, a same-named replacement, or unrelated new permission does not revive the old authority.
- **Paused:** the original scope can resume only after the existing pause is lifted and before the original deadline.
- **Further sends stopped / Sending deadline passed:** these work states do not erase already-saved or unknown results.
- **Saved on receiving device:** durable evidence records the save. This is not a read receipt or a promise the recipient has kept the file forever.

Each record has separate `status` and `deliveryOutcome` (`not_attempted`, `unknown`, `partial`, `confirmed`), exact child delivery IDs and saved file receipts. Mixed recipient outcomes remain mixed. Eligible automatic retries retain the original identities; they do not create replacement deliveries. Missing/corrupt staging or lost receiver evidence blocks recovery rather than authorizing blind replay.

## Unknown results, restart and stopping

If a response is lost, check `outbox show --request REQUEST_ID` or **Check original request**. The browser stores only unresolved request locators as recovery hints; authoritative contents and outcomes come from the server. A browser reload cannot turn an unknown result into a new send. Prepared reviews remain process-bound and never become send authority after an agent restart; admitted outbox status and cancellation do not need the previous process ID.

**Stop further sends** persistently fences additional sending. It may first show **Stopping further sends** while active work stops. A save that already won the receiver's publication boundary remains visible. Cancellation does not recall remote files, delete original inputs, or erase receipt evidence. Transmission expiry similarly stops further sending; bounded receipt-only reconciliation may continue. Neither expiry nor cancellation proves non-delivery.

Durable request IDs have the canonical form `ob1_<13-digit Unix milliseconds>_<32 lowercase cryptorandom hex digits>`. The clients allocate one only when beginning a new explicit preparation. The server admits a new unknown key only within five minutes of its creation time, and the reviewed transmission deadline cannot exceed key creation plus seven days. After key creation plus eight days and five minutes, an unknown key is permanently `outbox_retired`. Safe terminal history can therefore be pruned without a lifetime admission counter or an unbounded tombstone list. Retained unknown/partial outcomes remain queryable under finite active storage capacity. Keep the original key on a lost response or retirement error; neither generates or authorizes a replacement.

There is no outbox “forget” bypass for correctness-critical receipt retention. Owned staged data is eligible for cleanup only after durable non-runnable disposition and release by every owner/reader. Capacity failures do not silently evict active work. Inspect the original record and resolve blocked storage or permissions rather than repeatedly restarting or replacing the request.

## Local API

The authenticated, CSRF-protected API adds `POST /api/outbox/prepare`. Multipart fields are ordered as `peerIds` (JSON array), `processId`, `expiresAt` (RFC3339), `requestId`, `manifest` (existing selection-entry JSON), then `files`. The upload response reports staging/preparation, never delivery.

Command family:

- `outbox.prepare`: `{processId, peerIds, paths, expiresAt}` under the caller's original command `requestId`
- `outbox.commit`: `{processId, outboxId, reviewRevision, confirm:true}`
- `outbox.status`: `{outboxId}` or `{requestId}`; durable lookup is process-independent
- `outbox.list`: `{}`; returns version/kind, status/reasonCode and `items`
- `outbox.cancel`: `{outboxId, deliveryId?}`; omitted child selector fences all reviewed recipients
- `outbox.discard`: `{processId, outboxId}`; uncommitted preparation only

Records use `version:1`, `kind:"outbox"`, stable `outboxId`/`requestId`, `reviewRevision`, `admission`, `status`, `deliveryOutcome`, `reasonCode`, `peerIds`, immutable `entries`, numeric `totalBytes`, RFC3339 `createdAt`/`expiresAt`/`updatedAt`, and `children`. `outboxId` equals the canonical original `requestId`; subsequent command-envelope IDs and ordinary immediate-send IDs retain their existing format. Each child contains `peerId`, `deliveryId`, state/evidence/reason and exact `files` receipts (`batchId`, `fileId`, `size`, `sha256`). Only prepared/discarded records carry the preparation's `processId`. Machine names and values are language-independent.

The immediate dispatch v1 schema, process guards and online send behavior are unchanged. This separate outbox API does not reinterpret existing transfers as durable requests. See [development principles](DEVELOPMENT_PRINCIPLES.en.md) and the existing verification record for the distinction between implemented behavior, synthetic evidence and pending acceptance.

## Bounded acceptance case

`TestOutboxThreeProcessBrowser` reuses the native three-process Direct-LAN and authenticated browser fixtures. It selects two genuinely stopped recipients, reviews and queues real uploaded bytes, drops only a successful admission response, recovers its original request, reloads the browser, then restarts both receiving processes and accepts their original offers. It checks durable saved evidence and one exact saved output per receiver. The same case includes English at 375px, Japanese at 320px, horizontal-overflow and Escape/focus checks.

The case requires the final integrated Web assets and Core, `SOBALINK_RUN_DIRECT_INTEGRATION=1`, `SOBALINK_RUN_TRANSFER_BROWSER_NATIVE=1`, and the existing prepared browser executable/runtime. It is a separate opt-in gate, not satisfied by component mocks. No browser sandbox flags, retry count or existing timeout are changed for it. Physical-device, suspend/login and power-loss durability acceptance remain separate.

`TestOutboxProcessRestart` is the separate non-browser companion: it commits a file and a directory-only request, gracefully stops and reopens the actual sender before delivery, accepts their original offers while the sender is stopped, then reopens the receiver after saved evidence. It checks that acceptance alone publishes no output before authenticated start, including a directory-only request. It repeats the original status and commit operations and checks stable identities, exact receipts, staged original bytes and one output per request. This covers graceful sender/receiver process restart, not abrupt death or loss of a saved acknowledgement. The reused fixture’s third process stays idle; the browser case separately covers two recipients. Both cases require execution against the final integrated source. Saved-output inventory permits only the receiver’s exact empty reserved staging directory while normal cleanup is pending; arbitrary hidden files, extra outputs, symlinks and nonempty stages fail. These process cases do not establish final stage reclamation.

`TestDurableAbruptDeathPublicationBoundaries` exercises four real subprocess exits using the existing private test seam: before final link, after link, after final-directory synchronization, and after durable saved evidence but before the file call returns. The parent requires the dedicated exit code and exact cut marker, reopens the original lease and ledger, and verifies the same native file identity, original bytes, one output, and stable saved evidence across another reopen. These are process-death checks on each executing platform, not power-loss proof or an end-to-end lost-network-acknowledgement case.

`TestOutboxCLIProtectedIPC` sends real CLI requests over the protected local control socket or pipe into Core. It checks immutable staging, reviewed admission, repeated command identities, status/list and cancellation with synthetic offline peers and no peer traffic. The separately named `TestOutboxCLIInProcessCoreContract` does not cross the local IPC boundary and never substitutes for the protected test. Neither establishes remote delivery or unauthorized-caller rejection.

## Busy storage and retained evidence

`outbox_storage_busy` is an unconfirmed result: check the original request first. If it is still prepared and the original confirmation is valid, retry the same commit with its original process/revision and deadline. Do not create a replacement or infer that nothing was sent.

For receiving, `transfer_accept_busy` means this attempt saved no new acceptance; earlier consent may still exist and destination checks may already have run. Wait briefly, then explicitly accept the same transfer into the same resolved directory and profile. The Web keeps that directory for the retry; CLI users must keep the same explicit directory. A new explicit invocation uses a new command ID because the original busy response stays cached. `transfer_accept_unconfirmed`, a lost response or an unknown error instead requires checking the original transfer and receiver storage state before another action; do not infer non-acceptance or blindly retry. Existing disk-space errors retain their specific guidance.

`outbox_evidence_capacity` identifies sender evidence exhaustion; `durable_evidence_capacity` identifies receiver evidence exhaustion. These retained-evidence quotas are separate from active work and physical staged/file bytes. Existing status, exact reconciliation and cancellation remain available. Visible-history cleanup may not free evidence capacity. Keep uncertain evidence rather than erasing it to admit new work.
