# Verification status

Updated 2026-10-01. This record distinguishes implementation, local automated checks, native CI, and real application acceptance.

## Local checks

- TCP forwarding, half-close, timeout/cancellation, shutdown, and loopback binding: automated local tests
- Authenticated SOCKS CONNECT, failed authentication, malformed input, BIND/UDP rejection: automated local tests
- Persistent per-source UDP mappings, asynchronous replies, idle expiry, limits, cancellation and policy revocation: automated local tests
- Strict profile parsing, private persistence, single-instance lock, allowlisted peer resolution, netstack-only dispatch: automated tests
- Transport race suite repeated 20 times successfully; coverage 91.8% at the review checkpoint
- Unix-domain IPC and service-IPC lifecycle tests cannot execute in the development cloud runtime because its socket API returns operation not permitted. This is not marked as a pass; native CI runs the tests without weakening them

- Focused app lifecycle tests (request-context isolation, revoked peer closure, stopped-listener recovery, partial bind rollback, logout failure) passed, repeated five times under the race detector
- Full `go vet ./...` passed; Windows binary and ACL/IPC test executables cross-compiled successfully
- All five targets cross-packaged, Linux packaged execution passed, and two Linux packaging runs produced identical complete output digests
- Packslip manifest/checksum generation passed; actual signature round-trip remains pending CI

## Native CI

The workflow defines Linux amd64 and arm64, macOS arm64 and amd64, and Windows amd64 jobs. Each runs race tests and vet, then builds deterministic archives, checks packaged binaries, and generates distribution metadata. Until the linked exact-commit runs finish successfully, this definition is a test plan rather than a passed result. Follow [Actions](https://github.com/webkaz-labs/tsnet-bridge/actions).

Windows hosted CI runs as administrator. Passing it cannot establish standard-user runtime/enrollment behavior. Cross-compilation also cannot establish native runtime behavior.

## Required before a supported release

- [ ] Windows standard-user clean install, interactive enrollment, state save/reuse, stop and logout
- [ ] Linux nonroot real enrollment, state reuse, shutdown, and recovery on amd64/arm64
- [ ] macOS real enrollment and state lifecycle on supported architectures
- [ ] Real RustDesk Mac-to-Windows and Windows-to-Mac registration, screen, input, disconnect and reconnect
- [ ] Linux interoperability in each intended controller/controlled role
- [ ] Idle UDP registration and later incoming connection notifications
- [ ] Relay address propagation, common local relay port, server rewrite settings, and mixed-profile rejection/documentation
- [ ] Direct Tailscale and DERP paths; blocked UDP and restrictive upstream network conditions
- [ ] Sleep/resume, network change, outages, node expiry/revocation, ACL refusal, process/application restart
- [ ] Native mise/Packslip installation, version pinning, upgrade and rollback using the actual signed public manifest
- [ ] Final dependency/security review, verification of upstream private logging behavior and decision about OS signing/notarization

No real tailnet credentials are required by CI. Real enrollment creates persistent external access and must be explicitly authorized before testing. Do not replace an unchecked item with a mock result.
