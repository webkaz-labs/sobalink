# Tailnet sign-in

[日本語](SIGN_IN.ja.md) · [Startup](LIFECYCLE.en.md) · [Nearby setup](CONNECTION_SETUP.en.md)

Start the local application and select Tailnet. The local Web settings provide an official sign-in link. Pairing through a selected relay is a separate setup flow and does not use this login.

## Choose a sign-in method

```sh
soba login --browser
soba login --qr
soba login --link
```

`--browser` opens the official Tailscale page and waits. `--qr` generates a QR entirely on this device, requires a private terminal, and waits for completion or device approval. A narrow terminal offers the private link instead of a wrapped QR. `--link` explicitly prints the private link and returns; add `--wait` to wait too. A redirected browser-mode output omits the private link. Never share these links or capture them in screenshots.

An already connected node or one awaiting administrator approval does not restart authentication. An existing valid link is reused; `--refresh` explicitly requests a fresh flow. `--timeout 5m` controls the local wait, with other positive durations available; it does not change or infer the server's link expiry. Ctrl+C cancels the wait, without revoking the displayed link or stopping the node. Use `soba stop` to stop the application.

Plain `soba login` keeps the machine JSON interface. `soba login --wait` waits and returns a final state as JSON. Use `--state-dir PATH` before the command to retain the selected profile.

Mocked tests cover URL validation, repeated requests, pending approval, cancellation and QR pixels/display restoration. Phone scanning, real enrollment, native IME and terminal/font combinations require real-device acceptance.

## Read the outcome

| Result | Next step |
| --- | --- |
| Connected | Inspect `soba peers`, then choose a service |
| Waiting for administrator approval | Complete the Tailnet approval process; do not restart authentication unnecessarily |
| Still waiting or timed out | Inspect `soba status`; a local timeout does not revoke the sign-in link |
| Stopped waiting with Ctrl+C | The node and link may remain active; use `soba stop` to stop the application |

Tailnet enrollment and the one-time code for the local Web UI are separate sign-ins. Never paste an enrollment link into a shared issue or diagnostic log.
