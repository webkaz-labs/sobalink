//go:build lanlink_integration

package core

import (
	"context"
	"errors"
	"io"
	"testing"
)

func TestTextWireDiagnosticCategories(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{{nil, "none"}, {context.DeadlineExceeded, "deadline"}, {context.Canceled, "canceled"}, {io.EOF, "eof"}, {io.ErrUnexpectedEOF, "unexpected_eof"}, {errors.New("private payload and address"), "other"}} {
		if got := textWireErrorCategory(tc.err); got != tc.want {
			t.Fatalf("unexpected fixed category %q", got)
		}
	}
	if textWireOperation("private payload") != "other" {
		t.Fatal("unbounded operation")
	}
}
