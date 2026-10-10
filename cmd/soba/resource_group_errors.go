package main

import (
	"context"
	"errors"
)

type resourceCollectionErrorText struct{ en, ja string }

var resourceCollectionErrorTexts = map[string]resourceCollectionErrorText{
	"resource_group_invalid":                  {"Use the exact bounded group selection, review and explicit peer subset; apply requires --confirm. See resource group --help", "正確な上限内の選択・確認内容・相手の一覧を指定してください。apply には --confirm が必要です。resource group --help を参照してください"},
	"resource_group_file_invalid":             {"Use an unchanged bounded regular Selection or preview JSON file; symlinks, special files and executable command payloads are not accepted", "変更されていない上限内の通常ファイルで Selection または preview の JSON を指定してください。シンボリックリンク・特殊ファイル・実行コマンドを含むデータは受け付けません"},
	"resource_group_response_invalid":         {"The group response did not match the exact request; no response is presented as approval or successful execution. Inspect the same known run locally before proceeding", "グループの応答が正確な要求と一致しません。承認や実行成功として扱いません。続ける前に同じ既知の実行をローカルで確認してください"},
	"resource_group_busy":                     {"A group operation is active; inspect its local status or explicitly cancel further admission", "グループ操作が進行中です。ローカルの状態を確認するか、新たな受付を明示的に取り消してください"},
	"resource_group_review_unavailable":       {"The current-process review is unavailable; inspect historical run status if already applied, otherwise obtain a new explicit preview", "現在のプロセスの確認内容を利用できません。適用済みなら過去の実行状態を確認し、未適用なら明示的に新しい preview を取得してください"},
	"resource_group_review_changed":           {"The local review or context changed; inspect the exact selection and obtain a new explicit review", "ローカルの確認内容または実行環境が変わりました。正確な選択を調べ、明示的に新しく確認してください"},
	"resource_group_reconcile_required":       {"Unresolved evidence blocks a new operation; inspect the known run and explicitly refresh only its original peers. Do not replay unknown work", "未解決の記録が新たな操作を遮断しています。既知の実行を確認し、元の相手だけを明示的に照会してください。結果不明の処理を再実行しないでください"},
	"resource_group_capacity":                 {"Bounded local group evidence capacity is exhausted; unresolved records cannot be evicted or automatically split", "ローカルのグループ記録の上限に達しました。未解決の記録を削除したり、自動分割したりできません"},
	"resource_group_storage_unavailable":      {"Private group evidence is unavailable or uncertain; stop new admission and inspect the owning agent. Do not delete state to recreate approval", "非公開のグループ記録を利用できないか保存結果が不明です。新たな受付を止め、本体の状態を確認してください。承認を作り直すために記録を削除しないでください"},
	"resource_group_unavailable":              {"The owning group context is unavailable; verify the selected agent and inspect the same known run before further work", "グループを所有する実行環境を利用できません。選択した本体と同じ既知の実行を確認してから続けてください"},
	"resource_catalog_invalid":                {"Select exact catalog sources and at most one remote peer; no arbitrary commands or JSON patches are accepted. See resource catalog --help", "正確なカタログ情報源と最大1台の相手を指定してください。任意のコマンドや JSON パッチは受け付けません。resource catalog --help を参照してください"},
	"resource_catalog_response_invalid":       {"The catalog response did not match the exact selected sources; refresh explicitly before using existing workflows", "カタログの応答が正確な選択情報源と一致しません。既存の操作に進む前に明示的に更新してください"},
	"resource_catalog_resolution_unavailable": {"Local settings could not be resolved from resource list. No complete all-local catalog was produced; retry resource list or explicitly select only --services --transfers", "resource list からローカル設定を特定できませんでした。ローカル全体の完全なカタログは作成していません。resource list を確認するか、--services --transfers だけを明示的に選択してください"},
	"resource_catalog_unavailable":            {"The local catalog context is unavailable; verify the owning agent and refresh. Unknown or unreachable control does not prove the catalog is unsupported", "ローカルのカタログ実行環境を利用できません。本体を確認して更新してください。不明または接続不能という結果だけでカタログ非対応とは判断できません"},
	"resource_catalog_capacity":               {"The selected catalog cannot fit its finite view budget; explicitly select fewer sources or review the configured resource page budget", "選択したカタログが表示上限に収まりません。情報源を明示的に減らすか、リソースのページ上限設定を確認してください"},
}

func resourceCollectionError(code string, cause error) error {
	if value, ok := resourceCollectionErrorTexts[code]; ok {
		return &resourceCLIError{code: code, message: value.en, cause: cause}
	}
	return resourceControlError(cause)
}
func resourceCollectionControlError(err error) error {
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	var coded interface{ ErrorCode() string }
	if errors.As(err, &coded) {
		if _, ok := resourceCollectionErrorTexts[coded.ErrorCode()]; ok {
			return resourceCollectionError(coded.ErrorCode(), err)
		}
	}
	return resourceControlError(err)
}
func localizeResourceCollectionError(ja bool, err error) error {
	if !ja {
		return err
	}
	var coded interface{ ErrorCode() string }
	if errors.As(err, &coded) {
		if value, ok := resourceCollectionErrorTexts[coded.ErrorCode()]; ok {
			return &localizedDiskSpaceError{err, value.ja}
		}
	}
	return err
}
