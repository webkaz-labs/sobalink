# Nearby connection setup

[日本語](CONNECTION_SETUP.ja.md) · [Security boundaries](../SECURITY.md)

This source implementation provides a short-lived, explicitly scoped setup session for the LAN backend. Nearby discovery and QR/contact exchange are unverified hints. Pairing requires selection on both devices, comparison of the complete plan fingerprint through the other device's QR or copied text, and a separate local approval on each device. Pairing never enables message/file trust, autosave, service sharing or arbitrary port access.

This is source-development work. Deterministic tests, isolated loopback acceptance, packaged-binary validation and real-device validation are separate gates; one does not stand in for another. This document does not claim real-device or released-binary acceptance.

## Normal flow

1. Open connection setup on both devices. Choose an explicit display name and one current local network scope. Approve the short-lived advertisement and exact bootstrap listener. Choose explicitly whether this device may relay the connection or the other device must relay it; neither role is preselected. For a new connection, allow at least one device to relay, using a distinct high port. Host eligibility and reachability are checked afterward, not assumed from this choice. A device already connected through another host must keep that approved host.
2. Select the other device on both sides. If multicast discovery is unavailable, copy or scan each device's current setup contact. Reading a contact does not approve it or start an arbitrary scan.
3. Review the connection plan. It shows both identities, the selected host, exact relay address and certificate pin, and each device's chosen scope. The LAN backend uses the strict allowed-LAN-destinations policy for the local selected prefix. This permits the backend's UDP/TCP traffic within that prefix; it is not physical-interface confinement or a whole-process egress sandbox.
4. Scan or copy the other device's complete comparison text and check that it exactly matches. If necessary, compare all 64 hexadecimal fingerprint characters. Do not compare only a prefix, a few digits, a color, or a display name. Approve locally on both devices.
5. Wait for mutual authenticated protocol confirmation. Configured, paired and connected are separate states. Approve application trust separately when message/file offers are wanted.

A QR shown by an untrusted third party, or a comparison value merely echoed through the same unverified network exchange, does not authenticate the intended device. Use the other device's screen or a separately authenticated exchange.

## Short CLI path and corrections

For a fresh profile, run on both devices:

```sh
soba init
soba start --offline --background
soba nearby
```

If using another profile, keep the same `--state-dir DIR` on every command. If the profile is already running and requires an offline restart, explicitly stop it first; active transfers and connections will stop. Do not replace or clear the profile.

Enter an alias, review the network scope, choose `y` to permit this device to relay or `n` to use the other device, then separately approve the displayed listeners. Empty or invalid role input asks again and grants nothing. At the final review, `p` edits setup/relay ports, `b` edits the inputs, and `q` cancels. Port edits are local-only and show the new exact endpoints before new consent. Ports must be distinct, 1024–65535, excluding 54543–54545 and the management port. Port availability is checked at start; there is no silent port remapping.

In the active CLI wizard, `r` or Enter refreshes the original session, `m` accepts the other device's contact text, `v` compares and approves a displayed plan, and `x` exits while setup keeps running until its original deadline. `q` or end-of-input at the active action prompt requests cancellation and waits for cleanup; `q` inside contact or fingerprint entry returns to the action prompt. Interrupting the process or losing a reply does not prove cleanup. Check `soba nearby status` (with `--session ID` when known) in the same profile; never blindly repeat `start`.

If no permitted, reachable relay is found, the cause is not inferred. If both devices declined, wait for confirmed cleanup and choose “Review relay permission and try again” in the Web UI, or accept the fresh-review question in the CLI. On both devices, start new sessions with new alias/scope/role consent and let one permitted device relay. No prior verification or approval carries over. Only an uncommitted, clean no-host failure exposes this retry after all temporary owners have joined; recovery, saved-network and anchor conflicts retain their separate review requirements. Existing connections keep their approved host.

Web setup displays QR codes and reads QR text from a local PNG upload; it has no camera scanner or QR download button. Copying complete contact/fingerprint text always remains available. The CLI wizard uses text. Optional terminal QR output is available through `nearby status --qr`, `nearby contact --session ID --qr`, and `nearby verification --session ID --qr`; the CLI imports and exports text, not PNGs.

After both approvals, the saved LAN connection and chosen relay can continue beyond the 120-second setup. Use the Web network control “Stop sobalink” or `soba stop` for the same profile to stop current connections and listeners. This also stops active transfers and does not revoke saved pairs. Cancel setup only closes temporary setup resources.

## Exact permission and lifecycle

- One session lasts at most 120 seconds. Candidates have their own shorter observation lifetime. Before bilateral approval, a changed or expired candidate invalidates its old selection. After both approvals, only the exact verified transcript remains pinned until its original deadline.
- Discovery advertises only the chosen alias, random session ID, protocol/capability and bootstrap endpoint. No permanent identity, invitation, filename, service list or OS hostname is advertised.
- Bootstrap accepts only the locally selected peer session and exact selected source/address scope. Receiving a request cannot auto-select a peer or trigger a callback to an unselected endpoint.
- Both keys and session IDs, aliases, public scopes, fresh random per-session local consent revisions, allowed roles, endpoints, exact relay pin, plan revision and expiry are bound into the complete transcript. Local interface name/hardware bindings stay private.
- Before out-of-band comparison, encrypted proofs establish possession of self-asserted selected keys. They do not authenticate a human display name. The full SHA-256 comparison avoids introducing an unreviewed short-code protocol susceptible to attacker-controlled key grinding.
- A fixed session-order coordinator makes one bounded host decision from authenticated directional observations. An existing compatible host is preferred; otherwise session order breaks ties. A failed direction stays unavailable or unknown. It does not prove a firewall, sleep state or network cause.
- Repeated approvals and protocol messages use the same plan and invitation. Changing the peer, scope, role, endpoint or certificate requires cancellation and a fresh session with new approvals.
- After mutual connection evidence, discovery stops promptly. The exact-session bootstrap listener can remain until the original 120-second deadline for final receipt exchange; it cannot issue another invitation or extend the deadline. Cancelling explicitly ends that remaining listener too.
- A cancelled or expired setup closes its discovery and bootstrap resources. An unused invitation is cancelled. If pairing may already have committed, the result requires recovery; cancellation does not claim rollback. Failure to confirm resource closure retains a recovery fence.
- Successful approval saves the LAN network and pair for later use. The promoted backend and any selected host listener continue beyond the short setup session until stopped; stopping does not revoke the saved pair.
- Connecting another device is a deliberate fresh action. Close any remaining setup receipt listener and wait for joined cleanup before `nextAction: start_new_setup`. The previous result and active LAN backend remain; the new form/session requires fresh scope, role and verification approvals.
- A newly started LAN backend is owned by setup until mutual success. Cancelling it never closes an independently running backend that setup borrowed.

## Existing connections and process restart

Existing authenticated peer connections remain available through ordinary peer actions; opening setup is not required to use them. The wizard can borrow an already-running LAN host or relay client only when its strict single-prefix destination policy and exact existing relay anchor match the reviewed plan. A relay client must not request a new host role, and its selected peer must offer that same already-anchored local host. Two clients pointing to an external relay do not invent a new host.

Incompatible saved peers, prepared routes or unreviewed startup/application authority require existing-network review. They are not reset or reactivated under discovery consent. A different process-scoped backend, failed-close owner or changed network identity produces a stop/restart or recovery state before new setup networking. Stop and reopen the same profile offline, then create a new setup session. Old fingerprints and approvals never survive restart. No seamless TCP/backend migration is promised.

The product does not change OS firewall rules, routes, network security, router settings or privileged services. QR contact exchange does not bypass a network restriction. If a fresh host-role review still cannot establish a permitted, reachable relay, use an already-approved working connection or review advanced network setup without broadening permissions silently.

## Local command contract

Local Web and CLI commands share the Core contract:

- `setup.scopes {}`: read-only current local choices with opaque `scopeId`
- `setup.start`: explicit alias, `scopeId`, bootstrap port, host choice/relay port and consent
- `setup.status`: current public state, contact text/QR and exact plan comparison; never an invitation
- `setup.select`: select one current candidate ID
- `setup.manual`: select bounded canonical contact text for the same confirmation flow
- `setup.approve`: exact session ID, plan revision, complete fingerprint and explicit local verification
- `setup.cancel`: stop the exact session and report cancellation versus recovery

Contact text uses `sobalink-setup-contact-v1:` followed by canonical base64url JSON. Comparison text uses `sobalink-setup-verify-v1:` followed by the full SHA-256 digest. Neither format is an application-trust grant or a reusable capability.

After an ambiguous start response, `setup.status {}` only reports the current process session. It does not prove that a particular client created or exclusively owns that session. Reconciliation must reject the previous session ID, check the exact requested public contact/scope, clear old approvals, and require review of the current full plan before approval. Another authenticated local client may also manage the same Core.
