# Local transfer settings resource: source-build preview

[日本語](RESOURCE_SETTINGS.ja.md) · [User guide](GENERIC.en.md)

This checkout provides a local resource catalog, inspection and change preview for two transfer-admission settings. This is source-build functionality, not a new published release. Apply, durable operation results and remote management are not implemented in this slice. No version or release commitment is implied.

## Inspect and preview

Start the local agent, then use another terminal with the same profile selection:

```sh
soba resource list
soba resource inspect --id RESOURCE_ID
soba resource preview --id RESOURCE_ID --concurrent-files 4 --concurrent-per-peer 2
soba resource preview --id RESOURCE_ID --concurrent-files default --concurrent-per-peer default
```

Replace `RESOURCE_ID` with the exact opaque ID returned by `list`. Both preview choices are required. Use `default` or a positive finite integer; `unlimited`, zero, negative values and values outside the supported JSON integer range are rejected. The provider also checks the complete proposed capacity policy. Preview never changes settings or starts transfers. A lower limit in a future apply implementation would govern admission rather than cancel active transfers.

All three commands return language-independent JSON; `--json` is accepted explicitly. Human help and input errors follow `--locale auto|ja|en`. `requested` retains the requested choices and `effective` shows their resolved values. Global `--dry-run` only validates local input and prints the request without contacting the agent; it does not inspect current state or issue an authoritative review. Global `--offline` definition editing does not support these commands. A running agent started with `start --offline` can inspect settings without starting its saved network.

Only `list`, `inspect` and `preview` are advertised as supported operations. There is no usable apply or operation-status command. The interface uses existing authenticated local control; it creates no peer endpoint, remote management grant or discovery advertisement.

## Identity, review and compatibility contract

- The one resource has type `transfer-admission-settings`, local authority/provider scope and an opaque persistent ID. The ID is not a device name, path, address or peer identity. Copying the complete private state directory copies this local identity; no remote clone-resolution behavior is claimed.
- Only `transferConcurrentFiles` and `transferConcurrentPerPeer` are represented. Their canonical owner remains the existing capacity policy. The adapter preserves every unrelated setting and the existing profile/capacity file formats.
- Reviews bind the current policy/profile, exact requested choices and current process authority revision. Existing local authority writes invalidate a review, including changes later reverted. Restart requires a new review. A revision is an opaque review token, not a proof of historical completion.
- A separate bounded private sidecar persists identity. Owned startup validates and atomically republishes it before exposing a usable resource. Invalid, unsupported or uncertain resource state disables this new feature rather than silently replacing its identity; legacy features remain available. Older builds need not understand the sidecar. Downgrading a resource-aware build may make resources unavailable if it cannot understand later sidecar records, without altering legacy policy/profile formats.
- Descriptors and previews expose neither private paths nor peer lists, transport identities or full profiles. The fixed local authority scope does not identify an individual user. No management permission is inferred from pairing.

Successful preview is not proof of saved settings, live transfer changes, durable operation recovery, remote application or physical-device acceptance. Those remain outside this slice. See the [development principles](DEVELOPMENT_PRINCIPLES.en.md) and [security boundary](../SECURITY.md).
