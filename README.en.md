# tsnet-bridge

[日本語](README.md)

An application-scoped tailnet bridge built with Go and embedded tsnet for macOS, Windows, and Linux. It does not change system-wide VPN, routing, or DNS settings.

> **Experimental.** Local/fake transport tests are distinct from real tailnet enrollment and RustDesk screen/control tests. Bidirectional remote control and initial Windows standard-user enrollment are unverified. There is no public Release yet. See [verification status](docs/VERIFICATION.md).

## Features

- Interactive authentication as a separate persistent tsnet node
- Loopback-only fixed TCP/UDP forwarding, with persistent per-source UDP mappings and asynchronous replies
- Authenticated SOCKS5 TCP CONNECT; BIND and UDP ASSOCIATE rejected
- Explicit peer/port allowlist and netstack-only transport, with no OS DNS/routing fallback
- Background startup, status, diagnostics, stop, forwarding recreation, and logout
- OS-backed single-instance lock, user-private local IPC, and protected state directories

No GUI, autostart registration, system service, subnet router, exit node, or generic internet proxy. RustDesk settings are shown for manual entry, never rewritten behind the application's back.

## Targets and installation

Linux x64/ARM64, macOS Apple Silicon/Intel, and Windows x64. Linux/macOS archives are tar.gz; Windows archives are zip with an exe. CI runner versions do not establish minimum OS support. Windows standard-user enrollment remains a release gate.

For development, install [mise](https://mise.jdx.dev/getting-started.html), then:

```sh
mise install
mise exec -- go test -race ./...
mise exec -- go build -trimpath -o bin/tsnet-bridge ./cmd/tsnet-bridge
```

On Windows use `bin/tsnet-bridge.exe` as the output name. The repository pins Go 1.27.1 and Tailscale 1.102.5 with go.sum integrity checks.

Signed Packslip through mise is the planned distribution entry point. **This command is not usable until a verified public release exists:**

```sh
# Future release workflow; replace <version> with a published explicit version
mise use -g packslip:github.com/webkaz-labs/tsnet-bridge@<version>
mise exec -- tsnet-bridge
```

End users will not need a Go compiler. See [distribution](docs/DISTRIBUTION.md).

## First run

```sh
tsnet-bridge setup
tsnet-bridge
tsnet-bridge login
```

Setup requests the ID server's tailnet peer name/IP and RustDesk public key. It saves the profile without network authentication. Start runs an embedded node; login opens a private Tailscale authorization URL for explicit enrollment. This is a separate identity from any installed Tailscale app. Use `login --no-browser` to open the URL manually; do not share it.

Inherited auth keys, OAuth/workload credentials, and alternate control-plane environment settings are rejected. A different profile path uses the global `--state-dir PATH` before the command. Setup refuses to overwrite existing profiles. Stop, back up, and deliberately edit profile.json for changes.

## Experimental RustDesk forwarding profile

Default bindings:

| Local | Remote |
| --- | --- |
| TCP + UDP 127.0.0.1:32116 | hbbs:21116 |
| TCP 127.0.0.1:32115 | hbbs:21115 |
| TCP 127.0.0.1:32117 | hbbr:21117 |

Back up RustDesk's existing server and proxy settings first. Run `tsnet-bridge settings` and enter its ID server, relay, and public key. Leave the proxy blank, keep UDP enabled, disable WebSocket, and connect using `remote-ID/r`.

**Every endpoint using this profile must run the helper with the same loopback relay address and port.** RustDesk forwards the relay address to its peer. One-sided deployment, independently chosen ports, and mixed native-tailnet/loopback profiles are unverified. Also check hbbs relay-address rewrite settings. `/r` does not guarantee the absence of direct probes; NAT reporting and address propagation require real testing.

Ports are fixed after setup and never silently changed. `setup --relay-host <tailnet-host>` supports a separate relay server.

### SOCKS limitations

`setup --mode socks` enables authenticated CONNECT-only SOCKS. Reveal credentials only in a private terminal with `settings --show-secrets`.

RustDesk 1.4.9 switches registration to TCP when a proxy is configured. OSS server 1.1.16 returns NOT_SUPPORT for TCP RegisterPk. **SOCKS alone cannot register the controlled endpoint in this combination.** Controller-only use also requires validation. The app-wide proxy may break updates/API calls outside the allowlist. See [sources and constraints](docs/ARCHITECTURE.md).

## Operation

```sh
tsnet-bridge                 # Start, or show status if already running
tsnet-bridge status --json
tsnet-bridge doctor
tsnet-bridge reconnect       # Recreate forwarding, retain saved login
tsnet-bridge stop            # Stop forwarding, retain saved login
tsnet-bridge logout          # Log out a running node, then stop
tsnet-bridge run             # Foreground; Ctrl+C stops forwarding
```

`ready` only means the allowed peer and TCP server ports were reachable. RustDesk screen/control always remains separately labeled `unverified`. Temporary failures close listeners and retry with increasing intervals. Authentication, admin approval, peer policy, and port conflicts have distinct messages.

If logout fails, forwarding stops but server-side logout is explicitly reported unconfirmed. Deleting the node in the Tailscale admin console is a separate operation. Stopping the helper does not restore RustDesk settings; use the backup. Canceling a login wait leaves the background process running; use `stop` if needed.

## Security and development

Unix directories/files use 0700/0600; Windows uses current-user DACLs. This is access control, not state encryption. Processes running as the same user are not isolated from each other. Fixed TCP/UDP forwarding cannot insert SOCKS authentication; use narrowly scoped tailnet policy and RustDesk authentication.

[Security](SECURITY.md) · [Architecture](docs/ARCHITECTURE.md) · [Verification](docs/VERIFICATION.md) · [Distribution](docs/DISTRIBUTION.md)

MIT license. Distribution archives include dependency notices.
