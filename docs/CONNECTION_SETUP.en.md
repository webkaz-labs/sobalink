# Nearby connection setup

[日本語](CONNECTION_SETUP.ja.md) · [Security boundaries](../SECURITY.md)

This source implementation provides a short-lived, explicitly scoped setup session for the LAN backend. Nearby discovery and QR/contact exchange are unverified hints. Pairing requires selection on both devices, comparison of the complete plan fingerprint through the other device's QR or copied text, and a separate local approval on each device. Pairing never enables message/file trust, autosave, service sharing or arbitrary port access.

This is source-development work. Deterministic tests, isolated loopback acceptance, packaged-binary validation and real-device validation are separate gates; one does not stand in for another. This document does not claim real-device or released-binary acceptance.

## Normal flow

1. Open connection setup on both devices. Choose an explicit display name and one current local network scope. Approve the short-lived advertisement and exact bootstrap listener. Hosting a relay requires an additional explicit host-role choice and a distinct high port.
2. Select the other device on both sides. If multicast discovery is unavailable, copy or scan each device's current setup contact. Reading a contact does not approve it or start an arbitrary scan.
3. Review the connection plan. It shows both identities, the selected host, exact relay address and certificate pin, and each device's chosen scope. The LAN backend uses the strict allowed-LAN-destinations policy for the local selected prefix. This permits the backend's UDP/TCP traffic within that prefix; it is not physical-interface confinement or a whole-process egress sandbox.
4. Scan or copy the other device's complete comparison text and check that it exactly matches. If necessary, compare all 64 hexadecimal fingerprint characters. Do not compare only a prefix, a few digits, a color, or a display name. Approve locally on both devices.
5. Wait for mutual authenticated protocol confirmation. Configured, paired and connected are separate states. Approve application trust separately when message/file offers are wanted.

A QR shown by an untrusted third party, or a comparison value merely echoed through the same unverified network exchange, does not authenticate the intended device. Use the other device's screen or a separately authenticated exchange.

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

The product does not change OS firewall rules, routes, network security, router settings or privileged services. QR contact exchange does not bypass a network restriction. If no permitted reachable host exists, use an already-approved working connection or advanced network setup.

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
