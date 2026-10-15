# sobalink documentation

[日本語](README.md) · [Project overview](../README.en.md)

This is an experimental open-source project; no user support is provided. These self-service notes do not promise responses, fixes or compatibility.

Start with the [user guide](GENERIC.en.md). The [verification record](VERIFICATION.en.md) is authoritative for published versions, development sources and executed checks. Each workflow identifies its target version and limitations; automated tests do not establish physical-device acceptance.

This index routes readers to the right document. Dedicated guides own workflows, contracts own guarantees and limits, verification owns executed evidence, and the roadmap owns remaining work. Japanese and English pages are paired views of the same role. Architecture, distribution and the Web acceptance proposal use shared English documents.

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
- [Private sign-in and cancellation](SIGN_IN.en.md)
- [SSH/HTTP settings and RustDesk](CLIENT_HELPERS.en.md)
- [Saved definitions, groups and tasks](SAVED_SERVICES.en.md)
- [Run, stop, sign out and OS sign-in](LIFECYCLE.en.md)
- [Explicit startup and private proxy profiles](STARTUP.en.md)
- [Durable files and folders](DURABLE_OUTBOX.en.md)
- [Capacity, budgets and history](CAPACITY.en.md)
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

These workflows are described for v0.5.0-alpha.1. Their inclusion in the release does not establish physical-device acceptance; consult each guide and the exact-source evidence.

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
- [Diagram source and maintenance](assets/visuals/README.md)
- [Security boundaries](../SECURITY.md)
- [Route recovery contract](ROUTE_RECOVERY_DESIGN.en.md)
- [Catalog view contract](CATALOG_VIEW_CONTRACT.en.md)
- [Installation, build and signed distribution](DISTRIBUTION.md)
- [Change-aware CI](CI_EFFICIENCY.en.md)

## Evidence, remaining acceptance and history

- [Published baseline and exact-source run results](VERIFICATION.en.md)
- [Implementation and remaining acceptance](ROADMAP.en.md)
- [Batch-send acceptance conditions](BATCH_SEND_ACCEPTANCE.en.md)
- [Published-binary resource observations](RESOURCE_MEASUREMENT.en.md)
- [Isolated relay traffic observations](RELAY_TRAFFIC_MEASUREMENT.en.md)
- [0.3-series feature parity history](FEATURE_PARITY.en.md)
- [0.3-series convenience planning and evidence](CONVENIENCE_PLAN.en.md)
- [Web acceptance design and source-specific history](WEB_ACTIVATION_ACCEPTANCE_PROPOSAL.md)
