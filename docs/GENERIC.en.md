# Named connections and time-limited sharing

This guide describes the current source, including unreleased service discovery. The published `0.2.0-alpha.1` binary has named connections but retains the peer/purpose wizard; `0.1.0-alpha.2` has only the legacy workflow. Legacy RustDesk profiles remain supported as an experimental separate workflow. [日本語](GENERIC.ja.md) · [Verification](VERIFICATION.en.md)

**[0.2.0-alpha.1 is published](https://github.com/webkaz-labs/tsnet-bridge/releases/tag/v0.2.0-alpha.1).** [Exact-source CI](https://github.com/webkaz-labs/tsnet-bridge/actions/runs/36910805666) and the [complete release/native mise-install workflow](https://github.com/webkaz-labs/tsnet-bridge/actions/runs/36911703369) passed, including Japanese/English output, OS-locale fallback and exact JSON checks on all four targets. Real enrollment and application acceptance remain incomplete. [Detailed distribution record](DISTRIBUTION.md)

The common path is **shared service → review → connect**. Only fresh authenticated responses describing active shares permitted to this node are selectable. Peer, purpose, protocol and shared port are filled in; no JSON editing, peer-ID typing or guessing the remote port is required. `connect --manual` keeps the peer/purpose/port path for ordinary Tailscale services, older bridges and known endpoints. Sharing recipients are still chosen from all current eligible peers, including peers that publish no services.

The release results above do not verify this newer source or its discovery feature. The commands below use a source-built `bin/tsnet-bridge`; on Windows use `bin/tsnet-bridge.exe`. See [current-source verification scope](VERIFICATION.en.md#service-discovery-current-source).

The design targets ordinary-user installation and operation without OS VPN, route or DNS changes. This does not remove authorization requirements for the tailnet or destination service. An additional isolated non-root Linux offline installation passed for `0.2.0-alpha.1`. Real enrollment, Windows standard-user authentication, actual tailnet ACLs and applications, phone QR authentication and OS sleep/login behavior remain unverified for the new version.

## Display language

Language selection is automatic: Japanese locales use Japanese, otherwise English is the safe fallback. The order is `LC_ALL`, `LC_MESSAGES`, then `LANG`; when unset, Windows reads the current user's UI language and macOS reads the first preferred language. Override only when desired:

```sh
bin/tsnet-bridge --lang ja help
bin/tsnet-bridge --lang en help
bin/tsnet-bridge --lang auto help
```

`TSNET_BRIDGE_LANG=ja`, `en` or `auto` is also supported. Command/flag names and complete machine JSON remain unchanged. Endpoints, identifiers and user values are not translated. Rule/group names may use Japanese letters as well as other letters/digits, hyphens and underscores. English and Japanese confirmation/edit/back/cancel inputs are accepted.

Use a UTF-8 terminal. In supported interactive terminals, Up/Down highlights one peer, purpose or service; Enter chooses it. Left/Right moves within typed text. Numbers, names and control words still work; type comma-separated peer numbers or names to share with several peers. Arrow navigation alone never confirms saving or starting. Redirected input/output keeps plain line prompts without terminal controls. Malformed UTF-8 and unsupported control sequences are rejected instead of being saved, and terminal settings are restored after prompting. Exact terminal, IME and native-platform combinations remain an acceptance check.

## First use

From a current source checkout with [mise](https://mise.jdx.dev/getting-started.html), build with the pinned toolchain, then initialize and sign in:

```sh
mise install
mise exec -- go build -trimpath -o bin/tsnet-bridge ./cmd/tsnet-bridge
bin/tsnet-bridge init
bin/tsnet-bridge login
```

On Windows, build with `-o bin/tsnet-bridge.exe` and use that executable. These are development binaries, separate from signed release artifacts. Stop at OS security warnings instead of bypassing them. To install the older published release, use the [version-pinned README procedure](../README.en.md#install-the-published-020-alpha1-release); that binary does not provide service discovery or the new discovery flags.

`init` writes an idle profile without networking. `login` starts a separate embedded node and presents the official private sign-in URL. Review the account, tailnet and node authorization yourself; never share login URLs, secrets or credentials. Existing profiles are never silently overwritten. Use `login --no-browser` for manual browser opening.

### Choose how to sign in

- `login`: open the official sign-in URL in this device's browser
- `login --qr`: display a private, locally generated QR and link; scan using a trusted phone/tablet and confirm the account, tailnet and bridge node in its browser
- `login --link` or `login --no-browser`: display the private link for manual opening

[Tailscale documents this cross-device QR flow](https://tailscale.com/docs/features/access-control/device-management/how-to/set-up-qr-code). The bridge node is enrolled, not the scanning phone. Account authentication, MFA and device approval still apply. This bridge's complete real-phone flow remains unverified.

QR generation runs entirely in memory using the same pinned Go encoder as Tailscale; no external QR service, screenshot or image file is used. QR output refuses redirected files/pipes. Do not share, record or screenshot the terminal. A terminal narrower than the required QR width receives a specific width message and link fallback instead of wrapped QR output. Enlarge the terminal, use `--qr-format large`, or fall back to the private link if scanning is difficult. On Windows, QR rendering temporarily enables supported ANSI/VT color output and restores the previous mode; if that capability cannot be enabled, the private link is offered instead. Native Windows terminal scanning remains unverified.

Waiting, connected and device-approval-pending states are distinct. The default five-minute local wait can be changed with `--timeout 10m`; it is not the server link's expiration. Rerun `login` to request the current sign-in link if it expired. Upstream may reuse its cached link; rerunning does not guarantee rotation or revocation. Cancellation stops the wait, not the node or an already displayed authorization link; use `stop` to close the node.

## Use a peer's service

```sh
bin/tsnet-bridge connect
```

1. Select a shared service with Up/Down and Enter, or type its number. The list shows the peer, purpose, TCP/UDP shared port, check time and sharing expiry
2. Accept or edit the local rule name and proposed local port
3. Review the actual local endpoint and remote service, then start

The selected remote peer, purpose, protocol and shared port stay together. At review, `e` edits only the local port, `s` or `back` returns to services, `m` switches deliberately to manual configuration and `r` changes the name. `r` at the service picker refreshes instead. `q`/`cancel` cancels without saving. Observations are usable for at most 15 seconds, never beyond sharing expiry. If an observation ages while you read the list, the same grant is rechecked when selected; an unchanged service with fresh, valid metadata needs no extra selection. Selection is rechecked before save and again before start; a changed, stopped, expired or newly denied share requires a fresh choice. If the final check fails after save, the rule remains saved but disabled. Advertised expiry is the earlier of the share TTL and a task’s current lease. Normal lease renewal can keep the same grant/service selectable after rechecking; it does not retarget the connection.

The list confirms recent sharing metadata, not application health. A peer being online, a TCP handshake or a `ready` listener cannot establish an application's success. No result does not prove the remote service is stopped. Refresh after checking that the provider started a discoverable share and allowed this bridge node. If discovery is unsupported, blocked or unavailable, use `m` or `connect --manual` for a known service.

### What the service provider needs

For a bridge-published share, the provider runs both the actual local application and a signed-in bridge node. A started, unexpired share must explicitly allow the receiving bridge node. Discovery must also be enabled: review the new interactive share preview, or deliberately add `--discoverable` when using `--confirm`. Tailnet policy must allow the discovery port and actual shared service port separately.

On the provider, after building the current source:

```sh
bin/tsnet-bridge init
bin/tsnet-bridge login
bin/tsnet-bridge share
```

Run `init` only once; skip it when a profile already exists. Start the local application with authentication before `share`, then choose the receiver, actual service port and lifetime, and review before starting. Keep the application and bridge running. The receiver refreshes `connect` after the provider starts sharing. An empty list or timeout does not establish that a bridge is missing or the peer is offline.

An ordinary service already listening on a Tailscale peer does **not** need a bridge on that peer. Use `connect --manual` with its known peer, actual service port and TCP/UDP transport; the service listener, tailnet policy and application authentication must permit access.

Same-port forwarding is preferred. Privileged or occupied local ports produce an alternative that requires confirmation; no silent renumbering or elevation occurs. Forward local ports are 1024..65535, destination service ports 1..65535. Existing RustDesk fixed-port rules remain separate.

```sh
bin/tsnet-bridge connect web-demo
bin/tsnet-bridge settings
bin/tsnet-bridge stop web-demo
```

Replace `web-demo` with your saved name. `connect NAME` and `start NAME` reuse the saved pinned peer and port; they do not claim a fresh discovery observation. A new service-picker configuration is the path that revalidates discovery before save/start. Copy the displayed service endpoint into the application, not its SOCKS field. TLS names/SNI, origin/Cookie/CORS and SSH host-key checks remain application concerns. Never disable verification. Saving with `--save-only` does not connect. Replacing a saved rule requires stopping it and explicitly selecting `--replace`. A different node with the same display name is never silently substituted for a saved peer; select the current peer again.

<details>
<summary>Manual configuration and advanced connection details</summary>

Purpose presets only suggest editable port numbers; they do not install/configure applications or choose TCP/UDP:

| Purpose | Example and suggested port |
| --- | --- |
| `web` | HTTP service, `8080` |
| `ssh` | SSH/SFTP, `22` |
| `db` | PostgreSQL example, `5432`; other databases may differ |
| `ai` | Local AI API example, `11434`; other AI services may differ |
| `custom` | Enter the actual service port |

The picker shows the selected transport. It defaults to TCP; pass `--network udp` for a UDP service. Every suggested port can be changed. Discovery instead takes the provider's actual shared port and transport, with no preset guessing. The final review distinguishes the local loopback entry point from the remote tailnet endpoint, or the shared tailnet listener from its local loopback target.

`connect --manual` selects a current peer, purpose and actual port. Existing `--peer`, `--purpose`, `--port` or `--network` flags also choose this path. Purpose presets are suggestions, not evidence that the service exists. Here `e` edits ports/lifetime, `p`/`back` returns to peers, `u` changes purpose and `r` changes the name. Typing mistakes can be retried in place.

```sh
# Replace demo with a peer currently shown in your list
bin/tsnet-bridge connect --peer demo --purpose web --port 8080 --name web-demo
bin/tsnet-bridge connect --peer demo --purpose ssh --listen-port 2222 --name ssh-demo
bin/tsnet-bridge connect --peer demo --purpose custom --network udp --port 9000 --name udp-demo
```

Input troubleshooting: redirected streams or `TERM=dumb` use plain number/name/CSV line input. If `TEA_TRACE` is set, unset that debugging variable before retrying; interactive input refuses it to prevent input logs, and does not save prompt history.

</details>

## Share a local service

Start the application with appropriate authentication, then:

```sh
bin/tsnet-bridge share
bin/tsnet-bridge shares
bin/tsnet-bridge stop api-demo
bin/tsnet-bridge stop-shares
```

Select explicitly allowed current peers, service and lifetime. Only an exact numeric `127.0.0.1` or `::1` target is allowed. TCP and UDP are supported; use `--network udp` for UDP or `--loopback ::1` for an IPv6 local service. LAN/public IPs, arbitrary hostnames and blanket sharing are rejected. The receiving app connects to the provider bridge node's displayed tailnet address, not its own localhost. Tailnet ACLs and per-rule pinned-peer authorization both apply.

A new interactive share enables discovery metadata in the full confirmation preview. Only its allowed peers can receive purpose, protocol, shared port, expiry, an opaque identifier and the explicit `application: unverified` marker. Rule names, local target addresses/ports, owners, paths and free-form descriptions are not announced. Use `share --no-discovery` when configuring a new share to disable this metadata. Existing scripts using `--confirm` keep discovery off unless they explicitly add `--discoverable`; the two discovery flags cannot be combined. Saved rules retain their setting, and older profiles with no `discoverable` field remain off. To change a saved share's setting, stop it and deliberately replace its configuration with `--replace`.

Discovery reads only TCP `54543` at `/.well-known/tsnet-bridge/services/v1`, on the provider's embedded tailnet node while a discoverable share is active. Tailnet ACLs must permit that discovery port as well as the separately selected TCP/UDP service port. Allowing one does not allow the other. Blocked discovery does not remove the manual connection path. There is no central registry, public/LAN discovery listener or application-port scan. A read checks at most 128 peers with four requests in parallel, two seconds per peer and an eight-second total budget; a limited scan is reported. Results are not persisted.

A concurrent change to reviewed rules/groups rejects startup and requires review again. Changing an active TTL/lease requires stop and a new reviewed start. A verified identical active scope/owner/lifetime only displays current status, without another confirmation or mutation, keeping the original expiry.

Saved shares require a fresh explicit lifetime, for example `share --ttl 30m api-demo`. TTL is 1 second..24 hours. Stop/expiry closes the listener and existing streams/datagram mappings. It does not retract data or cancel an already running remote job. Local applications see the bridge's loopback connection: do not expose a sensitive API relying on “localhost means trusted” instead of authentication.

## Groups, status and task cleanup

```sh
bin/tsnet-bridge group save dev web-demo ssh-demo
bin/tsnet-bridge group start dev
bin/tsnet-bridge group stop dev
bin/tsnet-bridge status --json
bin/tsnet-bridge doctor
bin/tsnet-bridge wait-ready --timeout 30s web-demo
bin/tsnet-bridge task --rules web-demo --timeout 30s -- curl http://127.0.0.1:8080/
```

Group startup rolls back only newly started members if a member fails. Shares within groups require `--ttl` and review. Rule status includes direction, endpoints, selected identities, owner, reason code, checked time and expiry.

| State | Next action |
| --- | --- |
| `idle` | The node is running; select the needed connection |
| `needs-login` / `approval-required` | Complete official sign-in or node approval |
| `ready` | The selected listener is ready; verify the actual application separately |
| `partial` | Inspect usable and unavailable rules individually |
| `recovering` | Rechecking the same identity, authorization and lifetime; traffic may close |
| `failed` | Review the reason, peer and ports, then start deliberately |
| `stopped` / `expired` | Finished; recovery, reconnect and process restart do not resume it |

Recovery never falls back to the OS network or replays application requests.

`task` generates an owner, starts only its rules, waits, executes the command directly without a shell, and cleans up on exit/error/cancellation. A 30-second lease renewed every 10 seconds expires after caller death. It cannot reuse or stop another owner's active rule. Ownership is a cleanup boundary, not isolation from another process of the same OS user. Remote job scheduling, authorization, cancellation and results remain the application's responsibility. TCP forwarding does not turn stdio MCP into HTTP MCP.

Machine JSON excludes login URLs and SOCKS credentials but includes node names and endpoints that may be identifying. Review and redact before sharing. Use the same global `--state-dir PATH` before each command when selecting a separate profile.

`stop` without names stops the entire node while retaining login. `reconnect` recreates only currently requested listeners. Stopped/expired rules never restart after recovery or process restart.

## Migration, local export/import, optional startup

- `migrate` previews v1 RustDesk fixed forwarding into four disabled rules and a group. Stop the node and use `migrate --confirm --id-peer-id ID [--relay-peer-id ID]` after identifying current peers. The exact old file is retained privately. SOCKS stays supported in v1 and is not lossily converted
- `export` previews data and privacy implications. `export --output FILE --confirm` writes a private disabled local copy. It excludes credentials/login state but includes node names, peer IDs and service endpoints. Review before sharing; no upload occurs
- `import FILE` previews; `import --confirm FILE` applies while stopped. Existing replacement additionally requires `--replace`. Imported rules stay disabled and an existing node identity is retained
- `autostart enable` previews. `autostart enable --apply` registers an idle v2 node for a future user login; `autostart disable --apply` removes registration. Linux uses user systemd, macOS LaunchAgents, Windows a least-privilege interactive logon task. No immediate node start, forwarding or sharing is included. Registration and current process stop are separate. OS login behavior is not yet real-device verified; review the executable path after updating/moving binaries

No GUI, subnet/exit routing, Funnel, arbitrary destination relay, automatic certificate issuance, OS-wide sandboxing, iOS bridge binary or application authentication proxy is included. See the [roadmap](ROADMAP.ja.md) for remaining acceptance work.

## Evidence and remaining acceptance

The published `0.2.0-alpha.1` source `236bd8e217f213a93b667f3d8d0509811d4f5464` passed native race, real IPC and package checks on all four targets, including actual OS-locale fallback, Japanese/English output and exact machine-JSON equality in packaged binaries. Actual public-release mise installation passed the same offline checks on all four targets, recorded separately in [distribution](DISTRIBUTION.md). Local/mocked tests and distribution checks do not replace real tailnet enrollment, ACL/application acceptance, phone QR, OS login/sleep or RustDesk screen/input tests. See the [verification report](VERIFICATION.en.md).
