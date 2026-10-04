package config

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const atomicTestSnapshot = `{"generation":"new","secret":"test-only"}`

func TestOwnedAtomicChild(t *testing.T) {
	path := os.Getenv("SOBALINK_ATOMIC_CHILD_PATH")
	if path == "" {
		t.Skip("child process helper")
	}
	phase := os.Getenv("SOBALINK_ATOMIC_CHILD_PHASE")
	err := atomicWriteOwned(path, []byte(atomicTestSnapshot), &atomicHooks{barrier: func(actual string) {
		if actual != phase {
			return
		}
		fmt.Fprintln(os.Stdout, actual)
		var release [1]byte
		if _, err := io.ReadFull(os.Stdin, release[:]); err != nil {
			panic(err)
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
}

func startAtomicChild(t *testing.T, path, phase string) (*exec.Cmd, io.WriteCloser, *bytes.Buffer) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestOwnedAtomicChild$")
	cmd.Env = append(os.Environ(), "SOBALINK_ATOMIC_CHILD_PATH="+path, "SOBALINK_ATOMIC_CHILD_PHASE="+phase)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr := new(bytes.Buffer)
	cmd.Stderr = stderr
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stdin.Close(); _ = cmd.Process.Kill() })
	line := make(chan string, 1)
	go func() {
		s, err := bufio.NewReader(stdout).ReadString('\n')
		if err != nil {
			s += err.Error()
		}
		line <- strings.TrimSpace(s)
	}()
	select {
	case got := <-line:
		if got != phase {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			t.Fatalf("child barrier %q, want %q; stderr=%s", got, phase, stderr.String())
		}
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("child did not reach %s; stderr=%s", phase, stderr.String())
	}
	return cmd, stdin, stderr
}

func atomicSnapshotState(t *testing.T, dir string) (int, int64) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dir, atomicNamespace))
	if errors.Is(err, os.ErrNotExist) {
		return 0, 0
	}
	if err != nil {
		t.Fatal(err)
	}
	var count int
	var size int64
	for _, e := range entries {
		if e.Name() == atomicLeaseName {
			continue
		}
		if e.Name() != atomicSnapshot {
			t.Fatalf("unknown namespace entry %s", e.Name())
		}
		info, err := e.Info()
		if err != nil {
			t.Fatal(err)
		}
		count++
		size += info.Size()
	}
	return count, size
}

func assertFormalJSON(t *testing.T, path string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(b) || (string(b) != `{"generation":"old"}` && string(b) != atomicTestSnapshot) {
		t.Fatalf("partial or unexpected formal JSON: %q", b)
	}
}

func TestOwnedAtomicTempCrashAndConcurrentWriter(t *testing.T) {
	for _, phase := range []string{"afterCreateTemp", "afterSync", "beforeReplace"} {
		t.Run(phase, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "profile.json")
			if err := AtomicWritePrivate(path, []byte(`{"generation":"old"}`)); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 20; i++ {
				cmd, _, stderr := startAtomicChild(t, path, phase)
				if err := cmd.Process.Kill(); err != nil {
					t.Fatal(err)
				}
				if err := cmd.Wait(); err == nil {
					t.Fatal("child unexpectedly succeeded", stderr.String())
				}
				count, size := atomicSnapshotState(t, dir)
				if count != 1 || size > int64(len(atomicTestSnapshot)) {
					t.Fatalf("unbounded crash leftovers: count=%d bytes=%d", count, size)
				}
				assertFormalJSON(t, path)
				// Recovery is part of every production write, not just startup.
				if err := AtomicWritePrivate(path, []byte(atomicTestSnapshot)); err != nil {
					t.Fatal(err)
				}
				count, size = atomicSnapshotState(t, dir)
				if count != 0 || size != 0 {
					t.Fatalf("recovery retained %d/%d", count, size)
				}
				assertFormalJSON(t, path)
			}
		})
	}
	t.Run("live-writer-different-target", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "first.json")
		cmd, stdin, stderr := startAtomicChild(t, path, "afterSync")
		before, err := os.Stat(filepath.Join(dir, atomicNamespace, atomicSnapshot))
		if err != nil {
			t.Fatal(err)
		}
		started := time.Now()
		if err = AtomicWritePrivate(filepath.Join(dir, "second.json"), []byte(`{}`)); !errors.Is(err, ErrAtomicBusy) {
			t.Fatalf("second process admitted: %v", err)
		}
		if time.Since(started) > 3*time.Second {
			t.Fatal("lease admission was not finite")
		}
		after, err := os.Stat(filepath.Join(dir, atomicNamespace, atomicSnapshot))
		if err != nil || !os.SameFile(before, after) {
			t.Fatal("live paused writer snapshot deleted", err)
		}
		if _, err = stdin.Write([]byte{1}); err != nil {
			t.Fatal(err)
		}
		_ = stdin.Close()
		if err = cmd.Wait(); err != nil {
			t.Fatal(err, stderr.String())
		}
		assertFormalJSON(t, path)
		if err = AtomicWritePrivate(filepath.Join(dir, "second.json"), []byte(`{}`)); err != nil {
			t.Fatal(err)
		}
	})
}

func seedOwnedSnapshot(t *testing.T, dir string) string {
	t.Helper()
	if err := AtomicWritePrivate(filepath.Join(dir, "profile.json"), []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, atomicNamespace, atomicSnapshot)
	if err := os.WriteFile(p, []byte("test-only leftover"), 0600); err != nil {
		t.Fatal(err)
	}
	// Windows's CreateFile defaults inherit rather than protect its DACL.
	if err := Protect(p, false); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestOwnedAtomicCleanupFailureBlocksAdmission(t *testing.T) {
	dir := t.TempDir()
	p := seedOwnedSnapshot(t, dir)
	before, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		err = atomicWriteOwned(filepath.Join(dir, "profile.json"), []byte(atomicTestSnapshot), &atomicHooks{
			barrier: func(phase string) {
				if phase == "afterCreateTemp" {
					t.Fatal("created another snapshot after cleanup failed")
				}
			},
			remove: func(*os.File, string, *os.File) error { return os.ErrPermission },
		})
		if !errors.Is(err, ErrAtomicRecovery) || !errors.Is(err, os.ErrPermission) {
			t.Fatal(err)
		}
		after, err := os.Stat(p)
		if err != nil || !os.SameFile(before, after) || after.Size() != before.Size() {
			t.Fatal("retained charge changed", err)
		}
		count, _ := atomicSnapshotState(t, dir)
		if count != 1 {
			t.Fatal("cleanup failure grew inventory")
		}
	}
	if err := AtomicWritePrivate(filepath.Join(dir, "profile.json"), []byte(atomicTestSnapshot)); err != nil {
		t.Fatal(err)
	}
}

func TestOwnedAtomicLegacyBudgetsAndForeignFiles(t *testing.T) {
	t.Run("preserved-and-counted", func(t *testing.T) {
		dir := t.TempDir()
		for i := 0; i < atomicLegacyCount; i++ {
			if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf(".write-legacy-%d", i)), []byte("foreign"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		foreign := filepath.Join(dir, "backend.tmp123")
		if err := os.WriteFile(foreign, []byte("backend"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := AtomicWritePrivate(filepath.Join(dir, "profile.json"), []byte(`{}`)); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < atomicLegacyCount; i++ {
			b, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf(".write-legacy-%d", i)))
			if err != nil || string(b) != "foreign" {
				t.Fatal("legacy deleted", err)
			}
		}
		if b, err := os.ReadFile(foreign); err != nil || string(b) != "backend" {
			t.Fatal("backend touched", err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".write-user-owned"), []byte("extra"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := AtomicWritePrivate(filepath.Join(dir, "profile.json"), []byte(`{}`)); !errors.Is(err, ErrAtomicRecovery) {
			t.Fatal("legacy count overflow admitted", err)
		}
		count, _ := atomicSnapshotState(t, dir)
		if count != 0 {
			t.Fatal("temp allocated beyond legacy count")
		}
	})
	t.Run("logical-bytes", func(t *testing.T) {
		dir := t.TempDir()
		p := filepath.Join(dir, ".write-sparse")
		f, err := os.Create(p)
		if err != nil {
			t.Fatal(err)
		}
		if err = f.Truncate(atomicLegacyBytes + 1); err != nil {
			t.Fatal(err)
		}
		_ = f.Close()
		if err = AtomicWritePrivate(filepath.Join(dir, "profile.json"), []byte(`{}`)); !errors.Is(err, ErrAtomicRecovery) {
			t.Fatal("oversized sparse legacy admitted", err)
		}
		info, err := os.Stat(p)
		if err != nil || info.Size() != atomicLegacyBytes+1 {
			t.Fatal("legacy touched", err)
		}
		count, _ := atomicSnapshotState(t, dir)
		if count != 0 {
			t.Fatal("temp allocated beyond legacy byte budget")
		}
	})
	t.Run("nonrecursive-bounded-inventory", func(t *testing.T) {
		dir := t.TempDir()
		if err := AtomicWritePrivate(filepath.Join(dir, "profile.json"), []byte(`{}`)); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < atomicScanEntries; i++ {
			if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("foreign-%d", i)), nil, 0600); err != nil {
				t.Fatal(err)
			}
		}
		if err := AtomicWritePrivate(filepath.Join(dir, "profile.json"), nil); !errors.Is(err, ErrAtomicRecovery) {
			t.Fatal("unbounded directory admitted", err)
		}
	})
}

func TestOwnedAtomicUnknownEntriesAndOwnership(t *testing.T) {
	for _, name := range []string{"foreign", "nested-directory", "owner-version"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			p := seedOwnedSnapshot(t, dir)
			switch name {
			case "foreign":
				if err := os.WriteFile(filepath.Join(dir, atomicNamespace, "foreign"), []byte("preserve"), 0600); err != nil {
					t.Fatal(err)
				}
			case "nested-directory":
				if err := os.Mkdir(filepath.Join(dir, atomicNamespace, "nested"), 0700); err != nil {
					t.Fatal(err)
				}
			case "owner-version":
				if err := os.WriteFile(filepath.Join(dir, atomicNamespace, atomicLeaseName), []byte("unknown owner"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := AtomicWritePrivate(filepath.Join(dir, "profile.json"), nil); !errors.Is(err, ErrAtomicRecovery) {
				t.Fatal("unknown ownership/inventory admitted", err)
			}
			if _, err := os.Stat(p); err != nil {
				t.Fatal("known snapshot reclaimed amid unknown inventory", err)
			}
		})
	}
}

func TestOwnedAtomicPreservesExistingParentPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permissions; Windows DACL tested separately")
	}
	for _, write := range []func(string, []byte) error{AtomicWrite, AtomicWritePrivate} {
		dir := t.TempDir()
		if err := os.Chmod(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := write(filepath.Join(dir, "profile.json"), []byte(`{}`)); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(dir)
		if err != nil || info.Mode().Perm() != 0755 {
			t.Fatal("parent permissions changed", err)
		}
		for _, p := range []string{atomicNamespace, filepath.Join(atomicNamespace, atomicLeaseName), "profile.json"} {
			info, err := os.Stat(filepath.Join(dir, p))
			if err != nil || info.Mode().Perm()&0077 != 0 {
				t.Fatal("owned object not private", p, err)
			}
		}
	}
}

func TestOwnedAtomicSingleSnapshotRetainsUnlimitedCallerAdmission(t *testing.T) {
	dir := t.TempDir()
	// The legacy budget is not an individual snapshot size limit. Callers that
	// explicitly admit unlimited profile bytes must keep that behavior.
	payload := make([]byte, int(atomicLegacyBytes)+1)
	if err := AtomicWritePrivate(filepath.Join(dir, "unlimited.bin"), payload); err != nil {
		t.Fatal("hidden individual snapshot cap", err)
	}
	info, err := os.Stat(filepath.Join(dir, "unlimited.bin"))
	if err != nil || info.Size() != int64(len(payload)) {
		t.Fatal("snapshot size changed", err)
	}
	if count, size := atomicSnapshotState(t, dir); count != 0 || size != 0 {
		t.Fatalf("committed snapshot retained: %d/%d", count, size)
	}
}

func TestOwnedAtomicInProcessAdmissionFinite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "profile.json")
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- atomicWriteOwned(path, []byte(`{}`), &atomicHooks{barrier: func(phase string) {
			if phase == "afterCreateTemp" {
				close(entered)
				<-release
			}
		}})
	}()
	<-entered
	started := time.Now()
	err := AtomicWritePrivate(path, nil)
	close(release)
	if !errors.Is(err, ErrAtomicBusy) || time.Since(started) > 3*time.Second {
		t.Fatal("in-process admission was not bounded", err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	if count, _ := atomicSnapshotState(t, dir); count != 0 {
		t.Fatal("temp retained")
	}
}

func TestOwnedAtomicRefusesProtocolDestinations(t *testing.T) {
	dir := t.TempDir()
	for _, relative := range []string{atomicNamespace, filepath.Join(atomicNamespace, atomicLeaseName), filepath.Join(atomicNamespace, atomicSnapshot)} {
		if err := AtomicWritePrivate(filepath.Join(dir, relative), nil); !errors.Is(err, ErrAtomicRecovery) {
			t.Fatal("protocol destination admitted", relative, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(dir, atomicNamespace)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("invalid protocol destination created a namespace", err)
	}
	if err := AtomicWritePrivate(filepath.Join(dir, "profile.json"), nil); err != nil {
		t.Fatal(err)
	}
}

func TestOwnedAtomicCommittedSaveDoesNotAttemptCleanup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "profile.json")
	if err := AtomicWritePrivate(path, []byte(`{"generation":"old"}`)); err != nil {
		t.Fatal(err)
	}
	err := atomicWriteOwned(path, []byte(atomicTestSnapshot), &atomicHooks{
		remove: func(*os.File, string, *os.File) error {
			t.Error("temp cleanup attempted after committed rename")
			return os.ErrPermission
		},
	})
	if err != nil {
		t.Fatal("committed save reported failure", err)
	}
	assertFormalJSON(t, path)
	if count, _ := atomicSnapshotState(t, dir); count != 0 {
		t.Fatal("committed snapshot retained")
	}
}

func TestOwnedAtomicPostRenameDurabilityOutcome(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		t.Run(fmt.Sprint("uncertain=", uncertain), func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "profile.json")
			if err := AtomicWritePrivate(path, []byte(`{"generation":"old"}`)); err != nil {
				t.Fatal(err)
			}
			parent, err := atomicOpenDirectory(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer parent.Close()
			prior, err := atomicOpenChild(parent, "profile.json", false, false, false)
			if err != nil {
				t.Fatal(err)
			}
			defer prior.Close()
			priorMeta, err := atomicFileMetadata(prior)
			if err != nil {
				t.Fatal(err)
			}
			oldData, readErr := io.ReadAll(prior)
			closeErr := prior.Close()
			if readErr != nil || closeErr != nil || string(oldData) != `{"generation":"old"}` {
				t.Fatal("prior snapshot precondition failed", readErr, closeErr)
			}
			flushes := 0
			beforeReplace := false
			err = atomicWriteOwned(path, []byte(atomicTestSnapshot), &atomicHooks{
				barrier: func(phase string) {
					if phase == "beforeReplace" {
						beforeReplace = true
						current, err := atomicChildMetadata(parent, "profile.json")
						if err != nil || current.id != priorMeta.id {
							t.Fatal("destination changed before replacement", err)
						}
					}
				},
				remove: func(*os.File, string, *os.File) error {
					t.Error("cleanup ran after replacement")
					return os.ErrPermission
				},
				syncReplacement: func(f, parent, owned *os.File) error {
					flushes++
					if !beforeReplace {
						t.Fatal("durability boundary before replacement phase")
					}
					snapshot, err := atomicFileMetadata(f)
					if err != nil {
						t.Fatal(err)
					}
					published, err := atomicChildMetadata(parent, "profile.json")
					if err != nil || published.id != snapshot.id || published.id == priorMeta.id {
						t.Fatal("durability boundary before snapshot publication", err)
					}
					if _, err := atomicChildMetadata(owned, atomicSnapshot); !errors.Is(err, os.ErrNotExist) {
						t.Fatal("published snapshot still has temporary binding", err)
					}
					// Run the native implementation, including both directory syncs on Unix
					// or the renamed handle flush on Windows, before injecting a failure.
					if err := atomicSyncReplacement(f, parent, owned); err != nil {
						return err
					}
					if uncertain {
						return os.ErrPermission
					}
					return nil
				},
			})
			if flushes != 1 {
				t.Fatal("publication not flushed exactly once", flushes, err)
			}
			if uncertain {
				if !errors.Is(err, ErrAtomicCommitted) || !errors.Is(err, os.ErrPermission) {
					t.Fatal("post-rename outcome/cause lost", err)
				}
			} else if err != nil {
				t.Fatal("durably published save reported failure", err)
			}
			// Read payloads after the writer closes its renamed native handle.
			data, readErr := os.ReadFile(path)
			if readErr != nil || string(data) != atomicTestSnapshot {
				t.Fatal("published destination does not contain new snapshot", readErr)
			}
			if count, _ := atomicSnapshotState(t, dir); count != 0 {
				t.Fatal("committed snapshot retained")
			}
			// An explicit later write demonstrates the failed flush released its lease.
			if err := AtomicWritePrivate(filepath.Join(dir, "other.json"), []byte(`{}`)); err != nil {
				t.Fatal("post-commit lease retained", err)
			}
		})
	}
}

func TestOwnedAtomicExportAtApprovedDirectoryEntryBound(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "export.json")
	if err := AtomicWritePrivate(path, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	// The destination and owned namespace count toward the approved total bound.
	for i := 0; i < atomicScanEntries-2; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("foreign-%d", i)), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := AtomicWritePrivate(path, []byte(atomicTestSnapshot)); err != nil {
		t.Fatal("exactly bounded export directory regressed", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "one-too-many"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := AtomicWritePrivate(path, []byte(`{}`)); !errors.Is(err, ErrAtomicRecovery) {
		t.Fatal("over-bound export admitted", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != atomicTestSnapshot {
		t.Fatal("failed export altered published destination", err)
	}
}
