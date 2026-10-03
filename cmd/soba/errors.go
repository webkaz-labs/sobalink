package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

type jsonCommandError struct{ cause error }

func (e *jsonCommandError) Error() string { return e.cause.Error() }
func (e *jsonCommandError) Unwrap() error { return e.cause }

func writeCommandError(out io.Writer, err error) {
	var structured *jsonCommandError
	if !errors.As(err, &structured) {
		fmt.Fprintln(out, "soba:", err)
		return
	}
	code := "command_failed"
	var coded interface{ ErrorCode() string }
	if errors.As(err, &coded) && validErrorCode(coded.ErrorCode()) {
		code = coded.ErrorCode()
	} else if errors.Is(err, context.Canceled) {
		code = "canceled"
	} else if errors.Is(err, context.DeadlineExceeded) {
		code = "deadline_exceeded"
	}
	_ = json.NewEncoder(out).Encode(struct {
		Code  string `json:"code"`
		Error string `json:"error"`
	}{code, err.Error()})
}

func validErrorCode(code string) bool {
	if len(code) == 0 || len(code) > 64 {
		return false
	}
	for _, r := range code {
		if (r < 'a' || r > 'z') && r != '_' {
			return false
		}
	}
	return true
}

func localizeDiskSpaceError(ja bool, err error) error {
	if !ja {
		return err
	}
	var coded interface{ ErrorCode() string }
	if !errors.As(err, &coded) {
		return err
	}
	messages := map[string]string{
		"peer_storage_unavailable": "受信側で転送内容を保存できませんでした。受信側の保存容量と受信設定を確認して再試行してください",
		"disk_space_low":           "ディスク・ディスクquotaが満杯か、転送用に残す空き容量が不足しています。空き容量・quotaか diskReserveBytes を確認して再試行してください。元のファイルと保存済みファイルは残ります",
		"disk_space_unknown":       "ディスクの空き容量を確認できません。保存先のボリュームとアクセス権を確認して再試行してください。元のファイルと保存済みファイルは残ります",
		"peer_disk_space_low":      "受信側のディスク・ディスクquotaが満杯か、空き容量が不足しています。受信側の空き容量・quotaか diskReserveBytes を確認し、必要なら保留中のバッチを許可して再試行してください。保存済みファイルは残ります",
		"peer_disk_space_unknown":  "受信側で空き容量を確認できません。受信側の保存先ボリュームとアクセス権を確認し、必要なら保留中のバッチを許可して再試行してください。保存済みファイルは残ります",
	}
	if message := messages[coded.ErrorCode()]; message != "" {
		return &localizedDiskSpaceError{err, message}
	}
	return err
}

type localizedDiskSpaceError struct {
	cause   error
	message string
}

func (e *localizedDiskSpaceError) Error() string { return e.message }
func (e *localizedDiskSpaceError) Unwrap() error { return e.cause }
