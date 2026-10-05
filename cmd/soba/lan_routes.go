package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/netip"
	"strings"
	"time"
)

const routeHelpEN = `Prepared routes for the same paired device

  soba lan routes list [PEER_ID]
  soba lan routes add --relay IP:PORT --certificate SHA256 --scope local|external
  soba lan routes remove CANDIDATE_ID
  soba lan routes offer PEER_ID [--ttl 168h]
  soba lan routes withdraw PEER_ID [--ttl 168h]
  soba lan routes inspect PEER_ID --json-file UPDATE_FILE
  soba lan routes approve PEER_ID --json-file UPDATE_FILE --withdrawal
  soba lan routes approve PEER_ID --json-file UPDATE_FILE --candidates ID[,ID] [--ttl 24h]
  soba lan routes approve PEER_ID --current --candidates ID[,ID] [--ttl 24h]
  soba lan routes revoke PEER_ID [--candidates ID[,ID]]

Add/remove prepared relays while running soba --offline, then restart. Existing
pair identities stay unchanged. Configure both devices before changing networks.
Exchange an offer privately; inspect its peer, endpoint, pin and expiry before
approving exact candidate IDs. Authentication alone never approves a route.
Approve defaults to 24 hours and never exceeds the offer expiry. Offer lifetime
is at most 720 hours. Revoke without --candidates removes every local route grant.
Use --stdin instead of --json-file for piped input; never pass an update as an
argument. A local relay candidate is not a strict no-external-egress mode.
Existing TCP connections may need application reconnect; bytes are not replayed.`

const routeHelpJA = `同じペアの端末へ戻るための経路

  soba lan routes list [PEER_ID]
  soba lan routes add --relay IP:PORT --certificate SHA256 --scope local|external
  soba lan routes remove CANDIDATE_ID
  soba lan routes offer PEER_ID [--ttl 168h]
  soba lan routes withdraw PEER_ID [--ttl 168h]
  soba lan routes inspect PEER_ID --json-file UPDATE_FILE
  soba lan routes approve PEER_ID --json-file UPDATE_FILE --withdrawal
  soba lan routes approve PEER_ID --json-file UPDATE_FILE --candidates ID[,ID] [--ttl 24h]
  soba lan routes approve PEER_ID --current --candidates ID[,ID] [--ttl 24h]
  soba lan routes revoke PEER_ID [--candidates ID[,ID]]

追加の中継は soba --offline で起動中に追加・削除して再起動します。
ペアの識別は維持します。ネットワークを移動する前に両端を準備してください。
経路情報は安全な方法で渡し、相手・接続先・証明書・期限を確認してから
候補IDを明示して許可します。認証できたことだけでは通信を許可しません。
許可は既定24時間で、相手の情報の期限を超えません。提供期限は最大720時間。
revoke の --candidates 省略時は、その相手への経路許可をすべて取り消します。
パイプ入力には --json-file の代わりに --stdin を使い、経路情報を引数へ
貼り付けないでください。local は中継の区分で、外部通信ゼロの保証ではありません。
既存TCPはアプリ側の再接続が必要な場合があり、データを自動再送しません。`

func routeIDs(value string) ([]string, error) {
	if value == "" {
		return nil, nil
	}
	ids := strings.Split(value, ",")
	if len(ids) > 4 {
		return nil, errors.New("at most four exact candidate IDs")
	}
	seen := map[string]bool{}
	for _, id := range ids {
		b, err := hex.DecodeString(id)
		if err != nil || len(b) != 32 || id != strings.ToLower(id) || seen[id] {
			return nil, errors.New("invalid or repeated candidate ID")
		}
		seen[id] = true
	}
	return ids, nil
}

func readRouteInput(ctx context.Context, path string, pipe bool, in io.Reader, ja bool) (string, error) {
	if (path == "") == !pipe {
		return "", errors.New(text(ja, "Choose exactly one of --json-file or --stdin", "--json-file と --stdin のどちらか一つを指定してください"))
	}
	args := []string{"lan.routes.inspect", "--stdin"}
	if path != "" {
		args = []string{"lan.routes.inspect", "--json-file", path}
	}
	raw, err := commandPayload(ctx, args, in, ja)
	if err != nil {
		return "", err
	}
	var envelope map[string]json.RawMessage
	if json.Unmarshal(raw, &envelope) != nil {
		return "", errors.New(text(ja, "Route update must be a JSON object", "経路更新にはJSONオブジェクトが必要です"))
	}
	update := string(raw)
	if value, ok := envelope["update"]; ok {
		if json.Unmarshal(value, &update) != nil || !json.Valid([]byte(update)) {
			return "", errors.New(text(ja, "Invalid route update envelope", "経路更新の内容が正しくありません"))
		}
	}
	return update, nil
}

func lanRoutesCommand(ctx context.Context, args []string, ja, dryRun bool, out io.Writer, in io.Reader, query commandQuery, request actionRequest) error {
	if len(args) == 0 || (len(args) == 1 && (args[0] == "--help" || args[0] == "-h" || args[0] == "help")) {
		_, err := io.WriteString(out, text(ja, routeHelpEN, routeHelpJA)+"\n")
		return err
	}
	operation, rest := args[0], args[1:]
	switch operation {
	case "list":
		if len(rest) > 1 {
			return usageError(ja, "lan routes list [PEER_ID]")
		}
		peer := ""
		if len(rest) == 1 {
			peer = rest[0]
		}
		return request("lan.routes.list", map[string]string{"peerId": peer})
	case "remove":
		if len(rest) != 1 {
			return usageError(ja, "lan routes remove CANDIDATE_ID")
		}
		return request("lan.routes.remove", map[string]string{"candidateId": rest[0]})
	case "add":
		f := commandFlags("lan routes add", ja, out)
		address := f.String("relay", "", text(ja, "exact numeric relay IP:port", "中継の正確な数値IP:ポート"))
		pin := f.String("certificate", "", text(ja, "exact certificate SHA-256", "証明書のSHA-256"))
		scope := f.String("scope", "", text(ja, "explicit local or external scope", "local または external を明示"))
		if err := parseFlags(f, rest, ja); err != nil {
			return err
		}
		ap, err := netip.ParseAddrPort(*address)
		if err != nil || ap.String() != *address || ap.Port() == 0 || ap.Addr().IsUnspecified() || ap.Addr().IsMulticast() || ap.Addr().Zone() != "" {
			return errors.New(text(ja, "Use an exact numeric relay IP:port", "正確な数値の中継IP:ポートを指定してください"))
		}
		decoded, err := hex.DecodeString(*pin)
		if err != nil || len(decoded) != 32 || (*scope != "local" && *scope != "external") || (*scope == "local" && !ap.Addr().IsPrivate() && !ap.Addr().IsLoopback()) {
			return errors.New(text(ja, "Review the exact certificate and local/external scope", "正確な証明書と local/external の区分を確認してください"))
		}
		return request("lan.routes.add", map[string]string{"address": *address, "certificateSHA256": strings.ToLower(*pin), "scope": *scope})
	}
	if len(rest) == 0 {
		return usageError(ja, "lan routes "+operation+" PEER_ID [options]")
	}
	peer, flags := rest[0], rest[1:]
	if decoded, err := hex.DecodeString(peer); err != nil || len(decoded) != 32 || peer != strings.ToLower(peer) {
		return errors.New(text(ja, "Exact paired public identity required", "ペアにした相手の正確な公開IDが必要です"))
	}
	f := commandFlags("lan routes "+operation, ja, out)
	switch operation {
	case "offer", "withdraw":
		ttl := f.Duration("ttl", 168*time.Hour, text(ja, "offer lifetime, at most 720h", "提供期限（最大720時間）"))
		if err := parseFlags(f, flags, ja); err != nil {
			return err
		}
		if *ttl < time.Second || *ttl > 720*time.Hour || *ttl%time.Second != 0 {
			return errors.New(text(ja, "Use whole seconds from 1s through 720h", "1秒〜720時間の整数秒で指定してください"))
		}
		return request("lan.routes.export", map[string]any{"peerId": peer, "ttlSeconds": int(ttl.Seconds()), "withdraw": operation == "withdraw"})
	case "revoke":
		value := f.String("candidates", "", text(ja, "comma-separated IDs; omit to revoke all", "候補IDをカンマ区切りで指定（省略時は全件取消）"))
		if err := parseFlags(f, flags, ja); err != nil {
			return err
		}
		ids, err := routeIDs(*value)
		if err != nil {
			return errors.New(text(ja, "Invalid candidate IDs", "候補IDが正しくありません"))
		}
		return request("lan.routes.revoke", map[string]any{"peerId": peer, "candidateIds": ids})
	case "inspect", "approve":
		path := f.String("json-file", "", text(ja, "private update JSON file", "非公開の経路更新JSONファイル"))
		pipe := f.Bool("stdin", false, text(ja, "read update from a pipe", "パイプから経路更新を読む"))
		withdrawal := f.Bool("withdrawal", false, text(ja, "explicitly apply an authenticated empty withdrawal", "認証済みの空の経路取消を明示して適用"))
		current := f.Bool("current", false, text(ja, "review the current saved offer", "保存済みの現在の提供情報を確認"))
		value := f.String("candidates", "", text(ja, "exact reviewed candidate IDs, comma-separated", "確認した候補IDをカンマ区切りで指定"))
		ttl := f.Duration("ttl", 24*time.Hour, text(ja, "local approval lifetime, at most 720h", "この端末での許可期限（最大720時間）"))
		if err := parseFlags(f, flags, ja); err != nil {
			return err
		}
		if *current && (*path != "" || *pipe) {
			return errors.New(text(ja, "--current cannot be combined with update input", "--current と更新情報の入力は併用できません"))
		}
		update := ""
		var err error
		if !*current {
			update, err = readRouteInput(ctx, *path, *pipe, in, ja)
			if err != nil {
				return err
			}
		}
		inspect := "lan.routes.inspect"
		payload := map[string]any{"peerId": peer}
		if *current {
			inspect = "lan.routes.review"
		} else {
			payload["update"] = update
		}
		if operation == "inspect" {
			return request(inspect, payload)
		}
		ids, err := routeIDs(*value)
		if err != nil || (!*withdrawal && len(ids) == 0) || (*withdrawal && len(ids) != 0) {
			return errors.New(text(ja, "Approve requires explicit --candidates from the reviewed update", "許可には確認済みの候補を --candidates で明示してください"))
		}
		if *ttl < time.Second || *ttl > 720*time.Hour {
			return errors.New(text(ja, "Approval lifetime must be 1s through 720h", "許可期限は1秒〜720時間で指定してください"))
		}
		var review struct {
			Digest     string    `json:"digest"`
			Expires    time.Time `json:"expires"`
			Candidates []struct {
				CandidateID string `json:"candidateId"`
			} `json:"candidates"`
		}
		if !dryRun {
			if err := query(inspect, payload, &review); err != nil {
				return err
			}
			if *withdrawal && len(review.Candidates) != 0 {
				return errors.New(text(ja, "Withdrawal approval requires an authenticated empty offer", "取消の適用には認証済みの空の提供情報が必要です"))
			}
			allowed := map[string]bool{}
			for _, c := range review.Candidates {
				allowed[c.CandidateID] = true
			}
			for _, id := range ids {
				if !allowed[id] {
					return errors.New(text(ja, "Selected candidate is absent from this reviewed update", "選んだ候補は今回確認した更新に含まれていません"))
				}
			}
		}
		expires := time.Now().UTC().Add(*ttl)
		if !review.Expires.IsZero() && review.Expires.Before(expires) {
			expires = review.Expires
		}
		if *withdrawal {
			expires = time.Time{}
		}
		payload["digest"] = review.Digest
		payload["candidateIds"] = ids
		payload["expires"] = expires
		action := "lan.routes.apply"
		if *current {
			action = "lan.routes.approve"
		}
		return request(action, payload)
	default:
		return errors.New(text(ja, "Unknown route action; use soba lan routes --help", "不明な経路操作です。soba lan routes --help を参照してください"))
	}
}
