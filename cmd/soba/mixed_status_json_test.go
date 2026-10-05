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

func TestReviewDamagedMixedJSONRemainsStable(t *testing.T) {
	dir := t.TempDir()
	c, e := core.Open(context.Background(), core.Options{Directory: dir, SkipNetworkStart: true})
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	if e = os.WriteFile(filepath.Join(dir, "mixed.json"), []byte(`{}`), 0600); e != nil {
		t.Fatal(e)
	}
	var first string
	for _, locale := range []string{"en", "ja"} {
		var out bytes.Buffer
		e = runWith(context.Background(), []string{"--locale", locale, "mixed", "show", "--json"}, &out, strings.NewReader(""), func(ctx context.Context, dir, raw string, v any) error {
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
		if e != nil {
			t.Fatal(e)
		}
		var got map[string]any
		if e = json.Unmarshal(out.Bytes(), &got); e != nil {
			t.Fatal(e)
		}
		if got["configured"] != false || got["error"] != "saved mixed configuration unavailable" || len(got) != 2 {
			t.Fatal("machine result changed", got)
		}
		if first != "" && first != out.String() {
			t.Fatal("locale modified JSON")
		}
		first = out.String()
	}
}
