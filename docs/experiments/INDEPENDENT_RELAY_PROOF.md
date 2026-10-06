# Independent relay mechanism experiment

This is an opt-in, test-only mechanism proof. It does not enable a product feature, third-host discovery, host election, or a production relay-grant protocol.

An existing authenticated pair can carry application TCP through an independent relay when that host explicitly admits the exact public role keys and both endpoints approve the existing pair-bound route. A reachable participant should normally host first: the existing host setup and recipient-bound pairing already supply that pair's admission. Automatic takeover by the other participant is not currently implemented.

## What the fixture checks

- A and B complete the actual pairing protocol over their original relay
- Independent C has separate device/relay identities and no A/B pair records; separate A/C and B/C pairs alone do not grant A/B role admission
- C rejects the A/B role over pinned TLS/DERP before an explicit experimental local grant
- The grant binds exact public device/role keys, pair generation, C identity, candidate endpoint/pin/scope, expiry and a reviewed revision. C receives no A/B private key, transport capability, PSK or invitation
- After existing pair-bound route approval, the original relay is closed and UDP transport is compiled out. Actual TCP echo must cross C; original identities and pair roles remain unchanged
- Cancelled, stale, expired, substituted and unrelated grants are rejected. Revocation blocks a new admission from a formerly granted role; whole-C shutdown ends the experiment's existing transport

The fixture uses real loopback sockets with supported synthetic interface metadata. It runs in one process, with independent Node and relay identities. It does not establish physical-device, NIC, suspend, WAN/NAT or application compatibility.

## Source and recorded verification

The final fixture SHA-256 is `2f86ae9c677aeccd7369e5d726617af8167708b959c6421f4fca7ffeffb0e329`. It is byte-identical to the previously reviewed isolation revision. The pre-isolation fixture SHA-256 was `03b6387b9b8d2722cf7bebb1f301315ee77fa690ce055fad2bc03d367d81adc6`.

Recorded historical Linux amd64 results were three tests repeated ten times under race (17.780 seconds), an independent single race run (2.544 seconds), and tagged vet. The isolation-only revision then passed a single race run (3.314 seconds) and tagged vet; independent review checked its tag, ordering and command changes without another run. These are summarized historical results, not a fresh validation of this checkpoint. Complete original stdout and the original complete Git trees are not included in this source record. No new test execution is claimed here.

The source is isolated behind both `lanlink_integration` and `lanlink_independent_relay_experiment`. Eligibility checks precede installing the synthetic interface getter. Its cleanup resets the getter to the default; it cannot restore an arbitrary preinstalled getter. Use a dedicated fresh, focused test process, not a package-wide coexistence assumption.

## Reproduce explicitly

Use the repository's pinned Go toolchain and prepare/verify its existing engine adaptation first. From the repository root:

```sh
go run ./cmd/prepare-engine
SOBALINK_RUN_LAN_INTEGRATION=1 go test -p=2 -race -count=1 -v -timeout=150s \
  -tags=lanlink_integration,lanlink_independent_relay_experiment,ts_omit_udptransport,ts_omit_portmapper,ts_omit_captiveportal,ts_omit_useproxy \
  -run '^TestIndependentRelay(AdmissionContractPrototype|CurrentNeighborAdmissionPrototype|CommunicationPrototype)$' ./internal/lanlink
go vet -p=2 \
  -tags=lanlink_integration,lanlink_independent_relay_experiment,ts_omit_udptransport,ts_omit_portmapper,ts_omit_captiveportal,ts_omit_useproxy \
  ./internal/lanlink
```

The dedicated process clears proxy overrides only for its synthetic numeric-loopback fixture. It does not change OS routes, firewall/router settings, or live profiles. Ordinary product builds do not include this file.

## Remaining product requirements

A production third host still needs authenticated, versioned, bounded grant exchange; explicit host willingness; durable approval/revocation and recovery; pair-change notification; and configurable finite resource budgets. Discovering a host or adding candidate metadata does not grant admission. The finite expiry chosen by this fixture is not a proposed product lifetime limit.

The revocation result concerns new admission. Existing-session termination here uses whole-host shutdown and does not prove selective immediate eviction of one grant on a multi-pair host. Global election is not needed merely to prefer among already running, explicitly admitted candidates. Tailscale remains the normal outside-LAN path.
