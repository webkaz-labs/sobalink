# sobalink

[日本語](README.md) · [User guide](docs/GENERIC.en.md) · [Verification](docs/VERIFICATION.en.md)

**Close, even from afar.** Connect devices and use their applications through selected TCP/UDP services. Start `soba`, choose a peer in its local Web UI, then connect to SSH/SFTP, web, database or other application ports. Share a local service with explicit peers and a chosen lifetime. Text, images, files and folders are also available. The embedded React UI and CLI use the same Go authorization checks.

**Experimental software.** Check [sobalink Releases](https://github.com/webkaz-labs/sobalink/releases) for a published version's source, signed assets and verification results. Source feature descriptions alone do not establish acceptance for that version. Real-device enrollment, application compatibility, phone QR, native IME, OS sign-in and sleep/wake need separate checks. [Exact-source verification results](docs/VERIFICATION.en.md)

## Get started

### Use a signed release

The latest published prerelease is [0.3.0-alpha.2](https://github.com/webkaz-labs/sobalink/releases/tag/v0.3.0-alpha.2), source `00cc6a99809df77bf1754936ea7bf5ca4c5d0741`. Its [release workflow](https://github.com/webkaz-labs/sobalink/actions/runs/37178488713) passed all 15 jobs, including signatures, public download and installation on all four native targets. Prepared multi-relay recovery below is newer, unreleased source under verification; it is not in alpha.2. [Assets and supported targets](docs/DISTRIBUTION.md#install-a-signed-prerelease)

Run with mise **2026.9.18** available. `mise use -g` selects the version for normal use.

```sh
mise install "packslip:github.com/webkaz-labs/sobalink[prerelease=true]@0.3.0-alpha.2"
mise use -g "packslip:github.com/webkaz-labs/sobalink[prerelease=true]@0.3.0-alpha.2"
mise exec -- soba version
mise exec -- soba
```

### Build from source

Build this checkout with Go **1.27.1**, Node **24.19.0** and npm **11.9.0**. Frontend dependencies are pinned in the lockfile.

```sh
npm --prefix web ci --no-audit --no-fund
npm --prefix web run build
go build -tags ts_omit_portmapper,ts_omit_captiveportal,ts_omit_useproxy -trimpath -o bin/soba ./cmd/soba
./bin/soba
```

On Windows, build with `go build -tags ts_omit_portmapper,ts_omit_captiveportal,ts_omit_useproxy -trimpath -o bin/soba.exe ./cmd/soba` and run `./bin/soba.exe`. Node is not required to run the resulting binary.

### After starting

1. Open the printed `http://127.0.0.1:PORT` in a browser on the same device, then enter the one-time code shown in the terminal. The code is never part of the URL. Run `soba ui` in another terminal to issue a new code
2. Choose a network. For an existing Tailnet, enroll sobalink's separate node through the official Tailscale sign-in page. Tailcat LAN setup is available in the local UI and CLI, using an explicit trusted relay and pairing. See the [LAN guide](docs/LAN.en.md)
3. Review the peer, ports and lifetime before sharing or connecting a service. Use the actual endpoint in your application and verify its authentication and operation
4. To exchange messages or files, review the current identity and trust the intended sender. Send text explicitly; review images, multiple files and folders as a batch
5. The receiver normally chooses a directory and accepts each batch. Autosave requires an explicit choice of backend, trusted peer, current trust generation and destination
6. Stop an individual service, use `soba stop-shares` for all inbound shares, revoke trust, or run `soba stop` when finished

Examples use `soba` as an executable on PATH. Substitute `./bin/soba`, or `./bin/soba.exe` on Windows, for a source build. Keep its starting terminal open. A fresh profile has no selected network and starts no enrollment or sharing. [Complete UI and CLI guide](docs/GENERIC.en.md)

## Capabilities and boundaries

| Action | Scope |
| --- | --- |
| Service sharing | Explicit peers; finite 1-hour default or explicit until-revoked; compact TCP ranges, with reserved endpoints excluded |
| Connections | Until-stopped by default; ordinary Tailnet services work without sobalink on the target |
| Saved workflows | Stopped definitions, profiles, groups, reviewed import/export and leased local tasks; saving never starts a listener |
| Capacity | Explicit default/limited/unlimited logical choices, separate adjustable finite resource budgets |
| Text, images, files and folders | Explicit send, reviewable batches and manual text copying |
| Receiving | Per-batch acceptance by default; per-peer autosave to a fixed destination is optional |
| Retry | Retry unfinished whole files during the same running session; no partial-byte or restart resume |
| Local management | Exact `127.0.0.1` on an ephemeral port, one-time code, session, Origin and CSRF checks |

Received files are never automatically opened, executed or allowed to overwrite existing files. Executable attributes and links are not preserved. Browser APIs can omit empty folders; use the CLI and verify the resulting tree when exact folder structure matters.

A broad TCP share also permits applications started later inside that range while the permission remains active. Keep the range narrow and use exclusions. Discovery `54543`, peer API `54544`, pairing `54545` and backend-internal endpoints are excluded from service sharing. UDP and local connection listeners use a separately adjustable finite budget, initially 64 listeners. Counts such as 32 share peers and batches of 256 entries / 1 GiB are initial logical defaults, not fixed product maxima. Removing a logical limit never removes identity, path-safety, protocol or resource checks. [Capacity choices](docs/CAPACITY.en.md) · [Security boundaries](SECURITY.md)

Direct and Relay describe the encrypted traffic's route; reconnecting means communication is being re-established. Unknown routes remain unknown instead of being inferred from latency. Network backends are never exchanged automatically, and existing TCP sessions are not guaranteed to survive. Tailcat never falls back to arbitrary public relays. [Network design](docs/ARCHITECTURE.md)

## Prepared route recovery (unreleased)

Keep the same Tailcat pairing while preparing exact LAN-local and external relay candidates. Before changing networks, configure the candidates on both devices, privately exchange their authenticated offers, and approve the selected routes locally. The new CLI and local UI are implemented under verification. [Short route setup](docs/LAN.en.md#prepare-another-route-unreleased)

```mermaid
flowchart LR
    A[Application reconnect] --> L[Same local service entrance]
    L --> P[Same paired device]
    P --> R[Approved local relay]
    P --> E[Approved external relay]
```

The diagram describes the recovery target, not a verified availability guarantee. Saved-state LAN cold start with external services unavailable still needs native proof. Local means the relay address class; normal builds retain direct peer traffic, which may use public paths. Strict LAN/no-external-egress mode is deferred. Existing TCP may break, applications must reconnect, and file retry remains whole-item retry during the same process. [Evidence and limits](docs/VERIFICATION.en.md#route-recovery-gate)

## Short CLI example

```sh
soba setup --network tailnet
soba login --browser
soba peers
soba --dry-run share --preset ssh --peers PEER_ID
soba share --preset ssh --peers PEER_ID
soba connect --preset ssh --peer PEER_ID
soba trust PEER_ID
soba message PEER_ID "Hello"
soba send PEER_ID ./image.png ./notes.txt ./sample-folder
soba status
soba stop
```

Replace `PEER_ID` with a current peer and run share/connect on the respective devices. The SSH preset suggests TCP 22 and local connection port 2222. `--preset web` suggests TCP 8080/local 8080; override target and local ports with `--ports` and `--local-port`. Run and authenticate to the actual application separately. Omitted names use an unused name and never overwrite saved settings.

Use the local UI or [guided CLI](docs/CLI_GUIDE.en.md) to select, edit and review a service; explicit CLI flags support automation. LAN host/relay setup, invitation files, receive folders, autosave and saved-service save/inspect/copy/restart need no JSON editing. [Private browser/link/QR sign-in](docs/SIGN_IN.en.md), [saved services, groups and tasks](docs/SAVED_SERVICES.en.md), and [startup/logout](docs/LIFECYCLE.en.md) have dedicated guides. See `soba help examples`, `soba lan --help` and `soba service --help`. `--dry-run` checks inputs before applying; it is not a reachability or free-port test. Language is automatic, with `soba --locale ja ...` / `en` overrides. Status, peers, groups and service workflows provide localized human views; use `--json` for machine output. Advanced typed actions retain structured results. Explicit login display modes give private human guidance. `--json-errors` writes failures as `{code,error}` to stderr. [Detailed CLI guide](docs/GENERIC.en.md#language-automation-and-troubleshooting)

An optional [authenticated TCP proxy and diagnostics](docs/PROXY_DIAGNOSTICS.en.md) are also available. Broadcast delivery, a durable offline outbox, remote administration and remote filesystem browsing are outside the current implementation.

[Explicit startup and private proxy profiles](docs/STARTUP.en.md) · [Application settings and RustDesk](docs/CLIENT_HELPERS.en.md) · [Local Web controls](docs/WEB_CONTROLS.en.md) · [Feature parity and acceptance](docs/FEATURE_PARITY.en.md) · [Documentation index](docs/README.en.md)

## Development and verification

Follow the shared [development and usability principles](docs/DEVELOPMENT_PRINCIPLES.en.md). See [architecture](docs/ARCHITECTURE.md), [distribution](docs/DISTRIBUTION.md), [verification](docs/VERIFICATION.en.md), and [roadmap](docs/ROADMAP.en.md).

Retain Go race/vet checks and frontend unit tests, native Linux x64/ARM64, macOS ARM64 and Windows x64 CI, reproduction of locked frontend assets, repeated package builds, signing, provenance and actual installed-binary verification. Results from earlier releases are not evidence for this source.
