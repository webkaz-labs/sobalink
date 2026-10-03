package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"time"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/core"
	"github.com/webkaz-labs/sobalink/internal/ranges"
)

func proxyCommand(ctx context.Context, args []string, dir string, ja bool, out io.Writer, stdin io.Reader, request actionRequest) error {
	if len(args) == 0 || (len(args) == 1 && (args[0] == "--help" || args[0] == "-h" || args[0] == "help")) {
		_, err := io.WriteString(out, text(ja, proxyHelpEN, proxyHelpJA)+"\n")
		return err
	}
	operation, rest := args[0], args[1:]
	switch operation {
	case "list":
		if len(rest) != 0 {
			return usageError(ja, "proxy list")
		}
		return request("proxy.list", map[string]any{})
	case "stop":
		if len(rest) != 1 {
			return usageError(ja, "proxy stop ID")
		}
		return request("proxy.stop", map[string]string{"id": rest[0]})
	case "preview", "review":
		if len(rest) > 0 && (rest[0] == "--json-file" || rest[0] == "--stdin") {
			raw, err := commandPayload(ctx, append([]string{"proxy.preview"}, rest...), stdin, ja, dir)
			if err != nil {
				return err
			}
			return request("proxy.preview", raw)
		}
		f := commandFlags("proxy preview", ja, out)
		name := f.String("name", "", text(ja, "explicit proxy name", "プロキシの名前"))
		peer := f.String("peer", "", text(ja, "exact current target peer ID", "現在の接続先の正確な端末 ID"))
		ports := f.String("ports", "", text(ja, "exact allowed TCP ports or ranges", "許可する TCP ポート一覧または範囲"))
		backend := f.String("backend", "", text(ja, "selected network; omit to review the active network", "ネットワーク（省略時は稼働中のネットワークを確認）"))
		local := f.Int("local-port", 1080, text(ja, "loopback proxy listener port", "ループバックのプロキシ入口ポート"))
		host := f.String("loopback-host", "127.0.0.1", text(ja, "127.0.0.1 or ::1", "127.0.0.1 または ::1"))
		lifetime := f.String("lifetime", "until-stopped", text(ja, "until-stopped or finite", "until-stopped または finite"))
		ttl := f.Duration("ttl", 0, text(ja, "finite whole-second lifetime, for example 2h or 72h", "有限の有効期間（整数秒、例: 2h、72h）"))
		if err := parseFlags(f, rest, ja); err != nil {
			return err
		}
		if hasFlag(rest, "ttl") && !hasFlag(rest, "lifetime") {
			*lifetime = "finite"
		}
		if !config.ValidName(*name) || !config.ValidPeerID(*peer) {
			return errors.New(text(ja, "Specify --name and an exact --peer identity", "--name と正確な --peer ID を指定してください"))
		}
		if *host != "127.0.0.1" && *host != "::1" {
			return errors.New(text(ja, "Proxy listener must use 127.0.0.1 or ::1", "プロキシの入口は 127.0.0.1 または ::1 に限定されます"))
		}
		if *local < 1024 || *local > 65535 || (*local >= 54543 && *local <= 54545) {
			return errors.New(text(ja, "Choose --local-port in 1024..65535 outside reserved ports", "--local-port は予約ポート以外の 1024〜65535 を指定してください"))
		}
		if (*lifetime == "until-stopped" && *ttl != 0) || (*lifetime == "finite" && (*ttl < time.Second || *ttl%time.Second != 0)) || (*lifetime != "until-stopped" && *lifetime != "finite") {
			return errors.New(text(ja, "Use --lifetime until-stopped without --ttl, or a positive whole-second --ttl for finite lifetime", "--lifetime until-stopped は --ttl なしで、有限の期間は正の整数秒の --ttl で指定してください"))
		}
		seconds := int64(*ttl / time.Second)
		if *lifetime == "finite" {
			if _, err := capacity.Duration(seconds); err != nil {
				return err
			}
		}
		selected, err := ranges.ParseWithLimit(*ports, 65535)
		if err != nil {
			return errors.New(text(ja, "Specify --ports as application TCP ports such as 443,8443", "--ports に 443,8443 などのアプリ用 TCP ポートを指定してください"))
		}
		expanded, err := selected.ExpandWithLimit(65535)
		if err != nil {
			return err
		}
		targets := make([]core.ProxyTarget, 0, len(expanded))
		for _, port := range expanded {
			if port >= 54543 && port <= 54545 {
				return errors.New(text(ja, "Management ports cannot be proxy targets", "管理用ポートはプロキシの接続先にできません"))
			}
			targets = append(targets, core.ProxyTarget{PeerID: *peer, Port: int(port)})
		}
		return request("proxy.preview", map[string]any{"scope": core.ProxyScope{Name: *name, Backend: *backend, LoopbackHost: *host, LocalPort: *local, Lifetime: *lifetime, TTLSeconds: int(seconds), Targets: targets}})
	case "start":
		if len(rest) == 1 && (rest[0] == "--help" || rest[0] == "-h") {
			return usageHelp(out, ja, "proxy start --json-file PRIVATE_FILE | --stdin")
		}
		if err := validatePrivateProxyInput(append([]string{"proxy.start"}, rest...), ja); err != nil {
			return err
		}
		raw, err := commandPayload(ctx, append([]string{"proxy.start"}, rest...), stdin, ja, dir)
		if err != nil {
			return err
		}
		return request("proxy.start", raw)
	default:
		return errors.New(text(ja, "Unknown proxy action; use soba proxy --help", "不明なプロキシ操作です。soba proxy --help を参照してください"))
	}
}

func validatePrivateProxyInput(args []string, ja bool) error {
	if len(args) == 2 && args[1] == "--stdin" {
		return nil
	}
	if len(args) == 3 && args[1] == "--json-file" {
		return nil
	}
	return errors.New(text(ja, "Proxy credentials require --json-file PRIVATE_FILE or --stdin (pipe only); do not put them in command arguments", "プロキシの認証情報には --json-file PRIVATE_FILE または --stdin（パイプのみ）を使ってください。コマンド引数に含めないでください"))
}
func privateProxyFileCheck(file *os.File, ja bool) error {
	if err := checkProxyPrivateFile(file); err != nil {
		return errors.New(text(ja, "Proxy credentials require a file private to the current OS user; use a private file, a pipe, or the local Web UI", "プロキシの認証情報には現在の OS ユーザーだけが読めるファイルが必要です。非公開ファイル、パイプ、またはローカル画面を使ってください"))
	}
	return nil
}
func redactProxyPayload(raw json.RawMessage) json.RawMessage {
	// Whitelist the review fields. Unknown input keys may also contain secrets.
	var input struct {
		Scope            json.RawMessage `json:"scope"`
		ExpectedRevision string          `json:"expectedRevision"`
	}
	if json.Unmarshal(raw, &input) != nil {
		return json.RawMessage(`{"credentials":"[private input omitted]"}`)
	}
	var scope core.ProxyScope
	if json.Unmarshal(input.Scope, &scope) != nil {
		input.Scope = json.RawMessage(`{}`)
	} else {
		input.Scope, _ = json.Marshal(scope)
	}
	redacted, _ := json.Marshal(map[string]any{"scope": input.Scope, "expectedRevision": input.ExpectedRevision, "credentials": "[private input omitted]"})
	return redacted
}

const proxyHelpEN = `Optional authenticated SOCKS5 proxy (advanced)

  soba proxy preview --name NAME --peer PEER_ID --ports 443,8443
       [--local-port 1080] [--loopback-host 127.0.0.1|::1]
       [--backend tailnet|lan] [--lifetime until-stopped | --ttl 72h]
  soba proxy preview --json-file SCOPE_FILE | --stdin
  soba proxy start --json-file PRIVATE_FILE | --stdin
  soba proxy list
  soba proxy stop ID

Review the name, selected network, exact target peers and TCP ports, local
endpoint and lifetime first. Preview returns scope and revision; start input
contains scope, expectedRevision, username and password. Credentials are explicit
runtime inputs of 1..255 bytes each. Do not put them in argv, logs or shared files.
A start file must be readable only by the current OS user; stdin must be a pipe.
For multiple peers, preview input is {"scope":{...,"targets":[{"peerId":"PEER_ID","port":443}]}}.
The default lifetime is until-stopped; finite --ttl also supports periods over 24h.
Optional private persistence and explicit generation: soba proxy saved-help.

TCP CONNECT only. BIND, UDP ASSOCIATE, system DNS and OS target dialing are never
fallbacks. Targets must retain their reviewed current peer identities. Network
loss, revocation, expiry, logout or process exit stops the proxy; restart needs
fresh review and credentials unless explicitly saved with proxy save/generate.
Ordinary proxy start never saves permissions or credentials.
TCP reachability does not prove application compatibility or health.`
const proxyHelpJA = `認証付き SOCKS5 プロキシ（任意・詳細設定）

  soba proxy preview --name NAME --peer PEER_ID --ports 443,8443
       [--local-port 1080] [--loopback-host 127.0.0.1|::1]
       [--backend tailnet|lan] [--lifetime until-stopped | --ttl 72h]
  soba proxy preview --json-file SCOPE_FILE | --stdin
  soba proxy start --json-file PRIVATE_FILE | --stdin
  soba proxy list
  soba proxy stop ID

最初に名前、ネットワーク、正確な接続先 ID と TCP ポート、ローカルの入口、
有効期間を確認してください。preview は scope と revision を返します。
開始入力には scope、expectedRevision、username、password を含めます。
認証情報は各 1〜255 バイトの明示的な実行時入力です。引数、ログ、共有ファイルに
含めないでください。ファイルは現在の OS ユーザーだけが読めるものを使い、
stdin はパイプで入力します。複数の相手の確認入力は
{"scope":{...,"targets":[{"peerId":"PEER_ID","port":443}]}} の形式です。
既定は until-stopped です。有限の --ttl は 24 時間を超える期間にも対応します。
任意の非公開保存と明示的な生成: soba proxy saved-help。

TCP CONNECT のみです。BIND、UDP ASSOCIATE、OS の DNS や通常の接続への
代替はありません。確認した接続先の端末 ID が現在も一致する必要があります。
ネットワーク切断、取消し、期限切れ、ログアウト、本体終了で停止します。
通常の再開始には再確認と認証情報の入力が必要です。proxy save/generate で
明示的に保存した場合を除き、許可と認証情報は保存しません。
TCP の到達確認だけでは、アプリの互換性や正常動作は確認できません。`
