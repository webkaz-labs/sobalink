package main

import (
	"strings"
	"testing"
)

func TestStartupCLILocalizedReviewAndRevision(t *testing.T) {
	for _, locale := range []string{"en", "ja"} {
		m := &mockCLI{}
		out, err := runMock(t, m, []string{"--locale", locale, "startup", "--help"}, "")
		if err != nil || len(m.commands) != 0 || !strings.Contains(out, "store-review") {
			t.Fatal("startup help", out, err)
		}
		out, err = runMock(t, m, []string{"--locale", locale, "startup", "save", "--name", "example", "--group", "example", "--review", "scope-revision", "--store-review", "store-revision"}, "")
		if err != nil {
			t.Fatal(out, err)
		}
		if len(m.commands) != 1 || m.commands[0].Name != "startup.save" {
			t.Fatal("wrong startup command")
		}
		payload := payloadOf(t, m.commands[0].Payload)
		if payload["expectedRevision"] != "scope-revision" || payload["expectedStoreRevision"] != "store-revision" || payload["group"] != "example" {
			t.Fatal("startup scope altered")
		}
	}
	for _, args := range [][]string{{"startup", "save", "--name", "example", "--ids", "one"}, {"startup", "preview", "--name", "example", "--ids", "one", "--group", "two"}, {"startup", "disable", "--name", "example"}} {
		m := &mockCLI{}
		if _, err := runMock(t, m, args, ""); err == nil || len(m.commands) > 0 {
			t.Fatal("missing review accepted", args)
		}
	}
}
