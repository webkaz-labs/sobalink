# Optional proxy and diagnostics

[日本語](PROXY_DIAGNOSTICS.ja.md) · [Generic guide](GENERIC.en.md) · [Security](../SECURITY.md)

The advanced SOCKS5 entry point and explicit TCP diagnostics are available through `soba` and the authenticated local command API. They use the same current peer identity and transport policy as service connections. New logic is covered by mock and in-memory transport tests; native socket, actual-device and application compatibility checks are separate acceptance gates.

## Review an optional proxy

A proxy is optional. For a single service, the normal `soba connect` workflow is usually simpler.

```sh
soba proxy preview --name example-proxy --peer PEER_ID --ports 443,8443
soba proxy preview --name example-proxy --peer PEER_ID --ports 443 --ttl 72h
```

The preview returns the complete normalized `scope`, its `revision`, target peer names and the loopback endpoint. Review the name, selected network, exact peer IDs and TCP ports, listener address and lifetime. Preview opens no listener. The default is `until-stopped`; a positive whole-second `--ttl` selects a finite lifetime and supports periods longer than 24 hours. A reviewed scope never outlives the process.

Prepare private start input with the returned `scope`, copy `revision` into `expectedRevision`, and supply the application's explicit `username` and `password` fields. Each credential must contain 1–255 bytes. Use an existing private file or a pipe; do not put credentials in command arguments, shared configuration, logs or source control. The CLI checks that an opened credential file belongs only to the current OS user (0600-style permissions on Unix, an owner-only protected ACL on Windows).

```sh
soba proxy start --json-file PRIVATE_INPUT_FILE
soba proxy start --stdin < PRIVATE_INPUT_FILE
soba proxy list
soba proxy stop PROXY_ID
```

Multiple peers are supported by `soba proxy preview --json-file SCOPE_FILE` or `--stdin`. The JSON input is an object containing `scope`; its `targets` is an array of objects with exactly `peerId` and `port`. The logical `sharePeers` policy counts distinct peer identities, while the finite local command envelope bounds target metadata. Active proxy listeners and TCP flows share the normal resource controller's listener, connection, policy and peer budgets. Lowered admission settings apply to new scopes and connections.

The local command API has `proxy.preview`, `proxy.start`, `proxy.list` and `proxy.stop`. It never returns credentials in ordinary status, review, list or start responses. CLI dry runs omit credentials, including unknown extra fields. Ordinary `proxy start` is ephemeral and never saves credentials. Explicit [saved proxy profiles](STARTUP.en.md) provide reviewed save/generate, private-file reveal and opt-in future launch through a separate protected store. Portable profile exports still exclude credentials and startup approvals.

The listener uses exactly `127.0.0.1` or `::1`, with username/password authentication and TCP CONNECT only. BIND and UDP ASSOCIATE are unsupported. Dialing uses the selected userspace backend, current approved peer identity and exact allowed TCP ports. Unrecognized names never reach OS DNS or an OS connection fallback. Management ports cannot be targets. An active proxy listener cannot be exposed through a service share, including a mapped share; a port already covered by a share cannot become a proxy listener.

Network loss, peer revocation, identity reassignment, expiry, logout, listener failure or process exit stops the proxy. An ephemeral proxy restart requires fresh review and runtime credentials. A separately saved approval can support explicit restart, recovery within its original expiry, or an opted-in future process launch; changed scope or revoked identity requires renewed approval. Stopping a proxy closes tracked connections; it does not cancel work an application has already accepted.

## Inspect failures and check one TCP target

```sh
soba doctor
soba doctor --service SERVICE_ID --tcp
soba doctor --service SERVICE_ID --tcp --port 8443
```

Without `--tcp`, doctor shows current service observations and the last runtime failure. With `--tcp`, it opens and closes one connection to an active service's approved target. A range requires one explicit effective port. Forward checks use the selected userspace backend and current target identity. Share checks use only that share's approved numeric loopback target, including a reviewed local-port mapping.

A successful result says `tcp_reachable`, transport `reachable`, and application `unverified`. It proves TCP acceptance only. The probe sends no application data and does not establish application health, compatibility, TLS, host-key verification or successful login. A generic UDP probe cannot establish those facts either and is rejected.

Results include a stable code, UTC check time and Japanese/English next actions. Local service views retain `lastFailure` after a successful check, transport recovery or stop, until the process ends or the definition is deleted. This added failure history is process-local; the earlier implementation exposed only its current diagnostic result. It is not a durable cross-restart diagnostic journal. Raw target errors and local destinations are not included in peer discovery. Diagnostics neither renew a grant nor change its saved definition.
