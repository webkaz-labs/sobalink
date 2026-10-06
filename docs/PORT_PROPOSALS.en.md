# Explicit alternate-port checks

[日本語](PORT_PROPOSALS.ja.md) · [CLI guide](CLI_GUIDE.en.md) · [Capacity](CAPACITY.en.md) · [API](../web/API.md)

A stopped saved outbound TCP/UDP connection can explicitly check alternate local entry ports. The existing fixed port stays unchanged until a separate reviewed restart. The local Web UI does not yet offer this action.

```sh
soba service show NAME_OR_ID
soba service ports NAME_OR_ID
soba --dry-run service restart NAME_OR_ID --local-port PORT --expected-revision REVISION
soba service restart NAME_OR_ID --local-port PORT --expected-revision REVISION
```

Use the revision and chosen port returned by `service ports`, and retain the same `--state-dir` throughout. Review the entire saved scope and lifetime before restarting: normal explicit restart begins the reviewed permission lifetime. An active service must first be stopped explicitly. Shares and application-side target ports are outside this proposal command.

The check first attempts the saved numeric loopback mapping. Only an OS address-in-use error triggers alternate checking. A currently free original mapping returns `listener_no_conflict`; there is no automatic restart. Every check temporarily binds the selected TCP/UDP protocol and exact `127.0.0.1` or `::1` family. No socket accepts or forwards application traffic, and all sockets close before returning. No saved configuration, remote permission, startup approval, router or security setting changes.

## Mapping and review

Candidate windows use only high ports, skipping known application, management, proxy and backend reserved ports. They preserve the saved protocol, loopback family and complete effective remote scope. As in ordinary `connect --local-port`, consecutive local ports map to ascending remote ports after exclusions. For example, remote `8000-8002` with exclusion `8001` maps local `49152-49153` to remote `8000,8002`. The result includes the full original configuration and its revision, so the original fixed-port intent remains visible.

A proposal is not a reservation or a promise of later availability. Another process may claim it immediately. The separate explicit restart rechecks each bind and uses the existing atomic listener rollback on failure. A failed start retains the explicitly chosen saved configuration without switching to another port. `--expected-revision` rejects changes made since the proposal instead of refreshing a stale review into authorization. Current backend, peer identity, discovery review and capacity checks still apply at start.

## Finite, configurable checking work

`--from-port PORT` selects a starting point from 1024 to 65535; the default is 49152. `--count N` and `--attempts N` request result count and candidate-window checks. Omitted/zero values follow the corresponding finite capacity settings:

- `portProposalResults`: 3 results by default
- `portProposalAttempts`: 32 candidate windows by default, including skipped reserved windows
- `portProposalBinds`: 4096 total bind attempts, including the original mapping, by default
- `portProposalSeconds`: a 5-second checking deadline by default, independent of service lifetime
- Remaining `materializedListeners`: maximum simultaneously held check sockets

These defaults can be raised or lowered through the normal capacity preview/apply flow. Explicit positive requests must fit the chosen budgets. The port domain itself bounds the search to 64512 high starting positions. Total work and result count are finite, independent of service permission lifetime. Larger mapped ranges need enough simultaneous-listener and total-bind budget to check a complete window. The deadline begins before configuration preflight and includes the backend-state read. Normalized exclusion scanning is linear; parsing and normalization are bounded by the physical port domain. The Core command lock stays held for one consistent review. Cancellation or timeout closes partial windows and returns a stable error without a proposal result.

`stopReason` reports `requested_count`, `attempt_budget`, `bind_budget` or `port_range`; fewer results do not prove that all other ports are occupied. Use another high starting point or review the finite budgets. `--dry-run service ports ...` prints the planned command without binding. `--json` retains the same schema and values in Japanese and English. These observations are not retained in the request deduplication cache: even a repeated request ID performs a fresh explicit check. The selected finite result count and physical port domain bound a single result; the usual local response-size envelope still applies. A response or decoding failure returns an error, not usable partial proposals.

## Known failures

| Code | Meaning and next step |
| --- | --- |
| `listener_conflict` | OS address-in-use; explicitly check alternatives or stop the competing listener |
| `listener_no_conflict` | Original ports are currently free; no alternate was chosen or started |
| `listener_capacity` | Configured simultaneous-listener or OS descriptor/memory/socket capacity is exhausted; changing ports does not solve it |
| `listener_probe_timeout`, `listener_probe_canceled` | The explicit check expired or was canceled; no saved settings changed; review checking work and retry explicitly |
| `listener_probe_capacity` | Requested checking work exceeds its finite budget; review check budgets |
| `listener_permission_denied` | OS bind permission denied; no automatic elevation or security changes |
| `listener_address_unavailable` | Selected loopback family/address is unavailable; review that explicit choice |
| `listener_mapping_invalid` | Invalid saved protocol, loopback, ports/exclusions or overflowing high-port mapping |
| `listener_unavailable` | Unclassified bind failure; its cause remains unknown |
| `listener_proposals_exhausted` | No checked proposal within this bounded search; not a whole-system availability claim |

Existing authentication, active-service, missing-service, backend and revision errors remain separate. Probe observations are local-management data and are not advertised to peers. Automated synthetic and native loopback checks do not establish application compatibility, actual enrollment, or OS-login/suspend acceptance.
