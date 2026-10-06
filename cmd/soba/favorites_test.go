package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/control"
	"github.com/webkaz-labs/sobalink/internal/core"
	"github.com/webkaz-labs/sobalink/internal/webui"
)

func TestFavoritesCLIHumanJSONDryRunAndFreshRequests(t *testing.T) {
	view := core.FavoritesView{Version: 1, Revision: strings.Repeat("a", 64), Entries: []core.FavoriteEntry{
		{FavoriteReference: core.FavoriteReference{Kind: "service", ServiceID: "service-a"}, Available: true},
		{FavoriteReference: core.FavoriteReference{Kind: "group", GroupName: "セット"}, Available: false},
	}, DurabilityUncertain: true}
	var requests []webui.Command
	client := func(_ context.Context, _ string, raw string, result any) error {
		var request webui.Command
		if err := json.Unmarshal([]byte(raw), &request); err != nil {
			return err
		}
		requests = append(requests, request)
		encoded, _ := json.Marshal(view)
		return json.Unmarshal(encoded, result)
	}
	run := func(locale string, args ...string) (string, error) {
		var out bytes.Buffer
		err := runWith(context.Background(), append([]string{"--state-dir", t.TempDir(), "--locale", locale}, args...), &out, strings.NewReader(""), client)
		return out.String(), err
	}
	var jsonEN string
	for _, locale := range []string{"en", "ja"} {
		human, err := run(locale, "favorites", "list")
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{view.Revision, "service-a", "セット", text(locale == "ja", "missing", "対象なし"), text(locale == "ja", "uncertain", "確認できません")} {
			if !strings.Contains(human, want) {
				t.Fatalf("missing %q in %s", want, human)
			}
		}
		machine, err := run(locale, "favorites", "list", "--json")
		if err != nil {
			t.Fatal(err)
		}
		var parsed core.FavoritesView
		if err := json.Unmarshal([]byte(machine), &parsed); err != nil || !reflect.DeepEqual(view, parsed) {
			t.Fatal("JSON changed", err)
		}
		if locale == "en" {
			jsonEN = machine
		} else if jsonEN != machine {
			t.Fatal("JSON depended on locale")
		}
		before := len(requests)
		preview, err := run(locale, "--dry-run", "favorites", "add", "group", "セット", "--review", view.Revision)
		if err != nil || len(requests) != before || !strings.Contains(preview, `"validation": "local-input-only"`) || !strings.Contains(preview, "セット") {
			t.Fatal("dry run changed or queried state", err, preview)
		}
		if _, err := run(locale, "favorites", "add", "group", "セット", "--review", view.Revision, "--json"); err != nil {
			t.Fatal(err)
		}
		var input core.FavoriteChangeRequest
		last := requests[len(requests)-1]
		if err := json.Unmarshal(last.Payload, &input); err != nil || last.Name != "favorites.add" || input.Reference.GroupName != "セット" || input.Reference.Kind != "group" || input.ExpectedRevision != view.Revision {
			t.Fatal("mutation contract changed", err)
		}
	}
	seen := map[string]bool{}
	for _, r := range requests {
		if !strings.HasPrefix(r.RequestID, "cli-favorites-") || len(strings.TrimPrefix(r.RequestID, "cli-favorites-")) < 26 {
			t.Fatal("favorite request lacks independent random suffix")
		}
		if seen[r.RequestID] {
			t.Fatal("request IDs reused")
		}
		seen[r.RequestID] = true
	}
}

func TestFavoritesCLIHelpInvalidInputAndAutomaticLocale(t *testing.T) {
	client := func(context.Context, string, string, any) error {
		t.Fatal("help or invalid input contacted agent")
		return nil
	}
	run := func(args ...string) (string, error) {
		var out bytes.Buffer
		err := runWith(context.Background(), append([]string{"--state-dir", t.TempDir()}, args...), &out, strings.NewReader(""), client)
		return out.String(), err
	}
	for _, locale := range []string{"en", "ja"} {
		for _, args := range [][]string{{"favorites", "--help"}, {"help", "favorites"}, {"favorites", "add", "--help"}, {"--offline", "favorites", "--help"}} {
			out, err := run(append([]string{"--locale", locale}, args...)...)
			if err != nil || !strings.Contains(out, "favorites remove group") || !strings.Contains(out, text(locale == "ja", "Inert favorites", "お気に入り")) {
				t.Fatal("incomplete help", args, err)
			}
		}
	}
	for _, args := range [][]string{{"favorites", "add", "service", "id"}, {"favorites", "remove", "group", "name", "--review", "old"}, {"favorites", "add", "peer", "id", "--review", strings.Repeat("a", 64)}, {"favorites", "list", "extra"}, {"favorites", "start"}, {"favorites", "add", "service", "bad/id", "--review", strings.Repeat("a", 64)}, {"favorites", "remove", "group", " spaced ", "--review", strings.Repeat("a", 64)}} {
		if _, err := run(args...); err == nil {
			t.Fatal("invalid input accepted", args)
		}
	}
	for _, locale := range []string{"ja_JP.UTF-8", "unknown_LOCALE"} {
		t.Setenv("LC_ALL", locale)
		out, err := run("favorites", "--help")
		if err != nil || !strings.Contains(out, text(locale == "ja_JP.UTF-8", "Inert favorites", "お気に入り")) {
			t.Fatal("automatic locale failed", err)
		}
		out, err = run("--locale", "en", "favorites", "--help")
		if err != nil || !strings.Contains(out, "Inert favorites") {
			t.Fatal("locale override failed", err)
		}
	}
}

func TestFavoritesCLILocalizedErrorsAndStableJSONErrors(t *testing.T) {
	for _, code := range []string{"favorites_invalid_request", "favorites_unavailable", "favorites_revision_conflict", "favorites_target_missing", "favorites_capacity", "favorites_persistence_uncertain"} {
		client := func(context.Context, string, string, any) error {
			return &control.RemoteError{Code: code, Message: "fixture failure"}
		}
		for _, locale := range []string{"ja", "en"} {
			var out bytes.Buffer
			err := runWith(context.Background(), []string{"--state-dir", t.TempDir(), "--locale", locale, "favorites", "list"}, &out, strings.NewReader(""), client)
			var coded interface{ ErrorCode() string }
			if err == nil || !errors.As(err, &coded) || coded.ErrorCode() != code || (locale == "ja" && strings.Contains(err.Error(), "fixture failure")) {
				t.Fatal("localized code lost", err)
			}
			err = runWith(context.Background(), []string{"--state-dir", t.TempDir(), "--locale", locale, "--json-errors", "favorites", "list"}, &out, strings.NewReader(""), client)
			out.Reset()
			writeCommandError(&out, err)
			var result map[string]string
			if json.Unmarshal(out.Bytes(), &result) != nil || result["code"] != code || result["error"] != "fixture failure" {
				t.Fatal("machine error localized", out.String())
			}
		}
	}
}

func TestFavoritesCLIOfflineMetadataOnlyLockAndRestart(t *testing.T) {
	dir := t.TempDir()
	client := func(context.Context, string, string, any) error {
		t.Fatal("offline metadata used network control")
		return nil
	}
	run := func(args ...string) (string, error) {
		var out bytes.Buffer
		err := runWith(context.Background(), append([]string{"--state-dir", dir, "--offline"}, args...), &out, strings.NewReader(""), client)
		return out.String(), err
	}
	data, err := run("service", "save", "share", "--backend", "tailnet", "--name", "example", "--ports", "8080", "--peers", "peer-example")
	if err != nil {
		t.Fatal(err)
	}
	var saved core.SavedServiceConfiguration
	if err := json.Unmarshal([]byte(data), &saved); err != nil {
		t.Fatal(err)
	}
	profileBefore, err := os.ReadFile(filepath.Join(dir, "sobalink.json"))
	if err != nil {
		t.Fatal(err)
	}
	data, err = run("favorites", "list", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var initial core.FavoritesView
	if err := json.Unmarshal([]byte(data), &initial); err != nil {
		t.Fatal(err)
	}
	data, err = run("favorites", "add", "service", saved.Configuration.ID, "--review", initial.Revision, "--json")
	if err != nil {
		t.Fatal(err)
	}
	var updated core.FavoritesView
	if err := json.Unmarshal([]byte(data), &updated); err != nil || len(updated.Entries) != 1 || !updated.Entries[0].Available {
		t.Fatal("offline addition missing", err)
	}
	data, err = run("favorites", "list", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var restarted core.FavoritesView
	if json.Unmarshal([]byte(data), &restarted) != nil || !reflect.DeepEqual(updated, restarted) {
		t.Fatal("offline restart changed preferences")
	}
	profileAfter, _ := os.ReadFile(filepath.Join(dir, "sobalink.json"))
	if string(profileBefore) != string(profileAfter) {
		t.Fatal("favorite rewrote profile")
	}
	for _, name := range []string{"identity", "lan.json", "direct-lan.json", "messages.json", "startup.json", "startup-revocations.json", "saved-proxies.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("offline favorite touched %s: %v", name, err)
		}
	}
	file, err := os.Open(filepath.Join(dir, "favorites.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := checkProxyPrivateFile(file); err != nil {
		t.Fatal("favorites file not private", err)
	}
	lock, err := config.AcquireLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := run("favorites", "remove", "service", saved.Configuration.ID, "--review", updated.Revision); err == nil {
		t.Fatal("offline favorite bypassed profile lock")
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := run("favorites", "remove", "service", saved.Configuration.ID, "--review", updated.Revision, "--json"); err != nil {
		t.Fatal(err)
	}
}

func TestFavoritesCLIFirstUseDoesNotMaterializeProfile(t *testing.T) {
	dir := t.TempDir()
	client := func(context.Context, string, string, any) error {
		t.Fatal("offline favorites contacted agent")
		return nil
	}
	run := func(args ...string) (core.FavoritesView, error) {
		var out bytes.Buffer
		err := runWith(context.Background(), append([]string{"--state-dir", dir, "--offline", "favorites"}, args...), &out, strings.NewReader(""), client)
		var view core.FavoritesView
		if err == nil {
			err = json.Unmarshal(out.Bytes(), &view)
		}
		return view, err
	}
	initial, err := run("list", "--json")
	if err != nil || initial.Version != 1 || len(initial.Entries) != 0 || len(initial.Revision) != 64 {
		t.Fatal("initial favorites unavailable", err)
	}
	second, err := run("list", "--json")
	if err != nil || !reflect.DeepEqual(initial, second) {
		t.Fatal("first-use revision was not stable", err)
	}
	removed, err := run("remove", "service", "missing-service", "--review", initial.Revision, "--json")
	if err != nil || !reflect.DeepEqual(initial, removed) {
		t.Fatal("first-use remove changed empty preferences", err)
	}
	_, err = run("add", "service", "missing-service", "--review", initial.Revision, "--json")
	var coded interface{ ErrorCode() string }
	if err == nil || !errors.As(err, &coded) || coded.ErrorCode() != "favorites_target_missing" {
		t.Fatal("first-use add accepted missing target", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() != "process.lock" {
			t.Fatalf("first-use favorite materialized non-lock state: %s", entry.Name())
		}
	}
}
