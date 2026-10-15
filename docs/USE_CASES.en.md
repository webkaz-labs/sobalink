# Where sobalink helps

[日本語](USE_CASES.ja.md) · [User guide](GENERIC.en.md) · [Security](../SECURITY.md) · [Verification](VERIFICATION.en.md)

Connect devices and use application services with explicit peers, ports and lifetimes. Send text and files when you choose. The [verification record](VERIFICATION.en.md) identifies the release, exact-source automated checks and distribution results. Physical-network and application-compatibility acceptance remain open; use the [LAN guide](LAN.en.md) for that setup.

| Goal | Action | What to check |
| --- | --- | --- |
| Temporarily share a Web app or API | Share a narrow TCP scope with chosen peers and an expiry | Authentication, later-started applications within the scope, TLS and origin |
| Use SSH, SFTP or a database | Create a local entry for the peer’s service | SSH host keys, database permissions and authentication; ordinary Tailnet targets need no sobalink |
| Use an authenticated job API | Connect only the required service | The application owns job authorization, results and cancellation |
| Send short text or an image | Select the recipient and send; copy manually | Exact recipient and content; no clipboard synchronization |
| Send several files or a folder | Review a batch; the receiver approves a destination | Empty folders, count, size and receiver save, separately from local staging |
| Save transfers from one known peer | Enable autosave for the exact peer and destination | Backend/trust generation, pause and revocation; no overwrite or automatic execution |

## Try one small service first

1. Start the real application on its host and enable its own authentication.
2. Set up the selected network. For sobalink sharing, the host reviews the exact peer, ports and lifetime; an ordinary Tailnet application instead uses its existing network and application policy.
3. On the client, use the [guided connection](CLI_GUIDE.en.md) and copy the actual local endpoint into the application.
4. Perform an application operation, then stop the service when finished. Listener readiness alone does not establish application success.

To deliver while a peer is offline, explicitly choose the [file queue](DURABLE_OUTBOX.en.md) or [text queue](DURABLE_TEXT.en.md). Local acceptance is not receiver-save evidence, and cancellation cannot recall saved content.

## Two service addresses

| Address | Who uses it | Purpose |
| --- | --- | --- |
| Client loopback and the entry port shown in status | The application on that client | Enter the selected forward |
| Host embedded-node address and approved port | A permitted peer | Reach the shared service |
| Temporary local management URL | The browser on the same device | Manage sobalink; never share or forward it |

Matching port numbers do not make these the same endpoint. See the [connection illustration](GENERIC.en.md) and [local API boundary](../web/API.md). If ordinary Tailscale or an existing shared service already meets the need, consider whether a separate node or additional local entry is useful.

## Connection and application responsibilities

Keep transport readiness, peer discovery, application success and remote-job completion separate. A tunnel does not provide certificates or login, translate stdio into HTTP, configure applications, or allocate/cancel remote jobs. Stopping cannot recall data already sent.

Select sharing peers explicitly. The initial 32-peer choice can be changed to a finite value or explicit unlimited policy. Shares default to a finite hour; durations beyond 24 hours and explicit until-revoked permission are available. Outbound connections default to until-stopped. Review the ports and exclusions. UDP, local connections and optional proxy listeners have an adjustable finite budget, initially 64. [Logical choices and resource budgets](CAPACITY.en.md) are separate. A broad TCP share also permits applications started later within that range while permission is active; weigh the scope alongside convenience.

Direct and Relay describe observed paths. Reconnecting means communication is being re-established. Keep an unknown current path unknown, and do not assume an established TCP stream survives. After a network change, sleep/wake or interruption, recheck identity and permission, then reconnect the application or retry the unfinished file as appropriate.
