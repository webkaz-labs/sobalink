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
