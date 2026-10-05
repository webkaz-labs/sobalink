# Experimental LAN destination guards

[日本語](README.ja.md)

This is an isolated research fixture, not a product feature or a release change.
A passing job does **not** prove physical-LAN-only operation or whole-process
no-egress. It establishes selected write-boundary behavior in a patched copy of
`tailscale.com v1.104.0`, including real loopback UDP sockets on native Linux CI.

## What runs

1. A socket-free policy model tests selected IPv4/ULA prefixes, rejected global
   and other-private destinations, invalid/empty policy, endpoint changes,
   batching, exact relay TCP admission, concurrent policy changes and ambiguous
   routes. The route-proof callback is a fake, not an OS route implementation.
2. `prepare_engine.py` verifies the SHA-256 of upstream `rebinding_conn.go`,
   copies the full pinned module to a fresh directory and inserts a small
   experiment hook into both lower write/retry boundaries. It removes upstream
   magicsock test files from that copy and supplies only this bounded fixture.
   All production magicsock code and its real dependencies compile; this is not
   a source extraction. The upstream regression suite is **not** run.
3. Full-package fake writers test single writes, disco transport path, UDP
   netcheck, batch and lazyEndpoint cookie-style replies; both fast batch and
   fallback, nil-policy denial, and rebind/retry revocation are covered.
4. Opt-in native CI uses unprivileged listeners on `127.0.0.1` (allowed) and
   `127.0.0.2` (denied). Seven allowed datagrams must arrive. Five denied
   entrypoints must return the policy error and the denied receiver must reach
   its bounded empty-read timeout. A policy revocation must reject a later send.
   The kernel-backed batching adapter is used when available and reported.
5. Removing the guard must make the fake entrypoint test fail. The negative
   control does not send to public destinations or run a native socket test.

The experiment never changes the application go.mod, production constructors,
release workflows or artifacts. No namespace, route, firewall, capability,
security-setting, account or device changes are needed. Native traffic is only
loopback; global/private test addresses occur only in fake-writer tests.

## Reproduce

Use Go 1.27.1 and Python 3. From this directory:

```sh
go test -race ./...
go vet ./...
go mod download tailscale.com@v1.104.0
python prepare_engine.py \
  --source "$(go env GOMODCACHE)/tailscale.com@v1.104.0" \
  --destination /tmp/lan-engine-experiment
cd /tmp/lan-engine-experiment
go test -race -run '^TestLANExperimentFake' ./wgengine/magicsock
```

The destination must be new. `LAN_GUARD_REAL_SOCKETS=1` explicitly opts into
`TestLANExperimentNativeLoopback`. The workflow compiles the test binary and
uses `run_native.py` to enable that fixture and generate an allowlisted JSON
aggregate. It publishes no raw runtime output, machine inventory, addresses
from the host, user settings or credentials.

## Why two lower boundaries

- `Conn.Send`'s `lazyEndpoint` cookie branch bypasses `sendUDP` and
  `sendUDPBatch`, calling `RebindingUDPConn.WriteWireGuardBatchTo` directly
- Fast batching calls `batching.Conn.WriteBatchTo` without the ordinary single
  writer, while fallback batching invokes the single writer per packet
- Both retry loops must recheck policy after a connection replacement

The experiment hook is intentionally incomplete as a production API: it is not
wired by constructors, missing policy denies all writes, and policy admission
is not atomic with changes in OS routing. Runtime integration requires a reviewed
policy lifetime, synchronization, error classification and rebind design.

## Unproven boundaries and next gates

- Physical same-link behavior and overlapping VPN routes: CIDR membership cannot
  prove which interface the OS will use; route lookup alone has a TOCTOU gap
- Full peer pairing and encrypted application transfer across two LAN devices
- Actual disco protocol construction/handshake-load generation: the fixture
  invokes their transport and cookie-style send entrypoints directly
- Separate DERP TCP, bootstrap, HTTPS/ICMP diagnostics and Linux raw-disco
  self-test paths; guarding magicsock UDP does not cover these
- Whole-process syscall or every-interface packet capture; the fixture observes
  only its selected receivers and writer calls over the bounded scenario
- Native macOS, Windows, ARM64 and real network transitions
- Complete upstream/application regression suites

A stronger routed fixture would need separately reviewed interface enforcement,
preconfigured test topology or explicitly approved disposable-runner network
changes. Passing this smaller fixture must never be relabeled as that proof.
