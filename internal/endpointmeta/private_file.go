package endpointmeta

import (
	"errors"
	"io"
	"os"

	"github.com/webkaz-labs/sobalink/internal/config"
)

// ReadSnapshotFile reads isolated model evidence with a positive caller-supplied
// byte budget. Missing files and read errors are returned without initialization.
// A successful read does not establish durability, current authority, or recovery
// completion. Pending records and saved times are retained without reconciliation.
//
// Callers must own the file's lifecycle exclusively. These local file checks are
// not a defense against malicious concurrent pathname substitution or whole-file
// rollback, and do not acquire a production profile owner.
func ReadSnapshotFile(path string, budget int) (Snapshot, error) {
	if budget <= 0 {
		return Snapshot{}, ErrCapacity
	}
	info, err := os.Lstat(path)
	if err != nil {
		return Snapshot{}, err
	}
	if !info.Mode().IsRegular() {
		return Snapshot{}, ErrInvalid
	}
	if info.Size() > int64(budget) {
		return Snapshot{}, ErrCapacity
	}
	f, err := os.Open(path)
	if err != nil {
		return Snapshot{}, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return Snapshot{}, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return Snapshot{}, ErrInvalid
	}
	if opened.Size() > int64(budget) {
		return Snapshot{}, ErrCapacity
	}
	return readSnapshotBounded(f, budget)
}

func readSnapshotBounded(r io.Reader, budget int) (Snapshot, error) {
	if budget <= 0 {
		return Snapshot{}, ErrCapacity
	}
	b, err := io.ReadAll(io.LimitReader(r, int64(budget)))
	if err != nil {
		return Snapshot{}, err
	}
	// Probe separately rather than adding one to a possibly maximum-int budget.
	// The stat checks are an early rejection, not the allocation/read boundary.
	var extra [1]byte
	n, err := io.ReadFull(r, extra[:])
	if n != 0 {
		return Snapshot{}, ErrCapacity
	}
	if err != io.EOF {
		return Snapshot{}, err
	}
	return ParseSnapshot(b, budget)
}

// SaveSnapshotFile encodes and publishes an isolated model using the existing
// private-file atomic primitive. It is unused by application profile loaders.
// A nil error confirms durable publication. ErrAtomicCommitted means publication
// occurred with uncertain durability; all other errors report no publication.
// The original error is retained, including errors.Is compatibility. Callers can
// pass this outcome to ResolveSave and must reconcile errors before further use.
//
// Atomic publication is not a logical compare-and-swap. The caller still owns
// exclusive model/lifecycle access, current authorization and recovery. This
// function neither retries nor treats a later read as confirmation of durability.
func SaveSnapshotFile(path string, candidate Snapshot, budget int) (published bool, err error) {
	return saveSnapshotFile(path, candidate, budget, config.AtomicWritePrivate)
}

func saveSnapshotFile(path string, candidate Snapshot, budget int, write func(string, []byte) error) (published bool, err error) {
	b, err := EncodeSnapshot(candidate, budget)
	if err != nil {
		return false, err
	}
	err = write(path, b)
	return err == nil || errors.Is(err, config.ErrAtomicCommitted), err
}
