# Public device cards

[日本語](DEVICE_CARDS.ja.md) · [Next convenience patch](CONVENIENCE_PLAN.en.md)

**Read-only Web and CLI controls make public keys easier to exchange.** A card
contains a public key, an explicitly chosen alias and optional address hints.
It does not pair devices, enable a network, update an endpoint or grant
application access. The Web UI can copy a reviewed key and alias into an inert
recipient draft; invitation creation stays separate. QR image reading is not
included.

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

## Web export and recipient review

Open **Set up network**, select **LAN** or **Direct LAN**, then choose
**Exchange device cards**.

1. To export, enter **Alias to share** explicitly. It starts empty and never
   inherits the configured device name. Export requires an existing identity;
   the card controls cannot create or repair one. Web export always omits
   address hints. Optionally select **Include a local QR code**, then choose
   **Export public card**. Copy the complete text or save the `.txt` file with
   the separate buttons. If clipboard access fails, select and copy the text
   manually
2. For a recipient, paste the `soba-card1.` text or open a small UTF-8 text file.
   Both routes enforce the 1,040-byte input bound. A file selection only fills
   the card field; choose **Review public card** to inspect it through Core.
   Wrong-mode, malformed, oversized and private-invitation input is rejected
   without repeating it in errors. Use the other mode's setup for its cards
3. Check the explicit **Identity: unverified** and **Address freshness: unknown**
   labels. Confirm the public key and alias through a separate trusted channel.
   Any address or certificate shown is an unverified hint, with no link or
   automatic network action. Cancel to leave the recipient draft untouched
4. Choose **Use recipient in draft** only after reviewing. This copies only the
   public key and alias into the existing recipient draft. Review that draft
   in the invitation section and use the existing invitation action separately
   when the network is ready. Existing invitations and application trust remain
   separate

Input edits, closing the card panel or setup, changing mode, identity or network
scope, and stale/locked state discard the review. Delayed responses cannot
restore it. Text and file input remain available without a camera or QR image
decoder. There is no browser storage for card drafts.

Component tests cover both languages, bounded parsing, strict QR bitmap and
quiet-zone checks, cancellation, stale responses and key/name-only draft writes.
The dedicated browser specification uses the actual local Core with isolated
synthetic fixtures: LAN export/QR/text-file delivery and recipient review, plus
direct-LAN review before identity setup. It does not simulate an existing
direct-LAN identity or establish direct-LAN export, native clipboard behavior,
physical QR scanning or real-device pairing. A listed browser test is not a
passing browser run; check the acceptance report for execution results.

## CLI export and inspection

```sh
soba card export --mode lan --name 'Example device'
soba card export --mode direct-lan --name 'Example device' --include-endpoint --qr
soba card inspect --mode lan --file ./device-card.txt
soba card inspect --mode lan --stdin --json < ./device-card.txt
soba --offline card inspect --mode lan --file ./device-card.txt --json
```

Export and normal inspection use the running local Core; select the same
`--state-dir` used to start it. Export requires an existing, healthy identity.
The CLI never creates or repairs identity, configures a network, issues an
invitation or grants trust. Missing identity and unavailable explicitly requested
hints return errors. `--offline card export` is unavailable.

Explicit `--offline card inspect` uses the pure card codec without contacting
Core, resolving the default profile, reading local state or writing anything.
Both inspection paths require an explicit mode and raw card text from exactly
one regular file or piped stdin. They bound the read to 1,040 bytes before parsing
and reject malformed input without echoing it. They do not accept card text as a
command-line argument, a private invitation or an export JSON envelope. Copy
only the `soba-card1.` line from human export output into the input file. Scripts
can use the `card` field of `card export --json`.

Human output automatically follows Japanese/English locale selection; `--locale`
can override it. `--json` returns the same public API fields in every locale;
`--json-errors` gives stable, input-safe errors. `--qr` adds a locally generated
terminal QR, or the API bitmap with `--json`. A narrow or unsupported terminal
keeps the complete card text available and explains the fallback. `--dry-run`
validates local inputs and previews the read request without contacting Core.

The CLI is covered by mocked read-only API, codec, bounded-input, cancellation,
locale, JSON and terminal bitmap tests. This is not real-device QR scanning or
end-to-end pairing acceptance. Invitation commands and their flags are unchanged.

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
an authenticated endpoint update. The Web controls review the card and copy
only the recipient key/alias into an inert draft before the separate existing
invitation action.

Automated codec/Core tests cover bounded parsing, canonicality, secret exclusion,
missing/recovery state, mode separation, read freshness and absence of side
effects. QR encoding is tested within the accepted wire bounds. This does not
establish physical-device scanning, camera cleanup, end-to-end pairing usability
or completion of the other convenience areas.
