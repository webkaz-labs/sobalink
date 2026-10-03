package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/control"
)

func TestCLIJSONErrorPreservesRemoteCode(t *testing.T) {
	var output bytes.Buffer
	err := runWith(t.Context(), []string{"--json-errors", "--locale", "ja", "status"}, &output, strings.NewReader(""), func(context.Context, string, string, any) error {
		return &control.RemoteError{Code: "service_revision_conflict", Message: "reload the saved configuration"}
	})
	if err == nil || output.Len() != 0 {
		t.Fatal("failed operation wrote success output", err, output.String())
	}
	writeCommandError(&output, err)
	var result struct{ Code, Error string }
	if json.Unmarshal(output.Bytes(), &result) != nil || result.Code != "service_revision_conflict" || !strings.Contains(result.Error, "reload the saved configuration") {
		t.Fatal(output.String())
	}
}

func TestCLIErrorOutputModes(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code string
	}{
		{context.Canceled, "canceled"},
		{context.DeadlineExceeded, "deadline_exceeded"},
		{errors.New("invalid input"), "command_failed"},
		{&control.RemoteError{Code: "invalid payload", Message: "invalid input"}, "command_failed"},
	} {
		var output bytes.Buffer
		writeCommandError(&output, &jsonCommandError{tc.err})
		var result struct{ Code string }
		if json.Unmarshal(output.Bytes(), &result) != nil || result.Code != tc.code {
			t.Fatal(output.String())
		}
	}
	var output bytes.Buffer
	writeCommandError(&output, errors.New("human-readable failure"))
	if output.String() != "soba: human-readable failure\n" {
		t.Fatal(output.String())
	}
	output.Reset()
	if err := runWith(t.Context(), []string{"--json-errors", "--help"}, &output, strings.NewReader(""), nil); err != nil {
		t.Fatal(err)
	}
}
