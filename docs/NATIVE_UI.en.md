# Optional Native UI (prerelease)

[日本語](NATIVE_UI.ja.md) · [Distribution and verification](DISTRIBUTION.md) · [CLI guide](CLI_GUIDE.en.md)

**`soba-native` is a separate, optional desktop client.** The Windows x64 and macOS ARM64 prerelease packaging adds it to the standard archives under `bin/`; Linux archives remain unchanged. The existing `soba` CLI and Web UI remain available. Packaging support is not evidence of successful publication or real desktop operation: check the exact release's verification record before use.

## Start with a fictional demo

After verifying the target release's signatures, build-origin evidence (provenance) and file checksums (digests), open a terminal in the extracted archive's `bin` directory. Go is not required to run the binaries.

Windows PowerShell:

```powershell
& .\soba-native.exe --demo --tray --locale auto
```

macOS (Apple Silicon):

```sh
./soba-native --demo --tray --locale auto
```

`auto` selects Japanese for a Japanese OS locale and English otherwise. Use `--locale ja` or `--locale en` to override it, or switch language in the window. Demo devices and exchanges are fictional; they do not connect or send files/messages. Omit `--tray` for a normal window.

Packslip continues to expose only `soba` as its regular command. Installing the companion does not select or launch it. If using mise, find the installation directory with `mise where` using the **same exact prerelease pin that passed verification**, then explicitly run its `bin/soba-native` (or `bin/soba-native.exe`). Do not substitute a guessed version or an old test kit. The companion has a separate dependency/license inventory; check it along with the release manifest.

## Read an explicitly selected profile

Prepare and start the daemon separately using the [CLI guide](CLI_GUIDE.en.md). For first evaluation, use a new dedicated test profile with fictional labels, not existing personal data. Replace `TEST_PROFILE_DIRECTORY` below with that profile's absolute path.

```sh
./soba-native --state-dir "TEST_PROFILE_DIRECTORY" --tray --locale auto
```

On Windows, use `& .\soba-native.exe` with the same arguments. Choose exactly one of `--demo` and `--state-dir`; no profile is selected implicitly. Live mode is read-only by default. Press **Refresh status** to request a snapshot over the existing user-private communication channel between local processes (IPC). There is no automatic refresh, daemon startup or configuration change. IPC success does not establish network health or delivery. Failed refresh clears stale displayed state.

## Send only with opt-in and review

```sh
./soba-native --state-dir "TEST_PROFILE_DIRECTORY" --tray --locale auto --enable-send
```

This enables reviewed message/file actions on the selected profile. Confirm the exact recipient and message, or file list and destination, before committing. Incoming files need destination review and explicit acceptance or decline. The UI does not change trust or pause state; existing daemon permissions remain authoritative. Connection setup, settings, permissions, updates and daemon management stay in the existing CLI/Web UI.

Requests can fail after being accepted. An uncertain result is not proof of non-delivery: do not automatically retry or evade a locked operation by restarting. Refresh history/status and verify with the recipient. Command acceptance is not delivery confirmation. History shows the latest six available records, not a complete archive.

## Hide and quit

With `--tray`, the menu-bar/notification-area icon toggles the panel. Close, Hide or Escape when it is not being handled by an input control hides it; **Quit UI (daemon continues)** exits only `soba-native`. The daemon is neither started nor stopped by this client. Drafts survive hiding, but are lost on UI exit. The panel does not dismiss automatically on focus loss, to avoid interrupting IME or file pickers. If tray setup fails, a normal window opens; closing that fallback exits the UI. No login item, service or automatic startup is installed.

## Verification boundaries

- Release signature/provenance checks establish distribution integrity and source identity. They are separate from Windows developer/publisher signing and macOS Developer ID signing/notarization. A checksum alone establishes neither.
- If OS or administrator policy blocks execution, stop and record the block. Do not disable protections, remove quarantine attributes, bypass Gatekeeper/SmartScreen, or retry with administrator privileges.
- Native windows, tray interaction, Japanese text input (IME), screen readers, GPU/high-DPI, suspend/resume, long sessions and real two-device network exchange still require physical-device acceptance. Cross-builds, images rendered without a display and tests with simulated data for protected IPC and shared application logic are not substitutes.
- The integrated source includes PR #66. Earlier unsigned PR #67 test kits without that change remain historical artifacts, not release assets or evidence for the new source. No final release version or passing release result is asserted here.
- Share only generalized findings and fictional data. Do not distribute real account/device names, paths, workplace information, logs or screenshots that identify a person or their usage environment.

Online source documentation (main branch; not the exact release verification record): [Device test procedure (Japanese)](https://github.com/webkaz-labs/sobalink/blob/main/poc/native/DEVICE_TEST.ja.md) · [Source-specific validation](https://github.com/webkaz-labs/sobalink/blob/main/poc/native/VALIDATION.md) · [Build the prototype](https://github.com/webkaz-labs/sobalink/blob/main/poc/native/README.md)
