# Shared-service catalog view contract

[日本語](CATALOG_VIEW_CONTRACT.ja.md) · [Development principles](DEVELOPMENT_PRINCIPLES.en.md)

This implementation adds eligibility data, owned private storage, explicit local
approval/revocation and a bounded peer list exchange. It retains original transport
attribution. Web/CLI review workflows and real-backend acceptance are still pending;
historical catalog rows remain stale and viewing does not authorize a connection.
No release version is assigned.

## Exact selection and coexistence

`resourcegrant.CatalogViewEnvelope` is independent of the existing inspection and
management `Envelope`. Their decoders reject each other's data. Existing grant
records, revisions, clock, single-active rule and runtime fences remain unchanged.
The catalog envelope has its own revision history, clock and at most one active
catalog grant, stored in `resource-catalog-views/state.json`. Unknown, corrupt,
replaced or previously observed missing state is denied. No legacy grant migration
creates catalog authority. Approval and revocation preserve the existing grant file.

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

## Local control and list exchange

The standard owning agent enables the implementation without creating a catalog
grant. Authenticated local commands are `resource.catalog.view.preview`, `confirm`,
`inspect`, `revoke` (each with the same prefix) and `resource.catalog.view.list`.
Preview selects target, peer key, exact service IDs and finite expiry without
writing. Confirm requires the unchanged review and explicit `confirm: true`;
revoke requires exact target, grant ID/revision and confirmation. Review hashes
are bound to this process. Saving failure freezes catalog access before activation.
Reopen preserves the absolute expiry and anchors a new boot's remaining monotonic
cutoff; it never gives another full lifetime. Clock uncertainty denies disclosure.

List selects one remote peer and an exact versioned target/grant selector. The
peer POST `/v1/catalog/services` accepts no self-claimed peer identity. It uses the
existing owned DirectLAN HTTP connection, managed relationship and original origin.
Only granted, active share generations are returned. `connectAllowed` separately
reports the current share's peer membership; viewing never changes that membership.
After final ownership, grant and service checks, the same revocable fence admits
one precomputed bounded response. A response admitted before revocation may finish;
revocation closes the fence before fallible persistence and denies later admission.

The receiver retains one explicitly selected view and original attribution in
private memory. Its public review revision is only a local reference. It cannot
promote historical catalog rows or replace remote grant revalidation before connect.

## Next acceptance steps

1. Exercise the implemented approval, independent persistence and list exchange
   through real managed DirectLAN processes, including startup and revoke races.
2. Integrate the permitted list with the catalog and connection review. Recheck
   publication origin, activation IDs, conditions, expiry and current grant at
   final disclosure and before handing a reviewed service to `service.connect`.
3. Align Japanese/English Web and CLI review, empty/error states and refresh.
4. Verify cancellation, expiry, re-pair and stale-selection races with real
   processes and browser tests, then verify actual service use and regressions.

The current tests cover the data contract, owned storage, local consent/revoke,
failed save, prepared disclosure versus revocation, legacy grant preservation and
retained-origin rejection. They are not real-device or completed workflow acceptance. Existing
browser sandbox behavior and CI conditions are unchanged.
