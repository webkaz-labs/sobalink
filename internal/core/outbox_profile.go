package core

import (
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"

	"github.com/webkaz-labs/sobalink/internal/config"
)

// DurableProfile is the current profile's initialization fence. Initializing
// grants no sending or receipt authority; ready never authorizes replacement
// of missing stores. Unsupported profile formats are rejected without mutation.
type DurableProfile struct {
	Version int    `json:"version"`
	Epoch   string `json:"epoch"`
	Phase   string `json:"phase"`
}

const currentProfileVersion = 3

func newDurableProfile() *DurableProfile {
	return &DurableProfile{Version: 1, Epoch: randomID(), Phase: "initializing"}
}

func validDurableProfile(p Profile) bool {
	g := p.DurableFiles
	if p.Version != currentProfileVersion || g == nil || g.Version != 1 || (g.Phase != "initializing" && g.Phase != "ready") {
		return false
	}
	b, err := hex.DecodeString(g.Epoch)
	return err == nil && len(b) == 16 && hex.EncodeToString(b) == g.Epoch
}

// An absent profile cannot adopt surviving private durable inventory.
func checkDurableProfileInventory(dir string, p Profile) error {
	if p.Version != 0 && p.Version != currentProfileVersion {
		return &localCommandError{"profile_format_unsupported", "this version requires a fresh profile; preserve the existing profile and choose a new directory"}
	}
	if p.Version != 0 && !validDurableProfile(p) {
		return durableProfileFailure("outbox_recovery_required")
	}
	if p.DurableFiles != nil {
		if !validDurableProfile(p) {
			return durableProfileFailure("outbox_recovery_required")
		}
		return nil
	}
	for _, name := range []string{"durable-v1", "durable-outbox-v1", "messages.json"} {
		if _, err := os.Lstat(filepath.Join(dir, name)); !errors.Is(err, os.ErrNotExist) {
			return durableProfileFailure("outbox_recovery_required")
		}
	}
	return nil
}

func durableProfileFailure(code string) error {
	return &localCommandError{code, "durable state needs review; do not remove or replace its profile or private stores"}
}

func (c *Core) setDurableProfileError(code string) {
	c.mu.Lock()
	c.durableProfileError = code
	c.mu.Unlock()
}

func (c *Core) durableProfileReady() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.profile.DurableFiles != nil && c.profile.DurableFiles.Phase == "ready" && c.durableProfileError == ""
}

// persistDurableProfile retains a published-but-uncertain initialization in memory.
// No later error rolls the profile back or grants permission to initialize an
// already-ready store. The extra parent barrier is needed on platforms whose
// ordinary atomic replacement only synchronizes the file handle.
func (c *Core) persistDurableProfile(p Profile, syncDirectory func(*os.Root) error) (err error) {
	defer func() {
		if err != nil {
			c.setDurableProfileError("outbox_recovery_required")
			if errors.Is(err, config.ErrAtomicCommitted) {
				c.lanStartUncertain.Store(true)
			}
		}
	}()
	if c.resourceLock == nil {
		return durableProfileFailure("outbox_recovery_required")
	}
	return c.resourceLock.WithOwnershipInfo(c.dir, func(directory, _ os.FileInfo) error {
		return c.persistOwnedDurableProfile(p, directory, syncDirectory)
	})
}

func (c *Core) persistOwnedDurableProfile(p Profile, directory os.FileInfo, syncDirectory func(*os.Root) error) (err error) {
	if !validDurableProfile(p) || p.DurableFiles == nil {
		return durableProfileFailure("outbox_invalid")
	}
	before, err := os.Lstat(c.dir)
	if err != nil || !before.IsDir() || before.Mode()&os.ModeSymlink != 0 || !os.SameFile(directory, before) {
		return durableProfileFailure("outbox_recovery_required")
	}
	root, err := os.OpenRoot(c.dir)
	if err != nil {
		return err
	}
	published := false
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			if published {
				closeErr = errors.Join(config.ErrAtomicCommitted, closeErr)
			}
			err = errors.Join(err, closeErr)
		}
	}()
	current, err := root.Stat(".")
	if err != nil || !os.SameFile(before, current) {
		return durableProfileFailure("outbox_recovery_required")
	}
	err = c.writeBoundDurableProfile(p, root, syncDirectory)
	published = atomicPublished(err)
	if published {
		c.mu.Lock()
		c.profile = cloneProfile(p)
		c.mu.Unlock()
	}
	if err != nil {
		return err
	}
	after, err := os.Lstat(c.dir)
	if err != nil || !after.IsDir() || after.Mode()&os.ModeSymlink != 0 || !os.SameFile(before, after) {
		return errors.Join(config.ErrAtomicCommitted, durableProfileFailure("outbox_recovery_required"))
	}
	return nil
}

func (c *Core) writeBoundDurableProfile(p Profile, root *os.Root, syncDirectory func(*os.Root) error) (err error) {
	parent, err := root.Open(".")
	if err != nil {
		return err
	}
	published := false
	defer func() {
		if e := parent.Close(); e != nil {
			if published {
				e = errors.Join(config.ErrAtomicCommitted, e)
			}
			err = errors.Join(err, e)
		}
	}()
	path := filepath.Join(c.dir, "sobalink.json")
	lease, err := config.AcquireAtomicWriteLease(path)
	if err != nil {
		return err
	}
	defer func() {
		if e := lease.Close(); e != nil {
			if published {
				e = errors.Join(config.ErrAtomicCommitted, e)
			}
			err = errors.Join(err, e)
		}
	}()
	if err := lease.CheckParent(parent); err != nil {
		return err
	}
	write := lease.Write
	// The existing private fault-injection seam is never configured by a user
	// request or environment variable. Production always uses the bound lease.
	if c.atomicWrite != nil {
		write = c.atomicWrite
	}
	err = c.writeProfileUsing(p, func(path string, b []byte) error {
		return c.writeAtomicUsing(path, b, write)
	})
	published = atomicPublished(err)
	if err != nil {
		return err
	}
	if err := syncDurableProfileNamespaces(root, syncDirectory); err != nil {
		return errors.Join(config.ErrAtomicCommitted, err)
	}
	if err := lease.CheckParent(parent); err != nil {
		return errors.Join(config.ErrAtomicCommitted, err)
	}
	return nil
}

func syncDurableProfileNamespaces(root *os.Root, syncDirectory func(*os.Root) error) (err error) {
	before, err := root.Lstat(".sobalink-atomic-v1")
	if err != nil || !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
		return durableProfileFailure("outbox_recovery_required")
	}
	owned, err := root.OpenRoot(".sobalink-atomic-v1")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, owned.Close()) }()
	opened, err := owned.Stat(".")
	if err != nil || !os.SameFile(before, opened) {
		return durableProfileFailure("outbox_recovery_required")
	}
	if err := syncDirectory(owned); err != nil {
		return err
	}
	after, err := root.Lstat(".sobalink-atomic-v1")
	if err != nil || !after.IsDir() || after.Mode()&os.ModeSymlink != 0 || !os.SameFile(before, after) {
		return durableProfileFailure("outbox_recovery_required")
	}
	return syncDirectory(root)
}

// restoreDurableProfile runs before maintenance or network startup. A new
// profile may finish only its same-epoch empty initialization. Ready stores are
// never recreated. Missing lifecycle ownership or uncertain storage disables
// message acceptance; a mandatory message store is never silently recreated.
func (c *Core) restoreDurableProfile() error {
	p := c.profileCopy()
	c.setDurableProfileError("outbox_recovery_required")
	if !validDurableProfile(p) || c.resourceLock == nil || c.resourceNonce == "" {
		return durableProfileFailure("outbox_recovery_required")
	}
	initialize := p.DurableFiles.Phase == "initializing"
	if initialize {
		// Certify the no-authority initialization fence before creating stores.
		if err := c.persistDurableProfile(p, outboxSyncDirectory); err != nil {
			return err
		}
	}
	if err := c.initializeMessageStore(initialize); err != nil {
		return err
	}
	if err := c.initializeOutbox(initialize); err != nil {
		return err
	}
	if err := c.initializeDurableReceiver(initialize); err != nil {
		return err
	}
	p.DurableFiles.Phase = "ready"
	if err := c.persistDurableProfile(p, outboxSyncDirectory); err != nil {
		return err
	}
	c.setDurableProfileError("")
	return nil
}
