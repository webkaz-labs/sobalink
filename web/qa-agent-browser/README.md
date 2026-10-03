# Browser journey acceptance

This isolated QA package pins the official [Vercel agent-browser CLI](https://github.com/vercel-labs/agent-browser) at 0.38.2. It does not change production web dependencies. The package requires Node.js 24 or later. Version and commands were checked against the [official command reference](https://agent-browser.dev/commands) and npm registry on 2026-10-02.

Run on an Ubuntu CI worker authorized to start the test server and Chrome:

```sh
npm ci --prefix web/qa-agent-browser
web/qa-agent-browser/node_modules/.bin/agent-browser install --with-deps
SOBA_E2E_SESSION_FILE=/path/to/private-session.json \
SOBA_SCREENSHOT_DIR=/path/to/qa-artifacts \
node web/scripts/agent-browser-journeys.mjs
```

Start the existing `cmd/soba-e2e` server separately and provide its private session file. The runner requires numeric loopback HTTP and a session file with no group/other access. It uses production Go HTTP, session, CSRF and embedded UI. Peers are fictional in-process integration fixtures. Browser actions use the agent-browser CLI itself; no Playwright, Puppeteer, API route substitution or direct command submission is used. JavaScript evaluation is limited to observation, a metadata-only request counter, and synthetic composition events.

The private session includes fixture metadata (capabilities are required and checked before launching a browser):

```json
{
  "scenario": "studio",
  "capabilities": ["service-lifecycle", "failed-upload-retry"],
  "receiveDirectory": "/temporary/fixture/received",
  "localServicePort": 43919
}
```

The server supplies its usual `url` and `code` fields privately. Do not copy them into reports, workflow output or artifacts. `scenario: "offline"` requires `offline-network` and runs the empty/offline network setup and LAN input-validation journey. The online/Studio scenario requires both capabilities shown above. A `failed-upload-retry` fixture must reject the first staging request without creating a transfer and accept the second request using its unchanged request identity; outgoing offers stay pending for cancellation and pause-impact checks. `service-lifecycle` means a real loopback listener can be started, stopped, read through `service.config`, and restarted with exact saved revision through the production command handler. The workflow candidate also checks authoritative copying and retained drafts without starting on read. File preview checks include additive selection and per-entry removal. Missing required capabilities fail the run. `requiredCoverageComplete` is true only when every named scenario journey actually passes. Run both scenarios to cover both paths. The offline scenario inspects the guided host form, its high-port default, and the absence of an automatically selected interface. Positive relay-start and invitation inspection remain separate acceptance gates. No test activates LAN, creates an invitation, enrolls a node, follows a sign-in link or contacts an external account.

The report records passed, failed and unavailable journeys, safe command categories, screenshots, scoped accessibility snapshots, and graph text metrics. CLI output is captured in memory and omitted from failure logs. Login code entry uses stdin, not process arguments. Snapshots and screenshots start only after authenticated workspace confirmation; sensitive controls must be absent or empty. No traces, HARs, cookies, storage exports, recordings or persistent browser profiles are written. Artifact uploads must include only the chosen screenshot directory, never the session file or CLI runtime directory.

Review the actual PNGs at full size before accepting layout, spacing, Japanese glyph appearance and contrast. Automated geometry and typography assertions do not replace visual inspection. Synthetic composition events do not establish native IME behavior. In-process peers and listeners do not establish real-device delivery, enrollment, application compatibility, LAN direct/relay behavior, or suspend/resume acceptance.

Run `node --test web/qa-agent-browser/runner-safety.test.mjs` for preflight privacy checks. These deliberately reject invalid sessions before any CLI call and do not launch a daemon or browser. They are not browser coverage.
