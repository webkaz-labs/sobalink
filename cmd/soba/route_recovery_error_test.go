package main

import (
	"context"
	"errors"
	"github.com/webkaz-labs/sobalink/internal/control"
	"io"
	"strings"
	"testing"
)

func TestRouteRecoveryErrorExplainsRestartBoundary(t *testing.T) {
	for _, locale := range []string{"en", "ja"} {
		for _, structured := range []bool{false, true} {
			remote := &control.RemoteError{Code: "lan_routes_recovery", Message: "stop soba; old permissions may remain on disk; inspect and reconcile saved approvals before restarting"}
			args := []string{"--locale", locale}
			if structured {
				args = append(args, "--json-errors")
			}
			args = append(args, "lan", "routes", "list")
			err := runWith(context.Background(), args, io.Discard, strings.NewReader(""), func(context.Context, string, string, any) error { return remote })
			var coded interface{ ErrorCode() string }
			if err == nil || !errors.As(err, &coded) || coded.ErrorCode() != remote.Code {
				t.Fatal("recovery code lost", err)
			}
			if locale == "ja" && !structured {
				for _, word := range []string{"停止", "以前の許可", "確認・修正", "再起動"} {
					if !strings.Contains(err.Error(), word) {
						t.Fatal("missing recovery action", word, err)
					}
				}
			} else if !strings.Contains(err.Error(), remote.Message) {
				t.Fatal("machine/English message changed", err)
			}
		}
	}
}
