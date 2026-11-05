package main

import (
	"runtime"
	"testing"
)

func TestVersionIdentity(t *testing.T) {
	got := versionInfo()
	if len(got) != 7 || got["product"] != "soba-native" || got["version"] != version || got["commit"] != sourceCommit || got["go_version"] != runtime.Version() || got["goos"] != runtime.GOOS || got["goarch"] != runtime.GOARCH || got["experimental"] != true {
		t.Fatalf("unexpected version identity: %+v", got)
	}
}
