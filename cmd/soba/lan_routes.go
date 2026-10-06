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
  soba lan routes offer PEER_ID (--until-revoked | --ttl 168h)
  soba lan routes withdraw PEER_ID (--until-revoked | --ttl 168h)
  soba lan routes inspect PEER_ID --json-file UPDATE_FILE
  soba lan routes approve PEER_ID --json-file UPDATE_FILE --withdrawal
  soba lan routes approve PEER_ID --json-file UPDATE_FILE --candidates ID[,ID] (--until-revoked | --ttl 168h)
  soba lan routes approve PEER_ID --current --candidates ID[,ID] (--until-revoked | --ttl 168h)
  soba lan routes revoke PEER_ID [--candidates ID[,ID]]

Add/remove prepared relays while running soba --offline, then restart. Existing
pair identities stay unchanged. Configure both devices before changing networks.
Exchange an offer privately; inspect its peer, endpoint, pin and lifetime before
approving exact candidate IDs. Authentication alone never approves a route.
Choose --until-revoked for no scheduled expiry, or --ttl 168h for a finite lifetime.
No lifetime is selected by default. A finite approval ends no later than a finite
offer. Until-revoked approval requires an until-revoked offer. Withdraw produces
an empty update; apply it on the recipient to remove the offered routes.
Revoke without --candidates removes every local route grant.
Use --stdin instead of --json-file for piped input; never pass an update as an
argument. A local relay candidate is not a strict no-external-egress mode.
Existing TCP connections may need application reconnect; bytes are not replayed.`

const routeHelpJA = `同じペアの端末へ戻るための経路

  soba lan routes list [PEER_ID]
  soba lan routes add --relay IP:PORT --certificate SHA256 --scope local|external
  soba lan routes remove CANDIDATE_ID
  soba lan routes offer PEER_ID (--until-revoked | --ttl 168h)
  soba lan routes withdraw PEER_ID (--until-revoked | --ttl 168h)
  soba lan routes inspect PEER_ID --json-file UPDATE_FILE
  soba lan routes approve PEER_ID --json-file UPDATE_FILE --withdrawal
  soba lan routes approve PEER_ID --json-file UPDATE_FILE --candidates ID[,ID] (--until-revoked | --ttl 168h)
  soba lan routes approve PEER_ID --current --candidates ID[,ID] (--until-revoked | --ttl 168h)
  soba lan routes revoke PEER_ID [--candidates ID[,ID]]

追加の中継は soba --offline で起動中に追加・削除して再起動します。
ペアの識別は維持します。ネットワークを移動する前に両端を準備してください。
経路情報は安全な方法で渡し、相手・接続先・証明書・有効期間を確認してから
候補IDを明示して許可します。認証できたことだけでは通信を許可しません。
期限なしは --until-revoked、期限付きは --ttl 168h のように明示して選びます。
有効期間の既定値はありません。期限付きの許可は期限付きの提供情報を超えません。
期限なしの許可には、相手の提供情報も期限なしである必要があります。
withdraw は空の更新を作ります。相手側で適用して、提供した経路を取り消します。
revoke の --candidates 省略時は、その相手への経路許可をすべて取り消します。
パイプ入力には --json-file の代わりに --stdin を使い、経路情報を引数へ
貼り付けないでください。local は中継の区分で、外部通信ゼロの保証ではありません。
既存TCPはアプリ側の再接続が必要な場合があり、データを自動再送しません。`

func routeLifetimeSelection(ttl time.Duration, ttlSet, untilRevoked, ja bool) (string, error) {
	if ttlSet == untilRevoked {
		return "", errors.New(text(ja, "Choose exactly one lifetime: --until-revoked or --ttl 168h", "有効期間を一つ選んでください: --until-revoked または --ttl 168h"))
	}
	if untilRevoked {
		return "until-revoked", nil
	}
	if ttl <= 0 {
		return "", errors.New(text(ja, "Use a positive duration, such as --ttl 168h", "--ttl 168h のように、正の有効期間を指定してください"))
	}
	return "finite", nil
}

func routeIDs(value string) ([]string, error) {
	if value == "" {
		return nil, nil
	}
	ids := strings.Split(value, ",")
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
		ttl := f.Duration("ttl", 0, text(ja, "explicit finite offer lifetime, for example 168h", "期限付きの提供期間を明示（例: 168h）"))
		untilRevoked := f.Bool("until-revoked", false, text(ja, "explicitly offer routes without a scheduled expiry", "提供する経路を期限なしにすることを明示"))
		if err := parseFlags(f, flags, ja); err != nil {
			return err
		}
		lifetime, err := routeLifetimeSelection(*ttl, hasFlag(flags, "ttl"), *untilRevoked, ja)
		if err != nil {
			return err
		}
		payload := map[string]any{"peerId": peer, "lifetime": lifetime, "withdraw": operation == "withdraw"}
		if lifetime == "finite" {
			if *ttl%time.Second != 0 {
				return errors.New(text(ja, "Use whole seconds for offers, such as --ttl 168h", "提供期間は --ttl 168h のように整数秒で指定してください"))
			}
			payload["ttlSeconds"] = int64(*ttl / time.Second)
		}
		return request("lan.routes.export", payload)
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
		ttl := f.Duration("ttl", 0, text(ja, "explicit finite local approval lifetime, for example 168h", "この端末での期限付きの許可期間を明示（例: 168h）"))
		untilRevoked := f.Bool("until-revoked", false, text(ja, "explicitly approve until locally revoked; requires an until-revoked offer", "この端末で取り消すまで許可することを明示（期限なしの提供情報が必要）"))
		if err := parseFlags(f, flags, ja); err != nil {
			return err
		}
		if *current && (*path != "" || *pipe) {
			return errors.New(text(ja, "--current cannot be combined with update input", "--current と更新情報の入力は併用できません"))
		}
		var ids []string
		lifetime := ""
		if operation == "approve" {
			var err error
			ids, err = routeIDs(*value)
			if err != nil || (!*withdrawal && len(ids) == 0) || (*withdrawal && len(ids) != 0) {
				return errors.New(text(ja, "Approve requires explicit --candidates from the reviewed update", "許可には確認済みの候補を --candidates で明示してください"))
			}
			if *withdrawal {
				if hasFlag(flags, "ttl") || hasFlag(flags, "until-revoked") {
					return errors.New(text(ja, "Apply --withdrawal without lifetime flags; it removes the offered routes", "--withdrawal は有効期間を指定せずに使ってください。提供された経路を取り消します"))
				}
			} else {
				lifetime, err = routeLifetimeSelection(*ttl, hasFlag(flags, "ttl"), *untilRevoked, ja)
				if err != nil {
					return err
				}
			}
		} else if hasFlag(flags, "ttl") || hasFlag(flags, "until-revoked") {
			return errors.New(text(ja, "Use lifetime flags with approve; inspect only reviews the offer", "有効期間は approve で指定してください。inspect は提供情報を確認する操作です"))
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
		var review struct {
			Digest     string    `json:"digest"`
			Lifetime   string    `json:"lifetime"`
			Expires    time.Time `json:"expires"`
			Candidates []struct {
				CandidateID string `json:"candidateId"`
			} `json:"candidates"`
		}
		if !dryRun {
			if err := query(inspect, payload, &review); err != nil {
				return err
			}
			if review.Lifetime == "" {
				review.Lifetime = "finite" // Legacy v1 reviews always had a finite expiry.
			}
			if (review.Lifetime == "finite" && (review.Expires.IsZero() || !review.Expires.After(time.Now()))) || (review.Lifetime == "until-revoked" && !review.Expires.IsZero()) || (review.Lifetime != "finite" && review.Lifetime != "until-revoked") {
				return errors.New(text(ja, "Offer lifetime is invalid or expired; inspect a fresh update", "提供情報の有効期間が不正か期限切れです。新しい更新を確認してください"))
			}
			if !*withdrawal && lifetime == "until-revoked" && review.Lifetime != "until-revoked" {
				return errors.New(text(ja, "This offer expires; use --ttl 168h or obtain an until-revoked offer", "この提供情報には期限があります。--ttl 168h を使うか、期限なしの提供情報を受け取ってください"))
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
		var expires any
		if *withdrawal {
			lifetime = review.Lifetime
		} else if lifetime == "finite" {
			deadline := time.Now().UTC().Add(*ttl)
			if review.Lifetime == "finite" && review.Expires.Before(deadline) {
				deadline = review.Expires
			}
			expires = deadline
		}
		payload["digest"] = review.Digest
		payload["candidateIds"] = ids
		payload["lifetime"] = lifetime
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
