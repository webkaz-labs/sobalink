# Development and usability principles

[日本語](DEVELOPMENT_PRINCIPLES.ja.md) · [User guide](GENERIC.en.md) · [Architecture](ARCHITECTURE.md)

**Make common goals take few steps, with detail available when needed.** Apply these principles to implementation, review and documentation. They govern the CLI, local Web UI and optional native companion.

## First-class Japanese and English

- Automatically select the language from the OS/runtime locale. Use Japanese for Japanese locales and English for other or unknown locales. Let the user override that choice
- Cover onboarding, prompts, confirmations, status, errors and next actions, not just help
- Keep command/flag names and the format and meaning of machine-readable JSON language-independent. Never translate endpoints, identifiers or user-supplied values
- Offer usable alternatives when browser launch, QR scanning or terminal width is unsuitable

## Simple and flexible

- Suggest useful purpose presets, while allowing the actual service settings to differ
- Require only inputs necessary for the goal; normal setup should not require JSON editing or internal peer IDs
- Show common actions first, then advanced configuration and diagnostics. Avoid an overwhelming initial help screen
- Repeated actions must not unexpectedly recreate state, request redundant approval or expand scope. Make consequential target/lifetime changes explicit
- Recover from typing errors in place where possible; support edit, back and cancellation before applying changes
- Share visual structure where responsibilities match, while keeping screen composition separate from state and operations. The [native UI policy](../poc/native/UI_IMPLEMENTATION.en.md) records the applied theme, frame/header boundaries and future menu policy

## Clear state and next actions

- Show peer, selected service, actual endpoint, lifetime and started/stopped state
- Explain what is known about a failure and give a specific next action; do not invent its cause
- Distinguish saved configuration, listener readiness, application success and remote-job completion. Report partial results honestly
- Preserve the selected profile in suggested follow-up commands
- For graphical interfaces, verify information hierarchy, controls, back navigation, narrow layouts, contrast and readable text

## Documentation starts with the answer

- Identify what works, the relevant version and remaining uncertainty at the top
- Put the shortest normal path first; place detailed conditions and exceptions later or in expandable sections
- Use consistent terms, tables for comparisons and diagrams such as Mermaid for relationships. Validate diagrams, links and command examples
- Separate implemented, mock-tested, distribution-verified and real-device-verified results. Claims must match evidence
- Reference these principles instead of maintaining conflicting copies

- Give each diagram one question; provide meaningful alt text and a short text equivalent. Use separate Japanese/English and narrow/wide layouts where labels would otherwise shrink. Maintain shared diagram sources in [documentation visuals (online source repository)](https://github.com/webkaz-labs/sobalink/blob/main/docs/assets/visuals/README.md), and distinguish rendered-image checks from browser/accessibility acceptance

## Change checklist

- [ ] Check Japanese and English onboarding/help/confirmation/status/errors/next actions
- [ ] Test automatic locale selection, overrides and unknown-locale fallback
- [ ] Walk through setup, authentication, connection, endpoint use and stop
- [ ] Test cancellation, edit/back, retry, repeat actions, partial failures and empty states
- [ ] Confirm conflicts/permission failures offer alternatives without silent setting changes
- [ ] Inspect QR/text/table width, contrast and information density
- [ ] Verify JSON and user values remain unchanged by localization/presentation
- [ ] Record native automated/distribution results separately from unperformed real-device tests
- [ ] Match documentation commands, versions, links and diagrams to implementation

See [SECURITY.md](../SECURITY.md) for security boundaries, [DISTRIBUTION.md](DISTRIBUTION.md) for publishing, and [ROADMAP.en.md](ROADMAP.en.md) for open acceptance gates.
