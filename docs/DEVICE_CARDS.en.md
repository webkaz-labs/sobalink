# Public device cards

[日本語](DEVICE_CARDS.ja.md) · [Next convenience patch](CONVENIENCE_PLAN.en.md)

**This is the read-only API foundation for easier pairing.** A card contains a
public key, an explicitly chosen alias and optional address hints. It does not
pair devices, enable a network, update an endpoint or grant application access.
Dedicated Web/CLI pairing controls and local QR image reading are not part of
this foundation.

A public key is not a secret, but it can identify the same device across copies.
An address hint can reveal network details. Choose an alias deliberately, leave
address hints omitted when unnecessary, and exchange cards through a suitable
private channel. Export never copies the configured hostname automatically.

## Export and inspect

The authenticated local command API supports:

- `device-card.export`: `{mode,name,includeEndpointHint?,qr?}`. Mode is exactly
  `lan` or `direct-lan`; the alias is required. Optional booleans default to false
- `device-card.inspect`: `{card,expectedMode}`. Works without a configured local
  identity; the expected mode must be explicitly selected

Export reads an already-created identity. It never initializes or repairs one.
Missing or recovery-uncertain identity returns an error. An explicitly requested
address hint that is unavailable returns `device_card_hint_unavailable`.

Results contain only the documented public fields and explicit
`verification:"unverified"`, `freshness:"unknown"` labels. Export additionally
returns the encoded `card` and optionally a locally generated QR bitmap.
Inspection returns `contentDigest`, the SHA-256 of the complete canonical card
text after permitted outer whitespace removal. This digest identifies content,
not a person, device owner or proof of key possession. Pure reads are not retained
in the command retry-result cache, so a retry reads current saved identity data.

For mixed configurations, choose a component's `lan` or `direct-lan` identity.
The mixed logical identity is not a component card. Tailnet uses its existing
sign-in flow and has no card in this format.

## Version 1 format

Text and QR carry `soba-card1.` followed by unpadded base64url of canonical UTF-8
JSON. Required ordered fields are `version:1`, `mode`, `publicKey`, and `name`.
Optional `endpoint` is direct-LAN-only; optional `relay` is LAN-only and contains
`address` then `certificateSHA256`. Both optional hints cannot appear together.
The alias is 1–80 UTF-8 bytes, without outer whitespace, controls, bidi controls
or line separators. Keys and pins are nonzero, lowercase, 64-character hex.

Direct endpoints use canonical numeric private/loopback addresses and ports
1024–65535, excluding 54543–54545. Relay validation checks numeric endpoint and
pin shape only. A relay hint can describe an external or loopback address; that
is never permission to contact it, trust its pin or select it as a relay.

Versioned wire bounds are 768 decoded bytes, 1,035 encoded card bytes and 1,040
input bytes before trimming outer ASCII space/tab/CR/LF. Unknown fields, duplicate
or case-variant keys, null/empty optional values, wrong-mode fields, noncanonical
base64/JSON/endpoints, invalid Unicode and trailing data are rejected. Canonical
JSON is the exact `encoding/json` output for the fixed ordered wire structure;
its escaping rules are covered by golden tests. These protocol maxima are
separate from the existing configurable finite local command/resource budgets.

Cards contain no pairing token, PSK, private key, tunnel key, route permission,
allowed prefix, interface name or group membership. They are reusable contact
hints with no authorization lifetime. Invitation expiry and authenticated proof
remain in the existing invitation protocol.

## Safety and acceptance

Inspection never resolves DNS, probes an address, opens a URL, changes a pin,
creates identity or revives a revoked pair. Address freshness and availability
remain unknown even after a successful scan. A changed address on a card is not
an authenticated endpoint update. Future pairing integration must review the
card and copy only recipient key/alias into an inert draft before the separate
existing invitation action.

Automated codec/Core tests cover bounded parsing, canonicality, secret exclusion,
missing/recovery state, mode separation, read freshness and absence of side
effects. QR encoding is tested within the accepted wire bounds. This does not
establish physical-device scanning, camera cleanup, end-to-end pairing usability
or completion of the other convenience areas.
