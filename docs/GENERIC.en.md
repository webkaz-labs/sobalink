# Named connections and time-limited sharing

For `0.2.0-alpha.1`; these features are absent from `0.1.0-alpha.2`. Legacy RustDesk profiles remain an experimental separate workflow. [日本語](GENERIC.ja.md) · [Verification](VERIFICATION.en.md)

**[0.2.0-alpha.1 is published](https://github.com/webkaz-labs/tsnet-bridge/releases/tag/v0.2.0-alpha.1).** [Exact-source CI](https://github.com/webkaz-labs/tsnet-bridge/actions/runs/36910805666) and the [complete release/native mise-install workflow](https://github.com/webkaz-labs/tsnet-bridge/actions/runs/36911703369) passed, including Japanese/English output, OS-locale fallback and exact JSON checks on all four targets. Real enrollment and application acceptance remain incomplete. [Detailed distribution record](DISTRIBUTION.md)

The common path is **peer → purpose → review**. Current peers are selected by number; web, SSH/SFTP, database and AI API presets suggest ports. Use `custom`, `--port`, `--listen-port`, `--network udp` or `--loopback ::1` when needed. No JSON editing or peer-ID typing is required for normal setup.

The design targets ordinary-user installation and operation without OS VPN, route or DNS changes. This does not remove authorization requirements for the tailnet or destination service. An additional isolated non-root Linux offline installation passed for `0.2.0-alpha.1`. Real enrollment, Windows standard-user authentication, actual tailnet ACLs and applications, phone QR authentication and OS sleep/login behavior remain unverified for the new version.

## Display language

Language selection is automatic: Japanese locales use Japanese, otherwise English is the safe fallback. The order is `LC_ALL`, `LC_MESSAGES`, then `LANG`; when unset, Windows reads the current user's UI language and macOS reads the first preferred language. Override only when desired:

```sh
mise exec -- tsnet-bridge --lang ja help
mise exec -- tsnet-bridge --lang en help
mise exec -- tsnet-bridge --lang auto help
```

`TSNET_BRIDGE_LANG=ja`, `en` or `auto` is also supported. Command/flag names and complete machine JSON remain unchanged. Endpoints, identifiers and user values are not translated. Rule/group names may use Japanese letters as well as other letters/digits, hyphens and underscores. English and Japanese confirmation/edit/back/cancel inputs are accepted.

## First use

Confirm `v0.2.0-alpha.1` is marked Pre-release on [Releases](https://github.com/webkaz-labs/tsnet-bridge/releases), with `packslip.sigstore.json` and the target archive present. Stop if publication or assets are missing. Targets are Linux x64/ARM64, macOS ARM64 and Windows x64; the verification version of mise is 2026.9.18:

```sh
mise use -g "packslip:github.com/webkaz-labs/tsnet-bridge[prerelease=true]@0.2.0-alpha.1"
mise exec -- tsnet-bridge version
mise exec -- tsnet-bridge init
mise exec -- tsnet-bridge login
```

Require `tsnet-bridge 0.2.0-alpha.1` from `version`. Keep the complete version pin, never `latest`. Exact pins are exempt from mise 2026.9.18's release-age discovery filter; signature, identity and digest verification remain active. Stop at OS security warnings instead of bypassing them.

`init` writes an idle profile without networking. `login` starts a separate embedded node and presents the official private sign-in URL. Review the account, tailnet and node authorization yourself; never share login URLs, secrets or credentials. Existing profiles are never silently overwritten. Use `login --no-browser` for manual browser opening.

### Choose how to sign in

- `login`: open the official sign-in URL in this device's browser
- `login --qr`: display a private, locally generated QR and link; scan using a trusted phone/tablet and confirm the account, tailnet and bridge node in its browser
- `login --link` or `login --no-browser`: display the private link for manual opening

[Tailscale documents this cross-device QR flow](https://tailscale.com/docs/features/access-control/device-management/how-to/set-up-qr-code). The bridge node is enrolled, not the scanning phone. Account authentication, MFA and device approval still apply. This bridge's complete real-phone flow remains unverified.

QR generation runs entirely in memory using the same pinned Go encoder as Tailscale; no external QR service, screenshot or image file is used. QR output refuses redirected files/pipes. Do not share, record or screenshot the terminal. A terminal narrower than the required QR width receives a specific width message and link fallback instead of wrapped QR output. Enlarge the terminal, use `--qr-format large`, or fall back to the private link if scanning is difficult.

Waiting, connected and device-approval-pending states are distinct. The default five-minute local wait can be changed with `--timeout 10m`; it is not the server link's expiration. Rerun `login` to request the current sign-in link if it expired. Upstream may reuse its cached link; rerunning does not guarantee rotation or revocation. Cancellation stops the wait, not the node or an already displayed authorization link; use `stop` to close the node.

## Use a peer's service

```sh
mise exec -- tsnet-bridge connect
```

Choose a peer and purpose, accept or change the suggested service port and rule name, and review the connection. Typing mistakes in peer, purpose, service/local ports, name or lifetime can be corrected in place. At review, `e` edits ports/lifetime, `p` or `back` returns to peers, `u` changes purpose and `r` changes the name. Every edit returns to the full review; `q`/`cancel` cancels at any prompt without saving. Same-port forwarding is preferred. Privileged or occupied local ports produce an alternative that requires confirmation; no silent renumbering or elevation occurs. Forward local ports are 1024..65535, destination service ports 1..65535. Existing RustDesk fixed-port rules remain separate.

```sh
mise exec -- tsnet-bridge connect web-demo
mise exec -- tsnet-bridge settings
mise exec -- tsnet-bridge stop web-demo
```

Replace `web-demo` with your saved name. Copy the displayed service endpoint into the application, not its SOCKS field. TLS names/SNI, origin/Cookie/CORS and SSH host-key checks remain application concerns. Never disable verification. Saving with `--save-only` does not connect. Replacing a saved rule requires stopping it and explicitly selecting `--replace`. A different node with the same display name is never silently substituted for a saved peer; select the current peer again.

<details>
<summary>Specify advanced connection details</summary>

```sh
# Replace demo with a peer currently shown in your list
mise exec -- tsnet-bridge connect --peer demo --purpose web --port 8080 --name web-demo
mise exec -- tsnet-bridge connect --peer demo --purpose ssh --listen-port 2222 --name ssh-demo
mise exec -- tsnet-bridge connect --peer demo --purpose custom --network udp --port 9000 --name udp-demo
```

</details>

## Share a local service

Start the application with appropriate authentication, then:

```sh
mise exec -- tsnet-bridge share
mise exec -- tsnet-bridge shares
mise exec -- tsnet-bridge stop api-demo
mise exec -- tsnet-bridge stop-shares
```

Select explicitly allowed current peers, service and lifetime. Only an exact numeric `127.0.0.1` or `::1` target is allowed. TCP and UDP are supported; use `--network udp` for UDP or `--loopback ::1` for an IPv6 local service. LAN/public IPs, arbitrary hostnames and blanket sharing are rejected. The receiving app connects to the provider bridge node's displayed tailnet address, not its own localhost. Tailnet ACLs and per-rule pinned-peer authorization both apply.

A concurrent change to reviewed rules/groups rejects startup and requires review again. Changing an active TTL/lease requires stop and a new reviewed start. A verified identical active scope/owner/lifetime only displays current status, without another confirmation or mutation, keeping the original expiry.

Saved shares require a fresh explicit lifetime, for example `share --ttl 30m api-demo`. TTL is 1 second..24 hours. Stop/expiry closes the listener and existing streams/datagram mappings. It does not retract data or cancel an already running remote job. Local applications see the bridge's loopback connection: do not expose a sensitive API relying on “localhost means trusted” instead of authentication.

## Groups, status and task cleanup

```sh
mise exec -- tsnet-bridge group save dev web-demo ssh-demo
mise exec -- tsnet-bridge group start dev
mise exec -- tsnet-bridge group stop dev
mise exec -- tsnet-bridge status --json
mise exec -- tsnet-bridge doctor
mise exec -- tsnet-bridge wait-ready --timeout 30s web-demo
mise exec -- tsnet-bridge task --rules web-demo --timeout 30s -- curl http://127.0.0.1:8080/
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

Source `236bd8e217f213a93b667f3d8d0509811d4f5464` passed native race, real IPC and package checks on all four targets, including actual OS-locale fallback, Japanese/English output and exact machine-JSON equality in packaged binaries. Actual public-release mise installation passed the same offline checks on all four targets, recorded separately in [distribution](DISTRIBUTION.md). Local/mocked tests and distribution checks do not replace real tailnet enrollment, ACL/application acceptance, phone QR, OS login/sleep or RustDesk screen/input tests. See the [verification report](VERIFICATION.en.md).
