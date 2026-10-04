package transfer

import (
	"context"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/diskspace"
)

func TestReceiveSpaceFailureBeforeAcceptancePreservesDestination(t *testing.T) {
	for _, probeErr := range []error{nil, errors.New("private probe detail")} {
		space := diskspace.New(func(*os.File) (uint64, error) { return 0, probeErr })
		m, peer := testManager(t, Options{DiskSpace: space})
		destination := t.TempDir()
		keep := filepath.Join(destination, "original")
		if err := os.WriteFile(keep, []byte("keep"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := m.Offer(peer, testManifest("batch", testEntry("file", "file", "payload"))); err != nil {
			t.Fatal(err)
		}
		_, err := m.Accept("batch", destination)
		want := diskspace.ErrLow
		if probeErr != nil {
			want = diskspace.ErrUnknown
		}
		if !errors.Is(err, want) {
			t.Fatalf("accept error %v", err)
		}
		entries, _ := os.ReadDir(destination)
		data, _ := os.ReadFile(keep)
		if len(entries) != 1 || string(data) != "keep" {
			t.Fatal("failed admission changed existing files")
		}
		batch, _ := m.Get("batch")
		if batch.State != Pending {
			t.Fatalf("failed acceptance is not retryable: %v", batch.State)
		}
	}
}

func TestReceiveSpaceDropsFailClosedAndRetryPreservesSavedFiles(t *testing.T) {
	for _, probeErr := range []error{nil, errors.New("private probe detail")} {
		var calls, failAfter atomic.Int32
		failAfter.Store(math.MaxInt32)
		space := diskspace.New(func(*os.File) (uint64, error) {
			if calls.Add(1) > failAfter.Load() {
				return 0, probeErr
			}
			return 1 << 40, nil
		})
		m, peer := testManager(t, Options{DiskSpace: space})
		payload := strings.Repeat("x", 3*diskspace.ChunkBytes)
		batch := testAccepted(t, m, peer, testManifest("batch", testEntry("saved", "saved", "keep"), testEntry("later", "later", payload)))
		saved, err := m.ReceiveFile(context.Background(), peer, "batch", "saved", strings.NewReader("keep"))
		if err != nil {
			t.Fatal(err)
		}
		// Early stream check and first write pass, then the next chunk fails.
		failAfter.Store(calls.Load() + 2)
		_, err = m.ReceiveFile(context.Background(), peer, "batch", "later", strings.NewReader(payload))
		want := diskspace.ErrLow
		if probeErr != nil {
			want = diskspace.ErrUnknown
		}
		if !errors.Is(err, want) {
			t.Fatalf("write error %v", err)
		}
		failed, _ := m.Get("batch")
		if failed.State != Partial || failed.Files[1].State != FileFailed || failed.Files[1].CompletedBytes != diskspace.ChunkBytes || failed.Files[1].Error != want.ErrorCode() {
			t.Fatalf("partial state %+v", failed)
		}
		if _, err := os.Stat(filepath.Join(batch.Destination, "later")); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("failed file finalized")
		}
		if data, err := os.ReadFile(filepath.Join(batch.Destination, "saved")); err != nil || string(data) != "keep" {
			t.Fatal("saved file changed")
		}
		stage := m.batches["batch"].stage
		entries, err := os.ReadDir(filepath.Join(batch.Destination, stage))
		if err != nil || (len(entries) != 1 || entries[0].Name() != receiveOwnerMarker) {
			t.Fatalf("active temporary file not cleaned: %v %v", entries, err)
		}
		// Saved acknowledgements work even when space cannot currently be read.
		if ack, err := m.ReceiveFile(context.Background(), peer, "batch", "saved", unreadableReader{t}); err != nil || ack != saved {
			t.Fatalf("saved ACK changed: %v %v", ack, err)
		}
		failAfter.Store(math.MaxInt32)
		if _, err := m.RetryFile("batch", "later"); err != nil {
			t.Fatal(err)
		}
		if _, err := m.ReceiveFile(context.Background(), peer, "batch", "later", strings.NewReader(payload)); err != nil {
			t.Fatal(err)
		}
		final, _ := m.Get("batch")
		if final.State != Completed {
			t.Fatalf("retry state %v", final.State)
		}
		if data, err := os.ReadFile(filepath.Join(batch.Destination, "later")); err != nil || string(data) != payload {
			t.Fatal("retry bytes changed")
		}
	}
}

func TestReceiveReserveUpdatesApplyBeforeNextWrite(t *testing.T) {
	space := diskspace.New(func(*os.File) (uint64, error) { return 1 << 30, nil })
	m, peer := testManager(t, Options{DiskSpace: space})
	batch := testAccepted(t, m, peer, testManifest("batch", testEntry("file", "file", "payload")))
	limits := DefaultLimits()
	limits.DiskReserveBytes = 2 << 30
	if err := m.UpdateLimits(limits); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ReceiveFile(context.Background(), peer, "batch", "file", unreadableReader{t}); !errors.Is(err, diskspace.ErrLow) {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(batch.Destination, "file")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed write committed")
	}
	limits.DiskReserveBytes = 1
	if err := m.UpdateLimits(limits); err != nil {
		t.Fatal(err)
	}
	if _, err := m.RetryFile("batch", "file"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ReceiveFile(context.Background(), peer, "batch", "file", strings.NewReader("payload")); err != nil {
		t.Fatal(err)
	}
}

func TestReceiveSpaceRechecksEmptyAndDeepDirectoryCreation(t *testing.T) {
	for _, kind := range []string{"empty-folders", "deep-empty-file"} {
		for _, unknown := range []bool{false, true} {
			t.Run(kind+map[bool]string{false: "-low", true: "-unknown"}[unknown], func(t *testing.T) {
				calls, restored := 0, false
				space := diskspace.New(func(*os.File) (uint64, error) {
					calls++
					if !restored && calls >= 4 {
						if unknown {
							return 0, errors.New("probe failed")
						}
						return 0, nil
					}
					return 1 << 40, nil
				})
				m, peer := testManager(t, Options{DiskSpace: space})
				manifest := testManifest("batch", testEntry("file", "a/b/c/d/empty", ""))
				if kind == "empty-folders" {
					manifest = testManifest("batch", Entry{ID: "a", Path: "a", Kind: Directory}, Entry{ID: "b", Path: "b", Kind: Directory}, Entry{ID: "c", Path: "c", Kind: Directory})
				}
				destination := t.TempDir()
				if err := os.WriteFile(filepath.Join(destination, "original"), []byte("keep"), 0600); err != nil {
					t.Fatal(err)
				}
				if _, err := m.Offer(peer, manifest); err != nil {
					t.Fatal(err)
				}
				want := diskspace.ErrLow
				if unknown {
					want = diskspace.ErrUnknown
				}
				for attempt := 0; attempt < 100; attempt++ {
					calls = 0
					_, err := m.Accept("batch", destination)
					if !errors.Is(err, want) || calls != 4 {
						t.Fatalf("metadata guard: calls=%d err=%v", calls, err)
					}
					directories := 0
					if err := filepath.WalkDir(destination, func(path string, entry os.DirEntry, err error) error {
						if err != nil {
							return err
						}
						if path != destination && entry.IsDir() {
							directories++
						}
						return nil
					}); err != nil {
						t.Fatal(err)
					}
					if directories != 0 {
						t.Fatalf("attempt %d retained %d provisional directories", attempt, directories)
					}
					if data, err := os.ReadFile(filepath.Join(destination, "original")); err != nil || string(data) != "keep" {
						t.Fatal("original changed")
					}
					if b := m.batches["batch"]; b.value.State != Pending || !b.reserved || m.metadata == 0 {
						t.Fatal("failed preparation released admission accounting")
					}
				}
				restored = true
				if _, err := m.Accept("batch", destination); err != nil {
					t.Fatalf("metadata retry failed: %v", err)
				}
				if kind == "deep-empty-file" {
					if _, err := m.ReceiveFile(context.Background(), peer, "batch", "file", strings.NewReader("")); err != nil {
						t.Fatal(err)
					}
				}
				result, _ := m.Get("batch")
				if result.State != Completed {
					t.Fatal("retry did not complete")
				}
			})
		}
	}
}

type finalProbeReader struct {
	reader *strings.Reader
	armed  *bool
}

func (r finalProbeReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	if err == io.EOF {
		*r.armed = true
	}
	return n, err
}

func TestReceiveCancellationDuringFinalSpaceProbeCannotCommit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	armed, triggered := false, false
	space := diskspace.New(func(file *os.File) (uint64, error) {
		info, err := file.Stat()
		if err != nil {
			return 0, err
		}
		if armed && info.IsDir() {
			triggered = true
			cancel()
		}
		return 1 << 40, nil
	})
	m, peer := testManager(t, Options{DiskSpace: space})
	batch := testAccepted(t, m, peer, testManifest("batch", testEntry("file", "file", "payload")))
	ack, err := m.ReceiveFile(ctx, peer, "batch", "file", finalProbeReader{strings.NewReader("payload"), &armed})
	if !triggered || !errors.Is(err, ErrCancelled) || ack != (FileAck{}) {
		t.Fatalf("final-probe cancellation: triggered=%v ack=%v err=%v", triggered, ack, err)
	}
	if _, err := os.Stat(filepath.Join(batch.Destination, "file")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("cancelled final probe committed file")
	}
}
