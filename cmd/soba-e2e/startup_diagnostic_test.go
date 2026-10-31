//go:build soba_e2e

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

type syntheticStartupCode string

func (e syntheticStartupCode) Error() string     { return "PRIVATE_STARTUP_SENTINEL" }
func (e syntheticStartupCode) ErrorCode() string { return string(e) }

func TestStartupDiagnosticClosedVocabulary(t *testing.T) {
	const secret = "PRIVATE_STARTUP_SENTINEL"
	for _, tc := range []struct {
		err  error
		want string
	}{
		{context.Canceled, "canceled"}, {context.DeadlineExceeded, "deadline"},
		{os.ErrPermission, "permission"}, {syscall.EADDRINUSE, "address-in-use"},
		{syscall.EADDRNOTAVAIL, "address-unavailable"}, {syscall.ECONNREFUSED, "connection-refused"},
		{os.ErrNotExist, "not-exist"}, {errors.New(secret), "unclassified"},
		{syntheticStartupCode("lan_listener_conflict"), "lan_listener_conflict"},
		{syntheticStartupCode(secret), "unclassified"},
		{errors.Join(errors.New(secret), syscall.EADDRINUSE), "multiple-causes"},
		{errors.Join(syntheticStartupCode("lan_listener_conflict"), syntheticStartupCode("lan_start_failed")), "multiple-causes"},
		{errors.Join(syscall.EADDRINUSE, nil), "address-in-use"},
	} {
		err := fmt.Errorf("%s: %w", secret, tc.err)
		if got := startupErrorCategory(err); got != tc.want {
			t.Fatalf("category %q, want %q", got, tc.want)
		}
		path := filepath.Join(t.TempDir(), "diagnostic.json")
		if err := writeStartupDiagnostic(path, "network-configure", err); err != nil {
			t.Fatal("could not write test diagnostic")
		}
		data, err := os.ReadFile(path)
		if err != nil || strings.Contains(string(data), secret) {
			t.Fatal("diagnostic missing or exposed private error")
		}
		var value startupDiagnostic
		if json.Unmarshal(data, &value) != nil || value.Stage != "network-configure" || value.Category != tc.want {
			t.Fatal("diagnostic did not retain fixed facts")
		}
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm()&0077 != 0 {
			t.Fatal("diagnostic is not private")
		}
	}
	path := filepath.Join(t.TempDir(), "diagnostic.json")
	if writeStartupDiagnostic(path, secret, errors.New(secret)) != nil {
		t.Fatal("diagnostic write failed")
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), secret) || !strings.Contains(string(data), "unknown") {
		t.Fatal("unknown stage escaped closed vocabulary")
	}
}

func TestStartupArgumentFailureWritesDiagnostic(t *testing.T) {
	previousFlags, previousArgs := flag.CommandLine, os.Args
	defer func() { flag.CommandLine, os.Args = previousFlags, previousArgs }()
	path := filepath.Join(t.TempDir(), "diagnostic.json")
	flag.CommandLine = flag.NewFlagSet("fixture", flag.ContinueOnError)
	flag.CommandLine.SetOutput(io.Discard)
	os.Args = []string{"fixture", "--scenario", "invalid", "--session-file", "unused", "--startup-diagnostic-file", path}
	if err := run(); err == nil {
		t.Fatal("invalid fixture unexpectedly started")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("startup failure diagnostic missing")
	}
	var value startupDiagnostic
	if json.Unmarshal(data, &value) != nil || value != (startupDiagnostic{Stage: "arguments", Category: "unclassified"}) {
		t.Fatal("wrong startup failure facts")
	}
}
