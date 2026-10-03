// Package boundedlog keeps a recent, finite plain-text log in one file.
package boundedlog

import (
	"errors"
	"os"
	"sync"
)

// Writer serializes writes and keeps the file at or below its byte limit.
// On rollover it retains the last half-file before appending new output. A
// single oversized write retains only its final bytes. Retention is byte-based,
// so the oldest retained line may be incomplete.
type Writer struct {
	mu    sync.Mutex
	file  *os.File
	limit int64
	size  int64
}

// New uses an exclusively owned regular file opened for reading and writing.
// The caller remains responsible for closing the file and its permissions.
func New(file *os.File, limit int64) (*Writer, error) {
	if limit < 2 {
		return nil, errors.New("log byte limit must be at least two")
	}
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("log must be a regular file")
	}
	w := &Writer{file: file, limit: limit, size: info.Size()}
	if w.size > limit {
		if err := w.retain(limit / 2); err != nil {
			return nil, err
		}
	}
	return w, nil
}

func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	written := len(p)
	if int64(len(p)) > w.limit {
		p = p[int64(len(p))-w.limit:]
	}
	if w.size > w.limit-int64(len(p)) {
		keep := min(w.limit/2, w.limit-int64(len(p)))
		if err := w.retain(keep); err != nil {
			return 0, err
		}
	}
	n, err := w.file.WriteAt(p, w.size)
	w.size += int64(n)
	return written - len(p) + n, err
}

func (w *Writer) retain(keep int64) error {
	keep = min(keep, w.size)
	tail := make([]byte, keep)
	if keep > 0 {
		if _, err := w.file.ReadAt(tail, w.size-keep); err != nil {
			return err
		}
	}
	// Reuse the existing file space before shortening it. Rollover never
	// needs a second file or temporarily exceeds the disk budget, and a
	// failed copy does not first discard all previously retained output.
	if _, err := w.file.WriteAt(tail, 0); err != nil {
		return err
	}
	if err := w.file.Truncate(keep); err != nil {
		return err
	}
	w.size = keep
	return nil
}
