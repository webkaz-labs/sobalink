# tsnet-bridge

[日本語](README.md) · **[Named connection guide](docs/GENERIC.en.md)** · [Experimental RustDesk acceptance](docs/VERIFICATION.en.md#legacy-rustdesk-acceptance-procedure)

**Choose a service shared with this node → review → connect.** An application-scoped tailnet bridge built with Go and embedded tsnet. The current source adds service-first discovery to named TCP/UDP connections and sharing limited to explicit peers, services and lifetimes. Japanese and English are selected automatically from the OS/runtime locale.

**Discovery is a current-source feature, not part of the published `0.2.0-alpha.1` binary.** Use the [source-build guide](docs/GENERIC.en.md#first-use) for the new flow. The release results below apply only to their recorded source; discovery has no release or real-tailnet acceptance claim.

It does not change system-wide VPN, routing or DNS settings. It targets ordinary-user installation and operation; destination permissions still apply and real Windows standard-user enrollment remains unverified.

> **Experimental acceptance-testing prerelease.** Exact-source native tests and package checks passed on four targets. Real enrollment, phone QR authentication, actual tailnet ACLs and applications, OS login/sleep behavior, and RustDesk bidirectional screen/input remain unverified. `ready` describes connection readiness, not application success. [Evidence and remaining limits](docs/VERIFICATION.en.md)

## 0.2.0-alpha.1 verification status

- [Published testing prerelease](https://github.com/webkaz-labs/tsnet-bridge/releases/tag/v0.2.0-alpha.1): 2026-10-01 at 19:11:08 UTC, with 19 assets
- [Exact-source CI for `236bd8e`](https://github.com/webkaz-labs/tsnet-bridge/actions/runs/36910805666): all five jobs passed. [Release workflow](https://github.com/webkaz-labs/tsnet-bridge/actions/runs/36911703369): all 15 jobs passed
- All public assets were downloaded without authentication and signature/provenance/content-verified. Actual mise installation, Japanese/English output, OS-locale fallback and exact JSON checks passed on macOS ARM64, Windows x64 and Linux x64/ARM64
- No signature bypass or release-age override. An additional isolated non-root Linux offline install also passed

[Distribution](docs/DISTRIBUTION.md) and [verification](docs/VERIFICATION.en.md) separate these results from unperformed real-device acceptance.

## Install the published 0.2.0-alpha.1 release

Targets: Linux x64/ARM64, macOS Apple Silicon (ARM64), and Windows x64. Intel macOS and Windows ARM64 are not included. CI runner versions do not establish minimum OS support or Windows standard-user operation.

Before installing, confirm `v0.2.0-alpha.1` is marked **Pre-release** on [Releases](https://github.com/webkaz-labs/tsnet-bridge/releases), with `packslip.sigstore.json` and the target archive present. Stop if publication or assets are missing. The verification version of [mise](https://mise.jdx.dev/getting-started.html) is **2026.9.18**. These commands work in PowerShell, macOS and Linux; no Go compiler or manual extraction is needed:

```sh
mise use -g "packslip:github.com/webkaz-labs/tsnet-bridge[prerelease=true]@0.2.0-alpha.1"
mise exec -- tsnet-bridge version
mise exec -- tsnet-bridge init
mise exec -- tsnet-bridge login
mise exec -- tsnet-bridge connect
```

Require `tsnet-bridge 0.2.0-alpha.1`. `init` saves an idle profile without networking. `login` starts a separate node from any installed Tailscale app and presents the official sign-in flow. Review the account, tailnet and permissions. `connect` asks for a current peer and purpose, then shows the actual endpoints for review. No JSON editing or RustDesk key is needed.

Existing profiles are never overwritten. Follow [migration or separate-profile guidance](docs/GENERIC.en.md#migration-local-exportimport-optional-startup); a separate profile uses the global `--state-dir PATH` before every command.

Explicitly opt into prereleases and pin the complete version; do not substitute `latest`. Exact pins are exempt from mise 2026.9.18's default 24-hour discovery cutoff. Keep signature, identity and digest checks enabled. Packslip does not establish OS code signing/notarization or application compatibility. Stop at OS security warnings instead of bypassing them.

## Everyday use with the current source

Build the current source first, using the [guide](docs/GENERIC.en.md#first-use). These examples use `bin/tsnet-bridge` (`bin/tsnet-bridge.exe` on Windows); the older release retains its peer/purpose flow.

```sh
bin/tsnet-bridge connect            # Use a peer's service
bin/tsnet-bridge share              # Review explicit peers, service and lifetime
bin/tsnet-bridge settings           # Display endpoints for the application
bin/tsnet-bridge status
bin/tsnet-bridge doctor
bin/tsnet-bridge stop               # Stop the node, retaining saved login
```

Resume a saved connection with `connect web-demo`, or stop only that rule with `stop web-demo`; replace the example with your saved name. Sharing exposes only the selected numeric-loopback service to the selected peers for the chosen lifetime. Application authentication is still required. Stop/expiry closes existing traffic but cannot retract data or cancel an already running remote job.

- Fresh authenticated shares permitted to this node, with peer, purpose, protocol and port filled in automatically
- `connect --manual` for ordinary Tailscale services, older bridges or known endpoints; no discovery result proves application health
- New interactive shares preview minimal discovery metadata; `share --no-discovery` disables it, and older profiles stay private by default
- Multiple named TCP/UDP connections with Web, SSH/SFTP, database and AI API purpose presets
- Pinned peer identities, share TTLs, grouped start/stop and partial-start rollback
- Per-rule JSON, readiness waits, task ownership and expiring cleanup leases
- Up/Down selection, Left/Right text editing, typed numbers/names and comma-separated sharing peers; explicit confirmation still controls saving/starting
- In-place input retries, edit/back/cancel, and repeat actions that preserve reviewed scope and expiry
- Browser, locally generated terminal QR, and private manual-link sign-in guidance
- Optional user-level autostart of an idle node, never active forwards or shares

For a discoverable share, the provider keeps the application and signed-in bridge running, starts an unexpired share allowing this node, and reviews discovery metadata. First-time provider setup is `init` → `login` → `share`; skip `init` for an existing profile. Ordinary services already exposed by a Tailscale peer need no remote bridge: use `connect --manual`. Presets are editable port examples, including local AI API port `11434`, not automatic application setup. [Provider requirements and presets](docs/GENERIC.en.md#what-the-service-provider-needs)

No language flag is needed normally. Override only when desired with `--lang ja`, `en` or `auto` before the command. Command names, user values and machine JSON are not translated. [Full bilingual, authentication, connection and sharing guide](docs/GENERIC.en.md)

## Historical release and experimental RustDesk workflow

`0.1.0-alpha.2` is the historical fixed RustDesk/SOCKS release and has no named connection features. Its [published release](https://github.com/webkaz-labs/tsnet-bridge/releases/tag/v0.1.0-alpha.2) passed [signing, public download verification and actual mise installation on all four targets](https://github.com/webkaz-labs/tsnet-bridge/actions/runs/36888899407) on 2026-10-01. That evidence does not establish distribution of the new version.

The `0.2.0-alpha.1` source retains legacy profiles, but real RustDesk screen/input remains unverified. The [alpha.2-pinned acceptance procedure](docs/VERIFICATION.en.md#legacy-rustdesk-acceptance-procedure) is preserved for comparison. RustDesk settings are never changed or restored automatically.

<details>
<summary>Read the legacy fixed-forwarding and SOCKS workflow (experimental)</summary>

### Legacy RustDesk first run

```sh
mise exec -- tsnet-bridge setup
mise exec -- tsnet-bridge
mise exec -- tsnet-bridge login
```

Setup requests the ID server's tailnet peer name/IP and RustDesk public key. It saves the profile without network authentication. Start runs an embedded node; login opens a private Tailscale authorization URL for explicit enrollment. This is a separate identity from any installed Tailscale app. Use `login --no-browser` to open the URL manually; do not share it.

Inherited auth keys, OAuth/workload credentials, and alternate control-plane environment settings are rejected. A different profile path uses the global `--state-dir PATH` before the command. Setup refuses to overwrite existing profiles. Stop, back up, and deliberately edit profile.json for changes.

### Experimental RustDesk forwarding profile

Default bindings:

| Local | Remote |
| --- | --- |
| TCP + UDP 127.0.0.1:32116 | hbbs:21116 |
| TCP 127.0.0.1:32115 | hbbs:21115 |
| TCP 127.0.0.1:32117 | hbbr:21117 |

Back up RustDesk's existing server and proxy settings first. Run `mise exec -- tsnet-bridge settings` and enter its ID server, relay, and public key. Leave the proxy blank, keep UDP enabled, disable WebSocket, and connect using `remote-ID/r`.

**Every endpoint using this profile must run the helper with the same loopback relay address and port.** RustDesk forwards the relay address to its peer. One-sided deployment, independently chosen ports, and mixed native-tailnet/loopback profiles are unverified. Also check hbbs relay-address rewrite settings. `/r` does not guarantee the absence of direct probes; NAT reporting and address propagation require real testing.

Ports are fixed after setup and never silently changed. `setup --relay-host <tailnet-host>` supports a separate relay server.

#### SOCKS limitations

`setup --mode socks` enables authenticated CONNECT-only SOCKS. Reveal credentials only in a private terminal with `settings --show-secrets`.

RustDesk 1.4.9 switches registration to TCP when a proxy is configured. OSS server 1.1.16 returns NOT_SUPPORT for TCP RegisterPk. **SOCKS alone cannot register the controlled endpoint in this combination.** Controller-only use also requires validation. The app-wide proxy may break updates/API calls outside the allowlist. See [sources and constraints](docs/ARCHITECTURE.md).

### Legacy operation

```sh
mise exec -- tsnet-bridge                 # Start, or show status if already running
mise exec -- tsnet-bridge status --json
mise exec -- tsnet-bridge doctor
mise exec -- tsnet-bridge reconnect       # Recreate forwarding, retain saved login
mise exec -- tsnet-bridge stop            # Stop forwarding, retain saved login
mise exec -- tsnet-bridge logout          # Log out a running node, then stop
mise exec -- tsnet-bridge run             # Foreground; Ctrl+C stops forwarding
```

`ready` only means the allowed peer and TCP server ports were reachable. RustDesk screen/control always remains separately labeled `unverified`. Temporary failures close listeners and retry with increasing intervals. Authentication, admin approval, peer policy, and port conflicts have distinct messages.

If logout fails, forwarding stops but server-side logout is explicitly reported unconfirmed. Deleting the node in the Tailscale admin console is a separate operation. Stopping the helper does not restore RustDesk settings; use the backup. Canceling a login wait leaves the background process running; use `stop` if needed.

</details>

## Security and development

Traffic is restricted to allowed current tailnet peers and ports with no OS DNS/routing fallback. Inbound sharing only targets explicit numeric loopback services. No GUI, subnet router, exit node or generic internet proxy is included.

Unix state directories/files use 0700/0600; Windows uses current-user DACLs. This is access control, not encryption or isolation between processes of the same user. Fixed forwarding cannot add SOCKS authentication; combine narrow tailnet policy with application authentication.

The repository pins Go 1.27.1 and Tailscale 1.102.5. In a source checkout:

```sh
mise install
mise exec -- go test -race ./...
mise exec -- go vet ./...
mise exec -- go build -trimpath -o bin/tsnet-bridge ./cmd/tsnet-bridge
```

On Windows use `bin/tsnet-bridge.exe`. A source build is separate from a signed release. To test it, replace `mise exec -- tsnet-bridge` in examples with that executable's path.

[Development and usability principles](docs/DEVELOPMENT_PRINCIPLES.en.md) · [Security](SECURITY.md) · [Architecture](docs/ARCHITECTURE.md) · [Verification](docs/VERIFICATION.en.md) · [Distribution](docs/DISTRIBUTION.md) · [Use cases](docs/USE_CASES.ja.md) · [Remaining acceptance and roadmap](docs/ROADMAP.ja.md)

MIT license. Distribution archives include dependency notices.
