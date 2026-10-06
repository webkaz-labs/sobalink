# sobalink documentation

[日本語](README.md) · [Project overview](../README.en.md)

The published baseline is [0.3.0-alpha.4](https://github.com/webkaz-labs/sobalink/releases/tag/v0.3.0-alpha.4), including prepared multi-relay recovery. Explicit LAN destination restrictions, relay operations, relayless LAN, WAN discovery and mixed-backend routing are development changes under verification. The direct-LAN cold-start regression is fixed and covered by localhost TCP/UDP lifecycle tests. Final four-target native/browser and release acceptance remain open. Start with a task below; the verification record distinguishes source implementation, automated evidence, signed distribution and physical-device acceptance.

For outside-LAN connections, use Tailscale. Relay hosting and upcoming placement improvements focus on LAN hosts; external relay deployment is out of scope for now. Advanced opt-in WAN candidates require an already reachable compatible pinned relay.

| Task | Guide |
| --- | --- |
| Get started | [GENERIC.en.md](GENERIC.en.md) |
| Guided CLI and automation | [CLI_GUIDE.en.md](CLI_GUIDE.en.md) |
| Local Web controls | [WEB_CONTROLS.en.md](WEB_CONTROLS.en.md) |
| SSH/HTTP settings and RustDesk | [CLIENT_HELPERS.en.md](CLIENT_HELPERS.en.md) |
| Private sign-in and cancellation | [SIGN_IN.en.md](SIGN_IN.en.md) |
| Saved definitions, groups and tasks | [SAVED_SERVICES.en.md](SAVED_SERVICES.en.md) |
| Run, stop, logout and OS sign-in | [LIFECYCLE.en.md](LIFECYCLE.en.md) |
| Explicit outbound startup and private proxy profiles | [STARTUP.en.md](STARTUP.en.md) |
| Scoped proxies and diagnostics | [PROXY_DIAGNOSTICS.en.md](PROXY_DIAGNOSTICS.en.md) |
| Explicit relay pairing and recovery | [LAN.en.md](LAN.en.md) |
| Development: LAN destination restrictions | [LAN destinations](LAN_DESTINATIONS.en.md) |
| Development: relayless LAN | [Direct LAN](DIRECT_LAN.en.md) |
| Development: mixed connections and new-flow routing | [Mixed connections](MIXED_CONNECTIONS.en.md) |
| Prepared-route implementation and gates | [Route recovery](ROUTE_RECOVERY_DESIGN.en.md) |
| Capacity, budgets and history | [CAPACITY.en.md](CAPACITY.en.md) |
| Development and usability principles | [DEVELOPMENT_PRINCIPLES.en.md](DEVELOPMENT_PRINCIPLES.en.md) |
| Feature parity and acceptance | [FEATURE_PARITY.en.md](FEATURE_PARITY.en.md) |
| Verification by source | [Verification](VERIFICATION.en.md) |
| Resource and transport observations | [Published binary resources](RESOURCE_MEASUREMENT.en.md) · [Isolated relay traffic](RELAY_TRAFFIC_MEASUREMENT.en.md) |
| Security boundaries | [Security](../SECURITY.md) |
| Architecture and distribution | [Architecture](ARCHITECTURE.md) · [Distribution](DISTRIBUTION.md) |
| Remaining acceptance work | [Roadmap](ROADMAP.en.md) |
