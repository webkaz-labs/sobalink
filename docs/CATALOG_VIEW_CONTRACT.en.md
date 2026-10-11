# Shared-service catalog view contract

[日本語](CATALOG_VIEW_CONTRACT.ja.md) · [Development principles](DEVELOPMENT_PRINCIPLES.en.md)

This first implementation adds eligibility data and retains discovery publication
attribution. It does **not** expose a new approval command, persist catalog grants,
enable current catalog rows, or authorize a connection. No release version is assigned.

## Exact selection and coexistence

`resourcegrant.CatalogViewEnvelope` is independent of the existing inspection and
management `Envelope`. Their decoders reject each other's data. Existing grant
records, revisions, clock, single-active rule and runtime fences remain unchanged.
The catalog envelope has its own revision history, clock and at most one active
catalog grant. Approval, revocation and storage integration are subsequent work;
they must not automatically migrate, replace or widen an existing grant.

Each catalog record selects one managed DirectLAN `Relationship` (target key,
peer key and immutable pair binding), one resource target, and a sorted, unique
list of 1–16 shared-service activation IDs. Those IDs are minted by the existing
share activation, not saved service configuration IDs. Restarting a share produces
a different ID and requires a new explicit selection. No wildcard, connection,
settings inspection or management action can be represented in this model.

`MatchesSelection` narrows eligibility only: it rejects another relationship,
pair binding, activation ID, revoked/expired state and an invalid lifetime.
It is not a transport admission gate. The future owned coordinator must retain
monotonic expiry, check current ownership and relationship, and perform bounded
final disclosure under the original publication origin and current view grant.
Connection still uses the existing independent service permission.

## Publication attribution

Discovery retains the single actual HTTP application's transport origin while
publishing its result under the existing publication permit. Cache deletion and
backend reset clear that origin too. DirectLAN catalog reads use the retained
origin; they cannot reconstruct it by capturing today's peer. Missing, stopped
or replaced origins reject the captured source. Multiple-route attribution is
unsupported. Historical catalog rows remain `stale` until explicit catalog grant
admission and the provenance association are integrated; changing a timestamp
cannot make a historical advertisement current.

## Next acceptance steps

1. Add explicitly confirmed catalog approval and independent owned persistence,
   preserving existing inspection/management grants across approval and revoke.
2. Fetch the permitted list for an explicitly selected DirectLAN peer. Recheck
   publication origin, activation IDs, conditions, expiry and current grant at
   final disclosure and before handing a reviewed service to `service.connect`.
3. Align Japanese/English Web and CLI review, empty/error states and refresh.
4. Verify cancellation, expiry, re-pair and stale-selection races with real
   processes and browser tests, then verify actual service use and regressions.

The current tests cover the data contract and retained-origin rejection only;
they are not evidence of real-device or completed workflow acceptance. Existing
browser sandbox behavior and CI conditions are unchanged.
