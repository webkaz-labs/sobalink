package core

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/webkaz-labs/sobalink/internal/config"
)

// All fields are Core.op-owned. state is only the last snapshot this owner
// successfully published, or a certified empty first-use view. Decoded startup
// bytes and attempted publications never become that snapshot on a failure.
type resourceGroupStore struct {
	state                          resourceGroupEnvelope
	profile, lock, directory, file os.FileInfo
	digest                         [sha256.Size]byte
	size                           int
	firstUse, frozen, uncertain    bool
	certified                      bool
	before, attempted              *resourceGroupFileCandidate
}

// At most two fixed-size candidates survive a failed write. Neither the digest
// nor inode is authority, and neither can clear an uncertainty latch.
type resourceGroupFileCandidate struct {
	file   os.FileInfo
	digest [sha256.Size]byte
	size   int
}

func resourceGroupCandidate(snapshot resourceGroupFileSnapshot) *resourceGroupFileCandidate {
	if snapshot.file == nil {
		return nil
	}
	return &resourceGroupFileCandidate{file: snapshot.file, digest: snapshot.digest, size: snapshot.size}
}

func (candidate *resourceGroupFileCandidate) matches(snapshot resourceGroupFileSnapshot) bool {
	return candidate != nil && candidate.file != nil && snapshot.file != nil && os.SameFile(candidate.file, snapshot.file) && candidate.digest == snapshot.digest && candidate.size == snapshot.size
}

func (s *resourceGroupStore) confirmedCandidate() *resourceGroupFileCandidate {
	if s == nil || !s.certified || s.file == nil {
		return nil
	}
	return &resourceGroupFileCandidate{file: s.file, digest: s.digest, size: s.size}
}

func (s *resourceGroupStore) freeze() {
	if s != nil {
		s.frozen = true
		if s.before == nil {
			s.before = s.confirmedCandidate()
		}
	}
}

// openResourceGroupStore is called under Core.op, outside every other ownership
// callback. It performs no grant, target-journal, provider or network operation.
func (c *Core) openResourceGroupStore() *resourceGroupStore {
	s := &resourceGroupStore{}
	if c == nil || c.resourceLock == nil {
		s.freeze()
		return s
	}
	var err error
	s.state, err = newResourceGroupEnvelope(c.resourceIdentity)
	if err != nil {
		s.freeze()
		return s
	}
	err = c.resourceLock.WithOwnershipInfo(c.dir, func(profile, lock os.FileInfo) (result error) {
		s.profile, s.lock = profile, lock
		b, err := openResourceGroupPathBinding(c.dir, profile, lock)
		if err != nil {
			return err
		}
		defer func() { result = errors.Join(result, b.close()) }()
		directory, err := os.Lstat(filepath.Dir(resourceGroupStatePath(c.dir)))
		if errors.Is(err, os.ErrNotExist) {
			if err := b.absent(); err != nil {
				return err
			}
			s.firstUse, s.certified = true, true
			return nil
		}
		if err != nil {
			return err
		}
		// Once observed, even failed opening or missing state is never first use.
		s.directory = directory
		if err := b.openGroup(directory); err != nil {
			return err
		}
		current, err := readResourceGroupSnapshot(b)
		if err != nil || current.state.ControllerResourceID != c.resourceIdentity {
			return errResourceGroupState
		}
		s.before = resourceGroupCandidate(current)
		next, err := normalizeResourceGroupEnvelope(current.state)
		if err != nil {
			return err
		}
		// Startup is the only normalization path. Publication certifies the
		// historical stopped view and never installs prepared admission.
		return s.publishBound(b, next, s.before)
	})
	if err != nil {
		s.freeze()
	}
	return s
}

func (s *resourceGroupStore) openBinding(c *Core, profile, lock os.FileInfo) (*resourceGroupPathBinding, error) {
	if s == nil || c == nil || s.profile == nil || s.lock == nil || profile == nil || lock == nil || !os.SameFile(s.profile, profile) || !os.SameFile(s.lock, lock) || s.state.ControllerResourceID != c.resourceIdentity {
		return nil, errResourceBinding
	}
	return openResourceGroupPathBinding(c.dir, profile, lock)
}

// current freshly certifies disclosure only. In a frozen owner, matching exact
// before/attempted evidence does not promote attempted state or resume writes.
func (s *resourceGroupStore) current(c *Core) error {
	if s == nil || c == nil || c.resourceLock == nil || !s.certified {
		s.freeze()
		return errResourceGroupState
	}
	err := c.resourceLock.WithOwnershipInfo(c.dir, func(profile, lock os.FileInfo) (result error) {
		b, err := s.openBinding(c, profile, lock)
		if err != nil {
			return err
		}
		defer func() { result = errors.Join(result, b.close()) }()
		return s.currentBound(b)
	})
	if err != nil {
		s.freeze()
		return errResourceGroupState
	}
	return nil
}

func (s *resourceGroupStore) currentBound(b *resourceGroupPathBinding) error {
	if !s.certified {
		return errResourceGroupState
	}
	if s.firstUse {
		if err := b.absent(); err != nil {
			// An observed child or ambiguous absence check is not pristine
			// first use if a later read happens to find the path missing again.
			s.firstUse = false
			return err
		}
		return nil
	}
	if err := b.openGroup(s.directory); err != nil {
		return err
	}
	current, err := readResourceGroupSnapshot(b)
	if err != nil || current.state.ControllerResourceID != s.state.ControllerResourceID {
		return errResourceGroupState
	}
	if s.frozen {
		if !s.before.matches(current) && !s.attempted.matches(current) {
			return errResourceGroupState
		}
	} else if !s.confirmedCandidate().matches(current) || !resourceGroupJSONEqual(s.state, current.state) {
		return errResourceGroupState
	}
	return b.check()
}

// Only the frozen model's deterministic acceptance/eviction or single-record
// update may change the envelope. Startup normalization uses its separate path.
func validResourceGroupPublication(before, next resourceGroupEnvelope) bool {
	if before.validate() != nil || next.validate() != nil || before.ControllerResourceID != next.ControllerResourceID {
		return false
	}
	if resourceGroupJSONEqual(before, next) {
		return true
	}
	if *next.HighWater > *before.HighWater && len(next.Runs) > 0 {
		candidate, err := withResourceGroupRun(before, next.Runs[len(next.Runs)-1])
		return err == nil && resourceGroupJSONEqual(candidate, next)
	}
	if *next.HighWater != *before.HighWater || len(next.Runs) != len(before.Runs) {
		return false
	}
	changed := -1
	for i := range before.Runs {
		if !resourceGroupJSONEqual(before.Runs[i], next.Runs[i]) {
			if changed >= 0 {
				return false
			}
			changed = i
		}
	}
	if changed < 0 {
		return false
	}
	candidate, err := withResourceGroupUpdate(before, next.Runs[changed])
	return err == nil && resourceGroupJSONEqual(candidate, next)
}

func (s *resourceGroupStore) publish(c *Core, desired resourceGroupEnvelope) error {
	if s == nil || c == nil || c.resourceLock == nil || s.frozen || !s.certified {
		return errResourceGroupState
	}
	next, err := cloneResourceGroupEnvelope(desired)
	if err != nil || s.firstUse && len(next.Runs) == 0 || !validResourceGroupPublication(s.state, next) {
		s.freeze()
		return errResourceGroupState
	}
	err = c.resourceLock.WithOwnershipInfo(c.dir, func(profile, lock os.FileInfo) (result error) {
		b, err := s.openBinding(c, profile, lock)
		if err != nil {
			return err
		}
		defer func() { result = errors.Join(result, b.close()) }()
		if err := s.currentBound(b); err != nil {
			return err
		}
		before := s.confirmedCandidate()
		if s.firstUse {
			// Consumed before any mkdir attempt, including ambiguous failures.
			s.firstUse = false
			if err := b.createGroup(); err != nil {
				return err
			}
			s.directory = b.groupInfo
		}
		return s.publishBound(b, next, before)
	})
	if err != nil {
		s.freeze()
		return privateAtomicError(errResourceGroupState, err)
	}
	return nil
}

// This classifier never advances state. A renamed-file receipt is required to
// retain attempted bytes; readback alone cannot manufacture self-write evidence.
func (s *resourceGroupStore) failedPublication(before *resourceGroupFileCandidate, attempted resourceGroupFileSnapshot, receipt os.FileInfo, data []byte, err error) {
	s.freeze()
	s.before = before
	s.uncertain = s.uncertain || receipt != nil || errors.Is(err, config.ErrAtomicCommitted)
	s.attempted = nil
	if receipt != nil && attempted.file != nil && os.SameFile(receipt, attempted.file) && attempted.digest == sha256.Sum256(data) && attempted.size == len(data) {
		s.attempted = resourceGroupCandidate(attempted)
	}
}

func (s *resourceGroupStore) publishBound(b *resourceGroupPathBinding, next resourceGroupEnvelope, before *resourceGroupFileCandidate) error {
	data, err := json.Marshal(next)
	if err != nil || next.ControllerResourceID != s.state.ControllerResourceID || len(data) > resourceGroupMaxStoreBytes {
		return errResourceGroupState
	}
	if _, err := resourceGroupReservedBytes(next); err != nil {
		return err
	}
	observed, receipt, err := writeResourceGroupSnapshot(b, data, before)
	if err != nil {
		s.failedPublication(before, observed, receipt, data, err)
		return err
	}
	if receipt == nil || !resourceGroupJSONEqual(observed.state, next) || b.check() != nil {
		s.failedPublication(before, observed, receipt, data, errResourceBinding)
		return errResourceBinding
	}
	// A failed handle release is also returned before promotion. The outer
	// callback's deferred close is then a harmless no-op on the closed binding.
	if err := b.close(); err != nil {
		s.failedPublication(before, observed, receipt, data, err)
		return errResourceBinding
	}
	s.state, s.directory, s.file = observed.state, b.groupInfo, observed.file
	s.digest, s.size = observed.digest, observed.size
	s.firstUse, s.certified = false, true
	s.before, s.attempted = nil, nil
	return nil
}
