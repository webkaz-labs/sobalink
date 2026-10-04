package core

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestInitializeProfileCreatesOnlyMetadataAndPreservesExisting(t *testing.T) {
	dir := t.TempDir()
	result, err := InitializeProfile(context.Background(), Options{Directory: dir, NodeFactory: func(string, string) (NetworkBackend, error) {
		t.Fatal("initialization created a network engine")
		return nil, nil
	}}, "example-node")
	if err != nil || result.State != "initialized" || result.Network != "none" {
		t.Fatal(result, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	if !reflect.DeepEqual(names, []string{".sobalink-atomic-v1", "process.lock", "sobalink.json"}) {
		t.Fatal("unexpected initialization state", names)
	}
	owned := filepath.Join(dir, ".sobalink-atomic-v1")
	children, err := os.ReadDir(owned)
	if err != nil || len(children) != 1 || children[0].Name() != "owner.lock" || !children[0].Type().IsRegular() {
		t.Fatal("unexpected owned persistence metadata or retained snapshot", children, err)
	}
	marker, err := os.ReadFile(filepath.Join(owned, "owner.lock"))
	if err != nil || string(marker) != "sobalink atomic persistence v1\n" {
		t.Fatal("invalid owned marker", err)
	}
	assertLANStatePrivate(t, owned)
	assertLANStatePrivate(t, filepath.Join(owned, "owner.lock"))
	before, err := os.ReadFile(filepath.Join(dir, "sobalink.json"))
	if err != nil {
		t.Fatal(err)
	}
	result, err = InitializeProfile(context.Background(), Options{Directory: dir}, "example-node")
	if err != nil || result.State != "exists" {
		t.Fatal(result, err)
	}
	if _, err = InitializeProfile(context.Background(), Options{Directory: dir}, "different-node"); networkErrorCode(err) != "profile_already_initialized" {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "sobalink.json"))
	if string(before) != string(after) {
		t.Fatal("repeat initialization changed profile")
	}
}

func TestInitializeInvalidOrCancelledDoesNotCreateProfile(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		dir := filepath.Join(t.TempDir(), "new-profile")
		ctx, cancel := context.WithCancel(context.Background())
		name := "invalid name"
		if cancelled {
			cancel()
			name = "valid-node"
		}
		if _, err := InitializeProfile(ctx, Options{Directory: dir}, name); err == nil {
			t.Fatal("invalid initialization accepted")
		}
		cancel()
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatal("invalid initialization created state", err)
		}
	}
}
