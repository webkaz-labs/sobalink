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
	"slices"
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

func (s ReceiveAccounting) MarshalJSON() ([]byte, error) {
	type legacy ReceiveAccounting
	if s.Version != 2 {
		return json.Marshal(legacy(s))
	}
	return json.Marshal(struct {
		Version     int                 `json:"version"`
		Roots       []ReceiveRoot       `json:"roots"`
		Preparation *ReceivePreparation `json:"preparation"`
	}{s.Version, s.Roots, s.Preparation})
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
	if err := validateAccountingFields(data, budget); err != nil {
		return state, err
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

func (s FileReceiveAccountingStore) SaveReceiveAccounting(state ReceiveAccounting, leases ...ReceiveRetirementLease) error {
	if len(leases) > 1 || (len(leases) == 1 && leases[0] == nil) {
		return ErrState
	}
	budget := accountingLimits(s.Limits)
	if err := validateAccounting(state, budget); err != nil {
		return err
	}
	// Empty indexes are written as explicit arrays; omission is never an empty index.
	if state.Roots == nil {
		state.Roots = []ReceiveRoot{}
	}
	data, err := json.Marshal(state)
	if err != nil || int64(len(data)) > budget.MaxBytes {
		return ErrLimit
	}
	write := func() error {
		if len(leases) == 1 {
			return leases[0].LeaseWrite(s.Path, data)
		}
		return config.AtomicWrite(s.Path, data)
	}
	if err := write(); err != nil {
		return ErrReceiveRecovery
	}
	return nil
}

// Required exact keys prevent omission, duplicate overwrite and case aliases
// from turning damaged accounting into an apparently empty inventory.
func accountingObject(data []byte, required ...string) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, ErrReceiveRecovery
	}
	fields := make(map[string]json.RawMessage, len(required))
	for decoder.More() {
		token, err := decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok || !slices.Contains(required, key) || fields[key] != nil {
			return nil, ErrReceiveRecovery
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, ErrReceiveRecovery
		}
		fields[key] = value
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
		return nil, ErrReceiveRecovery
	}
	if len(fields) != len(required) {
		return nil, ErrReceiveRecovery
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, ErrReceiveRecovery
	}
	return fields, nil
}

func validateAccountingFields(data []byte, budget AccountingLimits) error {
	var header struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return ErrReceiveRecovery
	}
	keys := []string{"version", "roots"}
	if header.Version == 2 {
		keys = append(keys, "preparation")
	}
	fields, err := accountingObject(data, keys...)
	if err != nil {
		return err
	}
	if header.Version == 2 && !bytes.Equal(bytes.TrimSpace(fields["preparation"]), []byte("null")) {
		if _, err := accountingObject(fields["preparation"], "destination", "destinationIdentity", "root", "stage", "ownerToken"); err != nil {
			return err
		}
	}
	var roots []json.RawMessage
	if err := json.Unmarshal(fields["roots"], &roots); err != nil {
		return ErrReceiveRecovery
	}
	if int64(len(roots)) > budget.MaxEntries {
		return ErrLimit
	}
	for _, root := range roots {
		if _, err := accountingObject(root, "destination", "destinationIdentity", "ownedRoot", "rootIdentity", "ownerToken", "stage", "stageIdentity"); err != nil {
			return err
		}
	}
	return nil
}

func validateAccounting(state ReceiveAccounting, budget AccountingLimits) error {
	if state.Version != 1 && state.Version != 2 || state.Version == 1 && state.Preparation != nil {
		return ErrReceiveRecovery
	}
	if budget.MaxBytes < 1 || budget.MaxEntries < 1 || budget.MaxDepth < 1 || budget.MaxPathBytes < 1 || int64(len(state.Roots)) > budget.MaxEntries {
		return ErrLimit
	}
	entries := int64(len(state.Roots))
	if p := state.Preparation; p != nil {
		entries++
		if !validDestination(p.Destination) || int64(len(p.Destination)) > budget.MaxPathBytes || int64(len(filepath.Join(p.Destination, p.Root))) > budget.MaxPathBytes || !hexToken(strings.TrimPrefix(p.Root, "sobalink-"), 32) || !strings.HasPrefix(p.Root, "sobalink-") || !strings.HasPrefix(p.Stage, ".incoming-") || !hexToken(strings.TrimPrefix(p.Stage, ".incoming-"), 32) || !hexToken(p.OwnerToken, 32) || p.DestinationIdentity == "" || len(p.DestinationIdentity) > 128 {
			return ErrUnsafePath
		}
	}
	if entries > budget.MaxEntries {
		return ErrLimit
	}
	canonical := state
	if canonical.Roots == nil {
		canonical.Roots = []ReceiveRoot{}
	}
	data, err := json.Marshal(canonical)
	if err != nil || int64(len(data)) > budget.MaxBytes {
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
		if p := state.Preparation; p != nil && r.OwnedRoot == filepath.Join(p.Destination, p.Root) && (r.Destination != p.Destination || r.DestinationIdentity != p.DestinationIdentity || r.Stage != p.Stage || r.OwnerToken != p.OwnerToken) {
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
	if err := verifyMissingPreparation(ctx, state.Preparation); err != nil {
		return state, 0, err
	}
	next := ReceiveAccounting{Version: state.Version}
	var total, entries, metadata int64
	if state.Preparation != nil {
		entries = 1
		data, _ := json.Marshal(state.Preparation)
		metadata = int64(len(data))
	}
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
	if err := validateReceiveOwnerMarker(stage, record.OwnerToken); err != nil {
		return 0, 0, 0, err
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
			// Only the exact, validated internal marker is metadata. Every other
			// regular staging file counts, regardless of its name.
			if name == receiveOwnerMarker {
				continue
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

func validateReceiveOwnerMarker(stage *os.Root, token string) error {
	marker, err := openAccountingFile(stage, receiveOwnerMarker)
	if err != nil {
		return err
	}
	defer marker.Close()
	value, err := io.ReadAll(io.LimitReader(marker, 129))
	if err != nil || string(value) != token {
		return ErrUnsafePath
	}
	return nil
}

// Verify a missing planned component under the original parent, without opening
// or interpreting an existing child. Repeat at the persistence/publication gate.
func verifyMissingPreparation(ctx context.Context, p *ReceivePreparation) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if p == nil {
		return nil
	}
	parent, err := openDestination(p.Destination)
	if err != nil {
		return err
	}
	defer parent.Close()
	id, err := rootIdentity(parent)
	if err != nil || id != p.DestinationIdentity {
		return ErrUnsafePath
	}
	if _, err := parent.Lstat(p.Root); !errors.Is(err, os.ErrNotExist) {
		return ErrReceiveRecovery
	}
	current, err := openDestination(p.Destination)
	if err != nil {
		return err
	}
	defer current.Close()
	id, err = rootIdentity(current)
	if err != nil || id != p.DestinationIdentity {
		return ErrUnsafePath
	}
	if _, err := current.Lstat(p.Root); !errors.Is(err, os.ErrNotExist) {
		return ErrReceiveRecovery
	}
	return ctx.Err()
}

func accountingChanged(a, b ReceiveAccounting) bool {
	return len(a.Roots) != len(b.Roots) || (a.Preparation == nil) != (b.Preparation == nil)
}

func (m *Manager) saveInventoryLocked(ctx context.Context, before, next ReceiveAccounting) error {
	if !accountingChanged(before, next) {
		return m.accountingStore.SaveReceiveAccounting(next)
	}
	return m.guardedRetirementLocked(ctx, before, next, false, nil)
}

func verifyPreparationParent(destination, identity string) error {
	parent, err := openDestination(destination)
	if err != nil {
		return err
	}
	defer parent.Close()
	current, err := rootIdentity(parent)
	if err != nil || current != identity {
		return ErrUnsafePath
	}
	return nil
}
