# Source-built relay traffic observation

[日本語](RELAY_TRAFFIC_MEASUREMENT.ja.md) · [Published-binary offline resources](RESOURCE_MEASUREMENT.en.md)

This workflow measures stock Tailcat/DERP transport with a synthetic TCP echo
workload. It builds source `8e6cbb00d60757f701d7d453adb92590cc5d2544` with test-only
instrumentation and OS UDP transport compiled out. It does not measure the
published executable, sobalink's file/message protocol, discovery, or Web UI
polling. A completed hosted run and its aggregate artifact are required before
reporting measured results.

## Fixture and phases

The job independently checks out that exact source, adds three test-only files,
and verifies that tracked production source remains unchanged. Stock pairing,
admission, WireGuard/Tailcat transport, and pinned TLS remain intact. Every
listener and the byte proxy's one fixed target are numeric loopback endpoints.
There is no external relay, persistent node, account login, OS network change,
or release-asset mutation. Synthetic fixture keys stay in memory.

A TCP proxy forwards TLS without terminating or modifying it. Successful stream
writes are counted once at the proxy boundary, separately toward and away from
the relay, across both peer-to-relay legs. Counting the read and write of the
same copied stream would incorrectly double-count that leg.

The bounded phases are:

1. Pairing, relay startup, and opening one authenticated overlay TCP echo stream
2. 60 seconds of idle transport
3. 1 MiB, 16 MiB, and 64 MiB synthetic payloads echoed over that same stream
4. 1,000 synthetic 256-byte TCP request/echo frames
5. At least 130 seconds of small echoes over the existing application connection,
   requiring actual relay reconnection and admission across a two-minute lease
6. 30 seconds of idle transport after the lease phase

Every echoed payload must have the expected byte count and streaming SHA-256.
Application redial/retry cannot hide loss of the TCP stream. Bulk I/O deadlines
are 60 seconds; small frames and lease echoes are bounded too. Payload phases
include 200 milliseconds of settling in the observation window; payload time
is separate. The test has a nine-minute context, ten-minute process deadline,
and bounded teardown. Cancellation stops the one owned fixture process, which
owns every relay, peer, and echo socket.

## Counter meaning

P is successfully delivered, digest-verified application payload across both
directions: an echoed N-byte payload contributes 2N. R is the measured encrypted
stream bytes across both directions of both relay legs during the same window.
Each useful byte normally crosses two relay legs, so its baseline is 2P.

The report includes P, R, R/P, R/(2P), and R−2P. R/P includes the two-leg topology;
R−2P is excess encrypted stream traffic at this boundary. Negative excess is a
measurement failure/boundary mismatch and is never clamped to zero. Idle phases
have null payload ratios and report bytes/second. Startup, idle, payload, and
lease/reconnection costs stay separate. Estimated idle traffic is not subtracted.

Included: TLS handshakes/records, DERP framing, WireGuard/overlay traffic, and
synthetic payload. Excluded: outer TCP/IP/Ethernet headers, ACK-only packets,
kernel retransmissions, ICMP diagnostics, direct UDP, and the relay's separate
local admission HTTP connection. This is not packet capture or billable WAN
traffic. Timing includes loopback proxy/fixture costs, not real WAN/NAT behavior.

Full sobalink file-transfer framing, message storage/acknowledgments, peer/service
discovery, and browser polling are not exercised. These results must not be
called end-to-end file-transfer overhead or complete application overhead.

## Evidence and privacy

Artifact `source-built-relay-only-linux-amd64-traffic` contains one JSON file:
source SHA, fixture/test-binary SHA-256, build tags, native toolchain, phase
counters/durations, reconnections, and cleanup status. Evidence is explicitly
`source_built_relay_only`, workload is `tailcat_tcp_echo`, and packet bytes are
null. The runner accepts only the exact allowlisted aggregate schema and discards
other output. Endpoints, paths, keys, invitations, payload contents, and raw logs
are not uploaded. Incomplete or invalid evidence cannot pass.

Existing cross-platform CI, stock relay acceptance, the release workflow, and
published assets remain unchanged. This job does not replace their results or
write to the repository or trusted main caches.
