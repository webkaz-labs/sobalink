# Tailnet sign-in

Start the local application and select Tailnet. The local Web settings provide an official sign-in link. Pairing through a selected relay is a separate setup flow and does not use this login.

For a terminal:

```sh
soba login --browser
soba login --qr
soba login --link
```

`--browser` opens the official Tailscale page and waits. `--qr` generates a QR entirely on this device, requires a private terminal, and waits for completion or device approval. A narrow terminal offers the private link instead of a wrapped QR. `--link` explicitly prints the private link and returns; add `--wait` to wait too. A redirected browser-mode output omits the private link. Never share these links or capture them in screenshots.

An already connected node or one awaiting administrator approval does not restart authentication. An existing valid link is reused; `--refresh` explicitly requests a fresh flow. `--timeout 5m` controls the local wait, with other positive durations available; it does not change or infer the server's link expiry. Ctrl+C cancels the wait, without revoking the displayed link or stopping the node. Use `soba stop` to stop the application.

Plain `soba login` keeps the machine JSON interface. `soba login --wait` waits and returns a final state as JSON. Use `--state-dir PATH` before the command to retain the selected profile.

Mocked tests cover URL validation, repeated requests, pending approval, cancellation and QR pixels/display restoration. Phone scanning, real enrollment, native IME and terminal/font combinations require real-device acceptance.
