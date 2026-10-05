package main

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/webkaz-labs/sobalink/internal/core"
	"strings"
	"testing"
)

func TestReviewCLIUsesActualOfflineIPCShape(t *testing.T) {
	c, err := core.Open(context.Background(), core.Options{Directory: t.TempDir(), SkipNetworkStart: true})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for _, args := range [][]string{{"direct-lan", "status"}, {"mixed", "show"}, {"mixed", "show", "--json"}, {"lan", "wan", "show"}, {"lan", "wan", "show", "--json"}, {"lan", "policy", "show"}} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			var out bytes.Buffer
			err := runWith(context.Background(), args, &out, strings.NewReader(""), func(ctx context.Context, dir, raw string, target any) error {
				result, e := c.IPC(ctx, raw)
				if e != nil {
					return e
				}
				data, e := json.Marshal(result)
				if e != nil {
					return e
				}
				return json.Unmarshal(data, target)
			})
			if err != nil {
				t.Fatalf("real Core.IPC result rejected: %v", err)
			}
			t.Log(out.String())
		})
	}
}

func TestReviewMixedHelpDispatch(t *testing.T) {
	for _, args := range [][]string{{"mixed", "--help"}, {"mixed", "-h"}, {"help", "mixed"}, {"mixed", "setup", "--help"}} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			var out bytes.Buffer
			err := runWith(context.Background(), args, &out, strings.NewReader(""), nil)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), "backends") {
				t.Fatalf("no mixed usage in output: %s", out.String())
			}
		})
	}
}
