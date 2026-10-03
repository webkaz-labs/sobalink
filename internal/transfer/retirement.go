package transfer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
)

// The shared predicate uses the platform barrier; tests can fail that barrier
// without replacing actual removals, guard persistence or manager recovery.
var retirementDirectorySync = retirementSyncDirectory

// ReceiveRetirementGuard is independent blocking evidence, not deletion authority.
// Before/After bind one removal-only transition, never a history of transfers.
type ReceiveRetirementGuard struct {
	Version    int                        `json:"version"`
	ID         string                     `json:"id"`
	Kind       string                     `json:"kind"`
	Before     ReceiveAccounting          `json:"before"`
	After      ReceiveAccounting          `json:"after"`
	BeforeHash string                     `json:"beforeHash"`
	AfterHash  string                     `json:"afterHash"`
	Witnesses  []ReceiveRetirementWitness `json:"witnesses"`
}

type ReceiveRetirementWitness struct {
	Root           ReceiveRoot `json:"root"`
	RootMissing    bool        `json:"rootMissing"`
	MarkerIdentity string      `json:"markerIdentity"`
}

// A lease retains guard identity and target-bound writer exclusion through
// Release and Close. LeaseWrite accepts only the acquired index and canonical
// After. Release may have committed even when it returns error; Close never
// removes evidence.
type ReceiveRetirementLease interface {
	LeaseWrite(string, []byte) error
	Release(func() error) error
	Close() error
}

func canonicalAccounting(s ReceiveAccounting) []byte {
	if s.Roots == nil {
		s.Roots = []ReceiveRoot{}
	}
	data, _ := json.Marshal(s)
	return data
}

func accountingHash(s ReceiveAccounting) string {
	sum := sha256.Sum256(canonicalAccounting(s))
	return hex.EncodeToString(sum[:])
}

func retiredRoots(before, after ReceiveAccounting) []ReceiveRoot {
	kept := make(map[ReceiveRoot]bool, len(after.Roots))
	for _, r := range after.Roots {
		kept[r] = true
	}
	var roots []ReceiveRoot
	for _, r := range before.Roots {
		if !kept[r] {
			roots = append(roots, r)
		}
	}
	return roots
}

func retirementGuard(before, after ReceiveAccounting, promoted bool) (ReceiveRetirementGuard, error) {
	id, err := newOwnerToken()
	if err != nil {
		return ReceiveRetirementGuard{}, err
	}
	g := ReceiveRetirementGuard{Version: 1, ID: id, Kind: "retire", Before: before, After: after, BeforeHash: accountingHash(before), AfterHash: accountingHash(after), Witnesses: []ReceiveRetirementWitness{}}
	roots := retiredRoots(before, after)
	if promoted {
		g.Kind = "promote"
		if before.Preparation == nil {
			return g, ErrReceiveRecovery
		}
		for _, r := range before.Roots {
			if r.OwnedRoot == filepath.Join(before.Preparation.Destination, before.Preparation.Root) {
				roots = append(roots, r)
			}
		}
	}
	for _, r := range roots {
		w, err := captureRetirementWitness(r)
		if err != nil {
			return g, err
		}
		g.Witnesses = append(g.Witnesses, w)
	}
	return g, nil
}

func validateRetirementGuard(g ReceiveRetirementGuard, budget AccountingLimits) error {
	if g.Version != 1 || !hexToken(g.ID, 32) || (g.Kind != "retire" && g.Kind != "promote") || g.BeforeHash != accountingHash(g.Before) || g.AfterHash != accountingHash(g.After) {
		return ErrReceiveRecovery
	}
	if err := validateAccounting(g.Before, budget); err != nil {
		return err
	}
	if err := validateAccounting(g.After, budget); err != nil {
		return err
	}
	if g.Before.Version != g.After.Version || g.After.Preparation != nil && !reflect.DeepEqual(g.Before.Preparation, g.After.Preparation) {
		return ErrReceiveRecovery
	}
	// After may only remove evidence; no addition/rewrite is licensed by a guard.
	indexed := make(map[ReceiveRoot]bool, len(g.Before.Roots))
	for _, r := range g.Before.Roots {
		indexed[r] = true
	}
	for _, kept := range g.After.Roots {
		if !indexed[kept] {
			return ErrReceiveRecovery
		}
	}
	expected := retiredRoots(g.Before, g.After)
	if g.Kind == "promote" {
		if g.Before.Preparation == nil || g.After.Preparation != nil || len(expected) != 0 {
			return ErrReceiveRecovery
		}
		for _, r := range g.Before.Roots {
			p := g.Before.Preparation
			if r.OwnedRoot == filepath.Join(p.Destination, p.Root) && r.Stage == p.Stage && r.OwnerToken == p.OwnerToken {
				expected = append(expected, r)
			}
		}
		if len(expected) != 1 {
			return ErrReceiveRecovery
		}
	} else if len(expected) == 0 && !(g.Before.Preparation != nil && g.After.Preparation == nil) {
		return ErrReceiveRecovery
	}
	if len(expected) != len(g.Witnesses) {
		return ErrReceiveRecovery
	}
	for i, w := range g.Witnesses {
		if w.Root != expected[i] || len(w.MarkerIdentity) > 128 || g.Kind == "promote" && (w.RootMissing || w.MarkerIdentity == "") {
			return ErrReceiveRecovery
		}
	}
	data, err := json.Marshal(g)
	// Under the writer lease, admission reserves current index + one bounded
	// snapshot + guard (2B+G). After failed retirement releases exclusion,
	// unrelated callers retain their limits: B+G+max(B,S_other), with protocol
	// metadata and preserved legacy allowances accounted separately.
	b, a := int64(len(canonicalAccounting(g.Before))), int64(len(canonicalAccounting(g.After)))
	if a > b {
		b = a
	}
	if err != nil || b > budget.MaxBytes/2 || int64(len(data)) > budget.MaxBytes-2*b {
		return ErrLimit
	}
	n := func(s ReceiveAccounting) int64 {
		n := int64(len(s.Roots))
		if s.Preparation != nil {
			n++
		}
		return n
	}
	entries := n(g.Before)
	if n(g.After) > entries {
		entries = n(g.After)
	}
	if 2*entries+n(g.Before)+n(g.After)+int64(len(g.Witnesses))+1 > budget.MaxEntries {
		return ErrLimit
	}
	return nil
}

func captureRetirementWitness(r ReceiveRoot) (ReceiveRetirementWitness, error) {
	w := ReceiveRetirementWitness{Root: r}
	parent, root, err := openRetirementRoot(r)
	if parent != nil {
		defer parent.Close()
	}
	if err != nil {
		return w, err
	}
	if root == nil {
		w.RootMissing = true
		return w, nil
	}
	defer root.Close()
	info, err := root.Lstat(r.Stage)
	if errors.Is(err, os.ErrNotExist) {
		return w, nil
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return w, ErrUnsafePath
	}
	stage, err := root.OpenRoot(r.Stage)
	if err != nil {
		return w, err
	}
	defer stage.Close()
	id, err := rootIdentity(stage)
	if err != nil || id != r.StageIdentity {
		return w, ErrUnsafePath
	}
	marker, err := openAccountingFile(stage, receiveOwnerMarker)
	if err != nil {
		return w, err
	}
	defer marker.Close()
	w.MarkerIdentity, err = accountingIdentity(marker)
	if err != nil {
		return w, err
	}
	return w, validateReceiveOwnerMarker(stage, r.OwnerToken)
}

func openRetirementRoot(r ReceiveRoot) (*os.Root, *os.Root, error) {
	parent, err := openDestination(r.Destination)
	if err != nil {
		return nil, nil, err
	}
	id, err := rootIdentity(parent)
	if err != nil || id != r.DestinationIdentity {
		return parent, nil, ErrUnsafePath
	}
	name := filepath.Base(r.OwnedRoot)
	info, err := parent.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return parent, nil, nil
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return parent, nil, ErrUnsafePath
	}
	root, err := parent.OpenRoot(name)
	if err != nil {
		return parent, nil, err
	}
	id, err = rootIdentity(root)
	if err != nil || id != r.RootIdentity {
		root.Close()
		return parent, nil, ErrUnsafePath
	}
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		root.Close()
		return parent, nil, ErrUnsafePath
	}
	return parent, root, nil
}

// Absence is only a namespace observation. While the guard still exists,
// acknowledge removal on the retained directory and check its public binding
// and the missing name again before discarding the corresponding evidence.
func syncMissingRetirementName(root *os.Root, name string, verifyBinding func() error) error {
	if err := verifyBinding(); err != nil {
		return err
	}
	if _, err := root.Lstat(name); !errors.Is(err, os.ErrNotExist) {
		return ErrUnsafePath
	}
	if err := retirementDirectorySync(root); err != nil {
		return err
	}
	if err := verifyBinding(); err != nil {
		return err
	}
	if _, err := root.Lstat(name); !errors.Is(err, os.ErrNotExist) {
		return ErrUnsafePath
	}
	return nil
}

func verifyMissingRetirementRoot(destination, identity, name string) error {
	parent, err := openDestination(destination)
	if err != nil {
		return err
	}
	defer parent.Close()
	id, err := rootIdentity(parent)
	if err != nil || id != identity {
		return ErrUnsafePath
	}
	p := ReceivePreparation{Destination: destination, DestinationIdentity: identity, Root: name}
	return syncMissingRetirementName(parent, name, func() error {
		return verifyMissingPreparation(context.Background(), &p)
	})
}

func verifyRetirementRootBinding(parent, root *os.Root, r ReceiveRoot) error {
	if err := verifyPreparationParent(r.Destination, r.DestinationIdentity); err != nil {
		return err
	}
	current, err := parent.Lstat(filepath.Base(r.OwnedRoot))
	opened, statErr := root.Stat(".")
	if err != nil || statErr != nil || !os.SameFile(current, opened) {
		return ErrUnsafePath
	}
	return nil
}

// cleanup is allowed only while the guard is durable. Saved root contents are
// never listed or removed. A restart may resume removal of the exact empty stage.
func verifyRetirementWitness(w ReceiveRetirementWitness, promoted, cleanup bool) error {
	r := w.Root
	parent, root, err := openRetirementRoot(r)
	if parent != nil {
		defer parent.Close()
	}
	if err != nil {
		return err
	}
	if root != nil {
		defer root.Close()
	}
	if w.RootMissing {
		if root != nil {
			return ErrUnsafePath
		}
		return syncMissingRetirementName(parent, filepath.Base(r.OwnedRoot), func() error {
			return verifyPreparationParent(r.Destination, r.DestinationIdentity)
		})
	}
	if root == nil {
		return ErrUnsafePath
	}
	info, err := root.Lstat(r.Stage)
	if errors.Is(err, os.ErrNotExist) && !promoted {
		return syncMissingRetirementName(root, r.Stage, func() error {
			return verifyRetirementRootBinding(parent, root, r)
		})
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrUnsafePath
	}
	stage, err := root.OpenRoot(r.Stage)
	if err != nil {
		return err
	}
	defer stage.Close()
	id, err := rootIdentity(stage)
	if err != nil || id != r.StageIdentity {
		return ErrUnsafePath
	}
	marker, err := openAccountingFile(stage, receiveOwnerMarker)
	if err == nil {
		defer marker.Close()
		id, idErr := accountingIdentity(marker)
		if idErr != nil || id != w.MarkerIdentity || validateReceiveOwnerMarker(stage, r.OwnerToken) != nil {
			return ErrUnsafePath
		}
	} else if !errors.Is(err, os.ErrNotExist) || promoted || w.MarkerIdentity == "" {
		return ErrUnsafePath
	}
	if promoted {
		return verifyPreparationParent(r.Destination, r.DestinationIdentity)
	}
	if !cleanup {
		return ErrReceiveRecovery
	}
	dir, err := stage.Open(".")
	if err != nil {
		return err
	}
	names, readErr := dir.Readdirnames(2)
	dir.Close()
	if readErr != nil && readErr != io.EOF {
		return readErr
	}
	for _, name := range names {
		if name != receiveOwnerMarker {
			return ErrReceiveRecovery
		}
	}
	// Revalidate all public bindings and both opened identities before removal.
	if err := verifyRetirementRootBinding(parent, root, r); err != nil {
		return err
	}
	current, err := root.Lstat(r.Stage)
	if err != nil || !os.SameFile(info, current) {
		return ErrUnsafePath
	}
	if marker != nil {
		current, err = stage.Lstat(receiveOwnerMarker)
		opened, statErr := marker.Stat()
		if err != nil || statErr != nil || !os.SameFile(current, opened) {
			return ErrUnsafePath
		}
		if err := stage.Remove(receiveOwnerMarker); err != nil {
			return err
		}
		if err := retirementDirectorySync(stage); err != nil {
			return err
		}
	}
	if err := root.Remove(r.Stage); err != nil {
		return err
	}
	return syncMissingRetirementName(root, r.Stage, func() error {
		return verifyRetirementRootBinding(parent, root, r)
	})
}

func verifyRetirement(g ReceiveRetirementGuard, cleanup bool) error {
	if g.Kind == "retire" && g.Before.Preparation != nil && g.After.Preparation == nil {
		p := g.Before.Preparation
		if err := verifyMissingRetirementRoot(p.Destination, p.DestinationIdentity, p.Root); err != nil {
			return err
		}
	}
	for _, w := range g.Witnesses {
		if err := verifyRetirementWitness(w, g.Kind == "promote", cleanup); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) runRetirementLocked(ctx context.Context, g ReceiveRetirementGuard, lease ReceiveRetirementLease, liveVerify func() error) error {
	defer lease.Close()
	pins, err := pinRetirementEvidence(g)
	if err != nil {
		return err
	}
	defer func() {
		for _, c := range pins {
			_ = c.Close()
		}
	}()
	m.guardPending = true
	// Failures retain guard evidence and block stale receive transitions.
	verify := func(cleanup bool) error {
		if liveVerify != nil {
			if err := liveVerify(); err != nil {
				return err
			}
		}
		return verifyRetirement(g, cleanup)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := verify(true); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	current, err := m.accountingStore.LoadReceiveAccounting()
	if err != nil {
		return err
	}
	hash := accountingHash(current)
	if hash != g.BeforeHash && hash != g.AfterHash {
		return ErrReceiveRecovery
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := m.accountingStore.SaveReceiveAccounting(g.After, lease); err != nil {
		return err
	}
	current, err = m.accountingStore.LoadReceiveAccounting()
	if err != nil || accountingHash(current) != g.AfterHash {
		return ErrReceiveRecovery
	}
	// Cancellation cannot suppress verification or undo a completed transition.
	if err := verify(false); err != nil {
		return err
	}
	// Terminal commit, not an atomic CAS with the destination filesystem.
	if err := lease.Release(func() error { return verify(false) }); err != nil {
		return err
	}
	m.guardPending = false
	m.accounting = g.After
	return nil
}

func (m *Manager) guardedRetirementLocked(ctx context.Context, before, after ReceiveAccounting, promoted bool, liveVerify func() error) error {
	if m.guardPending {
		return ErrReceiveRecovery
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// A failed promotion Save can leave the live snapshot with the exact
	// provisional root while disk still has only its preparation intent. Bind
	// the guard to the actual durable state, allowing only that recorded overlap.
	durable, err := m.accountingStore.LoadReceiveAccounting()
	if err != nil {
		return err
	}
	if accountingHash(durable) != accountingHash(before) {
		if promoted || before.Preparation == nil || after.Preparation != nil || !reflect.DeepEqual(before.Preparation, durable.Preparation) {
			return ErrReceiveRecovery
		}
		strip := func(s ReceiveAccounting) ReceiveAccounting {
			p := before.Preparation
			copy := s
			copy.Roots = nil
			for _, r := range s.Roots {
				if r.OwnedRoot != filepath.Join(p.Destination, p.Root) || r.OwnerToken != p.OwnerToken || r.Stage != p.Stage || r.DestinationIdentity != p.DestinationIdentity {
					copy.Roots = append(copy.Roots, r)
				}
			}
			return copy
		}
		if accountingHash(strip(before)) != accountingHash(strip(durable)) {
			return ErrReceiveRecovery
		}
		before = durable
	}
	g, err := retirementGuard(before, after, promoted)
	if err != nil {
		return err
	}
	if err := validateRetirementGuard(g, m.accountingLimits); err != nil {
		return err
	}
	pins, err := pinRetirementEvidence(g)
	if err != nil {
		return err
	}
	defer func() {
		for _, c := range pins {
			_ = c.Close()
		}
	}()
	if err := validateRetirementCandidate(g); err != nil {
		return err
	}
	m.guardPending = true // even failed/partial creation is not safe to overwrite
	lease, err := m.accountingStore.AcquireReceiveRetirementGuard(g, m.accountingLimits)
	if err != nil {
		return err
	}
	return m.runRetirementLocked(ctx, g, lease, liveVerify)
}

func (m *Manager) recoverRetirementLocked(ctx context.Context, state ReceiveAccounting) (ReceiveAccounting, bool, error) {
	g, lease, err := m.accountingStore.LoadReceiveRetirementGuard(m.accountingLimits)
	if errors.Is(err, os.ErrNotExist) {
		m.guardPending = false
		return state, false, nil
	}
	m.guardPending = true
	if err != nil {
		return state, false, err
	}
	if err := validateRetirementGuard(*g, m.accountingLimits); err != nil {
		lease.Close()
		return state, false, err
	}
	hash := accountingHash(state)
	if hash != g.BeforeHash && hash != g.AfterHash {
		lease.Close()
		return state, false, ErrReceiveRecovery
	}
	// A surviving promoted intent never proves ownership after restart.
	if g.Kind == "promote" {
		lease.Close()
		return state, false, ErrReceiveRecovery
	}
	if err := m.runRetirementLocked(ctx, *g, lease, nil); err != nil {
		return state, false, err
	}
	return g.After, true, nil
}

func (m *Manager) probeRetirementGuardLocked() error {
	_, lease, err := m.accountingStore.LoadReceiveRetirementGuard(m.accountingLimits)
	if lease != nil {
		lease.Close()
	}
	if errors.Is(err, os.ErrNotExist) {
		m.guardPending = false
		return nil
	}
	m.guardPending = true
	return err
}

// Keep identity-bearing handles across persistence, including marker removal,
// so ordinary inode reuse cannot turn a replacement into the captured object.
func pinRetirementEvidence(g ReceiveRetirementGuard) ([]io.Closer, error) {
	var pins []io.Closer
	fail := func(err error) ([]io.Closer, error) {
		for _, p := range pins {
			_ = p.Close()
		}
		return nil, err
	}
	if p := g.Before.Preparation; p != nil {
		parent, err := openDestination(p.Destination)
		if err != nil {
			return fail(err)
		}
		pins = append(pins, parent)
		id, err := rootIdentity(parent)
		if err != nil || id != p.DestinationIdentity {
			return fail(ErrUnsafePath)
		}
	}
	for _, w := range g.Witnesses {
		parent, root, err := openRetirementRoot(w.Root)
		if parent != nil {
			pins = append(pins, parent)
		}
		if err != nil {
			return fail(err)
		}
		if root == nil {
			if !w.RootMissing {
				return fail(ErrUnsafePath)
			}
			continue
		}
		pins = append(pins, root)
		if w.RootMissing {
			return fail(ErrUnsafePath)
		}
		stage, err := root.OpenRoot(w.Root.Stage)
		if errors.Is(err, os.ErrNotExist) && g.Kind != "promote" {
			continue
		}
		if err != nil {
			return fail(err)
		}
		pins = append(pins, stage)
		id, err := rootIdentity(stage)
		if err != nil || id != w.Root.StageIdentity {
			return fail(ErrUnsafePath)
		}
		marker, err := openAccountingFile(stage, receiveOwnerMarker)
		if errors.Is(err, os.ErrNotExist) && g.Kind != "promote" {
			continue
		}
		if err != nil {
			return fail(err)
		}
		pins = append(pins, marker)
		id, err = accountingIdentity(marker)
		if err != nil || id != w.MarkerIdentity {
			return fail(ErrUnsafePath)
		}
	}
	return pins, nil
}

// Reject already-known nonempty stages before occupying the singleton. Their
// existing index record remains authoritative and ordinary inventory can charge
// them on restart. No candidate check removes a filesystem object.
func validateRetirementCandidate(g ReceiveRetirementGuard) error {
	if g.Kind == "promote" {
		return verifyRetirement(g, false)
	}
	if g.Before.Preparation != nil && g.After.Preparation == nil {
		if err := verifyMissingPreparation(context.Background(), g.Before.Preparation); err != nil {
			return err
		}
	}
	for _, w := range g.Witnesses {
		parent, root, err := openRetirementRoot(w.Root)
		if parent != nil {
			defer parent.Close()
		}
		if err != nil {
			return err
		}
		if root == nil {
			if !w.RootMissing {
				return ErrUnsafePath
			}
			continue
		}
		defer root.Close()
		if w.RootMissing {
			return ErrUnsafePath
		}
		stage, err := root.OpenRoot(w.Root.Stage)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		defer stage.Close()
		id, err := rootIdentity(stage)
		if err != nil || id != w.Root.StageIdentity {
			return ErrUnsafePath
		}
		if err := validateReceiveOwnerMarker(stage, w.Root.OwnerToken); err != nil {
			return err
		}
		dir, err := stage.Open(".")
		if err != nil {
			return err
		}
		names, readErr := dir.Readdirnames(2)
		dir.Close()
		if readErr != nil && readErr != io.EOF {
			return readErr
		}
		for _, name := range names {
			if name != receiveOwnerMarker {
				return ErrReceiveRecovery
			}
		}
	}
	return nil
}
