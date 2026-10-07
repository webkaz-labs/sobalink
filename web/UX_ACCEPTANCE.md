# GUI workflow acceptance

This log separates the current UI refinement from earlier workflow checkpoints. Earlier test counts and open integration notes below describe their dated candidates, not the released product or this later UI change.

Apply the [development and usability principles](../docs/DEVELOPMENT_PRINCIPLES.en.md) ([日本語](../docs/DEVELOPMENT_PRINCIPLES.ja.md)). This document records workflow effort, evidence, and remaining acceptance work rather than duplicating those principles.

## UI refinement — 2026-10-03

The isolated UI work starts from the exact released `8e6cbb00d60757f701d7d453adb92590cc5d2544` source tree. It changes presentation, frontend regressions and the Japanese/English Web guide. Backend contracts, transport, storage and authorization logic are unchanged.

| Screen family | Refinement | Evidence boundary |
| --- | --- | --- |
| Header, device list, device details | Compact navigation and identity rows; one search/filter scope; independent presence and stored permission | DOM tests cover filter/reset and permission state; real pixel/focus inspection remains required |
| Network graph | Measured curved ports; compact peer lanes; selected information outside the canvas; no inferred Internet, OS or device form | Repeated selection and semantic graph regressions pass; synthetic Direct/Relay cases do not establish production route telemetry |
| Services and saved definitions | Keep endpoint/state/lifetime and active Stop visible; disclose secondary management and diagnostics | Copy/edit/delete journeys now open the actual disclosure; existing Core permissions and revision checks are preserved |
| Setup, login, Tailnet and LAN | Shared compact form rhythm, unchanged review/cancel paths, grouped settings | Form/component tests pass; real enrollment, pairing and relay activation are separate |
| Messages, files, receiving and trust | Denser content rows; Tailcat missing response is not called offline; existing send/permission gates retained | DOM tests cover retained drafts and independent observation/pause states; delivery and native pickers are separate |
| Preferences, capacity, startup and proxies | Topic rows and bounded dialog body; persistent heading/close; safe final action area | Formal tests add 375×844, 390×844 and 844×390 focus/input/cancel coverage; actual execution is pending |

The full integrated DOM suite passed 460 tests, type checking and production build passed, eight fixture/evidence safety tests passed, and two exact-lock builds produced three byte-identical assets. The formal Playwright suite enumerates 53 tests. Enumeration is not execution: the local Chromium launch is blocked by an OS socket restriction. Existing private-preview visual checks use a synthetic adapter and do not replace production Go HTTP/CSRF/browser acceptance. Native CI and release verification are not results of this UI-only checkpoint.

The visual review uses relevant accessibility, hierarchy, responsive layout and form guidance from the official [UI UX Pro Max reference](https://github.com/nextlevelbuilder/ui-ux-pro-max-skill/blob/main/.claude/skills/ui-ux-pro-max/references/quick-reference.md). No external installer or design-search script was run. The product retains its own style and repository principles.

### Hosted visual and interaction checks

The final presentation was inspected with a synthetic, private preview adapter. Default fixtures use one Tailnet backend and Unknown paths; a separate Tailcat fixture is internally consistent, and hypothetical reported-path cases are explicitly simulated. These checks do not validate production command execution.

| Actual combination | Result |
| --- | --- |
| Japanese, 3/6/12 peers, each at 375×844, 390×844, 1440×900 and an 844×390 frame | No horizontal overflow; measured connector-port offset 0 px |
| English, 12 peers, at the same four sizes | Same geometry result |
| Japanese/English, 3 peers, 1180×757 | Node switching, close/focus restoration and filter-clears-selection passed |
| Capacity form, 13 forward controls at each mobile/landscape size | Controls remained visible; 12 reverse steps also checked at 375 px |
| Service form, 14 reachable controls at 375 px | No viewport or sticky-footer occlusion; final labels and action area visually inspected |

This is not the full Cartesian product of languages, counts and sizes. The 844 px frame had an 829 px document client width after its scrollbar. List, topology, details, services, exchange/receive offer, preferences, capacity and service forms were visually inspected, including a desktop dark graph. Saved services, client/RustDesk entry, startup guidance and advanced proxy review were checked through DOM/interaction in the synthetic adapter. Actual authentication/QR, native file chooser/drop/paste, OS actions and live network probes were not exercised. The embedding frame's focus boundary prevents accepting complete modal focus trapping; the formal top-level Go-backed Playwright cases remain required.

### Topology evidence contract

| State | Current source can establish | UI boundary |
| --- | --- | --- |
| Peer presence | Tailnet's presence report or a fresh authenticated discovery reply | Tailcat without a reply is unconfirmed, not definitely offline |
| sobalink | Fresh supported service-discovery response | False does not mean not installed; it also covers stale, unavailable, unsupported or limited observations |
| Permission | Stored local trust and pause state | Remote acceptance is unknown; service access has a separate gate |
| Services | Scoped local listeners/rules and remote advertised metadata | A listener or grant is not application health or traffic |
| Route | Production currently emits Unknown | Direct/Relay artwork is conditional on explicit reported values; Relay is not synonymous with Internet |
| Selected relay | Explicit endpoint configuration | Configuration/readiness is not the peer's actual route |
| Network mode | One selected Tailnet or Tailcat backend | No simultaneous-backend or automatic-switching claim |
| Phone, tablet, OS, RTT | Not provided by the current API | Use neutral device artwork; do not infer from names or addresses |

Future route or device-type rendering needs independently sourced metadata with provenance and freshness. This UI change does not add those backend fields. Test fixtures with hypothetical reported routes are labeled synthetic and do not change production capabilities.

## Historical evidence and integration boundaries — 2026-10-02

| Evidence | Current result | Limits |
| --- | --- | --- |
| Read-only source audit | Service forms, saved configuration handling, attachment collection, receiving controls, LAN setup, navigation, and their tests inspected. | This audit edited only this document and did not execute product tests or operate a browser. |
| Current UI suite | 220 tests and strict TypeScript/build passed with the offline Stop correction. Seven Playwright fixture/report privacy tests passed, and 23 named browser tests were discovered without execution. The earlier two exact-lock builds produced three byte-identical output files. | DOM and helper tests use controlled responses; test discovery is not browser execution. Record the final revision and final suite result after integration; these counts are checkpoints, not claims about later edits. |
| Hosted global navigation and illustrated graph | The deployed `db8` preview was actually inspected at 1180 px in light and dark themes. Direct Devices from conversation, accurate titles, diagram/details, Escape, and Back passed. | This is current navigation/graph browser evidence. It does not cover the newer forms or prove real network behavior. |
| Earlier hosted screenshots | Four older 1180 px graph/list/conversation/details PNGs were visually inspected during the first audit. | Superseded for current navigation by the deployed `db8` inspection. Their “after” filenames do not establish coverage of later code. |
| Deterministic browser acceptance | Playwright Test now owns named production-HTTP fixture tests, following [Browser acceptance](BROWSER_ACCEPTANCE.md). The migrated suite awaits execution against its exact integrated revision. | The earlier Go-backed Playwright smoke passed for `1c5c195a`; all 14 PNGs were inspected. Captured regions were readable, but some modal/details bottoms were outside the captures. The earlier agent-browser attempt stopped before authentication and proves no journeys. Agent-browser is optional exploration. |
| Matching backend integration | Guided-host core `4a3a662` and core `321da1e` for full `service.config`/revision-aware replacement and atomic optional-field `peer.autosave` must be integrated with this UI. | The UI worktree deliberately starts from an older frozen core. Its local backend files are not proof that the planned contract is absent from the final product. New form behavior remains conditional on the matching integrated backend. |
| Real devices | Open; not performed by this audit. | Enrollment, pairing, actual file delivery, native IME/clipboard/folder selection, direct/relay paths, application compatibility, installed builds, OS login, and suspend/resume are not accepted from DOM or preview results. |

## Normal-path effort

Counts are source-derived control activations, not timed usability measurements. A button, tab, radio choice, or select choice counts as one activation; text entry is counted separately as required fields. Field focus, keystrokes, scrolling, external authentication, and native file-picker navigation are excluded. Existing defaults are kept, the application is unlocked, and there are no errors, conflicts, or missing prerequisites unless noted. Additional recipients or non-default options add one activation each. Counts start at the screen named in each row.

| Goal and starting screen | Normal path | Required text fields | App activations | Acceptance status |
| --- | --- | --- | --- | --- |
| Open the device list from conversation | Devices | 0 | 1 | Hosted `db8` passed; DOM covered. |
| Inspect a device from global navigation | Devices → device row | 0 | 2 | Hosted `db8` list/details and Escape/Back passed. No permission mutation. |
| Inspect the network from conversation | Network → device node | 0 | 2 | Hosted `db8` diagram/details passed. Reported paths remain separate from active transfers. |
| Add a Tailnet device from the workspace, initially unconfigured | Add device → Activate prefilled Tailnet → Sign in → Continue sign-in | 0 with valid prefilled name | 4, plus external sign-in | NetworkDialog DOM integration checks passed; real enrollment open. |
| Host initial LAN relay from the workspace | Add device → LAN → choose exact address → Review → Start | 0 with prefilled name/port | 5 | Eligible addresses load automatically; no implicit address selection. DOM tested; new browser/real-device flow open. |
| Host when the address list is unavailable | Add device → LAN → Enter a LAN address manually → exact private IP → Review → Start | 1 with prefilled name/port | 5 | Same private-IPv4/ULA classes as the list; no wildcard, loopback, public address, or first-interface fallback. DOM regression passed; browser and real-device checks remain open. |
| Create/copy a LAN invitation with configured relay and identity | Add device → enter recipient public ID and name → Create invitation → Copy invitation | 2 | 3 when Invite is the current tab; +1 otherwise | DOM tested for exact recipient, expiry, copy fallback, cancellation, and late acknowledgement. Real exchange open. |
| Join an invitation from an unconfigured workspace | Add device → LAN → Create identity if needed → Join tab → paste invitation → Review → Activate and join | 1 | 6 with new identity, 5 with existing identity | Copying the local public ID adds 1; delivery of that ID and invitation is outside these counts. DOM tested; browser/real-device open. |
| Join with LAN already ready and identity available | Add device → Join tab → paste invitation → Review → Pair with this device | 1 | 4; 3 if Join is already selected | Review is required before the separate pairing action. Pairing remains separate from communication trust and automatic receiving. DOM and synthetic-response browser regressions cover the UI; real pairing acceptance remains open. |
| Allow communication from a paired device conversation | Allow communication | 0 | 1 | Exact peer permission only; DOM tested. |
| Connect manually from a selected device conversation | Connect → enter ports → Start | 1; 2 if a high local start is required | 2 | Name and TCP prefilled; outbound connections default to until-stopped. Local ports below 1024 rejected before submit. DOM tested; new browser open. |
| Connect to an advertised service from conversation | Connect → select advertised service → Start | 0; 1 for a required high local start | 3 | Exact service ID/protocol/ports retained. Missing/changed advertised target blocks submit. DOM tested; new browser open. |
| Share ports from conversation | Details → Share → enter ports → Start | 1 | 3 | Current recipient preselected, discovery off, reserved ports excluded, lifetime reviewed. DOM tested; real scoped inbound use open. |
| Copy a rule from an open details panel | Copy settings → review → Start | 0 unless changing settings or resolving a port conflict | 2; +1 for legacy network review if required | Full authoritative config, unique new name, no replacement fields. DOM tested; matching backend and browser acceptance required. |
| Edit/restart an inactive rule from open details | Edit and start → review/change → Apply and start | 0 for unchanged restart | 2; +1 for legacy network review if required | Exact rule ID and revision supplied; active or stale edit blocked. DOM tested; matching backend and browser acceptance required. |
| Edit an active rule from open details | Stop → wait for inactive state → Edit and start → Apply and start | 0 unless changing settings | 3 | Stop is explicit; no silent replacement of an active rule. New integrated browser acceptance open. |
| Send selected files/folder from conversation | Attach or Folder → native picker → review → Send batch | 0; 1 file/folder selection | 2, plus native picker | Additions accumulate; Remove costs 1 per entry. DOM tested for aggregate limits and exact upload manifest. New browser open. |
| Send a pasted image from conversation | Paste → review → Send batch | 0; 1 paste operation | 1 | Paste does not send. Repeated clipboard names receive distinct reviewed names; original bytes are preserved. Native clipboard acceptance open. |
| Accept an offered batch using its displayed default | Accept batch | 0 | 1 | Explicit receive action; actual saved-file acceptance remains open. |
| Accept into another folder with a default configured | Change folder → type destination → Accept batch | 1 | 2 | Per-batch override only; no global default or autosave mutation. DOM tested; new browser open. |
| Accept without a configured default | Type destination → Accept batch | 1 | 1 | Accept disabled while empty. Backend validates actual destination. |
| Enable automatic receiving from conversation | Details → Enable automatic receiving → review folder → Enable | 0 with default folder, otherwise 1 | 3 | Enable action is explicit. Does not implicitly clear pause with the matching partial-update backend. DOM tested; integration/browser open. |
| Edit an automatic receiving folder from conversation | Details → Edit folder → type destination → Save folder | 1 | 3 | Sends only peer identity and directory; enabled/paused state is retained by the matching backend. DOM tested; integration/browser open. |
| Set the default receive directory from workspace | Settings → type directory → Save | 1 | 2 | In-place error recovery; unsaved directory input survives Close and clears on session expiry. |
| Stop the application from workspace | Add device/network settings → Stop application → confirm stop | 0 | 3 | Reviews whole-application effect and active work. NetworkDialog DOM integration checks passed; real restart recovery open. |
| Upgrade the application | Read the development-build instructions; stop, rebuild intended frontend/binary, check version, inspect state, explicitly restart services | Not a supported GUI flow | Not applicable | No released upgrade path or GUI updater. See [current upgrade instructions](../docs/GENERIC.en.md#stop-revoke-and-upgrade); GUI access to these instructions remains a gap. |

The first host setup does not require typing a relay address or certificate fingerprint: the selected local address and reviewed listener are sufficient. Joining uses the inspected invitation rather than asking for those values again. Manual relay fields remain an advanced alternative. Device names and host ports are editable defaults, not additional required typing on the normal path.

## Earlier findings and current resolution

| Earlier finding | Current implementation | Evidence and remaining gate |
| --- | --- | --- |
| Every new service required inventing a name, and generated names could collide across peers/directions. | `src/service-form.ts:14–25` suggests bounded names unique across connections and shares. `Dialogs.tsx:146–150` preserves manually chosen names and offers explicit collision recovery. New operations do not include replacement identity. | DOM/helper tests cover cross-direction collisions and manual-name preservation. Final backend must reject collisions without implicit replacement. |
| Saved rows exposed only Stop, and status lacked enough fields for safe reuse. | `App.tsx:39–41` offers Copy settings and Edit and start. `Dialogs.tsx:103–118` reads full `service.config`; `service-form.ts:27–72` validates complete settings, carries exclusions/lifetime/purpose/discovery/backend, and adds exact replacement ID/revision only for edit. | Partial status responses are rejected; active, stale, mismatched-network and unreviewed legacy cases are DOM-tested. Matching backend integration and real command journeys remain required. |
| Closing a service or LAN setup form discarded all draft work. | `App.tsx:78–80,127,131–143` keeps service drafts by peer/action/source in memory and clears them on session expiry. LAN hostname, address, port, manual relay fields, and selected section are retained in `LanDraft`. Reset/reload is explicit. | DOM coverage includes Close, Back, peer changes, session expiry, manual name retention, and saved revision change on reopen. Preferences, per-peer receiving folders, and per-batch overrides now also remain in authenticated session memory. |
| Adding an attachment replaced the entire unsent batch. | `Conversation.tsx:112–138` appends validated selections and removes individual entries. `files.ts:17–52` checks aggregate limits, duplicate/portable paths, file-parent conflicts, and empty folders. A changed manifest gets a new request identity; unchanged retry keeps its identity. | DOM tests cover file + folder + image, cancellation, navigation, duplicate/limit rejection, async collection, permission/session loss, and retry identity. Repeated clipboard image names are made distinct before preview; ordinary picker collisions remain rejected. |
| Local mapping validation admitted privileged local ports and rejected valid disjoint remapping. | `ports.ts:43–50` enforces local ports 1024–65535 and maps disjoint remote ranges sequentially. `Dialogs.tsx:157` shows exact mappings. | DOM/helper coverage includes advertised port 22, high local start, retained mapping, and overflow. OS port availability and remaining global capacity remain backend checks. |
| Changing a batch destination required changing global settings. | `Conversation.tsx:21–31` provides Change folder and Use default folder, with an explicit per-batch destination. | DOM tests verify override and discard without settings/policy changes. Native destination behavior remains open. |
| Editing an automatic receive folder required revoke/re-enable and risked stale flag writes. | `Dialogs.tsx:61–73` sends only directory on edit; pause/resume/revoke also send only their changed flag. `App.tsx:62` exposes Edit folder directly. | DOM tests cover paused/unpaused policies. Atomic optional-field backend integration is necessary before shipping these controls. |
| Initial hosting required a redundant Find addresses click and lost non-secret draft inputs. | `LanSetup.tsx:84–108` auto-loads read-only candidates and retains explicit selected address/port. Refresh stays available, and selection is revalidated against current candidates. | DOM tests cover automatic read, no automatic selection/start, closed/reopened drafts, cleared ports, and stopped-host recovery. New browser/real host acceptance open. |
| Switching away from the active network hid the whole-app Stop action. | `Dialogs.tsx:58` places reviewed Stop outside the selected network form. Tailnet Sign in is disabled before activation. | Final NetworkDialog DOM integration checks passed. No updater or remote administration is introduced. |
| Locale/theme persistence was unclear beside the directory Save action. | The Japanese/English preferences hint now explains immediate appearance changes and Save to the local application. | Source copy aligned; browser persistence acceptance for the newest candidate remains open. |

## Remaining bounded findings

### Receiving and clipboard follow-through

Repeated clipboard images now keep separate reviewed filenames, such as `image.png` and `image (2).png`, without changing their bytes. Ordinary file/folder collisions continue to reject the new addition and preserve the current batch. DOM tests verify both preview paths and the exact upload manifest; native clipboard behavior remains open.

Unfinished receiving-directory edits now stay in authenticated-session memory, separately for Preferences, each peer’s automatic policy, and each incoming batch. Closing or changing peers does not save the draft or enable receiving. Successful saves and session expiry clear the relevant state; per-batch Use default folder explicitly discards that override. DOM regressions verify separate destinations, navigation, no accidental command, and authentication clearing.

### P3 — Upgrade instructions are not reachable from the GUI

The inspected interface has no version/help/upgrade-instructions route and no update command. The user guide explicitly says no released upgrade path exists yet; development rebuild instructions already exist in [Stop, revoke and upgrade](../docs/GENERIC.en.md#stop-revoke-and-upgrade) and [Distribution](../docs/DISTRIBUTION.md).

Minimal follow-up: expose a read-only version and clearly labeled link to the supported instructions once a stable documentation destination is available. State that installation/rebuild is external to the GUI. Do not add a pretend Check for updates/Install button, invent an updater API, or claim migration/rollback compatibility.

## Acceptance still required

1. Integrate the matching backend and UI, then record the exact combined revision and final unit/DOM/build results. Verify full configuration reads, revision conflicts, collision handling, and atomic partial autosave semantics through the production command boundary.
2. Execute the named Playwright Test suite on the integrated candidate and retain the exact revision, run and sanitized report. Cover service naming/copy/edit/restart, retained drafts, guided host review, whole-app stop, additive selection/removal, same-manifest retry, receiving folder override and autosave edit/pause/revoke. Keep real hosting, invitation inspection/join and device behavior separate; optional agent exploration is not a mandatory duplicate CI suite.
3. Inspect actual new-form screenshots in Japanese/English, light/dark, narrow layouts, and at 1180 px with details open. Test keyboard focus, Escape/Back, validation, cancel, failure, and retry; synthetic events do not establish native IME/clipboard behavior.
4. Keep real-device gates open until observed separately: enrollment and two-device pairing; trust boundaries; exact file contents and destination behavior; scoped TCP/UDP use; direct/relay reporting; installed binaries; OS login; and interruption/restart/suspend recovery. Saved settings, accepted commands, ready listeners, and preview animation do not establish application success or delivery.

## Readability and density checkpoint — 2026-10-03

The current UI candidate retains readable text, permanent search/composer labels, grouped service/preferences forms, full Japanese lifetime values and separate service/exchange action groups. It removes repeated toolbar headings, widespread button/icon frames and action underlines. Service name and kind share a line; compact sidebar rows retain the same font sizes. Selected tabs use a fill and type weight, secondary actions use a quiet tonal surface, and utilities use recognizable icons and readable text. Decorative separators remain subtle; editable fields retain a purposeful boundary and keyboard focus has its own indicator. These changes are not present in the published alpha.1 binary.

Verified locally against the final runtime candidate: 521 DOM/token tests, typecheck, nine evidence-safety tests, two fresh exact-lock builds matching all three committed assets, and compilation of the Go embedded-UI fixture. The 65 formal Playwright cases run in exact-source CI; the restricted local environment cannot execute Chromium. Six new cases exercise rendered control states at desktop, 375px touch and 1024px coarse-pointer/no-hover. They check actually painted text or icons at rest and accessible names, rather than demanding a contrasting perimeter around every button. Inputs, field text size, touch targets and keyboard focus remain checked. Compact cases also check complete tab labels and selected lifetime values. These remain an integration gate.

Ten regression cases measure the actual light/dark/system-dark palette across neutral, selected and feedback surfaces: ordinary text pairs remain at least 4.5:1, and the dedicated input-boundary token at least 3:1. This verifies token pairs, not complete-page accessibility conformance. Rendered input samples retained 13.97:1 text and 3.60:1 boundary contrast in light mode; buttons retain a separate keyboard-focus ring. Inactive-control styling is an explicit usability choice, not a claim that WCAG requires disabled-control contrast.

Actual private synthetic-preview inspection covered resting Japanese service screens at 1180px and 375px in light/dark, 375px settings in both themes, and the complete Japanese local-port/lifetime values. Resting samples were kept separate from keyboard-focus evidence so a retained focus ring did not dominate the appearance comparison. The lighter three-device graph was remeasured at 1180×757 and 375×844: connector anchor error was 0px and no horizontal overflow was observed. The earlier same-structure English graph/services/detail-close checks remain supplemental; the full language × theme × size × peer-count matrix was not repeated after the visual correction. A final settings-only class cleanup removes the obsolete second separator around the newly grouped form.

The preview retains fictional responses. It does not establish Core authentication, real file operations, pairing, OS behavior, full modal focus trapping, real-device traffic or native-target acceptance. A cloud-browser iframe pointer dispatch inconsistency was observed in the earlier pass; keyboard activation and state inspection worked, and no product cause was established. Review the final integrated source with formal browser and native CI before publication. Visual samples communicate the new direction; passing automated checks does not establish the user's aesthetic acceptance.

### Field-focus correction

The previous generic 3px outline with a 3px gap also applied to clicked text inputs, producing a detached double contour beside the colored input border. Search, ordinary inputs, selects and standalone textareas now use one joined 2px contour at the existing field edge, without changing geometry. The message editor owns its outer contour only while the textarea is focused; focusing a toolbar button does not also highlight that container. Validation coloring and a system-color outline in forced-colors mode are preserved.

The existing six readability browser profiles now assert pointer/keyboard focus, joined contours without a second shadow, unchanged field bounds/radius, composite-editor ownership, validation color and forced-colors visibility. They still require real execution with the integrated Go-backed candidate; test enumeration is not a pass. The field-focus-only revision passed the 478 DOM/token tests, typecheck, nine evidence-safety tests, two matching exact-lock builds and embedded-UI compilation.

Actual synthetic-preview checks reproduced the old double contour and verified the correction: desktop receiving input by click in light/dark and by Tab/Shift+Tab in dark; desktop search by click in dark; standalone read-only textarea by click and Tab/Shift+Tab in dark; desktop composer in dark with focus moved to its attachment toolbar; 375px receiving input by click in both themes and Tab/Shift+Tab in light; 375px search and composer by click in light. Captured fields had a single 2px outline with -1px offset and no visible clipping; the composer’s inner textarea had no border/outline, and its outer contour cleared when focus moved to the toolbar. This is a targeted sample, not every theme/input/viewport combination. Invalid-red and forced-colors cases are authored but not runtime-verified in that preview.

### Additional review of common flows

A bounded synthetic-browser audit covered English saved-service selection/review/cancel at 1180px and 390px, a 390px empty state, Japanese 390px invalid-port feedback, an English 844×390 service form and its action area, and a Japanese 390px graph with 12 long device names. No clipping or horizontal overflow was observed in those samples; graph endpoint error remained 0px and closing details restored node focus. This is targeted evidence, not a complete scenario matrix.

The audit reproduced lost focus when advanced connections replaced the scope form with its review, opened runtime credential fields, or returned to editing. The corrected flow focuses the review heading, then the first credential field, and returns to the name field on explicit edit. If a list refresh still disables that field, focus returns to the persistent edit heading and stays there when the request completes. Four DOM regressions cover repeated Japanese/English transitions and both pending-refresh return paths; the normal-flow cases failed before the fix. Existing formal browser cases also assert the focus targets without starting a proxy.

Two optional copy follow-ups remain: explain the proxy name syntax beside its field, and distinguish the mobile searchable device list from the graph card-list view in their navigation labels. No additional redesign was applied.

### First combined-source browser run and correction

[Run 37168816255](https://github.com/webkaz-labs/sobalink/actions/runs/37168816255), head `d432ece5`, passed all four native targets and the manifest job; its browser result was 53/65. Recovery and delivery-uncertainty cases passed. The failed cases covered compact keyboard cycles, native-select focus and graph state checks. Follow-up changes keep Tab within current modal boundaries, dismiss a native select popup before measuring keyboard traversal, and replace an obsolete decorative-outline threshold with painted route text/icon and connector contrast while retaining hit areas and all interaction checks. The revised source requires its own complete CI result; earlier passing subsets do not satisfy that gate. Reports now include only allowlisted source basenames and numeric failure coordinates, and failed captures have unique test identifiers. Raw error values, stacks, credentials and private paths remain excluded.
