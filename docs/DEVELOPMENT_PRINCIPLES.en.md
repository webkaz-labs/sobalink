# Development and usability principles

[日本語](DEVELOPMENT_PRINCIPLES.ja.md) · [User guide](GENERIC.en.md) · [Architecture](ARCHITECTURE.md)

**Make common goals take few steps, with detail available when needed.** Apply these principles to implementation, review and documentation. They govern the current CLI and any future graphical interface.

## First-class Japanese and English

- Automatically select from the OS/runtime locale; use Japanese for Japanese locales and English as the safe fallback. Allow an explicit override
- Cover onboarding, prompts, confirmations, status, errors and next actions, not just help
- Keep command/flag names and the machine JSON contract language-independent. Never translate endpoints, identifiers or user-supplied values
- Offer usable alternatives when browser launch, QR scanning or terminal width is unsuitable

## Simple and flexible

- Suggest useful purpose presets, while allowing the actual service settings to differ
- Require only inputs necessary for the goal; normal setup should not require JSON editing or internal peer IDs
- Show common actions first, then advanced configuration and diagnostics. Avoid an overwhelming initial help screen
- Repeated actions must not unexpectedly recreate state, request redundant approval or expand scope. Make consequential target/lifetime changes explicit
- Recover from typing errors in place where possible; support edit, back and cancellation before applying changes

## Clear state and next actions

- Show peer, selected service, actual endpoint, lifetime and started/stopped state
- Explain what is known about a failure and a concrete next step; do not invent its cause
- Distinguish saved configuration, listener readiness, application success and remote-job completion. Report partial results honestly
- Preserve the selected profile in suggested follow-up commands
- For graphical interfaces, verify information hierarchy, controls, back navigation, narrow layouts, contrast and readable text

## Documentation starts with the answer

- Identify what works, the relevant version and remaining uncertainty at the top
- Put the shortest normal path first; place detailed conditions and exceptions later or in expandable sections
- Use consistent terms, tables for comparisons and diagrams such as Mermaid for relationships. Validate diagrams, links and command examples
- Separate implemented, mock-tested, distribution-verified and real-device-verified results. Claims must match evidence
- Reference these principles instead of maintaining conflicting copies

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

See [SECURITY.md](../SECURITY.md) for security boundaries, [DISTRIBUTION.md](DISTRIBUTION.md) for publishing, and [ROADMAP.ja.md](ROADMAP.ja.md) for open acceptance gates.
