package core

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/webkaz-labs/sobalink/internal/config"
)

// A resource operation retains both directory handles while the actual
// lifecycle mutex is held. Each publication must stay bound
// to that profile, process lock and private journal directory. This is not a
// transaction or protection against arbitrary same-user filesystem rollback.
type resourcePathBinding struct {
	grant     *os.File
	grantInfo os.FileInfo

	dir                                string
	profile, journal                   *os.File
	profileInfo, journalInfo, lockInfo os.FileInfo
}

var errResourceBinding = errors.New("resource lifecycle path binding changed")

func openResourcePathBinding(dir string, expectedProfile, expectedLock os.FileInfo) (*resourcePathBinding, error) {
	b := &resourcePathBinding{dir: dir, profileInfo: expectedProfile, lockInfo: expectedLock}
	if expectedProfile == nil || expectedLock == nil {
		return nil, errResourceBinding
	}
	var err error
	b.profile, err = os.Open(dir)
	if err != nil {
		return nil, err
	}
	b.journal, err = os.Open(filepath.Dir(resourceStatePath(dir)))
	if err != nil {
		b.close()
		return nil, err
	}
	opened, err := b.profile.Stat()
	if err == nil && !os.SameFile(expectedProfile, opened) {
		err = errResourceBinding
	}
	if err == nil {
		b.journalInfo, err = b.journal.Stat()
	}
	if err == nil {
		err = b.check()
	}
	if err != nil {
		b.close()
		return nil, err
	}
	return b, nil
}
func (b *resourcePathBinding) close() {
	if b.grant != nil {
		_ = b.grant.Close()
	}
	if b.journal != nil {
		_ = b.journal.Close()
	}
	if b.profile != nil {
		_ = b.profile.Close()
	}
}
func (b *resourcePathBinding) check() error {
	if b.grant != nil {
		certified, privateErr := config.OpenPrivateChildDirectoryBound(b.dir, "resource-grants", b.profileInfo)
		if privateErr != nil {
			return errResourceBinding
		}
		certifiedInfo, statErr := certified.Stat()
		closeErr := certified.Close()
		if statErr != nil || closeErr != nil || b.grantInfo == nil || !os.SameFile(certifiedInfo, b.grantInfo) {
			return errResourceBinding
		}
		current, err := os.Lstat(filepath.Dir(resourceGrantStatePath(b.dir)))
		opened, statErr := b.grant.Stat()
		if err != nil || statErr != nil || b.grantInfo == nil || current.Mode()&os.ModeSymlink != 0 || !current.IsDir() || !os.SameFile(current, b.grantInfo) || !os.SameFile(opened, b.grantInfo) {
			return errResourceBinding
		}
	}
	for _, entry := range []struct {
		path      string
		expected  os.FileInfo
		directory bool
	}{
		{b.dir, b.profileInfo, true},
		{filepath.Dir(resourceStatePath(b.dir)), b.journalInfo, true},
		{filepath.Join(b.dir, "process.lock"), b.lockInfo, false},
	} {
		current, err := os.Lstat(entry.path)
		if err != nil || entry.expected == nil || current.Mode()&os.ModeSymlink != 0 || !os.SameFile(entry.expected, current) || entry.directory && !current.IsDir() || !entry.directory && !current.Mode().IsRegular() {
			return errResourceBinding
		}
	}
	return nil
}
func (b *resourcePathBinding) write(path string, data []byte, injected func(string, []byte) error) error {
	if err := b.check(); err != nil {
		return err
	}
	var parent *os.File
	switch path {
	case resourceStatePath(b.dir):
		parent = b.journal
	case resourceGrantStatePath(b.dir):
		if b.grant == nil {
			return errResourceBinding
		}
		parent = b.grant
	case filepath.Join(b.dir, capacityPolicyFile):
		parent = b.profile
	default:
		return errResourceBinding
	}
	var err error
	if injected != nil {
		// The existing test seam injects persistence outcomes; checks on both sides
		// still prevent its substituted path from becoming acknowledged evidence.
		err = injected(path, data)
	} else {
		var lease *config.AtomicWriteLease
		lease, err = config.AcquireAtomicWriteLease(path)
		if err == nil {
			err = lease.CheckParent(parent)
			if err == nil {
				err = b.check()
			}
			if err == nil {
				err = lease.Write(path, data)
			}
			_ = lease.Close()
		}
	}
	if bindingErr := b.check(); bindingErr != nil {
		if atomicPublished(err) {
			return errors.Join(config.ErrAtomicCommitted, err, bindingErr)
		}
		return errors.Join(err, bindingErr)
	}
	return err
}
