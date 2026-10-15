# Common questions

[日本語](FAQ.ja.md) · [Start here](GENERIC.en.md) · [Things to check yourself](TROUBLESHOOTING.en.md)

This is an experimental open-source project; no user support is provided. These self-service notes do not promise responses, fixes or compatibility.

These answers describe v0.5.0-alpha.1. For executed checks and remaining real-device limits, use the [verification record](VERIFICATION.en.md).

## Do I need administrator rights or a VPN driver?

sobalink runs as an ordinary user process with a userspace network stack. It does not install a TUN driver or change OS routes/firewalls. This does not bypass blocked networks or remove an application's own installation/authentication requirements. [Network boundaries](ARCHITECTURE.md#network-boundaries)

## Does the other device need sobalink?

Ordinary Tailnet service targets do not. sobalink-to-sobalink sharing, messages, file batches and request-and-accept setup need their matching peer workflow. Choose the target and service first; do not install a second agent merely to reach an existing permitted Tailnet service. [Connect a service](GENERIC.en.md#connect-to-a-peers-service)

## Is nearby discovery proof of who I am connecting to?

No. Names and addresses are hints. Normal first-use setup accepts and pins a key (TOFU), which cannot guarantee protection against an active attacker during the first exchange. Use independent full-fingerprint comparison when stronger initial verification is needed. A later changed key is not silently accepted. [Connection setup](CONNECTION_SETUP.en.md)

## Does connecting allow files or share all ports?

No. Network pairing, sender trust and service permission are separate. Shares name peers, ports and lifetime. File reception normally needs batch consent; per-peer autosave is an explicit separate setting. [Security boundaries](../SECURITY.md)

## Why is my saved service stopped?

Saving stores a definition, not a running listener or restored permission. Review and start it, then use the actual endpoint in the application. Separately reviewed startup selections are optional; a successful start still does not prove application authentication. [Saved services](SAVED_SERVICES.en.md)

## Does accepted mean delivered or read?

No. Durable queue acceptance means the sender retained the job. Remote save evidence is a later result; it is not a read receipt or proof the content still exists. An unknown result needs inspection of the original job, not an unrelated resend. [Files and folders](DURABLE_OUTBOX.en.md) · [Text](DURABLE_TEXT.en.md)

## Can Direct/Relay switching preserve an existing connection?

There is no seamless existing-TCP migration or partial-byte replay guarantee. Applications may need to reconnect. Mixed-mode fallback only uses an approved route for the same authenticated peer after a positively identified availability failure; a permission denial is not a fallback trigger. [Mixed connections](MIXED_CONNECTIONS.en.md)

## Can I reuse an older profile or downgrade?

Profile format 3 is incompatible with v0.4.0-alpha.4 and earlier. Preserve old data and use a separate empty `--state-dir`; there is no automatic migration or wipe. Do not downgrade using the newer profile. [Install/update safely](DISTRIBUTION.md)

## Does a signed release mean every device combination works?

No. Signature/provenance and installed-binary checks establish their recorded distribution properties. Physical two-device and cross-OS discovery, real networks, application compatibility, OS sign-in and suspend/wake require separate acceptance. [Evidence and limits](VERIFICATION.en.md)
