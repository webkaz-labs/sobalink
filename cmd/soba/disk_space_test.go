package main

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/control"
)

func TestDiskSpaceErrorsLocalizeHumanOutputAndKeepJSONStable(t *testing.T) {
	for _, code := range []string{"peer_storage_unavailable", "disk_space_low", "disk_space_unknown", "peer_disk_space_low", "peer_disk_space_unknown"} {
		for _, locale := range []string{"en", "ja"} {
			for _, structured := range []bool{false, true} {
				remote := &control.RemoteError{Code: code, Message: "check storage and retry"}
				args := []string{"--locale", locale}
				if structured {
					args = append(args, "--json-errors")
				}
				args = append(args, "accept", "batch", t.TempDir())
				err := runWith(context.Background(), args, io.Discard, strings.NewReader(""), func(context.Context, string, string, any) error { return remote })
				if err == nil {
					t.Fatal("missing error")
				}
				var coded interface{ ErrorCode() string }
				if !errors.As(err, &coded) || coded.ErrorCode() != code {
					t.Fatal("stable code lost")
				}
				if locale == "ja" && !structured {
					if !strings.Contains(err.Error(), "再試行") {
						t.Fatal("Japanese recovery missing")
					}
				} else if !strings.Contains(err.Error(), remote.Message) {
					t.Fatal("machine/English detail changed")
				}
			}
		}
	}
}
