# sobalink documentation

[日本語](README.md) · [Project overview](../README.en.md)

This is an experimental open-source project; no user support is provided. These self-service notes do not promise responses, fixes or compatibility.

Start with the [user guide](GENERIC.en.md). The [verification record](VERIFICATION.en.md) is authoritative for published versions, development sources and executed checks. Each workflow identifies its target version and limitations; automated tests do not establish physical-device acceptance.

Use this index to find the right document. Guides explain operations, specifications describe behavior and limits, verification records show completed checks, and the roadmap lists remaining work. Japanese and English pages cover the same topics. Architecture, distribution and the Web acceptance proposal use shared English documents.

## Choose your next step

| Goal | Read first | Then check |
| --- | --- | --- |
| Understand the product | [Overview](../README.en.md) | [Common questions](FAQ.en.md) |
| Install or update | [Signed distribution and profile boundary](DISTRIBUTION.md) | [First run](GENERIC.en.md) |
| Use a service | [Connect to a service](GENERIC.en.md#connect-to-a-peers-service) | [Connection setup](CONNECTION_SETUP.en.md), if a new pairing is needed |
| Change limits or permissions | [Capacity](CAPACITY.en.md) | [Security](../SECURITY.md) |
| Diagnose a problem | [First failing stage](TROUBLESHOOTING.en.md) | The focused guide below |
| Build or verify a change | [Development principles](DEVELOPMENT_PRINCIPLES.en.md) | [Verification](VERIFICATION.en.md) |

## Start and use

- [First start, send and stop](GENERIC.en.md)
- [Guided CLI and automation](CLI_GUIDE.en.md)
- [Local Web controls](WEB_CONTROLS.en.md)
- [Optional desktop client](NATIVE_UI.en.md)
- [Private sign-in and cancellation](SIGN_IN.en.md)
- [SSH/HTTP settings and RustDesk](CLIENT_HELPERS.en.md)
- [Saved definitions, groups and tasks](SAVED_SERVICES.en.md)
- [Run, stop, sign out and OS sign-in](LIFECYCLE.en.md)
- [Explicit startup and private proxy profiles](STARTUP.en.md)
- [Durable files and folders](DURABLE_OUTBOX.en.md)
- [Capacity, resource limits and history](CAPACITY.en.md)
- [Scoped proxies and diagnostics](PROXY_DIAGNOSTICS.en.md)
- [Use cases](USE_CASES.en.md)
- [Common questions](FAQ.en.md)
- [Things to check yourself](TROUBLESHOOTING.en.md)

## Network workflows

- [Explicit relay pairing and recovery](LAN.en.md)
- [Relay host, certificates and resource budgets](RELAY_OPERATIONS.en.md)
- [LAN destination restrictions](LAN_DESTINATIONS.en.md)
- [Relayless LAN](DIRECT_LAN.en.md)
- [Mixed connections](MIXED_CONNECTIONS.en.md)
- [Review and exchange device cards](DEVICE_CARDS.en.md)
- [Review alternate ports](PORT_PROPOSALS.en.md)

## Connection, queues and resource settings

These workflows are described for v0.5.1-alpha.1. Their inclusion in the release does not establish physical-device acceptance; consult each guide and the exact-source evidence.

- [Connection setup](CONNECTION_SETUP.en.md)
- [Durable text](DURABLE_TEXT.en.md)
- [Resource catalog](RESOURCE_CATALOG.en.md)
- [Local resource settings](RESOURCE_SETTINGS.en.md)
- [Permitted peer inspection](RESOURCE_INSPECTION.en.md)
- [Permitted peer transfer-setting management](RESOURCE_MANAGEMENT.en.md)
- [Fixed peer-group settings](RESOURCE_GROUP.en.md)
- [Web activation handoff and checks](WEB_ACTIVATION_HANDOFF.en.md)

## Design, security and development contracts

- [Development and usability principles](DEVELOPMENT_PRINCIPLES.en.md)
- [Architecture and network boundaries](ARCHITECTURE.md)
- [Local browser API and peer boundary](../web/API.md)
- [Diagram source and maintenance (online source repository)](https://github.com/webkaz-labs/sobalink/blob/main/docs/assets/visuals/README.md)
- [Security boundaries](../SECURITY.md)
- [Route recovery contract](ROUTE_RECOVERY_DESIGN.en.md)
- [Catalog view contract](CATALOG_VIEW_CONTRACT.en.md)
- [Installation, build and signed distribution](DISTRIBUTION.md)
- [Change-aware CI](CI_EFFICIENCY.en.md)

<a id="evidence-remaining-acceptance-and-history"></a>

## Verification and remaining acceptance

- [Published baseline and exact-source run results](VERIFICATION.en.md)
- [Implementation and remaining acceptance](ROADMAP.en.md)
- [Reproducible browser acceptance procedure](../web/BROWSER_ACCEPTANCE.md)
- [Batch-send acceptance conditions](BATCH_SEND_ACCEPTANCE.en.md)
- [Published-binary resource observations](RESOURCE_MEASUREMENT.en.md)
- [Isolated relay traffic observations](RELAY_TRAFFIC_MEASUREMENT.en.md)
- [Web acceptance design and source-specific history](WEB_ACTIVATION_ACCEPTANCE_PROPOSAL.md)

## Historical records

[History index](history/README.en.md) separates the 0.3-series parity/plan snapshots and dated Web UI reviews from current instructions. Source IDs, failures and then-pending gates remain available. The original top-level paths and section links still lead to the retained records.

## Where to maintain documentation

| Content | Authoritative location |
| --- | --- |
| First steps and navigation | Root README, this index and the user guide; summarize and link to details |
| A complete operation or feature contract | Its dedicated guide above; keep executable recipes with the contract they depend on |
| Current implementation structure | Architecture, local API and the relevant `internal/`, `web/` or `poc/native/` document beside the code |
| Executed checks and publication evidence | Verification record; link detailed source-specific reports rather than copying a pass into every guide |
| Remaining work | Roadmap; do not use an old plan's open items as today's backlog |
| Historical snapshots | `docs/history/`; existing code-local review records can stay beside their implementation |

Current guides retain their stable paths. Do not move every file solely to create more folders. Retain short task-specific safety reminders where needed, but keep full commands, limits and recovery contracts in one owning guide. Japanese/English counterparts are intentional translations, not duplicate documents to remove.

Before merging documentation changes, follow the [development principles](DEVELOPMENT_PRINCIPLES.en.md) and [document CI contract](CI_EFFICIENCY.en.md#required-document-checks). Update both languages and inbound links together. Keep old section anchors when consolidating published pages. Check repository-wide links and images, and check packaged relative links when changing layout. Packaging intentionally includes only top-level Markdown in `docs/`, `docs/history/` and `web/`; code-local development notes are not installed guides. Nested or unknown changes retain conservative CI classification.
