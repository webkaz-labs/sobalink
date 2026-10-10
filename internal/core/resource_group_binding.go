package core

import (
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/webkaz-labs/sobalink/internal/config"
)

const resourceGroupDirectoryName = "resource-groups"

// These handles exist only inside one actual lifecycle ownership callback. The
// group sidecar is independent of both the target journal and the grant store.
// No callback, handle or writer lease may survive into a peer exchange.
type resourceGroupPathBinding struct {
	dir                              string
	profile, group                   *os.File
	profileInfo, groupInfo, lockInfo os.FileInfo
}

type resourceGroupFileSnapshot struct {
	state  resourceGroupEnvelope
	file   os.FileInfo
	digest [sha256.Size]byte
	size   int
}

func resourceGroupStatePath(dir string) string {
	return filepath.Join(dir, resourceGroupDirectoryName, "state.json")
}

func openResourceGroupPathBinding(dir string, profile, lock os.FileInfo) (*resourceGroupPathBinding, error) {
	if profile == nil || lock == nil {
		return nil, errResourceBinding
	}
	b := &resourceGroupPathBinding{dir: dir, profileInfo: profile, lockInfo: lock}
	var err error
	b.profile, err = os.Open(dir)
	if err == nil {
		err = b.check()
	}
	if err != nil {
		_ = b.close()
		return nil, errResourceBinding
	}
	return b, nil
}

func (b *resourceGroupPathBinding) close() error {
	if b == nil {
		return nil
	}
	var err error
	if b.group != nil {
		err = b.group.Close()
		b.group = nil
	}
	if b.profile != nil {
		err = errors.Join(err, b.profile.Close())
		b.profile = nil
	}
	return err
}

func (b *resourceGroupPathBinding) check() error {
	if b == nil || b.profile == nil || b.profileInfo == nil || b.lockInfo == nil {
		return errResourceBinding
	}
	opened, err := b.profile.Stat()
	current, pathErr := os.Lstat(b.dir)
	if err != nil || pathErr != nil || !current.IsDir() || current.Mode()&os.ModeSymlink != 0 || !os.SameFile(opened, b.profileInfo) || !os.SameFile(current, b.profileInfo) {
		return errResourceBinding
	}
	// This also certifies private parent permissions and a private, single-link
	// process-lock entry. The live lifecycle lock remains the actual owner.
	lock, err := config.OpenPrivateChildFileBound(b.dir, "process.lock", b.profileInfo)
	if err != nil {
		return errResourceBinding
	}
	lockInfo, statErr := lock.Stat()
	closeErr := lock.Close()
	if statErr != nil || closeErr != nil || !os.SameFile(lockInfo, b.lockInfo) {
		return errResourceBinding
	}
	if b.group == nil {
		return nil
	}
	opened, err = b.group.Stat()
	if err != nil || b.groupInfo == nil || !os.SameFile(opened, b.groupInfo) {
		return errResourceBinding
	}
	certified, err := config.OpenPrivateChildDirectoryBound(b.dir, resourceGroupDirectoryName, b.profileInfo)
	if err != nil {
		return errResourceBinding
	}
	current, statErr = certified.Stat()
	closeErr = certified.Close()
	if statErr != nil || closeErr != nil || !os.SameFile(current, b.groupInfo) {
		return errResourceBinding
	}
	return nil
}

func (b *resourceGroupPathBinding) openGroup(expected os.FileInfo) error {
	if b.check() != nil || expected == nil {
		return errResourceBinding
	}
	if b.group != nil {
		if !os.SameFile(expected, b.groupInfo) {
			return errResourceBinding
		}
		return b.check()
	}
	var err error
	b.group, err = config.OpenPrivateChildDirectoryBound(b.dir, resourceGroupDirectoryName, b.profileInfo)
	if err != nil {
		return errResourceBinding
	}
	b.groupInfo, err = b.group.Stat()
	if err != nil || !os.SameFile(expected, b.groupInfo) {
		return errResourceBinding
	}
	return b.check()
}

func (b *resourceGroupPathBinding) createGroup() error {
	if b.check() != nil || b.group != nil {
		return errResourceBinding
	}
	var err error
	b.group, err = config.CreatePrivateChildDirectoryBound(b.dir, resourceGroupDirectoryName, b.profileInfo)
	if err != nil {
		return errResourceBinding
	}
	b.groupInfo, err = b.group.Stat()
	if err != nil {
		return errResourceBinding
	}
	return b.check()
}

func (b *resourceGroupPathBinding) absent() error {
	if b.check() != nil || b.group != nil {
		return errResourceBinding
	}
	if _, err := os.Lstat(filepath.Dir(resourceGroupStatePath(b.dir))); !errors.Is(err, os.ErrNotExist) {
		return errResourceBinding
	}
	return b.check()
}

func readResourceGroupSnapshot(b *resourceGroupPathBinding) (result resourceGroupFileSnapshot, err error) {
	if b.check() != nil || b.group == nil {
		return result, errResourceBinding
	}
	path := resourceGroupStatePath(b.dir)
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Size() < 0 || before.Size() > resourceGroupMaxStoreBytes {
		return result, errResourceGroupState
	}
	f, err := config.OpenPrivateChildFileBound(filepath.Dir(path), "state.json", b.groupInfo)
	if err != nil {
		return result, errResourceBinding
	}
	defer func() {
		if closeErr := f.Close(); closeErr != nil {
			result, err = resourceGroupFileSnapshot{}, errResourceBinding
		}
	}()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return result, errResourceBinding
	}
	data, err := io.ReadAll(io.LimitReader(f, resourceGroupMaxStoreBytes+1))
	if err != nil || len(data) > resourceGroupMaxStoreBytes {
		return result, errResourceGroupState
	}
	state, err := decodeResourceGroupEnvelope(data)
	if err != nil {
		return result, err
	}
	after, err := os.Lstat(path)
	if err != nil || !after.Mode().IsRegular() || !os.SameFile(before, after) || after.Size() != int64(len(data)) {
		return result, errResourceBinding
	}
	certified, err := config.OpenPrivateChildFileBound(filepath.Dir(path), "state.json", b.groupInfo)
	if err != nil {
		return result, errResourceBinding
	}
	certifiedInfo, statErr := certified.Stat()
	closeErr := certified.Close()
	if statErr != nil || closeErr != nil || !os.SameFile(before, certifiedInfo) || b.check() != nil {
		return result, errResourceBinding
	}
	return resourceGroupFileSnapshot{state: state, file: after, digest: sha256.Sum256(data), size: len(data)}, nil
}

// The receipt is inert metadata from the writer's retained replacement handle.
// A path read alone cannot establish which inode this publication produced.
func writeResourceGroupSnapshot(b *resourceGroupPathBinding, data []byte, before *resourceGroupFileCandidate) (observed resourceGroupFileSnapshot, receipt os.FileInfo, err error) {
	if b.check() != nil || b.group == nil || len(data) > resourceGroupMaxStoreBytes {
		return observed, nil, errResourceBinding
	}
	path := resourceGroupStatePath(b.dir)
	lease, err := config.AcquireAtomicWriteLease(path)
	if err != nil {
		return observed, nil, err
	}
	defer func() {
		if closeErr := lease.Close(); closeErr != nil {
			err = errors.Join(err, errResourceBinding)
		}
	}()
	if lease.CheckParent(b.group) != nil || b.check() != nil {
		return observed, nil, errResourceBinding
	}
	// Admission itself can perform bounded atomic-namespace recovery. Recheck
	// the exact previous destination after acquiring the lease, before writing.
	if before == nil {
		if _, statErr := os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) {
			return observed, nil, errResourceBinding
		}
	} else {
		current, readErr := readResourceGroupSnapshot(b)
		if readErr != nil || !before.matches(current) {
			return observed, nil, errResourceBinding
		}
	}
	receipt, err = lease.WriteWithIdentity(path, data)
	if receipt == nil {
		if err == nil {
			err = errResourceBinding
		}
		return observed, nil, err
	}
	current, readErr := readResourceGroupSnapshot(b)
	if readErr != nil || current.file == nil || !os.SameFile(receipt, current.file) || current.digest != sha256.Sum256(data) || current.size != len(data) || b.check() != nil || lease.CheckParent(b.group) != nil {
		return observed, receipt, errors.Join(err, errResourceBinding)
	}
	return current, receipt, err
}
