package main

import (
	"context"
	"fmt"
	"io"

	"github.com/webkaz-labs/sobalink/internal/resourcegroup"
)

// Retrieval is one explicit local command. It never confirms or dispatches a run.
func resourceGroupCurrentReviewCLI(ctx context.Context, args []string, dir string, ja, dryRun bool, out io.Writer, client controlCaller) error {
	if len(args) == 0 || args[0] != "current" {
		return resourceCollectionError("resource_group_invalid", nil)
	}
	f := commandFlags("resource group review current", ja, out)
	machine := f.Bool("json", false, text(ja, "stable machine JSON", "機械処理用の安定した JSON"))
	if err := parseFlags(f, args[1:], ja); err != nil {
		return err
	}
	in := resourcegroup.CurrentReviewInput{SchemaVersion: resourcegroup.SchemaVersion}
	if dryRun {
		return resourceCollectionJSON(out, map[string]any{"applied": false, "command": resourcegroup.LocalCurrentReviewCommand, "payload": in, "validation": "local-input-only"})
	}
	raw, err := resourceCollectionCall(ctx, dir, resourcegroup.LocalCurrentReviewCommand, in, client)
	if err != nil {
		return err
	}
	view, err := resourcegroup.DecodeCurrentReviewView(raw)
	if err != nil {
		return resourceCollectionError("resource_group_response_invalid", nil)
	}
	if *machine {
		return resourceCollectionJSON(out, view)
	}
	if view.Prepared == nil {
		_, err := fmt.Fprintln(out, text(ja, "No unused review. This does not establish whether earlier work executed; use its known run ID to read status.", "未使用の確認はありません。過去の処理が実行されたかは分かりません。既知の実行 ID で status を確認してください。"))
		return err
	}
	if _, err := fmt.Fprintln(out, text(ja, "Recovered current review; no apply was requested. Confirm the complete selection again before applying.", "現在の確認を取得しました。適用は要求していません。適用前に選択内容全体を改めて確認してください。")); err != nil {
		return err
	}
	return writeResourceGroupPrepared(out, dir, ja, *view.Prepared)
}
