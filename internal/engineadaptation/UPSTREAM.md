# Reviewed Tailscale engine adaptation

This is a small, source-available adaptation of `tailscale.com v1.104.0`,
originally published at https://github.com/tailscale/tailscale/tree/v1.104.0.
The original source and its BSD-3-Clause license remain upstream. Adapted files
retain their original copyright headers; added upstream-module files carry
explicit license notices. This is not an unmodified Tailscale release.

`manifest.json` is the complete adaptation: exact byte-offset edits, original
and final SHA-256 file hashes, original Go module and go.mod checksums, and
complete original/adapted tree digests. A separate reviewed pin in `pin.go`
binds the manifest bytes. Updating either requires reviewing both. The source
changes add an instance-scoped underlay policy and put admission at the actual
UDP write and DERP dial boundaries, before any protected engine operation.
Unrestricted engines preserve upstream behavior. Policy revocation closes
tracked relay connections and prevents future writes/dials.

Run `go run ./cmd/prepare-engine` from the project root. The preparer obtains
the exact original Go module archive, independently verifies its Go checksums,
applies only exact manifest edits, and atomically writes a new generated module
at `.sobalink-deps/tailscale`. It does not patch the Go module cache. Existing
output is fully rehashed and rejected on drift. `--verify` performs read-only
verification. With the original archive cached, preparation works with
`GOPROXY=off`. No upstream source tree is committed to this repository.

The package tool allows only the exact version-qualified local replacement,
checks the complete generated tree before and after compilation, retains all
upstream module notices plus this file and the manifest, and records separate
upstream and adapted provenance in its SBOM and build metadata. Standard
signatures, reproducible-build checks, native acceptance and actual installed
binary checks remain required. Tests using synthetic peers establish only their
specified admission and transport behavior, not arbitrary application or
real-network acceptance.

Maintainers regenerate the compact record with
`python .github/scripts/update-engine-manifest.py ORIGINAL_MODULE_ZIP EDITED_COPY`.
The tool verifies the original checksum and rejects changes outside the explicit
reviewed path allowlist, deleted files, symlinks, module graph changes and
unrelated outputs. Review the complete diff, license notices and separate
manifest pin; rerun all preparation, packaging and native guard tests. This
command is never part of normal builds or release publication.
