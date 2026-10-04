package transfer

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/diskspace"
)

type crashAfterPartial struct{ read bool }

func (r *crashAfterPartial) Read(p []byte) (int, error) {
	if r.read {
		os.Exit(81)
	}
	r.read = true
	return copy(p, "1234567"), nil
}

func crashOptions(root string) Options {
	return Options{Limits: Limits{MaxReservedBytes: 8}, AccountingStore: FileReceiveAccountingStore{Path: filepath.Join(root, "receive-accounting.json")}}
}

func TestReceiveCrashProcess(t *testing.T) {
	root := os.Getenv("SOBALINK_RECEIVE_CRASH_FIXTURE")
	if root == "" {
		t.Skip("private subprocess helper")
	}
	m, err := NewManager(crashOptions(root))
	if err != nil {
		t.Fatal(err)
	}
	peer := Peer{ID: "fixture-peer", Generation: 1}
	if err := m.BindPeer(peer); err != nil {
		t.Fatal(err)
	}
	manifest := testManifest("before-crash", testEntry("file", "payload", "12345678"))
	if _, err := m.Offer(peer, manifest); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Accept(manifest.ID, filepath.Join(root, "receive")); err != nil {
		t.Fatal(err)
	}
	_, err = m.ReceiveFile(context.Background(), peer, manifest.ID, "file", &crashAfterPartial{})
	t.Fatalf("crash helper unexpectedly returned: %v", err)
}

func TestReceiveCrashRestartCannotReusePartialQuota(t *testing.T) {
	root := t.TempDir()
	destination := filepath.Join(root, "receive")
	if err := os.Mkdir(destination, 0700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestReceiveCrashProcess$")
	cmd.Env = append(os.Environ(), "SOBALINK_RECEIVE_CRASH_FIXTURE="+root)
	output, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 81 {
		t.Fatalf("private crash helper exit: %v, %s", err, output)
	}
	state, err := (FileReceiveAccountingStore{Path: filepath.Join(root, "receive-accounting.json")}).LoadReceiveAccounting()
	if err != nil {
		t.Fatal(err)
	}
	markers := make(map[string]bool, len(state.Roots))
	for _, record := range state.Roots {
		markers[filepath.Join(record.OwnedRoot, record.Stage, receiveOwnerMarker)] = true
	}
	var partialBytes int64
	err = filepath.WalkDir(destination, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && !markers[path] {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			partialBytes += info.Size()
		}
		return nil
	})
	if err != nil || partialBytes != 7 {
		t.Fatalf("crash did not retain the seven payload bytes: %d, %v", partialBytes, err)
	}
	options := crashOptions(root)
	options.ExistingState = true
	spaceAvailable := uint64(0)
	options.DiskSpace = diskspace.New(func(*os.File) (uint64, error) { return spaceAvailable, nil })
	m, err := NewManager(options)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	peer := Peer{ID: "fixture-peer", Generation: 1}
	if err := m.BindPeer(peer); err != nil {
		t.Fatal(err)
	}
	manifest := testManifest("after-crash", testEntry("file", "payload", "12345678"))
	_, err = m.Offer(peer, manifest)
	if !errors.Is(err, ErrLimit) {
		t.Fatalf("restart admitted %d new bytes despite %d retained partial bytes and an 8-byte quota: %v", manifest.Entries[0].Size, partialBytes, err)
	}
	oneByte := testManifest("after-crash-one", testEntry("file", "payload", "1"))
	if _, err = m.Offer(peer, oneByte); err != nil {
		t.Fatalf("restart rejected the final unreserved byte: %v", err)
	}
	if _, err = m.Accept(oneByte.ID, destination); !errors.Is(err, diskspace.ErrLow) {
		t.Fatalf("restored accounting bypassed the destination space guard: %v", err)
	}
	view := m.ReceiveRecovery()
	if view.ReservedBytes == nil || *view.ReservedBytes != 8 {
		t.Fatalf("failed space admission lost the retained or new reservation: %+v", view)
	}
	spaceAvailable = 1 << 40
	if _, err = m.Accept(oneByte.ID, destination); err != nil {
		t.Fatalf("accept after restoring disk space: %v", err)
	}
	if _, err = m.ReceiveFile(context.Background(), peer, oneByte.ID, "file", strings.NewReader("1")); err != nil {
		t.Fatalf("receive the final unreserved byte: %v", err)
	}
	view = m.ReceiveRecovery()
	if view.ReservedBytes == nil || *view.ReservedBytes != 7 {
		t.Fatalf("successful receive released crash-retained bytes: %+v", view)
	}
}

var _ io.Reader = (*crashAfterPartial)(nil)
