# Local resource catalog

[日本語](RESOURCE_CATALOG.ja.md) · [Development principles](DEVELOPMENT_PRINCIPLES.en.md) · [v0.5.0-alpha.1](https://github.com/webkaz-labs/sobalink/releases/tag/v0.5.0-alpha.1)

`soba resource catalog` presents a bounded local composition of existing observations. It does not add a peer-wire catalog, remotely enumerate resources, grant permission or perform changes. This describes source behavior; tests, real devices, installed binaries and distribution require separate acceptance.

The separate [shared-service catalog view](CATALOG_VIEW_CONTRACT.en.md) is implemented: it uses explicit peer-specific viewing consent, a bounded remote list and fresh connection review through `resource catalog view` and the Web services flow. The legacy-cache restrictions below apply to `resource catalog`, not to every service-listing capability.

## Choose the information you need

| Goal | Workflow | Permission boundary |
| --- | --- | --- |
| Read local settings, saved services and transfer activity | `soba resource catalog` | An observation; no changes or continuous monitoring |
| Inspect one peer's settings | Exact remote selector below | Existing inspection or management grant |
| View current shared services on a peer | [Shared-service view](CATALOG_VIEW_CONTRACT.en.md) | Separate view grant, then separate connection permission |

Missing or stale rows do not prove absence. Read each source's state and observation time before choosing its next workflow.

## Start with the normal command

Run `soba resource catalog` with the selected local agent running. It first resolves the local settings target through `resource.list`, then requests local settings, saved services and current-process transfer activity in one catalog command. If settings resolution fails, the command reports that category unresolved. It does not silently label a reduced selection complete. Retry `resource list`, or explicitly request the narrower `soba resource catalog --services --transfers`.

Use global `--state-dir DIR` before `resource` throughout the workflow. Human follow-up commands preserve that directory. `--locale auto|en|ja` controls human language; `--json` returns the same validated envelope in either language.

## Choose exact sources

Any source flag replaces the default source set. Combine flags only for sources you intend to inspect:

- `--settings` resolves one local settings resource with `resource.list`.
- `--settings-id RESOURCE_ID` selects that exact local settings target without resolution.
- `--services` selects saved local service observations.
- `--transfers` selects process-local transfer activity.
- `--discovery-peer PEER_ID` selects existing service-discovery observations for one exact peer.
- `--remote-version 1|2 --peer PEER_KEY --id RESOURCE_ID --grant-id GRANT_ID --grant-revision N` selects one exact remote settings inspection through the existing authorized protocol. v1 requires the appropriate inspect-only grant; v2 uses a management grant only for inspection.

At most one remote identity is selected. Combining discovery and remote settings requires the same exact peer identifier, not a display-name or backend alias. There is no all-peers fan-out, automatic protocol fallback, arbitrary command name or JSON patch. The command does not create a listener or change trust.

## Read observations accurately

Read each source's scope, state, observation time, completeness and next workflow separately. Settings, services and transfers can have different observation times within one response. `current` means successfully observed at that source's stated time, not continuously watched, reachable now or atomically read with every other source.

| Source at its own observation time | What the row establishes | Next check, and why |
| --- | --- | --- |
| Local settings | Values read from the exact resource | Inspect, then fresh preview before changes; past success does not fix current values |
| Saved services | Saved definitions at that read | `service show`, then the normal start/stop review; saved does not mean running or reachable |
| Transfer activity | Current-process activity for a batch and direction | Find that same batch/direction in the UI; restart does not preserve its process-local identity |
| Exact remote settings | An authorized read of one target | Use its existing exact-selector workflow; reading alone grants no change permission |
| Cached discovery | Historical rows, always `stale`, even when recently observed or empty | Use the separate [catalog view](CATALOG_VIEW_CONTRACT.en.md); publication attribution or a newer timestamp does not grant viewing or connection |

The table compares independent observations rather than implying an event sequence. Local settings, saved services and transfers have no reachability TTL. A successful empty source differs from unavailable, unconfirmed or limited results; missing rows do not prove deletion. A failed source stays visible when another succeeds. Only an authenticated explicit unsupported response establishes protocol non-support; an unreachable peer does not.

Persistent resource/service identities last with their saved records. Advertised service IDs last only for the activation. Transfer IDs retain their original batch ID, incoming/outgoing direction and current-process namespace. Transfer rows describe activity; they do not expose file contents or paths or establish durable file-share semantics.

### Service-discovery limitation

In this local catalog, cached `remote_service` observations are returned stale and non-actionable, including recently observed or confirmed-empty caches. Retained publication attribution alone does not authorize a current catalog observation or connection. An unknown or mixed backend may instead return unavailable. Running `discover --peer PEER_ID` explicitly refreshes the existing discovery workflow, but does not by itself lift this catalog gate. Use the separate [catalog-view workflow](CATALOG_VIEW_CONTRACT.en.md) for a currently authorized service list and fresh connection review; it does not promote these historical rows.

## Continue through existing workflows

Catalog only suggests next steps. It never applies settings, starts/stops services, connects, accepts/retries transfers, opens a file chooser or changes sharing permission.

- Settings: use the displayed `resource inspect` or exact `resource remote [manage] inspect` command, then obtain the existing provider's fresh review before changes.
- Saved services: use `service show SERVICE_ID`, then review the existing `services start` or `services stop` workflow. Suggested start/stop previews use global `--dry-run`. Failed service state offers inspection until the existing workflow resolves actual activity.
- Discovery: use explicit `discover --peer PEER_ID`, then the existing connect workflow with its normal local-port and lifetime review. The catalog limitation above still applies.
- Transfers: open the existing local UI and locate the same peer, direction and original batch ID. Restarted or missing activity is no longer the same process-local item; no action is automatically triggered.

Prior observation age never replaces fresh provider authorization or review. Snapshot IDs, epochs, scope IDs, digests and finite limits are correlation/presentation data, not authority.

## Local Web navigation

The authenticated local Web catalog offers explicit source selection and refresh for settings, saved services and process-local transfer activity. Opening it or receiving host-state updates does not fetch a catalog automatically. Unresolved categories remain visible; an incomplete snapshot is not presented as a complete empty result.

Settings navigation opens the existing settings review. Saved-service navigation selects an existing saved definition for its normal workflow. Transfer navigation focuses the original current-process batch and direction without accepting, retrying or sending anything. **Open service connections** opens a blank manual form for the explicitly selected peer; it carries no stale advertisement ID, ports, revision or lifetime and never connects automatically. Fresh selection, review and authorization belong to that existing workflow. Cached discovery rows remain stale and have no connection action.

Earlier mounted synthetic Web tests and the bounded three-Core native local-catalog scenario passed on their reviewed sources. The later [B1 run 38069582712](https://github.com/webkaz-labs/sobalink/actions/runs/38069582712) passed with a real authenticated browser and one offline Core: local settings, an inert saved service, an empty transfer catalog, settings navigation, keyboard/focus and narrow layout. This does not establish nonempty real-transfer or saved-service navigation, remote catalog capability or a two-provider group apply. B2's separate two-provider workflow is documented in the [group results](RESOURCE_GROUP.en.md#verified-scope-and-remaining-acceptance).

## Dry run and stable JSON

Global `--dry-run` makes zero agent calls and writes no profile. If the local settings ID is unknown, output lists `local_settings` in `unresolvedSources` with `requiresResourceList: true`. Concrete selected sources remain data-only under `resolvedSources`; there is no fabricated target or executable catalog payload. Supply `--settings-id` to validate a complete concrete local-settings selector without resolution.

`--json` validates the closed envelope, finite limits, exact source correspondence and the catalog aggregate digest before printing. The envelope contains `schemaVersion`, lower-camel-case `limits`, and `snapshot`. Fields and user-supplied values are not translated. Human output does not include raw provider errors, provider-private paths or arbitrary shell commands assembled from labels.

The B1 results above describe that historical local-catalog scope only. The separate implemented remote service-view capability and its process/browser tests are described in the [view contract](CATALOG_VIEW_CONTRACT.en.md). Arbitrary remote resource enumeration and persistent file sharing are not added by either workflow. Final-source regression, signed distribution and physical-device acceptance remain separate; consult [verification](VERIFICATION.en.md) for recorded outcomes.
