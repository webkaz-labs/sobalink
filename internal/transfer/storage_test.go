package transfer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestConcurrentDestinationCreationNeverOverwrites(t *testing.T) {
	m, peer := testManager(t, Options{})
	b := testAccepted(t, m, peer, testManifest("batch", testEntry("file", "file.txt", "payload")))
	reader := newGateReader("payload")
	done := startBlocked(t, m, context.Background(), peer, "batch", "file", reader, true)
	target := filepath.Join(b.Destination, "file.txt")
	if err := os.WriteFile(target, []byte("existing content"), 0600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	reader.finish()
	if result := waitReceive(t, done); !errors.Is(result.err, ErrConflict) {
		t.Fatalf("occupied target = %v", result.err)
	}
	after, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "existing content" || !os.SameFile(before, after) {
		t.Fatalf("existing target changed: %q, %v", data, err)
	}
	other := testAccepted(t, m, peer, testManifest("other", testEntry("file", "file.txt", "payload")))
	if other.Destination == b.Destination {
		t.Fatal("batches share destination")
	}
	if _, err = m.ReceiveFile(context.Background(), peer, "other", "file", strings.NewReader("payload")); err != nil {
		t.Fatalf("independent batch name collision: %v", err)
	}
}

func testSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
}

func TestDestinationSymlinkRejectedBeforeAcceptance(t *testing.T) {
	m, peer := testManager(t, Options{})
	outside, container := t.TempDir(), t.TempDir()
	link := filepath.Join(container, "destination")
	testSymlink(t, outside, link)
	if _, err := m.Offer(peer, testManifest("batch", testEntry("file", "file", "payload"))); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Accept("batch", link); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("symlink destination accepted: %v", err)
	}
	if err := m.SetReceivePolicy(ReceivePolicy{Peer: peer, Destination: link, AutoAccept: true}); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("symlink policy accepted: %v", err)
	}
	if entries := mustReadDir(t, outside); len(entries) != 0 {
		t.Fatalf("rejected destination changed: %v", entries)
	}
}

func TestParentSymlinkSubstitutionDuringStreamCannotEscape(t *testing.T) {
	m, peer := testManager(t, Options{})
	b := testAccepted(t, m, peer, testManifest("batch", testEntry("file", "nested/file.txt", "payload")))
	outside := t.TempDir()
	reader := newGateReader("payload")
	done := startBlocked(t, m, context.Background(), peer, "batch", "file", reader, true)
	directory := filepath.Join(b.Destination, "nested")
	if err := os.Remove(directory); err != nil {
		t.Fatal(err)
	}
	testSymlink(t, outside, directory)
	reader.finish()
	if result := waitReceive(t, done); result.err == nil {
		t.Fatal("symlink substitution succeeded")
	}
	if entries := mustReadDir(t, outside); len(entries) != 0 {
		t.Fatalf("stream escaped root: %v", entries)
	}
}

func TestStagingFileReplacementCannotCommitDifferentBytes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("open-file replacement exercises Unix file identity semantics")
	}
	m, peer := testManager(t, Options{})
	b := testAccepted(t, m, peer, testManifest("batch", testEntry("file", "file.txt", "payload")))
	reader := newGateReader("payload")
	done := startBlocked(t, m, context.Background(), peer, "batch", "file", reader, true)
	var stage string
	for _, entry := range mustReadDir(t, b.Destination) {
		if strings.HasPrefix(entry.Name(), ".incoming-") {
			stage = filepath.Join(b.Destination, entry.Name())
		}
	}
	if stage == "" {
		t.Fatal("no staging directory")
	}
	parts := mustReadDir(t, stage)
	if len(parts) != 1 {
		t.Fatalf("staged entries = %v", parts)
	}
	part := filepath.Join(stage, parts[0].Name())
	if err := os.Remove(part); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(part, []byte("unverified replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	reader.finish()
	if result := waitReceive(t, done); !errors.Is(result.err, ErrUnsafePath) {
		t.Fatalf("replaced stage accepted: %v", result.err)
	}
	if _, err := os.Stat(filepath.Join(b.Destination, "file.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("replacement became visible: %v", err)
	}
}

func TestStagingDirectorySymlinkSubstitutionCannotEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("open-directory rename exercises Unix rooted-handle semantics")
	}
	m, peer := testManager(t, Options{})
	b := testAccepted(t, m, peer, testManifest("batch", testEntry("file", "file.txt", "payload")))
	reader := newGateReader("payload")
	done := startBlocked(t, m, context.Background(), peer, "batch", "file", reader, true)
	var stage string
	for _, entry := range mustReadDir(t, b.Destination) {
		if strings.HasPrefix(entry.Name(), ".incoming-") {
			stage = filepath.Join(b.Destination, entry.Name())
		}
	}
	if stage == "" {
		t.Fatal("no staging directory")
	}
	if err := os.Rename(stage, stage+"-moved"); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	testSymlink(t, outside, stage)
	reader.finish()
	if result := waitReceive(t, done); result.err == nil {
		t.Fatal("substituted staging directory succeeded")
	}
	if entries := mustReadDir(t, outside); len(entries) != 0 {
		t.Fatalf("staging escaped root: %v", entries)
	}
	if _, err := os.Stat(filepath.Join(b.Destination, "file.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unsafe file became visible: %v", err)
	}
}

func TestAcceptedRootHandleSurvivesDestinationPathSubstitution(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("open-directory rename exercises Unix rooted-handle semantics")
	}
	m, peer := testManager(t, Options{})
	b := testAccepted(t, m, peer, testManifest("batch", testEntry("file", "file.txt", "payload")))
	reader := newGateReader("payload")
	done := startBlocked(t, m, context.Background(), peer, "batch", "file", reader, true)
	moved := b.Destination + "-moved"
	if err := os.Rename(b.Destination, moved); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	testSymlink(t, outside, b.Destination)
	reader.finish()
	if result := waitReceive(t, done); result.err != nil {
		t.Fatalf("confined open root failed: %v", result.err)
	}
	if entries := mustReadDir(t, outside); len(entries) != 0 {
		t.Fatalf("stream followed substituted destination: %v", entries)
	}
	data, err := os.ReadFile(filepath.Join(moved, "file.txt"))
	if err != nil || string(data) != "payload" {
		t.Fatalf("original root content = %q, %v", data, err)
	}
}

func TestReceivedFilesNeverGainExecutableOrSharedPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits do not represent Windows inherited ACLs")
	}
	m, peer := testManager(t, Options{})
	b := testAccepted(t, m, peer, testManifest("batch", testEntry("file", "directory/script.sh", "#!/bin/sh\n")))
	if _, err := m.ReceiveFile(context.Background(), peer, "batch", "file", strings.NewReader("#!/bin/sh\n")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{b.Destination, filepath.Join(b.Destination, "directory"), filepath.Join(b.Destination, "directory/script.sh")} {
		info, err := os.Stat(name)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0077 != 0 {
			t.Errorf("shared permissions on %s: %o", name, info.Mode().Perm())
		}
		if info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0 {
			t.Errorf("executable received file: %o", info.Mode().Perm())
		}
	}
}
