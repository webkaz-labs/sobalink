//go:build resource_process_native

package resourceacceptance

import (
	"context"
	"errors"
	"runtime/debug"
	"strings"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/resourceacceptance/processmodel"
)

func processEntryTestCLI(tail ...string) []string {
	return append([]string{"/reviewed/soba", "--state-dir", "/fixture/state/c", "--locale", "en", "--json-errors"}, tail...)
}

func TestProcessEntryArgumentsClosedValues(t *testing.T) {
	id, rev, peer, other := strings.Repeat("1", 32), strings.Repeat("2", 64), strings.Repeat("3", 64), strings.Repeat("4", 64)
	cases := [][]string{
		{"status", "--json"}, {"stop", "--json"}, {"resource", "list", "--json"},
		{"resource", "group", "preview", "--selection-file", "/fixture/inputs/selection-applied.json", "--json"},
		{"resource", "group", "apply", "--review-id", id, "--revision", rev, "--execute-peer", peer, "--execute-peer", other, "--confirm", "--json"},
		{"resource", "group", "review", "current", "--json"}, {"resource", "group", "status", "--run-id", id, "--json"},
		{"--dry-run", "resource", "group", "preview", "--selection-file", "/fixture/inputs/selection-applied.json", "--json"},
		{"--dry-run", "resource", "group", "apply", "--review-id", id, "--revision", rev, "--execute-peer", peer, "--execute-peer", other, "--confirm", "--json"},
		{"--dry-run", "resource", "group", "status", "--run-id", id, "--json"},
	}
	for _, tail := range cases {
		argv := processEntryTestCLI(tail...)
		if _, ok := processEntryParseArguments(argv); !ok {
			t.Fatalf("closed grammar rejected: %s", tail[0])
		}
		if _, ok := processEntryParseArguments(append(argv, "--help")); ok {
			t.Fatal("extra flag accepted")
		}
	}
	grants := [][]string{{"resource", "grant", "preview", "--management", "--id", id, "--peer", peer, "--expires-at", "2030-01-01T00:00:00Z", "--json"}, {"resource", "grant", "confirm", "--management", "--review-file", "/fixture/inputs/grant-a.json", "--confirm", "--json"}, {"resource", "grant", "inspect", "--id", id, "--json"}}
	for _, tail := range grants {
		argv := processEntryTestCLI(tail...)
		argv[2] = "/fixture/state/a"
		if _, ok := processEntryParseArguments(argv); !ok {
			t.Fatal("management grammar rejected")
		}
	}
	for _, tail := range [][]string{{"ui"}, {"start", "--background"}, {"resource", "group", "refresh", "--run-id", id, "--json"}, {"resource", "grant", "confirm", "--management", "--review-file", "-", "--confirm", "--json"}, {"--dry-run", "stop", "--json"}} {
		if _, ok := processEntryParseArguments(processEntryTestCLI(tail...)); ok {
			t.Fatal("unlisted operation admitted")
		}
	}
	if processObserver.Load() != nil || processEntryPhase.Load() != 0 {
		t.Fatal("pure parser changed entry state")
	}
}

func TestProcessEntryRoleAndBindingValues(t *testing.T) {
	argv := []string{"/reviewed/soba", "--state-dir", "/fixture/state/c", "--locale", "en", "run", "--offline"}
	a, ok := processEntryParseArguments(argv)
	if !ok {
		t.Fatal("offline owner rejected")
	}
	b := processmodel.Bootstrap{Invocation: processmodel.Invocation{Mode: processmodel.Owner, Role: processmodel.RoleC1}}
	b.RoleDirectory.Length = uint16(len(a.directory))
	copy(b.RoleDirectory.Bytes[:], a.directory)
	b.WorkingDirectory.Length = uint16(len(a.root))
	copy(b.WorkingDirectory.Bytes[:], a.root)
	if !processEntryArgumentsMatch(a, b) {
		t.Fatal("same binding rejected")
	}
	b.Invocation.Role = processmodel.RoleC2
	if processEntryArgumentsMatch(a, b) {
		t.Fatal("online role accepted offline argv")
	}
	for _, path := range []string{"relative", "/fixture/../other", "/fixture//state", "/fixture/", "/fixture\\state"} {
		if processEntryPath(path) {
			t.Fatal("unsafe path accepted")
		}
	}
}

func TestProcessEntryEnvironmentExactValues(t *testing.T) {
	values := []string{"HOME=/fixture/home", "USERPROFILE=/fixture/home", "XDG_CONFIG_HOME=/fixture/config", "XDG_CACHE_HOME=/fixture/cache", "APPDATA=/fixture/appdata", "LOCALAPPDATA=/fixture/localappdata", "TMPDIR=/fixture/tmp", "TMP=/fixture/tmp", "TEMP=/fixture/tmp", "LANG=C.UTF-8", "LC_ALL=C.UTF-8", "TZ=UTC", "RUNEWIDTH_EASTASIAN=0", "GOTRACEBACK=single"}
	if !processEntryEnvironment(values, "/fixture") {
		t.Fatal("exact environment rejected")
	}
	if processEntryEnvironment(append(values, "SOBALINK_BACKGROUND_LOG=/outside"), "/fixture") {
		t.Fatal("extra key accepted")
	}
	copyValues := append([]string(nil), values...)
	copyValues[1] = copyValues[0]
	if processEntryEnvironment(copyValues, "/fixture") {
		t.Fatal("duplicate accepted")
	}
	copyValues = append([]string(nil), values...)
	copyValues[0] = "HOME=/outside"
	if processEntryEnvironment(copyValues, "/fixture") {
		t.Fatal("ambient home accepted")
	}
}

func TestProcessEntryBuildInfoClosedValues(t *testing.T) {
	commit := strings.Repeat("1", 40)
	info := &debug.BuildInfo{GoVersion: "go1.27.1", Path: "github.com/webkaz-labs/sobalink/cmd/soba", Main: debug.Module{Path: "github.com/webkaz-labs/sobalink"}, Settings: []debug.BuildSetting{{Key: "-buildmode", Value: "exe"}, {Key: "-compiler", Value: "gc"}, {Key: "-tags", Value: processEntryTags}, {Key: "-trimpath", Value: "true"}, {Key: "CGO_ENABLED", Value: "0"}, {Key: "GOARCH", Value: "amd64"}, {Key: "GOOS", Value: "linux"}, {Key: "vcs", Value: "git"}, {Key: "vcs.revision", Value: commit}, {Key: "vcs.time", Value: "2030-01-01T00:00:00Z"}, {Key: "vcs.modified", Value: "false"}, {Key: "GOAMD64", Value: "v1"}}}
	if !processEntryBuildInfo(info, commit, "amd64") {
		t.Fatal("exact copied build info rejected")
	}
	info.Settings = append(info.Settings, debug.BuildSetting{Key: "-race", Value: "true"})
	if processEntryBuildInfo(info, commit, "amd64") {
		t.Fatal("extra instrumentation accepted")
	}
	var digest [32]byte
	if processEntryDecodeHex(strings.Repeat("0", 64), digest[:]) || processEntryDecodeHex(strings.Repeat("A", 64), digest[:]) {
		t.Fatal("empty or noncanonical stamp accepted")
	}
}

func TestProcessEntrySignalCauseValues(t *testing.T) {
	cause := processEntrySignalCause("interrupt signal received")
	if cause.Error() != "interrupt signal received" || !errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		t.Fatal("signal cancellation semantics changed")
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(cause)
	cancel(nil)
	if context.Cause(ctx) != cause || ctx.Err() != context.Canceled {
		t.Fatal("first cancellation cause not preserved")
	}
	if processFinalizationDeadline(time.Unix(10, 0), time.Unix(9, 0)) != time.Unix(10, 0) {
		t.Fatal("finalization cap changed")
	}
}
