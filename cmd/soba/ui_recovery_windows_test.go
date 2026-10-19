package main

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/control"
	"golang.org/x/sys/windows"
)

func TestUIRecoveryWindowsMissingNamedPipeClassification(t *testing.T) {
	dir := existingUIProfile(t)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	err := control.Call(ctx, dir, "ui", nil)
	if !control.Unavailable(err) || (!errors.Is(err, windows.ERROR_FILE_NOT_FOUND) && !errors.Is(err, windows.ERROR_PATH_NOT_FOUND)) {
		t.Fatal("missing native pipe was not classified", err)
	}
	for _, failure := range []error{windows.ERROR_ACCESS_DENIED, os.ErrPermission, context.DeadlineExceeded} {
		if control.Unavailable(failure) {
			t.Fatal("non-absence Windows failure hidden", failure)
		}
	}
}
