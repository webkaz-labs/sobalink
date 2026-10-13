# macOS DNS-SD discovery boundary

The macOS adapter uses the system DNS-SD service on the explicitly selected interface. It produces untrusted setup hints. A selected hint never grants trust or permission to connect; the existing numeric bootstrap endpoint, current remote session, full fingerprint comparison and both peers' approval remain mandatory.

## Scope and privacy

The adapter registers four bounded records for the current random session: PTR, SRV, TXT and one A record. Instance and host names derive only from that session, never the OS hostname. The A record contains only the selected IPv4 address. Queries follow only the exact service and validated session-derived instance/host names. No default hostname/domain, interface Any, automatic rename, P2P inclusion, generic hostname resolver or wider fallback is used.

DNSService selects an interface, not an IPv4-only packet transport. The advertised/bootstrap address stays within the approved IPv4 prefix, but the system API does not expose the original UDP source, source port, destination, IP TTL, packet family or complete datagram. It does not attest that the daemon used the selected IPv4 source. Linux and Windows retain actual packet admission checks; their bounded assembler accepts independently scoped advertised addresses and separate DNS records without treating packet source as endpoint ownership.

Permission denial is an unavailable result. Tests and the product do not accept Local Network permission prompts or change OS/network settings automatically. The user can use manual setup when the selected discovery mode is unavailable.

## Lifetime and cancellation

All exact queries remain active while their hints are displayed, for at most the immutable 120-second setup owner lifetime. These are OS-cached hints, not evidence of fresh wire traffic or reachability. Removal, zero TTL, conflicting/malformed updates, query failure, detected interface/address change and cancellation invalidate them. An invalidated observation cannot regain its old candidate ID. The selected binding is checked before operations/admission and by an independent one-second watcher during callback silence. The authenticated remote session remains the final freshness check.

There are at most 64 session bundles, 193 queries, four registrations, 512 setup/query calls, 512 callbacks, 16 parsed callbacks per second and 512 KiB admitted record bytes per owner. One C ring holds at most 64 observations; Go delivery is unbuffered. Exhaustion or malformed state updates stop admission rather than discard a possible invalidation. These are application work limits, not a daemon wire-packet budget.

The provider is owned before any potentially blocking daemon IPC. Calls and native callbacks use one serial dispatch queue. C retains no Go pointers or handles. Cancellation fences admission, wakes readers and deallocates the shared reference on that queue. Storage is released only after the queue and every in-flight call/read worker join. A two-second public close timeout retains unresolved ownership; it never proves physical shutdown. Mandatory teardown still runs after an admission budget is exhausted.

Deallocation terminates application registrations/queries, but does not acknowledge remote cache eviction, queued daemon packets or delivery of goodbye records. Canceled Core sessions and bootstrap listeners reject selection/commit regardless of cached hints elsewhere.

## Build and evidence

The adapter requires Darwin+cgo, the system SDK's dns_sd.h and dispatch headers, and the automatically linked libSystem umbrella that reexports DNS-SD and dispatch. It uses no package-specific linker flag or standalone libdns_sd SDK stub. There is no third-party FFI. A Darwin build without cgo explicitly reports unavailable. Native package provenance must record the compiler/SDK/system inputs and target cgo mode; Linux cross-compilation of the no-cgo fallback does not verify this adapter.

Native tests have independent closed opt-ins:

- `SOBALINK_DNSSD_LOCAL_ONLY=1`: two API registrations, reciprocal exact record callbacks and withdrawal, using the LocalOnly sentinel. This is same-host API evidence.
- `SOBALINK_DNSSD_SELECTED_LOOPBACK=1`: two actual providers/Sessions on the exact loopback interface/address. Unsupported loopback stays unavailable.
- `SOBALINK_DNSSD_SELECTED_INTERFACE=1`: a separately authorized hosted-only synthetic test on exactly one existing private IPv4 multicast-capable scope. Missing/ambiguous scope stays unavailable; there is no automatic fallback from the other tests.
- `SOBALINK_DNSSD_RAW_RECEIVE=1`: on the same strictly selected kind of hosted scope, an exact-group receive-only socket verifies OS advertisements through the raw metadata checks/record assembler. It never sends a raw packet or claims the reverse direction/cross-device delivery.

Each publishes a separate `NEARBY_DNSSD_CAPABILITY` marker after bounded owner cleanup. API success does not become raw-packet or physical cross-device proof. All use random session names and generic aliases, no bootstrap connection or OS/network changes. Local tests of backend seams prove ownership/contracts only; native execution and opposite-platform physical-network acceptance must be reported separately.

## Primary API contract

The implementation follows Apple's [pinned DNS-SD header](https://github.com/apple-oss-distributions/mDNSResponder/blob/mDNSResponder-2600.140.3/mDNSShared/dns_sd.h): QueryRecord callback cache TTL limitations, explicit interface selection, RegisterRecord conflict ownership, shared-reference deallocation and same-serial-queue teardown. No API record is wrapped in fabricated packet metadata.
