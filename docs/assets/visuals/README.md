# Documentation diagrams

[Documentation index](../../README.en.md) · [日本語](../../README.md)

Eleven distinct explanatory themes, each with Japanese/English, light/dark and wide/narrow variants: 88 SVG assets. Display variants are not additional concepts. The diagrams explain relationships and decisions; they are neither screenshots nor test evidence.

## One question per theme

| Theme | Question and document |
| --- | --- |
| `connection` | Where does the application's traffic enter and which shared service can it reach? [User guide](../../GENERIC.en.md) |
| `permissions` | Which receiving and sharing approvals are independent? [Security](../../../SECURITY.md) |
| `saved-and-running` | What survives when a service starts and stops? [Saved services](../../SAVED_SERVICES.en.md) |
| `delivery-evidence` | Why can canceled work still have saved evidence? [Durable files](../../DURABLE_OUTBOX.en.md) |
| `network-topologies` | What reachability does each connection mode require? [Mixed connections](../../MIXED_CONNECTIONS.en.md) |
| `pairing-exchange` | Which device selects, requests and approves, and what changes for full verification? [Connection setup](../../CONNECTION_SETUP.en.md) |
| `durable-save-gates` | How do text and files differ before receiver saving? [Durable text](../../DURABLE_TEXT.en.md) |
| `restart-state` | What returns in saved-network versus offline startup? [Lifecycle](../../LIFECYCLE.en.md) |
| `api-boundary` | Why can a peer not use the local browser management API? [API](../../../web/API.md) |
| `resource-gates` | Why does logical unlimited not provide unlimited resources? [Capacity](../../CAPACITY.en.md) |
| `port-observation` | Why is an observed free port not reserved for a later start? [Port proposals](../../PORT_PROPOSALS.en.md) |

The document-specific Mermaid figures cover branches, mappings and operation contracts where a compact relationship diagram is clearer than another illustration. Reference tables remain preferable for exact commands, fields, historical evidence and comparison data. Reuse a theme only when it answers the same question; link to the relevant explanation instead of adding the same generic picture to every guide.

## Embed and read

Use `picture` with narrow sources below 960px for the overview, and below 840px for the other themes. Include dark variants, descriptive `alt`, and a visible short text equivalent. Hosts that ignore `picture` still receive the fallback image and prose. The host controls the content column and may ignore media hints; link or open the narrow asset when a constrained renderer shrinks a wide diagram.

Solid paths show the illustrated relationship or operation, not confirmed live connectivity. Dashed paths distinguish conditional relationships, uncertain evidence or reference lines; labels carry the meaning without depending on color. Neither a check mark nor an arrow proves application success. Ordinary Tailnet service targets do not require a second sobalink instance; the connection illustration specifically shows sobalink-to-sobalink sharing.

## Regenerate and check

Run from this directory with Python 3:

```sh
python3 build.py
python3 explainers.py
python3 semantic.py
XDG_CACHE_HOME=/tmp python3 render.py
XDG_CACHE_HOME=/tmp python3 verify.py
```

Generation uses only the Python standard library. `semantic.py` contains shared inert SVG primitives and the document-specific layouts. `build.py` produces the device overview; `explainers.py` produces the three existing explanatory themes. Commit generators and changed SVG variants together. Generator changes need full validation; adding a new theme also requires explicit CI image-name registration rather than a wildcard allowance.

Optional PNG rendering uses librsvg and Cairo; contact sheets also need Pillow. Review sheets and PNGs remain untracked. The verifier measures each SVG's text bounds, explicit label widths encoded in inert IDs, and text-to-text collisions. These checks do not detect every arrow/label collision: inspect all language/theme/layout variants as rendered images. Mermaid sources use inert, plain labels without HTML, custom styles or executable configuration.

Review in three passes: implementation meaning and structure; visual hierarchy and narrow readability; first-read comprehension in the actual document and rendered output. Record concrete changes and any remaining limits. Image rendering does not establish screen-reader behavior, browser zoom acceptance, physical-device networking, application compatibility or OS-login/suspend qualification.
