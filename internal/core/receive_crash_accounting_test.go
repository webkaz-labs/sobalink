package core

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/transfer"
)

type coreCrashReader struct{ read bool }

func (r *coreCrashReader) Read(p []byte) (int, error) {
	if r.read {
		os.Exit(82)
	}
	r.read = true
	return copy(p, "1234567"), nil
}

func TestCoreReceiveCrashChild(t *testing.T) {
	dir := os.Getenv("SOBALINK_CORE_RECEIVE_CRASH_DIR")
	if dir == "" {
		t.Skip("private subprocess helper")
	}
	policy := capacity.Defaults()
	policy.Resources["receiveReservedBytes"] = capacity.Limited(8)
	if err := config.WriteJSON(filepath.Join(dir, capacityPolicyFile), policy); err != nil {
		t.Fatal(err)
	}
	c, err := Open(context.Background(), Options{Directory: dir, SkipNetworkStart: true})
	if err != nil {
		t.Fatal(err)
	}
	peer := transfer.Peer{ID: "fixture-peer", Generation: 1}
	if err := c.transfers.BindPeer(peer); err != nil {
		t.Fatal(err)
	}
	manifest := coreCrashManifest("before-core-crash", "12345678")
	if _, err := c.transfers.Offer(peer, manifest); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(dir, "receive")
	if err := os.Mkdir(destination, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := c.transfers.Accept(manifest.ID, destination); err != nil {
		t.Fatal(err)
	}
	_, err = c.transfers.ReceiveFile(context.Background(), peer, manifest.ID, "file", &coreCrashReader{})
	t.Fatalf("crash helper unexpectedly returned: %v", err)
}

func TestCoreOpenReconcilesReceiveCrashAccounting(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestCoreReceiveCrashChild$")
	cmd.Env = append(os.Environ(), "SOBALINK_CORE_RECEIVE_CRASH_DIR="+dir)
	output, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 82 {
		t.Fatalf("Core crash helper exit: %v, %s", err, output)
	}
	c, err := Open(context.Background(), Options{Directory: dir, SkipNetworkStart: true})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	peer := transfer.Peer{ID: "fixture-peer", Generation: 1}
	if err := c.transfers.BindPeer(peer); err != nil {
		t.Fatal(err)
	}
	if _, err := c.transfers.Offer(peer, coreCrashManifest("after-core-crash-eight", "12345678")); !errors.Is(err, transfer.ErrLimit) {
		t.Fatalf("Core.Open admitted eight bytes despite retained seven: %v", err)
	}
	if _, err := c.transfers.Offer(peer, coreCrashManifest("after-core-crash-one", "1")); err != nil {
		t.Fatalf("Core.Open rejected remaining one byte: %v", err)
	}
}

func coreCrashManifest(id, payload string) transfer.Manifest {
	// Keep fixture construction local so the public wire/runtime path stays out
	// of this package's tests while Core.Open supplies the production store.
	entry := transfer.Entry{ID: "file", Path: "payload", Kind: transfer.File, Size: int64(len(payload))}
	entry.SHA256 = coreCrashSHA256(payload)
	return transfer.Manifest{ID: id, Entries: []transfer.Entry{entry}}
}

func coreCrashSHA256(payload string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(payload)))
}
