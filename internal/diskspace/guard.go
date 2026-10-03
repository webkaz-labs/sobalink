// Package diskspace guards transfer writes using observed filesystem free space.
// This is a practical safety margin, not a quota or a crash-recovery journal.
package diskspace

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"
)

const (
	DefaultReserveBytes int64 = 512 << 20
	ChunkBytes                = 32 << 10
	// Each chunk charges at least 64 KiB, allowing for allocation rounding.
	WriteAllowanceBytes uint64 = 64 << 10
	MaxInFlightBytes    uint64 = 1 << 20
)

type Error struct{ code, message string }

func (e *Error) Error() string     { return e.message }
func (e *Error) ErrorCode() string { return e.code }

var (
	ErrLow     = &Error{"disk_space_low", "storage or its disk quota is full, or free space is below the transfer safety margin; free space, check the quota or review diskReserveBytes, then retry; original and saved files are preserved"}
	ErrUnknown = &Error{"disk_space_unknown", "available disk space could not be checked; check the destination volume and permissions, then retry; original and saved files are preserved"}
)

// Probe returns bytes available to this process, not total free blocks reserved
// for an administrator. The open file identifies the filesystem being written.
type Probe func(*os.File) (uint64, error)

// Guard shares bounded in-flight write allowances across receivers and outgoing
// staging. Accounting ends with each write; it is never a lifetime byte count.
// Different volumes conservatively share the same small in-flight allowance.
type Guard struct {
	mu       sync.Mutex
	probe    Probe
	inFlight uint64
	changed  chan struct{}
}

var Process = New(nil)

func New(probe Probe) *Guard {
	if probe == nil {
		probe = available
	}
	return &Guard{probe: probe, changed: make(chan struct{})}
}

// Check performs an early check before creating directories or a temporary file.
// Every payload write is checked again; a successful check reserves no future
// payload or filesystem metadata and cannot promise a complete batch will fit.
func (g *Guard) Check(ctx context.Context, file *os.File, reserve int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if reserve <= 0 || file == nil {
		return ErrUnknown
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	// Preflight checks do not hold an allowance or wait behind blocked writes.
	// This also keeps caller-held manager locks free of allowance waits.
	return g.checkLocked(ctx, file, reserve)
}

func (g *Guard) admit(ctx context.Context, file *os.File, reserve int64) (func(), error) {
	if reserve <= 0 || file == nil {
		return nil, ErrUnknown
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		g.mu.Lock()
		if g.inFlight > MaxInFlightBytes-WriteAllowanceBytes {
			changed := g.changed
			g.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-changed:
			}
			continue
		}
		if err := g.checkLocked(ctx, file, reserve); err != nil {
			g.mu.Unlock()
			return nil, err
		}
		g.inFlight += WriteAllowanceBytes
		g.mu.Unlock()
		return func() {
			g.mu.Lock()
			g.inFlight -= WriteAllowanceBytes
			close(g.changed)
			g.changed = make(chan struct{})
			g.mu.Unlock()
		}, nil
	}
}

func (g *Guard) checkLocked(ctx context.Context, file *os.File, reserve int64) error {
	free, err := g.probe(file)
	if err != nil {
		return ErrUnknown
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Subtraction avoids overflow even for hostile/injected capacity values.
	required := uint64(reserve)
	if free < required || free-required < g.inFlight || free-required-g.inFlight < WriteAllowanceBytes {
		return ErrLow
	}
	return nil
}

// Write checks every bounded chunk, accounting for admitted concurrent writes.
// The admission mutex is released before a filesystem write. Space may
// still change due to other processes, metadata, quotas, or delayed allocation.
func (g *Guard) Write(ctx context.Context, file *os.File, p []byte, reserve int64) (int, error) {
	return g.write(ctx, file, file, p, reserve)
}

func (g *Guard) write(ctx context.Context, file *os.File, dst io.Writer, p []byte, reserve int64) (int, error) {
	total := 0
	for len(p) > 0 {
		chunk := p[:min(len(p), ChunkBytes)]
		release, err := g.admit(ctx, file, reserve)
		if err != nil {
			return total, err
		}
		if err = ctx.Err(); err != nil {
			release()
			return total, err
		}
		n, err := dst.Write(chunk)
		release()
		if n < 0 || n > len(chunk) {
			return total, io.ErrShortWrite
		}
		total += n
		if err != nil {
			return total, NormalizeError(err)
		}
		if n != len(chunk) {
			return total, io.ErrShortWrite
		}
		p = p[n:]
	}
	return total, nil
}

func IsCapacityError(err error) bool { return errors.Is(err, ErrLow) || errors.Is(err, ErrUnknown) }

// NormalizeError recognizes only explicit native disk-full/quota failures.
// It removes private filesystem paths while retaining all unrelated I/O errors.
func NormalizeError(err error) error {
	if nativeNoSpace(err) {
		return ErrLow
	}
	return err
}
