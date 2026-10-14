package main

import "errors"

// Only the proven durable acceptance code offers a same-target retry. This is
// presentation for one explicit command, never a second invocation or lookup.
func localizeTransferAcceptError(ja bool, err error) error {
	if err == nil {
		return nil
	}
	var coded interface{ ErrorCode() string }
	if errors.As(err, &coded) {
		switch coded.ErrorCode() {
		case "disk_space_low", "disk_space_unknown", "peer_disk_space_low", "peer_disk_space_unknown", "peer_storage_unavailable":
			return err // Retain the established CLI storage-guidance family.
		}
	}
	if errors.As(err, &coded) && coded.ErrorCode() == "transfer_accept_busy" {
		return &localizedDiskSpaceError{err, text(ja,
			"Another local storage operation is busy. This attempt saved no new acceptance; earlier acceptance may exist. Wait briefly, then explicitly run accept again with the same transfer ID, directory and profile.",
			"別のローカル保存処理が実行中です。今回の操作では新たな受入承認は保存されていません。以前の承認が残っている可能性があります。少し待ってから、同じ転送ID・保存先・プロファイルで accept を明示的にもう一度実行してください。")}
	}
	return &localizedDiskSpaceError{err, text(ja,
		"Acceptance could not be confirmed. Before another action, review the original transfer and the receiver's storage state. Earlier acceptance may exist.",
		"受入結果を確認できません。次の操作の前に、元の転送と受信側の保存状態を確認してください。以前の承認が残っている可能性があります。")}
}
