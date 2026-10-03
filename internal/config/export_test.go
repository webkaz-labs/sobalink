package config

import (
	"fmt"
	"os"
	"runtime"
)

// atomicWriteWithTestHooks exercises the same writer used by AtomicWrite while
// allowing integration tests to stop at real persistence boundaries.
func atomicWriteWithTestHooks(path string, data []byte, phase func(string), afterSync func() error) error {
	return atomicWriteOwned(path, data, &atomicHooks{
		barrier: phase,
		syncReplacement: func(file, parent, old *os.File) error {
			if err := atomicSyncReplacement(file, parent, old); err != nil {
				return err
			}
			if afterSync != nil {
				return afterSync()
			}
			return nil
		},
	})
}

// AtomicWriteForTest exposes the real AtomicWrite implementation with a
// process-crash callback to external-package integration tests.
func AtomicWriteForTest(path string, data []byte, phase func(string)) error {
	return atomicWriteWithTestHooks(path, data, phase, nil)
}

// ObserveAtomicAdmissionForTest attaches the existing real boundary observer
// only to public leased admission. Tests using it must not run in parallel.
func ObserveAtomicAdmissionForTest(phase func(string), reclaimSync func() error) func() {
	original := acquirePublicAtomicWriteLease
	acquirePublicAtomicWriteLease = func(path string, _ *atomicHooks, reserved ...string) (*AtomicWriteLease, error) {
		return acquireAtomicWriteLease(path, &atomicHooks{barrier: phase, syncReclaim: func(dir *os.File) error {
			if reclaimSync != nil {
				if err := reclaimSync(); err != nil {
					return err
				}
			}
			return atomicSyncDirectory(dir)
		}}, reserved...)
	}
	return func() { acquirePublicAtomicWriteLease = original }
}

// DirectoryIdentityForTest prepares exported transfer intent fixtures using the
// native identity format without inventing an ownership proof.
func DirectoryIdentityForTest(path string) (string, error) {
	f, err := atomicOpenDirectory(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := atomicFileMetadata(f)
	if err != nil {
		return "", err
	}
	if runtime.GOOS == "windows" {
		return fmt.Sprintf("%x:%x:%x", info.id[0], info.id[1], info.id[2]), nil
	}
	return fmt.Sprintf("%x:%x", info.id[0], info.id[1]), nil
}
