# Local Web controls

[日本語](WEB_CONTROLS.ja.md) · [Saved services](SAVED_SERVICES.en.md) · [Startup and logout](LIFECYCLE.en.md)

The local Web UI supports service connections, reviewed sharing, files and messages, saved definitions, diagnostics, optional scoped proxies, and Tailnet sign-out. The controls below use the same local command API as the CLI. Browser checks, real-device compatibility and OS sign-in behavior remain separate acceptance gates.

## Save and manage a service while offline

Open **Saved services** in the header, then **Save a connection** or **Save a share**. Choose the saved network and enter the exact device ID, ports, mapping and lifetime. The visible-device picker is optional. A missing device stays an explicit saved reference; saving neither verifies it nor grants current access.

Choose **Review stopped definition**, check the complete scope, then **Save reviewed definition**. Use **Edit this draft** to correct it or **Cancel** to leave it unsaved. Definitions stay stopped. Each catalog entry provides **Edit saved definition**, **Copy saved definition** and **Remove definition**, including entries whose peers are unavailable. An active definition must be stopped before editing; a copy gets a separate identity. Changing an existing definition's network requires a copy.

Edits use the authoritative saved revision. After a conflict, reload the definition and review again. Removal reviews the current definition, any active service and affected groups; canceling leaves them intact. Task ownership and lease information appear when supplied. Manual operations cannot take another task's ownership; use the owning task or wait for its lease to end.

For live starts, advertised services retain the reviewed grant ID, purpose, protocol, ports and metadata review. Purpose and bound ports cannot be edited independently. A changed grant, purpose, scope or shorter expiry requires a fresh explicit review. Immediately before starting a new advertised connection, the UI rechecks that peer. If the same scope has equal or later expiry, it submits the fresh metadata revision; time spent reviewing the form alone does not require starting over. A changed scope stops the action for a new review. Saved historical restarts remain subject to Core’s exact saved-review revalidation. **Refresh advertised review** explicitly accepts the currently displayed metadata; **Enter ports manually** explicitly clears the advertised reference. Saving or copying historical references while offline does not authorize a live connection.

## Read service discovery results

Device details and connection setup show the last service-information observation. A confirmed empty reply means no services are currently advertised; known approved ports can still be entered manually. Pending, unconfirmed and unsupported replies do not prove that software is absent or the device/application is offline. A limited result points to the configured framing budget; review **Preferences → Capacity and history** and check again. A stale result needs another check before selecting an advertised service. **Check shared services** explicitly queries service metadata without restarting active transports. Ordinary peers remain selectable for explicit sharing.

## Sign out of Tailnet

Open **Set up network → Sign out of Tailnet**. The review lists active services, proxies and transfers. **Cancel** leaves them running. **Stop traffic and sign out** stops local traffic, requests embedded-node logout and exits the application, so this control page disconnects.

Success means the local backend acknowledged logout; it does not prove process exit or removal from Tailnet administration. Saved approvals, definitions and files remain. An unconfirmed result must not be treated as logout success. Wait for exit, use the same state directory to restart with the saved Tailnet network, then retry. The result panel provides copyable status, restart, logout and fresh-access-code commands. LAN pairing has separate revoke controls.

## Review startup at OS sign-in

Open **Preferences → Startup and sign-in guide**. OS registration is deliberately CLI-managed: Core has no reviewed OS-registration operation. The Web guide does not claim to know whether registration is installed and does not execute OS commands.

Choose **Restore saved network and receive approvals** or **Local management only**, and enable or disable. Copy the preview command, replace `STATE_DIRECTORY` with this application's state directory, and run it with the installed executable. Review the exact registration plan, network and automatic-receiving destinations. Only then run the matching apply command with that preview's `reviewToken` replacing `REVIEW_TOKEN`. Stop before the apply command to cancel. If the plan/profile changes or the service manager reports an error, inspect it and preview again; do not force replacement.

Registration applies at a future sign-in. Saved mode restores the saved network and previously approved, unpaused receiving; offline mode starts only local management. Saving definitions or registering startup alone does not authorize service or proxy launch. Optional launch behavior requires a separate reviewed startup approval; offline mode suppresses it. See the [startup approval guide](STARTUP.en.md) for its exact scope. Previous file transfers are not resumed. Disabling registration does not stop the currently running application. The preview is a plan, not an installed-status check; inspect its exact file/name in the OS sign-in manager to verify registration.


## Set up a client from saved services

Open **Saved services → Connection helpers → Save a RustDesk group** for an existing server. Choose the ID-server and relay devices separately, or enter their exact saved IDs if unavailable. Enter the server public key, remote/local ID and relay ports, numeric loopback and lifetime. Use only the public key, never a private key or password.

**Review four flows** shows NAT TCP, ID TCP, heartbeat UDP and relay TCP with each exact device and mapping. NAT uses the ID port minus one. Read the public key and the common-relay guidance, then **Save reviewed group**. **Back to settings** discards that preview; **Cancel** leaves definitions untouched. A profile conflict requires a fresh preview. A differing same-name group requires another name; an identical group is already saved. Saving does not open listeners. Use **Open saved group → Review start** for the separate runtime action.

**Client settings** on a service, or **Settings for selected services** under Connection helpers, shows actual saved mappings and current listener readiness. Copy the endpoint or SSH command as needed. SSH keeps a peer-specific HostKeyAlias; verify its host key independently. An HTTP URL is only a candidate; HTTPS still requires the original TLS hostname, SNI, origin and certificate verification. Ranges show addresses and exact port mappings instead of inventing a scalar endpoint. Use **Refresh settings** after changes. A failed copy leaves selectable text.

RustDesk settings include ID/relay endpoints, public key, blank proxy, enabled UDP and `/r`. All participating endpoints must use the same local relay address and port and have the four required forwards and server permissions. Back up existing RustDesk settings before changing them; stopping soba does not restore those settings. These controls never launch or configure an external application. Planned/saved/ready states do not verify application success. Import review names helper groups whose key or role metadata will be removed or changed.

## Choose a lifetime for one group start

In **Review start**, **Lifetime for this start** defaults to each saved lifetime. Choose one hour or a custom positive duration for any selection. All-forward selections also offer **Until stopped**; all-share selections offer **Until revoked**. Review the effective lifetime shown for every service, then start or cancel. The choice affects only this invocation. Saved definitions stay unchanged, and retrying does not extend an active permission. If a service is already active under another lifetime, explicitly stop it and review a new start. Client settings show the active runtime lifetime when one exists.


## Approve optional future startup

Select saved outbound connections or an outbound group, then open **Connection helpers → Review automatic startup**. The same screen also lists existing approvals without a selection. Enter an approval name and choose **Review future startup**. Check the network, node name, all device IDs, ports, mappings and lifetimes. If replacing a named approval, explicitly check that choice before **Approve reviewed future startup**. Cancel/back changes nothing. Saving starts nothing now and does not register an OS service.

On a future online process launch, each exact approved selection is attempted once after network readiness. Finite lifetimes start anew then. Offline launch suppresses startup for that process. Definition/group/network/hostname changes or peer revocation require fresh review; re-trusting does not restore old approvals. Stopping or expiring a run does not immediately restart it. A still-enabled future-process approval remains until disabled. The displayed launch result is not proof of current listener readiness or application success.

**Review disabling startup** shows the exact approval and its effect. Confirming disables future/pending startup without stopping existing services. A stale revision requires reloading and reviewing. If private storage fails, do not assume the approval or disable was saved; repair storage and reload before retrying. See [startup and private-profile details](STARTUP.en.md).

## Save optional private proxy credentials

In **Preferences → Advanced connections**, review an exact proxy scope as usual. **Continue to authentication** keeps the ordinary ephemeral flow. **Save this reviewed proxy** instead explains private storage, offers explicit generation or private input, and leaves **Also approve this exact proxy on future online launches** unchecked. Saving/generating stores credentials only in the protected local state directory, separately from portable definitions; it starts no listener. Generation never displays credentials automatically. Existing-name replacement requires a separate checkbox and a stopped same-name listener.

Open **Saved proxy credentials → Manage saved proxies** to inspect sanitized scopes and state. **Review saved proxy start** rechecks current identities and shows the complete listener/targets before an explicit start. **Review disabling future launch** prevents later automatic starts and transient recovery but leaves a current listener and its credentials in place. **Review deleting saved credentials** permanently removes the record and stops its associated session; canceling either review changes nothing. A saved proxy may recover transient transport loss within its original expiry. Expiry, explicit stop or invalidated scope never silently restarts it; a separately approved future process launch may begin a new lifetime.

**Reveal credentials temporarily** is the only display action. Hide/close and scope changes clear the private fields. Exports/history exclude them, and automated browser evidence capture rejects the private view. Copying is explicit and leaves the chosen value on the system clipboard until you replace it. Never include credentials in command arguments, source code, screenshots or shared files. Failed/uncertain saves clear input; reload the saved record and make a fresh scope review before retrying. All controls retain the local session/Host/Origin/CSRF boundary. Real installed startup, file ACL and external-client behavior still need their separate acceptance checks.
