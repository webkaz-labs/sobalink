package main

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/webkaz-labs/sobalink/internal/core"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReviewDamagedMixedStatusNotUnconfigured(t *testing.T) {
	dir := t.TempDir()
	c, e := core.Open(context.Background(), core.Options{Directory: dir, SkipNetworkStart: true})
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	if e = os.WriteFile(filepath.Join(dir, "mixed.json"), []byte(`{}`), 0600); e != nil {
		t.Fatal(e)
	}
	for _, locale := range []string{"en", "ja"} {
		t.Run(locale, func(t *testing.T) {
			var out bytes.Buffer
			err := runWith(context.Background(), []string{"--locale", locale, "mixed", "show"}, &out, strings.NewReader(""), func(ctx context.Context, dir, raw string, v any) error {
				res, e := c.IPC(ctx, raw)
				if e != nil {
					return e
				}
				b, e := json.Marshal(res)
				if e != nil {
					return e
				}
				return json.Unmarshal(b, v)
			})
			if err == nil {
				t.Fatalf("damaged saved state reported success: %s", out.String())
			}
			expected := "Saved mixed configuration is unavailable"
			if locale == "ja" {
				expected = "保存済みの複数方式設定を確認できません"
			}
			if !strings.Contains(err.Error(), expected) {
				t.Fatalf("missing localized recovery guidance: %v", err)
			}
			if out.Len() != 0 {
				t.Fatalf("unexpected success output: %s", out.String())
			}
		})
	}
}
