package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/core"
	"github.com/webkaz-labs/sobalink/internal/webui"
)

func favoritesCLI(ctx context.Context, args []string, dir string, ja, dryRun bool, out io.Writer, client controlCaller) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		if len(args) > 1 {
			return usageError(ja, "favorites list|add|remove")
		}
		_, err := fmt.Fprintln(out, text(ja, favoritesHelpEN, favoritesHelpJA))
		return err
	}
	action := args[0]
	if action != "list" && action != "add" && action != "remove" {
		return usageError(ja, "favorites list | favorites add/remove service ID|group NAME --review REVISION [--json]")
	}
	var payload any = struct{}{}
	f := commandFlags("favorites "+action, ja, out)
	machine := f.Bool("json", false, text(ja, "stable JSON output", "機械処理用の安定した JSON 出力"))
	if action == "list" {
		if err := parseFlags(f, args[1:], ja); err != nil {
			return err
		}
	} else {
		review := f.String("review", "", text(ja, "current preference revision from favorites list", "favorites list に表示された現在のお気に入り revision"))
		if len(args) == 2 && (args[1] == "--help" || args[1] == "-h") {
			_, err := fmt.Fprintln(out, text(ja, favoritesHelpEN, favoritesHelpJA))
			return err
		}
		if len(args) < 3 {
			return usageError(ja, "favorites "+action+" service ID|group NAME --review REVISION [--json]")
		}
		ref := core.FavoriteReference{Kind: args[1]}
		switch ref.Kind {
		case "service":
			ref.ServiceID = args[2]
			if !config.ValidPeerID(ref.ServiceID) {
				return errors.New(text(ja, "Use an exact saved service ID", "保存済みサービスの正確な ID を指定してください"))
			}
		case "group":
			ref.GroupName = args[2]
			if !config.ValidName(ref.GroupName) {
				return errors.New(text(ja, "Use an exact saved group name", "保存済みグループの正確な名前を指定してください"))
			}
		default:
			return errors.New(text(ja, "Favorite type must be service or group", "お気に入りの種類は service または group を指定してください"))
		}
		if err := parseFlags(f, args[3:], ja); err != nil {
			return err
		}
		decoded, err := hex.DecodeString(*review)
		if err != nil || len(decoded) != 32 {
			return errors.New(text(ja, "List favorites first, then pass --review REVISION", "先に favorites list を実行し、--review REVISION を指定してください"))
		}
		payload = core.FavoriteChangeRequest{Reference: ref, ExpectedRevision: *review}
	}
	write := func(value any) error {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(value)
	}
	name := "favorites." + action
	if dryRun {
		return write(map[string]any{"applied": false, "command": name, "payload": payload, "validation": "local-input-only"})
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	// Reads also get a new request ID: cached reads must not hide changes in
	// preferences or target availability after a later mutation/reload.
	request, err := json.Marshal(webui.Command{RequestID: newCLIRequestID("cli-favorites"), Name: name, Payload: raw})
	if err != nil {
		return err
	}
	var view core.FavoritesView
	if err := client(ctx, dir, string(request), &view); err != nil {
		return err
	}
	if *machine {
		return write(view)
	}
	fmt.Fprintf(out, text(ja, "Favorites: %d\nPreference revision: %s\n", "お気に入り: %d 件\nお気に入り revision: %s\n"), len(view.Entries), view.Revision)
	for _, entry := range view.Entries {
		kind, target := text(ja, "Service", "サービス"), entry.ServiceID
		if entry.Kind == "group" {
			kind, target = text(ja, "Group", "グループ"), entry.GroupName
		}
		state := text(ja, "missing; remove explicitly if no longer needed", "対象なし。不要な場合は明示的に削除してください")
		if entry.Available {
			state = text(ja, "saved definition available", "保存済み設定あり")
		}
		fmt.Fprintf(out, "%s %s: %s\n", kind, target, state)
	}
	if view.DurabilityUncertain {
		fmt.Fprintln(out, text(ja, "The last write was published but durability is uncertain; inspect private storage before retrying", "直前の書込みは反映されましたが、永続化を確認できません。再実行する前に非公開の保存先を確認してください"))
	}
	_, err = fmt.Fprintln(out, text(ja, "Favorites only mark saved references; review the current service scope before starting", "お気に入りは保存済み設定への目印です。開始前に現在のサービス範囲を確認してください"))
	return err
}

func localizeFavoritesError(ja bool, err error) error {
	if !ja {
		return err
	}
	var coded interface{ ErrorCode() string }
	if !errors.As(err, &coded) {
		return err
	}
	messages := map[string]string{
		"favorites_invalid_request":       "お気に入りの入力を確認してください。service ID または正確な group 名を1つ指定し、profileBytes の容量内に収めてください",
		"favorites_unavailable":           "お気に入りを読み書きできません。非公開のお気に入りファイルの形式・アクセス権・空き容量と profileBytes を確認してください",
		"favorites_revision_conflict":     "お気に入りが変わっています。favorites list で再確認し、現在の revision を指定してください",
		"favorites_target_missing":        "対象の保存済みサービスまたはグループがありません。現在の保存済み設定を確認してください",
		"favorites_capacity":              "お気に入りが profileBytes の容量を超えています。不要なお気に入りを削除するか保存容量を確認してください",
		"favorites_persistence_uncertain": "お気に入りは置換されましたが、永続化を確認できません。再実行前に favorites list と非公開の保存先を確認してください",
	}
	if message := messages[coded.ErrorCode()]; message != "" {
		return &localizedDiskSpaceError{err, message}
	}
	return err
}

const favoritesHelpEN = `Inert favorites for saved services and groups

  soba favorites list [--json]
  soba favorites add service SERVICE_ID --review REVISION [--json]
  soba favorites add group GROUP_NAME --review REVISION [--json]
  soba favorites remove service SERVICE_ID --review REVISION [--json]
  soba favorites remove group GROUP_NAME --review REVISION [--json]

Use the preference revision from favorites list. Service IDs and group names
are exact; favorites do not copy membership or permissions. Missing references
remain visible and can be removed explicitly. Adding requires an existing target.
A favorite never starts a listener, creates startup approval or extends expiry.
Review the full current service scope before starting, including imported or
reused names. Add --offline before favorites when the agent is stopped; offline
commands only edit local metadata under the profile lock. --dry-run previews
without applying, and --json keeps the same fields in every locale.`

const favoritesHelpJA = `保存済みサービスとグループのお気に入り

  soba favorites list [--json]
  soba favorites add service SERVICE_ID --review REVISION [--json]
  soba favorites add group GROUP_NAME --review REVISION [--json]
  soba favorites remove service SERVICE_ID --review REVISION [--json]
  soba favorites remove group GROUP_NAME --review REVISION [--json]

favorites list のお気に入り revision を指定してください。サービス ID と
グループ名は完全一致です。メンバーや許可は複製しません。対象がなくなっても
参照は表示され、明示的に削除できます。追加時には現在の対象が必要です。
お気に入りの操作は入口の開始・自動起動の承認・期限の延長を行いません。
読込みや名前の再利用を含め、開始前に現在のサービス範囲全体を確認してください。
本体が停止中は favorites の前に --offline を指定すると、プロファイルを
ロックしてローカルの情報だけを編集します。--dry-run は適用せずに確認します。
--json のフィールドは言語によらず同じです。`
