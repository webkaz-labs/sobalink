package main

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/webkaz-labs/sobalink/internal/transfer"
)

func receiveRecoveryCommand(args []string, dir string, ja, dryRun bool, out io.Writer, query func(string, any, any) error) error {
	if len(args) < 2 || args[0] != "recovery" || args[1] != "confirm" {
		return usageError(ja, "receive")
	}
	flags := commandFlags("receive recovery confirm", ja, out)
	reviewed := flags.Bool("reviewed", false, text(ja, "Confirm all previous receive folders were reviewed and untracked partials resolved", "過去の全受信先を確認し、未追跡の途中保存を整理済みとして受信を再開"))
	structured := flags.Bool("json", false, text(ja, "stable machine JSON", "安定した機械向け JSON"))
	if err := parseFlags(flags, args[2:], ja); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return usageError(ja, "receive")
	}
	var view transfer.ReceiveRecoveryView
	if err := query("receive.recovery.confirm", map[string]bool{"reviewed": *reviewed && !dryRun}, &view); err != nil {
		return err
	}
	if *structured {
		return json.NewEncoder(out).Encode(view)
	}
	fmt.Fprintln(out, text(ja, "Review every previous default, per-peer and manually chosen receive folder. Check unfinished staging and previously saved output; keep saved files. Before confirming, resolve unfinished old receives so no untracked partial data remains. Unavailable or forgotten old locations require your own review. --reviewed confirms this review; it does not delete any files.", "以前の既定・相手別・手動指定のすべての受信先を確認してください。途中保存の残骸と保存済みファイルを確認し、保存済みファイルは残してください。確認操作の前に、未追跡の途中保存データが残らない状態に整理してください。不明・接続できない旧保存先はご自身で確認が必要です。--reviewed は確認済みを明示し、ファイルを削除しません。"))
	state := text(ja, "Ready", "受信可能")
	if view.State != "ready" {
		state = text(ja, "Receive blocked", "受信停止中")
	}
	fmt.Fprintf(out, "%s (%s)\n", state, view.Code)
	if view.Applied {
		fmt.Fprintln(out, text(ja, "Review saved; receiving can resume.", "確認済み状態を保存しました。受信を再開できます。"))
	} else if view.Code == "legacy_review_required" {
		fmt.Fprintf(out, "%s: %s\n", text(ja, "After reviewing", "確認後の操作"), cliCommandExample(dir, "receive", "recovery", "confirm", "--reviewed"))
	} else if view.State == "blocked" {
		fmt.Fprintln(out, text(ja, "Reconnect or repair the recorded receive storage and restart soba. Confirmation preserves existing records; it cannot discard damaged accounting.", "記録された受信ストレージの接続・状態を修復し、soba を再起動してください。確認操作は既存記録を維持し、破損した索引を破棄しません。"))
	}
	return nil
}
