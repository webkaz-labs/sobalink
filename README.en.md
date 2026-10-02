# sobalink

[日本語](README.md) · [User guide](docs/GENERIC.en.md) · [Verification](docs/VERIFICATION.en.md)

**Nearby devices, one connection.** Start `soba`, choose a peer in its local Web UI, and send text, images, files or folders. Share selected TCP/UDP services with explicit peers for a limited time. The React UI is embedded in the Go agent; the UI and CLI use the same authorization checks.

**This is a development draft of sobalink, with no assigned release version.** The repository and Go module are now `github.com/webkaz-labs/sobalink`. Existing `tsnet-bridge` releases retain their historical signatures and do not establish availability of these new features. [Distribution and legacy releases](docs/DISTRIBUTION.md)

Commit `14ee61f8` passed [all six CI jobs](https://github.com/webkaz-labs/sobalink/actions/runs/37042721076). All four native platforms passed the real stock Tailcat loopback-relay test: existing TCP across a two-minute lease for 130 seconds, bidirectional TCP/UDP, denied-key admission, active revoke and cleanup. Native packages, the baseline browser checks and manifest also passed. The newer LAN setup UI, SVG graph and recovery controls are integrated locally, with 117 frontend tests, strict TypeScript and two byte-identical builds passing; their new font/graph browser checks and Core integration run await the next exact CI. This does not verify actual devices, direct LAN/WAN paths, sleep/wake or a release. [Exact verification scope](docs/VERIFICATION.en.md)

## Get started

Build this checkout with Go **1.27.1**, Node **24.19.0** and npm **11.9.0**. Frontend dependencies are pinned in the lockfile.

```sh
npm --prefix web ci --no-audit --no-fund
npm --prefix web run build
go build -tags ts_omit_portmapper,ts_omit_captiveportal,ts_omit_useproxy -trimpath -o bin/soba ./cmd/soba
./bin/soba
```

On Windows, build with `go build -tags ts_omit_portmapper,ts_omit_captiveportal,ts_omit_useproxy -trimpath -o bin/soba.exe ./cmd/soba` and run `./bin/soba.exe`. Node is not required to run the resulting binary.

1. Open the printed `http://127.0.0.1:PORT` in a browser on the same device, then enter the one-time code shown in the terminal. The code is never part of the URL. Run `soba ui` in another terminal to issue a new code
2. Choose a network. For an existing Tailnet, enroll sobalink's separate node through the official Tailscale sign-in page. Tailcat LAN setup is available in the local UI and CLI, using an explicit trusted relay and pairing. See the [LAN guide](docs/LAN.en.md)
3. Review the peer's current identity and trust the peers whose messages and transfer offers you want to receive. Send text explicitly; review images, multiple files and folders as a batch before sending
4. The receiver normally chooses a directory and accepts each batch. Autosave requires an explicit choice of backend, trusted peer, current trust generation and destination
5. Review the peer, ports and lifetime before sharing or connecting a service. Stop an individual service, revoke trust, or run `soba stop` when finished

Examples use `soba` as an executable on PATH. Substitute `./bin/soba`, or `./bin/soba.exe` on Windows, for a source build. Keep its starting terminal open. A fresh profile has no selected network and starts no enrollment or sharing. [Complete UI and CLI guide](docs/GENERIC.en.md)

## Capabilities and boundaries

| Action | Scope |
| --- | --- |
| Text, images, files and folders | Explicit send, reviewable batches and manual text copying |
| Receiving | Per-batch acceptance by default; per-peer autosave to a fixed destination is optional |
| Retry | Retry unfinished whole files during the same running session; no partial-byte or restart resume |
| Service sharing | Up to 32 explicit peers, 1 second–24 hours; TCP ranges map to the same loopback ports |
| Connections | Ordinary Tailnet services work without sobalink on the target |
| Local management | Exact `127.0.0.1` on an ephemeral port, one-time code, session, Origin and CSRF checks |

Received files are never automatically opened, executed or allowed to overwrite existing files. Executable attributes and links are not preserved. Browser APIs can omit empty folders; use the CLI and verify the resulting tree when exact folder structure matters.

A broad TCP share also permits applications started later inside that range while the permission remains active. Keep the range narrow and use exclusions. Discovery `54543`, peer API `54544`, pairing `54545` and backend-internal endpoints are excluded from service sharing. UDP and local connection listeners share a total 64-port cap. [Security boundaries](SECURITY.md)

Direct and Relay describe the encrypted traffic's route; reconnecting means communication is being re-established. Unknown routes remain unknown instead of being inferred from latency. Network backends are never exchanged automatically, and existing TCP sessions are not guaranteed to survive. Tailcat never falls back to arbitrary public relays. [Network design](docs/ARCHITECTURE.md)

## Short CLI example

```sh
soba setup --network tailnet
soba login
soba peers
soba trust PEER_ID
soba message PEER_ID "Hello"
soba send PEER_ID ./image.png ./notes.txt ./sample-folder
soba share --name preview --network tcp --ports 3000-3003 --peers PEER_ID --ttl 1h
soba connect --name preview --network tcp --ports 3000 --peer PEER_ID --ttl 1h
soba status
soba stop
```

Replace `PEER_ID` with a current peer. The provider must run and authorize access to the actual service. Language is selected automatically; override with `soba --locale ja ...` or `en`. Machine JSON field names and user values stay unchanged. Tailnet and transfer workflows do not require editing configuration JSON. LAN setup also has dedicated local controls; CLI payloads remain available for automation and embedded-relay hosting.

## Development and verification

Follow the shared [development and usability principles](docs/DEVELOPMENT_PRINCIPLES.en.md). See [architecture](docs/ARCHITECTURE.md), [distribution](docs/DISTRIBUTION.md), [verification](docs/VERIFICATION.en.md), and [remaining acceptance gates](docs/ROADMAP.ja.md).

Retain Go race/vet checks and frontend unit tests, native Linux x64/ARM64, macOS ARM64 and Windows x64 CI, reproduction of locked frontend assets, repeated package builds, signing, provenance and actual installed-binary verification. Results from earlier releases are not evidence for this source.
