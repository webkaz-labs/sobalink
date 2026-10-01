# Project guidance

Read [development and usability principles](docs/DEVELOPMENT_PRINCIPLES.en.md)
([日本語](docs/DEVELOPMENT_PRINCIPLES.ja.md)) before implementation or documentation changes.
Keep these linked principles authoritative rather than copying another checklist here.

- Preserve the netstack-only outward transport and explicit scoped inbound rules described in SECURITY.md and docs/ARCHITECTURE.md
- Keep Japanese and English human interfaces aligned, with automatic locale selection and stable machine JSON
- Run gofmt, go test -race and go vet; retain native Linux x64/ARM64, macOS ARM64 and Windows x64 CI
- Never mark real enrollment, application compatibility or OS-login/suspend acceptance complete from mocked tests alone
- Keep release signing, provenance, reproducible builds and actual installed-binary verification intact
