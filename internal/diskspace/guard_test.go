package diskspace

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func fixtureFile(t *testing.T) *os.File {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "write-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { file.Close() })
	return file
}

func TestGuardBoundariesAndInspectionFailure(t *testing.T) {
	file := fixtureFile(t)
	for _, tc := range []struct {
		name     string
		free     uint64
		reserve  int64
		probeErr error
		want     error
	}{
		{"exact", 100 + WriteAllowanceBytes, 100, nil, nil},
		{"below", 99 + WriteAllowanceBytes, 100, nil, ErrLow},
		{"unreadable", math.MaxUint64, 100, io.ErrUnexpectedEOF, ErrUnknown},
		{"empty", 0, 100, nil, ErrLow},
		{"negative", math.MaxUint64, -1, nil, ErrUnknown},
		{"zero", math.MaxUint64, 0, nil, ErrUnknown},
		{"large-reserve", math.MaxInt64, math.MaxInt64, nil, ErrLow},
		{"largest-report", math.MaxUint64, math.MaxInt64, nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			guard := New(func(*os.File) (uint64, error) { return tc.free, tc.probeErr })
			var dst bytes.Buffer
			n, err := guard.write(context.Background(), file, &dst, []byte("data"), tc.reserve)
			if !errors.Is(err, tc.want) {
				t.Fatalf("result %d, %v", n, err)
			}
			if tc.want != nil && (n != 0 || dst.Len() != 0) {
				t.Fatal("failed admission wrote bytes")
			}
			if guard.inFlight != 0 {
				t.Fatal("allowance leaked")
			}
		})
	}
}

func TestGuardRechecksEveryChunkAndReleasesFailures(t *testing.T) {
	file := fixtureFile(t)
	for _, failure := range []error{nil, io.ErrUnexpectedEOF} {
		calls := 0
		guard := New(func(*os.File) (uint64, error) {
			calls++
			if calls > 1 {
				return 0, failure
			}
			return 1 << 30, nil
		})
		var dst bytes.Buffer
		n, err := guard.write(context.Background(), file, &dst, make([]byte, 3*ChunkBytes), 1)
		want := ErrLow
		if failure != nil {
			want = ErrUnknown
		}
		if n != ChunkBytes || dst.Len() != ChunkBytes || !errors.Is(err, want) || calls != 2 || guard.inFlight != 0 {
			t.Fatalf("partial result %d %v calls=%d flight=%d", n, err, calls, guard.inFlight)
		}
	}
	guard := New(func(*os.File) (uint64, error) { return 1 << 30, nil })
	if _, err := guard.write(context.Background(), file, writerFunc(func([]byte) (int, error) { return 0, io.ErrClosedPipe }), []byte("x"), 1); !errors.Is(err, io.ErrClosedPipe) || guard.inFlight != 0 {
		t.Fatalf("write failure %v allowance=%d", err, guard.inFlight)
	}
	if _, err := guard.write(context.Background(), file, writerFunc(func([]byte) (int, error) { return 0, nil }), []byte("x"), 1); !errors.Is(err, io.ErrShortWrite) || guard.inFlight != 0 {
		t.Fatalf("short write %v allowance=%d", err, guard.inFlight)
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

func TestGuardConcurrentWritesChargeOutstandingAllowances(t *testing.T) {
	file := fixtureFile(t)
	guard := New(func(*os.File) (uint64, error) { return 1 + WriteAllowanceBytes, nil })
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := guard.write(context.Background(), file, writerFunc(func(p []byte) (int, error) { close(entered); <-release; return len(p), nil }), []byte("x"), 1)
		done <- err
	}()
	<-entered
	var dst bytes.Buffer
	if _, err := guard.write(context.Background(), file, &dst, []byte("y"), 1); !errors.Is(err, ErrLow) || dst.Len() != 0 {
		t.Fatalf("overlapping writer escaped reserve: %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	// A completed write releases its allowance. No monotonic lifetime counter.
	for range 100 {
		if _, err := guard.write(context.Background(), file, &dst, []byte("z"), 1); err != nil {
			t.Fatal(err)
		}
	}
}

func TestGuardFiniteInFlightBudgetAndCancelledWait(t *testing.T) {
	file := fixtureFile(t)
	var probes atomic.Int32
	guard := New(func(*os.File) (uint64, error) { probes.Add(1); return math.MaxUint64, nil })
	slots := int(MaxInFlightBytes / WriteAllowanceBytes)
	entered := make(chan struct{}, slots)
	release := make(chan struct{})
	var workers sync.WaitGroup
	for range slots {
		workers.Add(1)
		go func() {
			defer workers.Done()
			_, err := guard.write(context.Background(), file, writerFunc(func(p []byte) (int, error) { entered <- struct{}{}; <-release; return len(p), nil }), []byte("x"), 1)
			if err != nil {
				t.Error(err)
			}
		}()
	}
	for range slots {
		<-entered
	}
	checked := make(chan error, 1)
	go func() { checked <- guard.Check(context.Background(), file, 1) }()
	select {
	case err := <-checked:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("preflight waited behind blocked payload writes")
	}
	ctx, cancel := context.WithCancel(context.Background())
	waiting := make(chan error, 1)
	go func() { _, err := guard.write(ctx, file, io.Discard, []byte("z"), 1); waiting <- err }()
	cancel()
	select {
	case err := <-waiting:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled allowance wait blocked")
	}
	if probes.Load() != int32(slots+1) {
		t.Fatal("finite admission escaped")
	}
	close(release)
	workers.Wait()
	if guard.inFlight != 0 {
		t.Fatal("allowance leaked")
	}
}

func TestGuardCancellationAfterProbePreventsWrite(t *testing.T) {
	file := fixtureFile(t)
	ctx, cancel := context.WithCancel(context.Background())
	guard := New(func(*os.File) (uint64, error) { cancel(); return math.MaxUint64, nil })
	var dst bytes.Buffer
	if _, err := guard.write(ctx, file, &dst, []byte("x"), 1); !errors.Is(err, context.Canceled) || dst.Len() != 0 {
		t.Fatalf("cancelled result %v bytes=%d", err, dst.Len())
	}
}

func TestNativeSpaceProbeOpenAndClosedFile(t *testing.T) {
	file := fixtureFile(t)
	if _, err := available(file); err != nil {
		t.Fatalf("native available-space probe failed: %v", err)
	}
	file.Close()
	if err := New(nil).Check(context.Background(), file, 1); !errors.Is(err, ErrUnknown) {
		t.Fatalf("closed file did not fail closed: %v", err)
	}
}

func testNativeDiskErrors(t *testing.T, full, unrelated []error) {
	t.Helper()
	file := fixtureFile(t)
	for _, native := range full {
		for _, err := range []error{native, &os.PathError{Op: "write", Path: "private/path", Err: native}} {
			if got := NormalizeError(err); !errors.Is(got, ErrLow) || got.Error() != ErrLow.Error() {
				t.Fatalf("native full error was not sanitized: %v", got)
			}
			guard := New(func(*os.File) (uint64, error) { return 1 << 40, nil })
			n, got := guard.write(context.Background(), file, writerFunc(func([]byte) (int, error) { return 0, err }), []byte("x"), 1)
			if n != 0 || !errors.Is(got, ErrLow) || guard.inFlight != 0 {
				t.Fatalf("failed disk write leaked allowance: %d %v %d", n, got, guard.inFlight)
			}
		}
	}
	for _, err := range append(unrelated, nil, io.ErrClosedPipe) {
		if NormalizeError(err) != err {
			t.Fatalf("unrelated failure changed: %v", err)
		}
	}
}
