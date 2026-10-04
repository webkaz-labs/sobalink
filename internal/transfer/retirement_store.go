package transfer

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"sync"

	"github.com/webkaz-labs/sobalink/internal/config"
)

func (s FileReceiveAccountingStore) WithReceiveAccountingLimits(l AccountingLimits) ReceiveAccountingStore {
	s.Limits = l
	return s
}

func (s FileReceiveAccountingStore) retirementName() string {
	return filepath.Base(s.Path) + ".retirement"
}

type fileRetirementLease struct {
	mu             sync.Mutex
	writer         *config.AtomicWriteLease
	target         string
	after          []byte
	maxBytes       int64
	released       bool
	parent         *os.Root
	file           *os.File
	name           string
	data           []byte
	info           os.FileInfo
	parentIdentity string
}

func (l *fileRetirementLease) Close() error {
	if l == nil {
		return ErrState
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.writer == nil && l.parent == nil && l.file == nil {
		return ErrState
	}
	var err error
	if l.file != nil {
		err = l.file.Close()
		l.file = nil
	}
	if l.parent != nil {
		err = errors.Join(err, l.parent.Close())
		l.parent = nil
	}
	if l.writer != nil {
		err = errors.Join(err, l.writer.Close())
		l.writer = nil
	}
	return err
}

func (l *fileRetirementLease) LeaseWrite(path string, data []byte) error {
	if l == nil {
		return ErrState
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	target, err := filepath.Abs(path)
	if err != nil || target != l.target || l.writer == nil || l.file == nil || l.released || int64(len(data)) > l.maxBytes || !bytes.Equal(data, l.after) {
		return ErrState
	}
	if err := l.checkWriterParent(); err != nil {
		return err
	}
	return l.writer.Write(target, data)
}

func (l *fileRetirementLease) checkWriterParent() error {
	if l.writer == nil || l.parent == nil {
		return ErrState
	}
	f, err := l.parent.Open(".")
	if err != nil {
		return err
	}
	defer f.Close()
	if err := l.writer.CheckParent(f); err != nil {
		return ErrUnsafePath
	}
	return verifyPreparationParent(l.parent.Name(), l.parentIdentity)
}

func (l *fileRetirementLease) Release(verify func() error) error {
	if l == nil {
		return ErrState
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released {
		return ErrState
	}
	if err := l.checkWriterParent(); err != nil {
		return err
	}
	if l.file == nil || l.parent == nil {
		return ErrState
	}
	if err := verifyPreparationParent(l.parent.Name(), l.parentIdentity); err != nil {
		return err
	}
	f, err := openAccountingFile(l.parent, l.name)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !os.SameFile(l.info, info) || !accountingPrivate(f, info) {
		return ErrUnsafePath
	}
	data, err := io.ReadAll(io.LimitReader(f, int64(len(l.data))+1))
	if err != nil || string(data) != string(l.data) {
		return ErrReceiveRecovery
	}
	if verify == nil {
		return ErrState
	}
	if err := verify(); err != nil {
		return err
	}
	current, err := l.parent.Lstat(l.name)
	if err != nil || !os.SameFile(l.info, current) {
		return ErrUnsafePath
	}
	// No post-unlink repair save: unlink is the terminal commit operation.
	if err := l.parent.Remove(l.name); err != nil {
		return err
	}
	l.released = true
	return retirementSyncDirectory(l.parent)
}

func (s FileReceiveAccountingStore) LoadReceiveRetirementGuard(limits AccountingLimits) (*ReceiveRetirementGuard, ReceiveRetirementLease, error) {
	budget := accountingLimits(limits)
	if budget.MaxBytes < 1 || budget.MaxBytes >= math.MaxInt64 {
		return nil, nil, ErrLimit
	}
	parent, err := openDestination(filepath.Dir(s.Path))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			err = ErrReceiveRecovery
		}
		return nil, nil, err
	}
	l := &fileRetirementLease{parent: parent, name: s.retirementName()}
	fail := func(err error) (*ReceiveRetirementGuard, ReceiveRetirementLease, error) {
		l.Close()
		// Only the initial guard lookup can establish guard absence.
		if errors.Is(err, os.ErrNotExist) {
			err = ErrReceiveRecovery
		}
		return nil, nil, err
	}
	l.parentIdentity, err = rootIdentity(parent)
	if err != nil {
		return fail(err)
	}
	if _, err := parent.Lstat(l.name); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			l.Close()
			return nil, nil, os.ErrNotExist
		}
		return fail(err)
	}
	l.writer, err = config.AcquireAtomicWriteLease(s.Path, l.name)
	if err != nil {
		return fail(ErrReceiveRecovery)
	}
	if err := l.checkWriterParent(); err != nil {
		return fail(err)
	}
	l.target, err = filepath.Abs(s.Path)
	if err != nil {
		return fail(err)
	}
	l.maxBytes = budget.MaxBytes
	l.file, err = openAccountingFile(parent, l.name)
	if err != nil {
		return fail(err)
	}
	l.info, err = l.file.Stat()
	if err != nil || l.info.Size() > budget.MaxBytes || !accountingPrivate(l.file, l.info) {
		return fail(ErrUnsafePath)
	}
	l.data, err = io.ReadAll(io.LimitReader(l.file, budget.MaxBytes+1))
	if err != nil || int64(len(l.data)) > budget.MaxBytes {
		return fail(ErrLimit)
	}
	g, err := parseRetirementGuard(l.data, budget)
	if err != nil {
		return fail(err)
	}
	l.after = canonicalAccounting(g.After)
	return &g, l, nil
}

func (s FileReceiveAccountingStore) AcquireReceiveRetirementGuard(g ReceiveRetirementGuard, limits AccountingLimits) (ReceiveRetirementLease, error) {
	if err := validateRetirementGuard(g, accountingLimits(limits)); err != nil {
		return nil, err
	}
	data, err := json.Marshal(g)
	if err != nil {
		return nil, err
	}
	parent, err := openDestination(filepath.Dir(s.Path))
	if err != nil {
		return nil, err
	}
	l := &fileRetirementLease{parent: parent, name: s.retirementName(), data: data}
	fail := func(err error) (ReceiveRetirementLease, error) { l.Close(); return nil, err }
	l.parentIdentity, err = rootIdentity(parent)
	if err != nil {
		return fail(err)
	}
	l.writer, err = config.AcquireAtomicWriteLease(s.Path, l.name)
	if err != nil {
		return fail(err)
	}
	if err := l.checkWriterParent(); err != nil {
		return fail(err)
	}
	l.target, err = filepath.Abs(s.Path)
	if err != nil {
		return fail(err)
	}
	l.after = canonicalAccounting(g.After)
	l.maxBytes = accountingLimits(limits).MaxBytes
	l.file, err = retirementCreate(parent, l.name)
	if err != nil {
		return fail(err)
	}
	l.info, err = l.file.Stat()
	if err != nil || !accountingPrivate(l.file, l.info) {
		return fail(ErrUnsafePath)
	}
	n, err := l.file.Write(data)
	if err != nil {
		return fail(err)
	}
	if n != len(data) {
		return fail(io.ErrShortWrite)
	}
	if err := l.file.Sync(); err != nil {
		return fail(err)
	}
	// Check writer Close, then retain an identity-bearing read handle.
	f, err := openAccountingFile(parent, l.name)
	if err != nil {
		return fail(err)
	}
	opened, err := f.Stat()
	if err != nil || !os.SameFile(opened, l.info) {
		f.Close()
		return fail(ErrUnsafePath)
	}
	err = l.file.Close()
	l.file = f
	if err != nil {
		return fail(err)
	}
	if err := verifyPreparationParent(parent.Name(), l.parentIdentity); err != nil {
		return fail(err)
	}
	if err := retirementSyncDirectory(parent); err != nil {
		return fail(err)
	}
	return l, nil
}

func parseRetirementGuard(data []byte, budget AccountingLimits) (ReceiveRetirementGuard, error) {
	var g ReceiveRetirementGuard
	if int64(len(data)) > budget.MaxBytes {
		return g, ErrLimit
	}
	fields, err := accountingObject(data, "version", "id", "kind", "before", "after", "beforeHash", "afterHash", "witnesses")
	if err != nil {
		return g, err
	}
	for _, key := range []string{"before", "after"} {
		if err := validateAccountingFields(fields[key], budget); err != nil {
			return g, err
		}
	}
	if !bytes.HasPrefix(bytes.TrimSpace(fields["witnesses"]), []byte("[")) {
		return g, ErrReceiveRecovery
	}
	var witnesses []json.RawMessage
	if err := json.Unmarshal(fields["witnesses"], &witnesses); err != nil || int64(len(witnesses)) > budget.MaxEntries {
		return g, ErrReceiveRecovery
	}
	for _, raw := range witnesses {
		f, err := accountingObject(raw, "root", "rootMissing", "markerIdentity")
		if err != nil {
			return g, err
		}
		if _, err := accountingObject(f["root"], "destination", "destinationIdentity", "ownedRoot", "rootIdentity", "ownerToken", "stage", "stageIdentity"); err != nil {
			return g, err
		}
		if string(f["rootMissing"]) != "true" && string(f["rootMissing"]) != "false" {
			return g, ErrReceiveRecovery
		}
	}
	if err := json.Unmarshal(data, &g); err != nil {
		return g, ErrReceiveRecovery
	}
	return g, validateRetirementGuard(g, budget)
}
