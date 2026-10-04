package config

import (
	"bytes"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	atomicNamespace           = ".sobalink-atomic-v1"
	atomicLeaseName           = "owner.lock"
	atomicSnapshot            = "snapshot"
	atomicOwner               = "sobalink atomic persistence v1\n"
	atomicLegacyCount         = 64
	atomicLegacyBytes   int64 = 128 << 20
	atomicScanEntries         = 4096
	atomicAdmissionWait       = 2 * time.Second
)

var (
	ErrAtomicBusy     = errors.New("atomic persistence writer is busy")
	ErrAtomicRecovery = errors.New("atomic persistence inventory requires recovery")
	// ErrAtomicCommitted means replacement occurred but its durability could not
	// be confirmed. Callers must reconcile the destination before retrying.
	ErrAtomicCommitted = errors.New("atomic persistence replacement committed with uncertain durability")
	atomicAdmissions   = func() [64]chan struct{} {
		var gates [64]chan struct{}
		for i := range gates {
			gates[i] = make(chan struct{}, 1)
		}
		return gates
	}()
)

// A fixed number of gates bounds in-process coordination state. The OS lease
// supplies exclusion across aliases and processes; neither lease file nor
// namespace is ever unlinked. OS admission is nonblocking.
func admitAtomic(path string) (func(), error) {
	h := fnv.New64a()
	_, _ = h.Write([]byte(path))
	gate := atomicAdmissions[h.Sum64()%uint64(len(atomicAdmissions))]
	timer := time.NewTimer(atomicAdmissionWait)
	defer timer.Stop()
	select {
	case gate <- struct{}{}:
		return func() { <-gate }, nil
	case <-timer.C:
		return nil, ErrAtomicBusy
	}
}

type atomicMetadata struct {
	id                                      [3]uint64
	size                                    int64
	regular, directory, singleLink, private bool
}

// Hooks exercise the production path at process/crash and substitution
// boundaries. Public writers always use nil; no environment switches enable
// these hooks in an installed binary.
type atomicHooks struct {
	barrier         func(string)
	remove          func(*os.File, string, *os.File) error
	syncReplacement func(*os.File, *os.File, *os.File) error
	syncReclaim     func(*os.File) error
}

func (h *atomicHooks) at(phase string) {
	if h != nil && h.barrier != nil {
		h.barrier(phase)
	}
}

type atomicWriter struct {
	path                             string
	parent, owned, lease             *os.File
	parentInfo, ownedInfo, leaseInfo atomicMetadata
	hooks                            *atomicHooks
}

func recovery(err error) error { return fmt.Errorf("%w: %w", ErrAtomicRecovery, err) }

func atomicDestination(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	// Protocol entries cannot also be formal destinations.
	for _, component := range strings.Split(filepath.ToSlash(abs), "/") {
		if strings.EqualFold(component, atomicNamespace) {
			return "", recovery(errors.New("reserved persistence namespace"))
		}
	}
	return abs, nil
}

// AtomicWriteLease retains the existing parent writer exclusion for one target.
// Admission durably reclaims the owned snapshot before returning. Close releases
// exclusion only; independent caller evidence is never removed.
type AtomicWriteLease struct {
	mu       sync.Mutex
	writer   *atomicWriter
	target   string
	release  func()
	reserved []string
}

// AcquireAtomicWriteLease reserves additional parent entry names before metadata
// allocation. Reservations do not authorize writes to those entries.
func AcquireAtomicWriteLease(path string, reserved ...string) (*AtomicWriteLease, error) {
	return acquirePublicAtomicWriteLease(path, nil, reserved...)
}

// The public admission seam uses the same implementation; test-only observers
// supply the existing boundary hooks without an installed-binary switch.
var acquirePublicAtomicWriteLease = acquireAtomicWriteLease

func acquireAtomicWriteLease(path string, hooks *atomicHooks, reserved ...string) (*AtomicWriteLease, error) {
	abs, err := atomicDestination(path)
	if err != nil {
		return nil, err
	}
	dir, name := filepath.Dir(abs), filepath.Base(abs)
	release, err := admitAtomic(dir)
	if err != nil {
		return nil, err
	}

	w := &atomicWriter{path: dir, hooks: hooks}
	success := false
	defer func() {
		if !success {
			w.close()
			release()
		}
	}()
	w.parent, err = atomicOpenDirectory(dir)
	if err != nil {
		return nil, recovery(err)
	}
	w.parentInfo, err = atomicFileMetadata(w.parent)
	if err != nil || !w.parentInfo.directory {
		return nil, recovery(errors.New("unsafe destination directory"))
	}
	if err = w.checkDestination(name); err != nil {
		return nil, err
	}
	// Reserve missing protocol and destination entries before allocating any
	// metadata. Admission rechecks the complete inventory under the lease.
	if err = w.inventoryLegacy(name, reserved...); err != nil {
		return nil, recovery(err)
	}
	if err = w.openOwned(); err != nil {
		if errors.Is(err, ErrAtomicBusy) {
			return nil, err
		}
		return nil, recovery(err)
	}
	if err = w.verify(); err != nil {
		return nil, recovery(err)
	}
	if err = w.inventoryLegacy(name, reserved...); err != nil {
		return nil, recovery(err)
	}
	if err = w.reclaim(); err != nil {
		return nil, recovery(err)
	}
	if len(reserved) != 0 {
		syncDirectory := func(owned *os.File) error {
			return atomicSyncLeaseNamespace(w.path, owned)
		}
		if hooks != nil && hooks.syncReclaim != nil {
			syncDirectory = hooks.syncReclaim
		}
		if err = syncDirectory(w.owned); err != nil {
			return nil, recovery(err)
		}
	}
	if err = w.verify(); err != nil {
		return nil, recovery(err)
	}

	hooks.at("afterReclaim")
	success = true
	return &AtomicWriteLease{writer: w, target: abs, release: release, reserved: append([]string(nil), reserved...)}, nil
}

// CheckParent compares the caller's retained native handle with the admitted
// parent identity, and verifies the writer's retained protocol handles.
func (l *AtomicWriteLease) CheckParent(parent *os.File) error {
	if l == nil {
		return ErrAtomicRecovery
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.writer == nil || parent == nil {
		return ErrAtomicRecovery
	}
	info, err := atomicFileMetadata(parent)
	if err != nil || !info.directory || info.id != l.writer.parentInfo.id {
		return ErrAtomicRecovery
	}
	return l.writer.verify()
}

func (l *AtomicWriteLease) Write(path string, b []byte) error {
	if l == nil {
		return ErrAtomicRecovery
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	abs, err := atomicDestination(path)
	if err != nil {
		return err
	}
	if l.writer == nil || abs != l.target {
		return ErrAtomicRecovery
	}
	w := l.writer
	if err := w.verify(); err != nil {
		return recovery(err)
	}
	if err := w.inventoryLegacy(filepath.Base(abs), l.reserved...); err != nil {
		return recovery(err)
	}
	return w.write(filepath.Base(abs), b)
}

func (l *AtomicWriteLease) Close() error {
	if l == nil {
		return ErrAtomicRecovery
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.writer == nil {
		return ErrAtomicRecovery
	}
	l.writer.close()
	l.writer = nil
	l.release()
	l.release = nil
	return nil
}

func atomicWriteOwned(path string, b []byte, hooks *atomicHooks) error {
	l, err := acquireAtomicWriteLease(path, hooks)
	if err != nil {
		return err
	}
	defer l.Close()
	return l.writer.write(filepath.Base(l.target), b)
}

func (w *atomicWriter) write(name string, b []byte) (result error) {
	hooks := w.hooks
	f, err := atomicOpenChild(w.owned, atomicSnapshot, true, false, true)
	if err != nil {
		return recovery(err)
	}
	defer f.Close()
	meta, err := atomicFileMetadata(f)
	if err != nil || !meta.regular || !meta.singleLink || !meta.private {
		return recovery(errors.New("unsafe new snapshot"))
	}
	committed := false
	defer func() {
		if !committed {
			if e := w.removeSnapshot(f, meta); e != nil {
				result = errors.Join(result, recovery(e))
			}
		}
	}()
	hooks.at("afterCreateTemp")
	if _, err = f.Write(b); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = atomicSyncDirectory(w.owned); err != nil {
		return err
	}
	hooks.at("afterSync")
	hooks.at("beforeReplace")
	if err = w.verify(); err != nil {
		return recovery(err)
	}
	if err = w.checkSnapshot(f, meta); err != nil {
		return recovery(err)
	}
	if err = w.checkDestination(name); err != nil {
		return err
	}
	if err = atomicReplace(w.owned, atomicSnapshot, f, w.parent, name); err != nil {
		return err
	}
	committed = true
	// Rename consumes the only snapshot. No fallible temp deletion runs after
	// commit, so a cleanup problem cannot masquerade as a failed save.
	syncReplacement := atomicSyncReplacement
	if hooks != nil && hooks.syncReplacement != nil {
		syncReplacement = hooks.syncReplacement
	}
	if err = syncReplacement(f, w.parent, w.owned); err != nil {
		return fmt.Errorf("%w: %w", ErrAtomicCommitted, err)
	}
	return nil
}

func (w *atomicWriter) close() {
	if w.lease != nil {
		_ = w.lease.Close()
	} // Closing releases the OS lease, including on process death.
	if w.owned != nil {
		_ = w.owned.Close()
	}
	if w.parent != nil {
		_ = w.parent.Close()
	}
}

func (w *atomicWriter) checkDestination(name string) error {
	s, err := atomicChildMetadata(w.parent, name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !s.regular {
		return errors.New("refusing non-regular destination")
	}
	return nil
}

func (w *atomicWriter) openOwned() error {
	created := false
	s, err := atomicChildMetadata(w.parent, atomicNamespace)
	if errors.Is(err, os.ErrNotExist) {
		if err = atomicMkdir(w.parent, atomicNamespace); err != nil {
			return err
		}
		created = true
		s, err = atomicChildMetadata(w.parent, atomicNamespace)
	}
	if err != nil {
		return err
	}
	if !s.directory {
		return errors.New("unsafe persistence namespace")
	}
	w.owned, err = atomicOpenChild(w.parent, atomicNamespace, false, true, false)
	if err != nil {
		return err
	}
	w.ownedInfo, err = atomicFileMetadata(w.owned)
	if err != nil || s.id != w.ownedInfo.id || !w.ownedInfo.private {
		return errors.New("persistence namespace is not private or changed identity")
	}
	w.lease, err = atomicOpenChild(w.owned, atomicLeaseName, created, false, true)
	if err != nil {
		return err
	}
	w.leaseInfo, err = atomicFileMetadata(w.lease)
	if err != nil || !w.leaseInfo.regular || !w.leaseInfo.singleLink || !w.leaseInfo.private {
		return errors.New("unsafe persistence owner lease")
	}
	if err = atomicTryLease(w.lease); err != nil {
		return err
	}
	if created {
		if _, err = w.lease.Write([]byte(atomicOwner)); err != nil {
			return err
		}
		if err = w.lease.Sync(); err != nil {
			return err
		}
		if err = atomicSyncDirectory(w.owned); err != nil {
			return err
		}
		if err = atomicSyncDirectory(w.parent); err != nil {
			return err
		}
	} else {
		if w.leaseInfo.size != int64(len(atomicOwner)) {
			return errors.New("unknown persistence owner marker")
		}
		var marker [len(atomicOwner)]byte
		if _, err = w.lease.ReadAt(marker[:], 0); err != nil {
			return err
		}
		if !bytes.Equal(marker[:], []byte(atomicOwner)) {
			return errors.New("unknown persistence owner version")
		}
	}
	return nil
}

func (w *atomicWriter) verify() error {
	f, err := atomicOpenDirectory(w.path)
	if err != nil {
		return err
	}
	s, err := atomicFileMetadata(f)
	_ = f.Close()
	if err != nil || !s.directory || s.id != w.parentInfo.id {
		return errors.New("destination directory changed identity")
	}
	s, err = atomicChildMetadata(w.parent, atomicNamespace)
	if err != nil || !s.directory || s.id != w.ownedInfo.id {
		return errors.New("persistence namespace changed identity")
	}
	s, err = atomicFileMetadata(w.owned)
	if err != nil || !s.private {
		return errors.New("persistence namespace lost privacy")
	}
	s, err = atomicChildMetadata(w.owned, atomicLeaseName)
	if err != nil || !s.regular || !s.singleLink || s.id != w.leaseInfo.id {
		return errors.New("persistence lease changed identity")
	}
	s, err = atomicFileMetadata(w.lease)
	if err != nil || !s.private {
		return errors.New("persistence lease lost privacy")
	}
	return nil
}

// ReadDir is bounded including unrelated entries. Metadata-only legacy
// accounting neither opens nor parses secret payloads, follows links, or
// removes ambiguous entries. A size/count/inspection overflow blocks admission.
func (w *atomicWriter) inventoryLegacy(destination string, reservedNames ...string) error {
	// A fresh enumeration handle also supports the first-use preflight without
	// relying on directory seeks (which are not portable to Windows).
	f, err := atomicOpenDirectory(w.path)
	if err != nil {
		return err
	}
	defer f.Close()
	s, err := atomicFileMetadata(f)
	if err != nil || s.id != w.parentInfo.id || !s.directory {
		return errors.New("destination inventory changed identity")
	}
	entries, err := f.ReadDir(atomicScanEntries + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	reserved := 0
	names := []string{atomicNamespace, destination}
	for _, name := range reservedNames {
		if name == "" || name == "." || name == ".." || filepath.Base(name) != name || strings.ContainsAny(name, "/\\") {
			return errors.New("invalid reserved parent entry")
		}
		duplicate := false
		for _, existing := range names {
			if existing == name {
				duplicate = true
			}
		}
		if !duplicate {
			names = append(names, name)
		}
	}
	for _, name := range names {
		if _, err := atomicChildMetadata(w.parent, name); errors.Is(err, os.ErrNotExist) {
			reserved++
		} else if err != nil {
			return err
		}
	}
	if len(entries)+reserved > atomicScanEntries {
		return errors.New("destination inventory exceeds entry bound")
	}
	var count int
	var total int64
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), ".write-") {
			continue
		}
		s, err := atomicChildMetadata(w.parent, e.Name())
		if err != nil {
			return err
		}
		if !s.regular || !s.singleLink || s.size < 0 {
			return errors.New("legacy temp accounting is unknown")
		}
		count++
		if s.size > math.MaxInt64-total {
			return errors.New("legacy temp bytes overflow")
		}
		total += s.size
		if count > atomicLegacyCount || total > atomicLegacyBytes {
			return errors.New("legacy temp budget exceeded")
		}
	}
	return nil
}

func (w *atomicWriter) reclaim() error {
	entries, err := w.owned.ReadDir(3)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if len(entries) > 2 {
		return errors.New("unknown persistence namespace inventory")
	}
	for _, e := range entries {
		if e.Name() != atomicLeaseName && e.Name() != atomicSnapshot {
			return errors.New("unknown persistence namespace entry")
		}
	}
	for _, e := range entries {
		if e.Name() != atomicSnapshot {
			continue
		}
		s, err := atomicChildMetadata(w.owned, atomicSnapshot)
		if err != nil {
			return err
		}
		if !s.regular || !s.singleLink || s.size < 0 {
			return errors.New("owned snapshot accounting is unknown")
		}
		w.hooks.at("beforeInspectSnapshot")
		f, err := atomicOpenChild(w.owned, atomicSnapshot, false, false, true)
		if err != nil {
			return err
		}
		err = w.removeSnapshot(f, s)
		_ = f.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func (w *atomicWriter) checkSnapshot(f *os.File, expected atomicMetadata) error {
	s, err := atomicFileMetadata(f)
	if err != nil || !s.regular || !s.singleLink || !s.private || s.id != expected.id {
		return errors.New("unsafe or replaced snapshot handle")
	}
	s, err = atomicChildMetadata(w.owned, atomicSnapshot)
	if err != nil || !s.regular || !s.singleLink || s.id != expected.id {
		return errors.New("snapshot changed identity")
	}
	return nil
}

func (w *atomicWriter) removeSnapshot(f *os.File, expected atomicMetadata) error {
	w.hooks.at("beforeRemoveSnapshot")
	if err := w.verify(); err != nil {
		return err
	}
	if err := w.checkSnapshot(f, expected); err != nil {
		return err
	}
	var err error
	if w.hooks != nil && w.hooks.remove != nil {
		err = w.hooks.remove(w.owned, atomicSnapshot, f)
	} else {
		err = atomicRemove(w.owned, atomicSnapshot, f)
	}
	if err != nil {
		return err
	}
	syncDirectory := atomicSyncDirectory
	if w.hooks != nil && w.hooks.syncReclaim != nil {
		syncDirectory = w.hooks.syncReclaim
	}
	return syncDirectory(w.owned)
}
