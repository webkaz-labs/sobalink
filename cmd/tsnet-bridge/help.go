package main

import (
	"errors"
	"fmt"
	"io"
)

const help = `tsnet-bridge: experimental application-scoped tailnet bridge

First time:  init -> login -> connect
  login --qr          Sign in with a trusted phone
  share               Offer one local service to selected peers, with expiry

Everyday:
  connect             Choose a service shared with this node
  connect NAME        Start a saved connection
  status              See what is ready and what needs attention
  settings            Show endpoints to copy into your apps
  stop NAME           Stop one connection
  stop                Stop the node and all connections

More: rules, peers, group, wait-ready, task, doctor, reconnect,
      export, import, migrate, autostart, logout, setup (legacy RustDesk)

Use help COMMAND for details, or help all for the full reference.
Flags precede names. --state-dir PATH goes before the command.
Language is automatic; --lang ja|en|auto before COMMAND overrides it.
No OS VPN/route/DNS changes. Application compatibility remains unverified.`

func commandHelp(command string, out io.Writer) error {
	if command == "all" {
		fmt.Fprintln(out, helpAll)
		return nil
	}
	entries := map[string]string{
		"init":        "init [--hostname NAME]\nSave an idle profile without networking. Example: tsnet-bridge init",
		"login":       "login [--qr | --link | --no-browser] [--qr-format small|large] [--timeout 5m]\nBrowser by default; phone QR and manual private link are alternatives. Never share sign-in links.\nExample: tsnet-bridge login --qr",
		"connect":     "connect [NAME] or connect [--manual --peer PEER --purpose web|ssh|db|ai|custom]\nOptional: --name NAME --port PORT --listen-port PORT --network tcp|udp --save-only --replace --confirm\nChoose a recently confirmed share; peer, purpose, protocol and port are filled in. Application behavior remains unverified.\nUse --manual or remote-setting flags for ordinary Tailscale services and older bridges. Refresh/reselect if a share changes.\nExample: tsnet-bridge connect web-demo",
		"share":       "share [NAME] [--ttl 30m] or share [--peer PEER --purpose PURPOSE]\nOptional: --port PORT --listen-port PORT --network tcp|udp --loopback 127.0.0.1|::1 --save-only --replace --confirm --no-discovery --discoverable\nReview service, allowed peers and lifetime. App authentication remains required.\nInteractive shares announce only purpose, protocol, shared port and expiry to allowed peers; --no-discovery opts out.\nWith --confirm, discovery stays disabled unless --discoverable is explicit. Existing saved shares retain their setting.\nExample: tsnet-bridge share --ttl 30m api-demo",
		"status":      "status [--json]\nShow per-rule state and next actions. JSON is the stable machine-readable interface.",
		"doctor":      "doctor [--json]\nRecheck current peer identities and TCP reachability; application success is separate.",
		"settings":    "settings [--show-secrets]\nDisplay application endpoints. --show-secrets applies only to legacy SOCKS credentials in a private terminal.",
		"rules":       "rules [--json]\nList saved definitions. Saving never starts a rule.",
		"peers":       "peers [--json]\nList current eligible tailnet peers after explicit sign-in.",
		"shares":      "shares [--json]\nShow sharing recipients, actual listeners and expiry.",
		"start":       "start [NAME...] [--group GROUP] [--ttl 30m] [--owner OWNER] [--confirm] [--json]\nNo names starts the idle node; names explicitly start saved rules. Flags go before names.",
		"stop":        "stop [NAME...] [--group GROUP] [--owner OWNER] [--json]\nNo names stops the entire node. Named stops affect only the specified owner.",
		"stop-shares": "stop-shares [--json]\nExplicitly stop every share, including task-owned shares.",
		"group":       "group list | group save [--replace] [--confirm] NAME RULE... | group start/stop [flags] NAME\nStart flags: --ttl 30m --owner OWNER --confirm --json. Save alone never connects.\nExample: tsnet-bridge group save dev web-demo ssh-demo",
		"wait-ready":  "wait-ready [--rules NAMES|--group GROUP] [--owner OWNER] [--timeout 30s] [--json] [NAME...]\nWait for selected transport listeners; not remote-job completion.",
		"task":        "task --rules NAMES [--ttl 30m] [--timeout 30s] [--confirm] -- COMMAND ARG...\nStart owned rules, run the command directly, clean up on exit; abandoned 30s leases expire.\nExample: tsnet-bridge task --rules web-demo -- curl http://127.0.0.1:8080/",
		"export":      "export [--output FILE --confirm]\nPreview first. Confirm writes a private disabled local copy, never an upload. Review peer/service data before sharing.",
		"import":      "import [--replace] [--confirm] FILE\nPreview first; apply while stopped. Rules stay disabled and existing node identity is retained.",
		"migrate":     "migrate [--confirm --id-peer-id ID --relay-peer-id ID]\nPreview v1 fixed RustDesk migration; preserve exact backup. SOCKS stays v1.",
		"autostart":   "autostart [enable|disable] [--json] [--apply]\nPreview by default. Explicit apply changes only future user-login startup of an idle v2 node.",
		"run":         "run [--idle]\nForeground node; Ctrl+C closes connections. --idle refuses legacy auto-forwarding profiles.",
		"reconnect":   "reconnect [--json]\nRecheck and recreate currently requested connections; never revive stopped/expired grants.",
		"logout":      "logout [--json]\nStop local connections and request logout. Node deletion remains a separate operation.",
		"setup":       "setup [--id-host HOST --key PUBLIC_KEY] [--relay-host HOST] [--mode forward|socks]\nCreate a legacy RustDesk profile. For ordinary Web/SSH/API connections use init instead.",
		"version":     "version\nShow the installed version.",
	}
	text, ok := entries[command]
	if !ok {
		return errors.New("unknown help topic; use help all")
	}
	fmt.Fprintln(out, "Usage: tsnet-bridge "+text)
	return nil
}
