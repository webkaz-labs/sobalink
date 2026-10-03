package transfer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/webkaz-labs/sobalink/internal/config"
)

// FileReceiveAccountingStore has no Manager/Core callbacks. Callers hold the
// profile's exclusive lock for its lifetime; snapshots use the config writer.
type FileReceiveAccountingStore struct {
	Path   string
	Limits AccountingLimits
}

func accountingLimits(l AccountingLimits) AccountingLimits {
	if l.MaxBytes == 0 {
		l.MaxBytes = 16 << 20
	}
	if l.MaxEntries == 0 {
		l.MaxEntries = 65536
	}
	if l.MaxDepth == 0 {
		l.MaxDepth = 16
	}
	if l.MaxPathBytes == 0 {
		l.MaxPathBytes = 4096
	}
	return l
}

func (s FileReceiveAccountingStore) LoadReceiveAccounting() (ReceiveAccounting, error) {
	var state ReceiveAccounting
	budget := accountingLimits(s.Limits)
	if budget.MaxBytes < 1 || budget.MaxBytes >= math.MaxInt64 {
		return state, ErrLimit
	}
	root, err := openDestination(filepath.Dir(s.Path))
	if err != nil {
		return state, err
	}
	defer root.Close()
	f, err := openAccountingFile(root, filepath.Base(s.Path))
	if err != nil {
		return state, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() > budget.MaxBytes || !accountingPrivate(f, info) {
		return state, ErrUnsafePath
	}
	data, err := io.ReadAll(io.LimitReader(f, budget.MaxBytes+1))
	if err != nil || int64(len(data)) > budget.MaxBytes {
		return state, ErrLimit
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return state, ErrReceiveRecovery
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return state, ErrReceiveRecovery
	}
	return state, validateAccounting(state, budget)
}

func (s FileReceiveAccountingStore) SaveReceiveAccounting(state ReceiveAccounting) error {
	budget := accountingLimits(s.Limits)
	if err := validateAccounting(state, budget); err != nil {
		return err
	}
	data, err := json.Marshal(state)
	if err != nil || int64(len(data)) > budget.MaxBytes {
		return ErrLimit
	}
	if err := config.AtomicWrite(s.Path, data); err != nil {
		return ErrReceiveRecovery
	}
	return nil
}

func validateAccounting(state ReceiveAccounting, budget AccountingLimits) error {
	if state.Version != 1 {
		return ErrReceiveRecovery
	}
	if budget.MaxBytes < 1 || budget.MaxEntries < 1 || budget.MaxDepth < 1 || budget.MaxPathBytes < 1 || int64(len(state.Roots)) > budget.MaxEntries {
		return ErrLimit
	}
	seen := make(map[string]bool, len(state.Roots))
	for _, r := range state.Roots {
		if !validDestination(r.Destination) || !validDestination(r.OwnedRoot) || int64(len(r.Destination)) > budget.MaxPathBytes || int64(len(r.OwnedRoot)) > budget.MaxPathBytes || filepath.Dir(r.OwnedRoot) != r.Destination || !strings.HasPrefix(filepath.Base(r.OwnedRoot), "sobalink-") || !strings.HasPrefix(r.Stage, ".incoming-") || filepath.Base(r.Stage) != r.Stage || strings.ContainsAny(r.Stage, `/\\`) || !hexToken(r.OwnerToken, 32) || r.DestinationIdentity == "" || r.RootIdentity == "" || r.StageIdentity == "" {
			return ErrUnsafePath
		}
		if len(r.Stage) > 128 || len(r.DestinationIdentity) > 128 || len(r.RootIdentity) > 128 || len(r.StageIdentity) > 128 || seen[r.OwnedRoot] {
			return ErrUnsafePath
		}
		seen[r.OwnedRoot] = true
	}
	return nil
}

func hexToken(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func newOwnerToken() (string, error) { return randomName("") }

// openAccountingFile rejects symlinks/special files before opening, uses a
// no-follow platform open, and verifies the opened handle against the name.
func openAccountingFile(root *os.Root, name string) (*os.File, error) {
	before, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, ErrUnsafePath
	}
	f, err := accountingOpen(root, name)
	if err != nil {
		return nil, err
	}
	after, err := f.Stat()
	if err != nil || !after.Mode().IsRegular() || !os.SameFile(before, after) {
		f.Close()
		return nil, ErrUnsafePath
	}
	return f, nil
}

func rootIdentity(root *os.Root) (string, error) {
	f, err := root.Open(".")
	if err != nil {
		return "", err
	}
	defer f.Close()
	return accountingIdentity(f)
}

func receiveRootRecord(destination, actual, stage, token, destinationIdentity string, root *os.Root) (ReceiveRoot, error) {
	record := ReceiveRoot{Destination: destination, OwnedRoot: actual, Stage: stage, OwnerToken: token}
	parent, err := openDestination(destination)
	if err != nil {
		return record, err
	}
	defer parent.Close()
	record.DestinationIdentity, err = rootIdentity(parent)
	if err != nil || record.DestinationIdentity != destinationIdentity {
		return record, ErrUnsafePath
	}
	current, err := parent.Lstat(filepath.Base(actual))
	opened, openedErr := root.Stat(".")
	if err != nil || openedErr != nil || !os.SameFile(current, opened) {
		return record, ErrUnsafePath
	}
	record.RootIdentity, err = rootIdentity(root)
	if err != nil {
		return record, err
	}
	child, err := root.OpenRoot(stage)
	if err != nil {
		return record, err
	}
	defer child.Close()
	record.StageIdentity, err = rootIdentity(child)
	return record, err
}

// Inventory never examines or removes saved output. A missing child is zero
// only after the recorded parent identity is checked. An unavailable/replaced
// parent, unsafe file, overflow or incomplete traversal returns no usable total.
func inventoryReceive(ctx context.Context, state ReceiveAccounting, budget AccountingLimits) (ReceiveAccounting, int64, error) {
	if err := validateAccounting(state, budget); err != nil {
		return state, 0, err
	}
	next := ReceiveAccounting{Version: 1}
	var total, entries, metadata int64
	seen := map[string]bool{}
	for _, record := range state.Roots {
		if err := ctx.Err(); err != nil {
			return state, 0, err
		}
		entries++
		metadata += int64(len(record.Destination) + len(record.OwnedRoot) + len(record.Stage) + len(record.OwnerToken) + len(record.RootIdentity) + len(record.StageIdentity) + len(record.DestinationIdentity))
		if entries > budget.MaxEntries || metadata > budget.MaxBytes {
			return state, 0, ErrLimit
		}
		beforeEntries := entries
		bytes, count, meta, err := inventoryReceiveRoot(ctx, record, budget, entries, metadata, seen)
		if err != nil {
			return state, 0, err
		}
		entries, metadata = count, meta
		if bytes > math.MaxInt64-total {
			return state, 0, ErrLimit
		}
		total += bytes
		// Retire verified empty/deleted staging, but retain zero-length files
		// and duplicate hardlink names even when their added payload bytes are zero.
		if count > beforeEntries {
			next.Roots = append(next.Roots, record)
		}
	}
	return next, total, ctx.Err()
}

func inventoryReceiveRoot(ctx context.Context, record ReceiveRoot, budget AccountingLimits, entries, metadata int64, seen map[string]bool) (bytes, count, meta int64, resultErr error) {
	parent, err := openDestination(record.Destination)
	if err != nil {
		return 0, 0, 0, err
	}
	defer parent.Close()
	parentInfo, err := parent.Stat(".")
	if err != nil {
		return 0, 0, 0, err
	}
	defer func() {
		if resultErr != nil {
			return
		}
		current, err := os.Lstat(record.Destination)
		if err != nil || !os.SameFile(parentInfo, current) {
			bytes, count, meta, resultErr = 0, 0, 0, ErrUnsafePath
		}
	}()
	identity, err := rootIdentity(parent)
	if err != nil || identity != record.DestinationIdentity {
		return 0, 0, 0, ErrUnsafePath
	}
	name := filepath.Base(record.OwnedRoot)
	info, err := parent.Lstat(name)
	defer func() {
		if resultErr != nil {
			return
		}
		current, currentErr := parent.Lstat(name)
		if info == nil {
			if !errors.Is(currentErr, os.ErrNotExist) {
				bytes, count, meta, resultErr = 0, 0, 0, ErrUnsafePath
			}
		} else if currentErr != nil || !os.SameFile(info, current) {
			bytes, count, meta, resultErr = 0, 0, 0, ErrUnsafePath
		}
	}()
	if errors.Is(err, os.ErrNotExist) {
		return 0, entries, metadata, nil
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return 0, 0, 0, ErrUnsafePath
	}
	root, err := parent.OpenRoot(name)
	if err != nil {
		return 0, 0, 0, err
	}
	defer root.Close()
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		return 0, 0, 0, ErrUnsafePath
	}
	identity, err = rootIdentity(root)
	if err != nil || identity != record.RootIdentity {
		return 0, 0, 0, ErrUnsafePath
	}
	marker, err := openAccountingFile(root, ".sobalink-owner")
	if err != nil {
		return 0, 0, 0, err
	}
	token, readErr := io.ReadAll(io.LimitReader(marker, 129))
	marker.Close()
	if readErr != nil || string(token) != record.OwnerToken {
		return 0, 0, 0, ErrUnsafePath
	}
	stageInfo, err := root.Lstat(record.Stage)
	defer func() {
		if resultErr != nil {
			return
		}
		current, currentErr := root.Lstat(record.Stage)
		if stageInfo == nil {
			if !errors.Is(currentErr, os.ErrNotExist) {
				bytes, count, meta, resultErr = 0, 0, 0, ErrUnsafePath
			}
		} else if currentErr != nil || !os.SameFile(stageInfo, current) {
			bytes, count, meta, resultErr = 0, 0, 0, ErrUnsafePath
		}
	}()
	if errors.Is(err, os.ErrNotExist) {
		return 0, entries, metadata, nil
	}
	if err != nil || !stageInfo.IsDir() || stageInfo.Mode()&os.ModeSymlink != 0 {
		return 0, 0, 0, ErrUnsafePath
	}
	stage, err := root.OpenRoot(record.Stage)
	if err != nil {
		return 0, 0, 0, err
	}
	defer stage.Close()
	opened, err = stage.Stat(".")
	if err != nil || !os.SameFile(stageInfo, opened) {
		return 0, 0, 0, ErrUnsafePath
	}
	identity, err = rootIdentity(stage)
	if err != nil || identity != record.StageIdentity {
		return 0, 0, 0, ErrUnsafePath
	}
	// Staging is flat by contract; directories (including nested leftovers) are
	// indeterminate and fail closed rather than traversing an arbitrary tree.
	dir, err := stage.Open(".")
	if err != nil {
		return 0, 0, 0, err
	}
	defer dir.Close()
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return 0, 0, 0, err
		}
		names, readErr := dir.Readdirnames(64)
		for _, name := range names {
			if err := ctx.Err(); err != nil {
				return 0, 0, 0, err
			}
			entries++
			metadata += int64(len(name) + 128)
			if entries > budget.MaxEntries || metadata > budget.MaxBytes || int64(len(name)) > budget.MaxPathBytes {
				return 0, 0, 0, ErrLimit
			}
			f, err := openAccountingFile(stage, name)
			if err != nil {
				return 0, 0, 0, err
			}
			stat, statErr := f.Stat()
			id, identityErr := accountingIdentity(f)
			f.Close()
			if statErr != nil || identityErr != nil || stat.Size() < 0 {
				return 0, 0, 0, ErrUnsafePath
			}
			if !seen[id] {
				if stat.Size() > math.MaxInt64-total {
					return 0, 0, 0, ErrLimit
				}
				total += stat.Size()
				seen[id] = true
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return 0, 0, 0, readErr
		}
	}
	return total, entries, metadata, nil
}
