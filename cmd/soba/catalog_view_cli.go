package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/webkaz-labs/sobalink/internal/core"
	"github.com/webkaz-labs/sobalink/internal/ranges"
	"github.com/webkaz-labs/sobalink/internal/resource"
	"github.com/webkaz-labs/sobalink/internal/resourcegrant"
	"github.com/webkaz-labs/sobalink/internal/transfer"
)

const catalogViewHelpEN = `Choose one managed DirectLAN peer's permitted shared services

  soba resource catalog view preview --id RESOURCE_ID --peer PEER_KEY --service-ids ID[,ID] --expires-at RFC3339
  soba resource catalog view confirm --review-file REVIEW.json --confirm
  soba resource catalog view inspect --id RESOURCE_ID
  soba resource catalog view revoke --id RESOURCE_ID --grant-id GRANT_ID --grant-revision N --confirm
  soba resource catalog view list --id REMOTE_RESOURCE_ID --peer PEER_KEY --grant-id GRANT_ID --grant-revision N [--json]
  soba connect --peer PEER_KEY --catalog-review REVIEW_REVISION --catalog-service SERVICE_ID --name NAME --network tcp --ports PORT --purpose PURPOSE --local-port PORT --lifetime until-stopped

On the provider, obtain the resource ID from resource list and current share activation IDs
from status shares[].activationId. Preview exactly the peer, selected shares and finite expiry; save its complete
unchanged JSON to a private file, then confirm. Inspect supplies the exact list selector.
On the viewer, explicitly choose that peer and selector. List does not grant connection
permission. Human output shows each activation, conditions and current connection permission.
Connect uses the returned reviewRevision and exact service ID, network, ports and purpose.
Core rechecks the original provider, relationship, share generation, conditions and permission
before starting through the existing connect workflow. A failed or unknown start requires
a new list and review; do not replay the old review. Saved configuration is not a current review.
Revoke remains possible when the peer is disconnected. --json is stable in both languages.
--dry-run validates local input with zero agent calls; it does not approve or contact the peer.`

const catalogViewHelpJA = `管理対象の DirectLAN の相手1台から、閲覧を許可された共有を選択

  soba resource catalog view preview --id RESOURCE_ID --peer PEER_KEY --service-ids ID[,ID] --expires-at RFC3339
  soba resource catalog view confirm --review-file REVIEW.json --confirm
  soba resource catalog view inspect --id RESOURCE_ID
  soba resource catalog view revoke --id RESOURCE_ID --grant-id GRANT_ID --grant-revision N --confirm
  soba resource catalog view list --id REMOTE_RESOURCE_ID --peer PEER_KEY --grant-id GRANT_ID --grant-revision N [--json]
  soba connect --peer PEER_KEY --catalog-review REVIEW_REVISION --catalog-service SERVICE_ID --name NAME --network tcp --ports PORT --purpose PURPOSE --local-port PORT --lifetime until-stopped

提供側は resource list のリソース ID と status の shares[].activationId を使います。
相手・選択した共有・有限の期限を preview で確認し、JSON 全体を変更せず非公開ファイルに保存して
confirm します。inspect の正確な許可情報を閲覧側へ渡してください。
閲覧側は相手と許可情報を明示して list を実行します。一覧閲覧は接続権限を与えません。
表示には各共有世代・接続条件・現在の接続許可を含みます。
接続には返された reviewRevision、正確な共有 ID・network・ports・purpose を指定します。
Core は開始前に元の提供元・接続関係・共有世代・条件・許可を再確認し、既存の接続操作で開始します。
開始失敗や結果不明の場合は新しい list と確認が必要です。古い確認を再実行しないでください。
保存済みの接続設定は現在の確認ではありません。相手が未接続でも revoke できます。
--json は日英で同じ安定した形式です。--dry-run は本体に接続せず入力だけを検査します。`

// This separate CLI path never turns historical discovery into catalog consent.
func resourceCatalogViewCLI(ctx context.Context, args []string, dir string, ja, dryRun bool, out io.Writer, client controlCaller) error {
	if len(args) == 0 || len(args) == 1 && resourceCollectionHelp(args[0]) || len(args) == 2 && resourceCollectionHelp(args[1]) {
		_, err := fmt.Fprintln(out, text(ja, catalogViewHelpEN, catalogViewHelpJA))
		return err
	}
	action := args[0]
	if action != "preview" && action != "confirm" && action != "inspect" && action != "revoke" && action != "list" {
		return usageError(ja, "resource catalog view preview|confirm|inspect|revoke|list --help")
	}
	f := commandFlags("resource catalog view "+action, ja, out)
	machine := f.Bool("json", false, text(ja, "stable JSON", "安定した JSON"))
	var id, peer, ids, expiry, file, grant, revision string
	var confirm bool
	if action != "confirm" {
		f.StringVar(&id, "id", "", text(ja, "exact resource ID", "正確なリソース ID"))
	}
	if action == "preview" || action == "list" {
		f.StringVar(&peer, "peer", "", text(ja, "one exact managed peer key", "管理対象1台の正確な相手鍵"))
	}
	if action == "preview" {
		f.StringVar(&ids, "service-ids", "", text(ja, "selected current share activation IDs", "選択した現在の共有世代 ID"))
		f.StringVar(&expiry, "expires-at", "", text(ja, "finite RFC3339 expiry", "RFC3339 形式の有限の期限"))
	}
	if action == "confirm" {
		f.StringVar(&file, "review-file", "", text(ja, "unchanged private preview JSON file", "変更していない非公開の preview JSON ファイル"))
	}
	if action == "list" || action == "revoke" {
		f.StringVar(&grant, "grant-id", "", text(ja, "exact grant ID", "正確な許可 ID"))
		f.StringVar(&revision, "grant-revision", "", text(ja, "exact current grant revision", "現在の正確な許可の変更番号"))
	}
	if action == "confirm" || action == "revoke" {
		f.BoolVar(&confirm, "confirm", false, text(ja, "explicitly confirm", "明示的に承認する"))
	}
	if err := parseFlags(f, args[1:], ja); err != nil {
		return err
	}
	invalid := func() error {
		return errors.New(text(ja, "Choose the exact peer, scope and current review; see resource catalog view --help", "正確な相手・範囲・現在の確認内容を指定してください。resource catalog view --help を参照してください"))
	}
	target := resource.Target{SchemaVersion: resource.SchemaVersion, ResourceID: id}
	var payload any
	if action == "confirm" {
		if !confirm || file == "" {
			return invalid()
		}
		review, err := readCatalogViewReview(file)
		if err != nil {
			return invalid()
		}
		payload = resourcegrant.CatalogViewConfirmation{Review: review, Confirm: true}
	} else {
		if target.Validate() != nil {
			return invalid()
		}
		switch action {
		case "inspect":
			payload = struct {
				Target resource.Target `json:"target"`
			}{target}
		case "preview":
			deadline, err := time.Parse(time.RFC3339, expiry)
			selected := strings.Split(ids, ",")
			if err != nil || deadline.Unix() <= 0 || !resource.ValidDigest(peer) || len(selected) > resourcegrant.MaxRecords {
				return invalid()
			}
			sort.Strings(selected)
			for i, s := range selected {
				if !resource.ValidID(s) || i > 0 && selected[i-1] == s {
					return invalid()
				}
			}
			payload = resourcegrant.CatalogViewInputs{Target: target, PeerKey: peer, ServiceIDs: selected, ExpiresAt: deadline.Unix()}
		case "revoke", "list":
			n, err := strconv.ParseUint(revision, 10, 64)
			selector := resourcegrant.CatalogViewSelector{ProtocolVersion: resourcegrant.CatalogViewVersion, Target: target, GrantID: grant, GrantRevision: n}
			if err != nil || strconv.FormatUint(n, 10) != revision || selector.Validate() != nil {
				return invalid()
			}
			if action == "list" {
				input := resourcegrant.RemoteCatalogViewInput{PeerKey: peer, Request: selector}
				if input.Validate() != nil {
					return invalid()
				}
				payload = input
			} else {
				if !confirm {
					return invalid()
				}
				payload = resourcegrant.GrantRevoke{Target: target, GrantID: grant, GrantRevision: n, Confirm: true}
			}
		}
	}
	name := "resource.catalog.view." + action
	if dryRun {
		return resourceCollectionJSON(out, map[string]any{"applied": false, "command": name, "payload": payload, "validation": "local-input-only"})
	}
	result, err := resourceCollectionCall(ctx, dir, name, payload, client)
	if err != nil {
		return err
	}
	if action != "list" {
		return resourceCollectionJSON(out, result)
	}
	var observation catalogViewCLIObservation
	if resource.Decode(result, resourcegrant.MaxBytes, &observation) != nil || observation.PeerKey != peer || observation.Selector != payload.(resourcegrant.RemoteCatalogViewInput).Request || !resource.ValidID(observation.ReviewRevision) || observation.CheckedAt.IsZero() || observation.Services == nil || len(observation.Services) > resourcegrant.MaxRecords {
		return invalid()
	}
	previous := ""
	for _, row := range observation.Services {
		s := row.Service
		_, portsErr := ranges.ParseWithLimit(s.Ports, 65535)
		purposeOK := false
		for _, p := range []string{"", "generic", "custom", "web", "ssh", "db", "postgres", "ai", "local-ai", "desktop", "rustdesk"} {
			purposeOK = purposeOK || s.Purpose == p
		}
		lifetimeOK := s.Lifetime == "until-revoked" && s.ExpiresAt.IsZero() || (s.Lifetime == "finite" || s.Lifetime == "") && !s.ExpiresAt.IsZero()
		if !resource.ValidID(s.ID) || s.ID <= previous || (s.Network != "tcp" && s.Network != "udp") || portsErr != nil || !purposeOK || !lifetimeOK || s.Application != "unverified" {
			return invalid()
		}
		previous = row.Service.ID
	}
	if *machine {
		return resourceCollectionJSON(out, observation)
	}
	fmt.Fprintln(out, text(ja, "Viewing does not grant connection permission. Recheck before connection.", "一覧閲覧は接続権限を与えません。接続前に再確認します。"))
	fmt.Fprintf(out, "%s: %s\n%s: %s\n", text(ja, "Peer", "相手"), peer, text(ja, "Review", "確認参照"), observation.ReviewRevision)
	for _, row := range observation.Services {
		s := row.Service
		fmt.Fprintf(out, "%s | %s %s | %s | %s | %s: %t\n", s.ID, s.Network, displayText(s.Ports), displayText(s.Purpose), displayText(s.Lifetime), text(ja, "Connection allowed", "接続許可"), row.ConnectAllowed)
		if !s.ExpiresAt.IsZero() {
			fmt.Fprintf(out, "  %s: %s\n", text(ja, "Expires", "期限"), s.ExpiresAt.UTC().Format(time.RFC3339))
		}
	}
	_, err = fmt.Fprintln(out, text(ja, "Use the exact row conditions with soba connect --catalog-review and --catalog-service. After failure, fetch a new list.", "正確な行の条件と soba connect --catalog-review・--catalog-service を使ってください。失敗後は新しい一覧を取得してください。"))
	return err
}

type catalogViewCLIObservation struct {
	Selector resourcegrant.CatalogViewSelector `json:"selector"`
	Services []struct {
		Service        core.RemoteService `json:"service"`
		ConnectAllowed bool               `json:"connectAllowed"`
	} `json:"services"`
	PeerKey        string    `json:"peerKey"`
	CheckedAt      time.Time `json:"checkedAt"`
	ReviewRevision string    `json:"reviewRevision"`
}

func readCatalogViewReview(path string) (resourcegrant.CatalogViewReview, error) {
	var review resourcegrant.CatalogViewReview
	f, err := (transfer.Source{LocalPath: path}).Open()
	if err != nil {
		return review, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > resourcegrant.MaxBytes {
		return review, resourcegrant.ErrInvalid
	}
	data, err := io.ReadAll(io.LimitReader(f, resourcegrant.MaxBytes+1))
	if err != nil || resource.Decode(data, resourcegrant.MaxBytes, &review) != nil || !resource.ValidDigest(review.BaseRevision) || !resource.ValidDigest(review.ReviewRevision) {
		return resourcegrant.CatalogViewReview{}, resourcegrant.ErrInvalid
	}
	return review, nil
}
