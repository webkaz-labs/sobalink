package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"

	"github.com/webkaz-labs/sobalink/internal/core"
	"github.com/webkaz-labs/sobalink/internal/resource"
	"github.com/webkaz-labs/sobalink/internal/resourcecatalog"
)

const resourceCatalogHelpEN = `Observe existing resource sources locally (source build)

  soba resource catalog [--json]
  soba resource catalog --services --transfers [--json]
  soba resource catalog --settings [--settings-id RESOURCE_ID] [--json]
  soba resource catalog --discovery-peer PEER_ID [--json]
  soba resource catalog --remote-version 1|2 --peer PEER_KEY --id RESOURCE_ID --grant-id GRANT_ID --grant-revision N [--json]

With no source flags, resolve resource.list then observe local settings, saved services and
process-local transfer activity. A failed settings resolution does not produce a complete
all-local view: retry resource list or explicitly choose the narrower --services --transfers.
Explicit source flags replace the default set and can be combined. --settings-id selects settings.
Select at most one remote identity: discovery and remote settings must name the same exact peer
if combined. v1 uses an existing inspect-only grant; v2 uses an existing management grant for
inspection only. No protocol fallback, all-peers enumeration, new permission or listener.
The catalog reads existing discovery observations; use discover explicitly for a network refresh.
In this source slice cached discovery remains stale/non-actionable even after that refresh.
Human output includes scope, observation time, identity lifetime and partial/limited states.
Current means observed at the shown source time, never continuously watched. This is not an
atomic cross-source snapshot. Settings, services and transfers have no invented reachability TTL.
Next steps are existing fresh-review workflows. No apply, start, stop, connect, accept or retry
is performed by catalog. Transfer rows describe activity, not durable file shares or file browsing.
--json returns the same stable validated envelope in either language.
--dry-run makes zero agent calls. Unresolved local settings stay unresolved; no target is invented.
Unknown/unreachable local control does not establish catalog support. Only an explicit authenticated
unsupported source response is labeled unsupported. --offline and arbitrary JSON patches are unsupported.`

const resourceCatalogHelpJA = `既存のリソース情報源をローカルで観測（ソースビルド）

  soba resource catalog [--json]
  soba resource catalog --services --transfers [--json]
  soba resource catalog --settings [--settings-id RESOURCE_ID] [--json]
  soba resource catalog --discovery-peer PEER_ID [--json]
  soba resource catalog --remote-version 1|2 --peer PEER_KEY --id RESOURCE_ID --grant-id GRANT_ID --grant-revision N [--json]

情報源を指定しない場合は resource.list で対象を特定し、ローカル設定・保存済みサービス・
現在のプロセスの転送状況を観測します。設定の特定に失敗した場合、ローカル全体の完全な一覧として
返しません。resource list を確認するか、--services --transfers だけを明示的に選択してください。
情報源フラグの明示指定は既定の選択を置き換えます。併用できます。--settings-id も設定を選択します。
相手は最大1台です。discovery と遠隔設定を併用するときは同じ正確な相手を指定してください。
v1 は既存の参照専用許可、v2 は既存の管理許可を参照にのみ使います。通信方式の自動切替・
全相手の列挙・新しい許可・接続口の開始はありません。
既存のサービス発見の観測結果を読みます。通信して更新するには discover を明示的に実行してください。
今回のソースでは更新後もカタログの発見キャッシュを古い・操作不能な状態として扱います。
表示には範囲・観測時刻・識別子の有効期間・一部取得や上限到達の状態を含みます。
current は表示時刻で観測できたことだけを示し、常時監視ではありません。情報源全体を同時に
読み取るものではありません。設定・サービス・転送に架空の到達性の有効期限を付けません。
次の操作は既存の再確認手順へ進みます。適用・開始・停止・接続・受領・再試行は実行しません。
転送行は活動状況で、永続的なファイル共有やファイル閲覧ではありません。
--json は検査済みの同じ安定した形式を言語によらず返します。
--dry-run は本体に一度も接続しません。不明なローカル設定の対象 ID を作りません。
ローカル操作が不明・接続不能なだけではカタログ対応を判定できません。認証済みの情報源が
明示的に非対応と返した場合だけ非対応と表示します。--offline や任意の JSON パッチは使えません。`

// Pointer target permits exact source arms: a service selector has no target key.
// Core validates this closed request independently before any provider call.
type resourceCatalogCLISource struct {
	Kind          string           `json:"kind"`
	Target        *resource.Target `json:"target,omitempty"`
	PeerID        string           `json:"peerId,omitempty"`
	PeerKey       string           `json:"peerKey,omitempty"`
	GrantID       string           `json:"grantId,omitempty"`
	GrantRevision uint64           `json:"grantRevision,omitempty"`
}
type resourceCatalogCLIRequest struct {
	SchemaVersion int                        `json:"schemaVersion"`
	Sources       []resourceCatalogCLISource `json:"sources"`
}

func resourceCatalogCLI(ctx context.Context, args []string, dir string, ja, dryRun bool, out io.Writer, client controlCaller) error {
	if len(args) == 1 && resourceCollectionHelp(args[0]) {
		_, err := fmt.Fprintln(out, text(ja, resourceCatalogHelpEN, resourceCatalogHelpJA))
		return err
	}
	f := commandFlags("resource catalog", ja, out)
	machine := f.Bool("json", false, text(ja, "stable catalog envelope JSON", "安定したカタログ形式の JSON"))
	settings := f.Bool("settings", false, text(ja, "local transfer settings", "ローカルの転送設定"))
	services := f.Bool("services", false, text(ja, "saved local services", "保存済みのローカルサービス"))
	transfers := f.Bool("transfers", false, text(ja, "current-process transfer activity", "現在のプロセスの転送状況"))
	var settingsID, discoveryPeer, version, peer, id, grant, grantRevision string
	f.StringVar(&settingsID, "settings-id", "", text(ja, "exact optional local settings resource ID", "任意の正確なローカル設定リソース ID"))
	f.StringVar(&discoveryPeer, "discovery-peer", "", text(ja, "one exact peer's existing service discovery observations", "正確な相手1台の既存サービス発見の観測結果"))
	f.StringVar(&version, "remote-version", "", text(ja, "explicit remote settings inspection protocol: 1 or 2", "遠隔設定の参照方式を明示: 1 または 2"))
	f.StringVar(&peer, "peer", "", text(ja, "exact remote managed peer key", "遠隔の管理対象の正確な相手鍵"))
	f.StringVar(&id, "id", "", text(ja, "exact remote resource ID", "遠隔の正確なリソース ID"))
	f.StringVar(&grant, "grant-id", "", text(ja, "exact existing inspection or management grant", "既存の正確な参照または管理の許可 ID"))
	f.StringVar(&grantRevision, "grant-revision", "", text(ja, "current finite grant revision", "現在の有限の許可の変更番号"))
	if err := parseFlags(f, args, ja); err != nil {
		return err
	}
	invalid := func() error { return resourceCollectionError("resource_catalog_invalid", nil) }
	explicit := resourceCollectionFlagSet(f, "settings", "services", "transfers", "settings-id", "discovery-peer", "remote-version", "peer", "id", "grant-id", "grant-revision")
	if !explicit {
		*settings, *services, *transfers = true, true, true
	}
	if resourceCollectionFlagSet(f, "settings-id") {
		if !resource.ValidID(settingsID) || resourceCollectionFlagSet(f, "settings") && !*settings {
			return invalid()
		}
		*settings = true
	}
	if resourceCollectionFlagSet(f, "discovery-peer") && discoveryPeer == "" {
		return invalid()
	}
	remoteFlags := resourceCollectionFlagSet(f, "remote-version", "peer", "id", "grant-id", "grant-revision")
	if remoteFlags && version != "1" && version != "2" {
		return invalid()
	}
	request := resourceCatalogCLIRequest{SchemaVersion: 1, Sources: []resourceCatalogCLISource{}}
	if *settings && settingsID != "" {
		target := resource.Target{SchemaVersion: resource.SchemaVersion, ResourceID: settingsID}
		request.Sources = append(request.Sources, resourceCatalogCLISource{Kind: resourcecatalog.LocalSettings, Target: &target})
	}
	if *services {
		request.Sources = append(request.Sources, resourceCatalogCLISource{Kind: resourcecatalog.LocalService})
	}
	if *transfers {
		request.Sources = append(request.Sources, resourceCatalogCLISource{Kind: resourcecatalog.TransferActivity})
	}
	if discoveryPeer != "" {
		request.Sources = append(request.Sources, resourceCatalogCLISource{Kind: resourcecatalog.RemoteService, PeerID: discoveryPeer})
	}
	if remoteFlags {
		number, err := strconv.ParseUint(grantRevision, 10, 64)
		if err != nil {
			return invalid()
		}
		target := resource.Target{SchemaVersion: resource.SchemaVersion, ResourceID: id}
		kind := resourcecatalog.RemoteSettingsV1
		if version == "2" {
			kind = resourcecatalog.RemoteSettingsV2
		}
		request.Sources = append(request.Sources, resourceCatalogCLISource{Kind: kind, Target: &target, PeerKey: peer, GrantID: grant, GrantRevision: number})
	}
	sort.Slice(request.Sources, func(i, j int) bool { return request.Sources[i].Kind < request.Sources[j].Kind })
	unresolved := *settings && settingsID == ""
	// Validate every concrete arm before resolution or any other agent call.
	if len(request.Sources) > 0 {
		raw, _ := json.Marshal(request)
		if core.ValidateResourceCatalogRequest(raw) != nil {
			return invalid()
		}
	} else if !unresolved {
		return invalid()
	}
	if dryRun {
		if unresolved {
			kinds := []string{resourcecatalog.LocalSettings}
			for _, source := range request.Sources {
				kinds = append(kinds, source.Kind)
			}
			sort.Strings(kinds)
			return resourceCollectionJSON(out, struct {
				Applied              bool                       `json:"applied"`
				Command              string                     `json:"command"`
				RequestedSources     []string                   `json:"requestedSources"`
				ResolvedSources      []resourceCatalogCLISource `json:"resolvedSources"`
				UnresolvedSources    []string                   `json:"unresolvedSources"`
				RequiresResourceList bool                       `json:"requiresResourceList"`
				Validation           string                     `json:"validation"`
			}{false, "resource.catalog.snapshot", kinds, request.Sources, []string{resourcecatalog.LocalSettings}, true, "local-input-only"})
		}
		return resourceCollectionJSON(out, map[string]any{"applied": false, "command": "resource.catalog.snapshot", "payload": request, "validation": "local-input-only"})
	}
	if unresolved {
		target, err := resourceCatalogResolveLocal(ctx, dir, client)
		if err != nil {
			return err
		}
		request.Sources = append(request.Sources, resourceCatalogCLISource{Kind: resourcecatalog.LocalSettings, Target: &target})
		sort.Slice(request.Sources, func(i, j int) bool { return request.Sources[i].Kind < request.Sources[j].Kind })
	}
	raw, _ := json.Marshal(request)
	if core.ValidateResourceCatalogRequest(raw) != nil {
		return invalid()
	}
	result, err := resourceCollectionCall(ctx, dir, "resource.catalog.snapshot", request, client)
	if err != nil {
		return err
	}
	response, err := core.DecodeResourceCatalogResponse(result)
	if err != nil || !resourceCatalogMatches(request, response) {
		return resourceCollectionError("resource_catalog_response_invalid", nil)
	}
	if *machine {
		return resourceCollectionJSON(out, response)
	}
	return writeResourceCatalogHuman(out, dir, ja, response)
}

func resourceCatalogResolveLocal(ctx context.Context, dir string, client controlCaller) (resource.Target, error) {
	raw, err := resourceCollectionCall(ctx, dir, "resource.list", struct{}{}, client)
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return resource.Target{}, err
	}
	unresolved := func() (resource.Target, error) {
		return resource.Target{}, resourceCollectionError("resource_catalog_resolution_unavailable", err)
	}
	if err != nil {
		return unresolved()
	}
	// resource.list currently has exactly one narrow settings descriptor. This
	// bound applies to that discovery response, not configurable catalog pages.
	const listBytes = 16 * 1024
	var list resource.Catalog
	if resource.Decode(raw, listBytes, &list) != nil || list.SchemaVersion != resource.SchemaVersion || len(list.Resources) != 1 {
		return unresolved()
	}
	descriptor := list.Resources[0]
	selection := resourcecatalog.Selection{SourceID: resourcecatalog.LocalSettings, Kind: resourcecatalog.LocalSettings, Epoch: "local-cli-resolution", Target: descriptor.Target}
	limits := resourcecatalog.Limits{MaxSources: 1, MaxRemoteTargets: 1, MaxRows: 1, MaxPageRows: 1, MaxPages: 1, MaxBytes: listBytes, MaxPageBytes: listBytes, MaxStringBytes: listBytes}
	if _, err := resourcecatalog.ProjectLocalSettings(selection, descriptor, limits); err != nil {
		return unresolved()
	}
	return descriptor.Target, nil
}

func resourceCatalogMatches(request resourceCatalogCLIRequest, response core.ResourceCatalogResponse) bool {
	if len(request.Sources) != len(response.Snapshot.Sources) {
		return false
	}
	for i, want := range request.Sources {
		got := response.Snapshot.Sources[i].Selection
		if got.Kind != want.Kind || got.SourceID != want.Kind || got.GrantID != want.GrantID || got.GrantRevision != want.GrantRevision {
			return false
		}
		target := resource.Target{}
		if want.Target != nil {
			target = *want.Target
		}
		if got.Target != target {
			return false
		}
		peer := want.PeerKey
		if want.Kind == resourcecatalog.RemoteService {
			peer = want.PeerID
		}
		if got.PeerKey != peer {
			return false
		}
		// Epoch, scope, snapshot and process IDs are validated server-owned
		// correlation data; never replace them with caller-generated authority.
	}
	return true
}
