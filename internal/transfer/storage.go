package transfer

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/webkaz-labs/sobalink/internal/diskspace"
)

const receiveOwnerMarker = ".sobalink-owner"

func randomName(prefix string) (string, error) {
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(token[:]), nil
}

func openDestination(destination string) (*os.Root, error) {
	if !validDestination(destination) {
		return nil, ErrUnsafePath
	}
	info, err := os.Lstat(destination)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrUnsafePath
	}
	root, err := os.OpenRoot(destination)
	if err != nil {
		return nil, err
	}
	actual, err := root.Stat(".")
	if err != nil || !os.SameFile(info, actual) {
		_ = root.Close()
		return nil, ErrUnsafePath
	}
	return root, nil
}

// Each name is a single component relative to a retained parent handle. Only
// creations belong to this transaction; saved output never enters this list.
type preparedDirectory struct {
	parent   *os.Root
	ancestor *preparedDirectory
	name     string
	info     os.FileInfo
	root     *os.Root
	removed  bool
	depth    int
}

type preparedDestination struct {
	io                            *preparationIO
	parent                        *os.Root
	root                          *os.Root
	actual, token, parentIdentity string
	directories                   []*preparedDirectory
	stage                         *preparedDirectory
	marker                        *os.File
	markerInfo                    os.FileInfo
	markerRemoved                 bool
	disarmed                      bool
	plan                          *ReceivePreparation
	ctx                           context.Context
}

// Per-transaction I/O dependencies keep failure tests independent of global
// state and let them exercise the same rollback used by ordinary OS errors.
type preparationIO struct {
	mkdir    func(*os.Root, string, os.FileMode) error
	openRoot func(*os.Root, string) (*os.Root, error)
	protect  func(string) error
	write    func(*os.File, []byte) (int, error)
	sync     func(*os.File) error
	close    func(*os.File) error
}

func defaultPreparationIO() *preparationIO {
	return &preparationIO{
		mkdir:    (*os.Root).Mkdir,
		openRoot: (*os.Root).OpenRoot,
		protect:  protectDirectory,
		write:    (*os.File).Write,
		sync:     (*os.File).Sync,
		close:    (*os.File).Close,
	}
}

func (p *preparedDestination) mkdir(parent *os.Root, ancestor *preparedDirectory, name string) (*preparedDirectory, error) {
	if err := p.ctx.Err(); err != nil {
		return nil, err
	}
	if err := p.io.mkdir(parent, name, 0700); err != nil {
		return nil, err
	}
	// Register before any further fallible operation. An unknown identity fails
	// closed and keeps the transaction pending rather than guessing ownership.
	d := &preparedDirectory{parent: parent, ancestor: ancestor, name: name}
	if ancestor != nil {
		d.depth = ancestor.depth + 1
	}
	p.directories = append(p.directories, d)
	info, err := parent.Lstat(name)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return d, ErrUnsafePath
	}
	d.info = info
	d.root, err = p.io.openRoot(parent, name)
	if err != nil {
		return d, err
	}
	opened, err := d.root.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		return d, ErrUnsafePath
	}
	return d, nil
}

func (d *preparedDirectory) verify() error {
	if d.ancestor != nil {
		if err := d.ancestor.verify(); err != nil {
			return err
		}
	}
	info, err := d.parent.Lstat(d.name)
	if err != nil || d.info == nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !os.SameFile(d.info, info) {
		return ErrUnsafePath
	}
	return nil
}

func (p *preparedDestination) rollback() error {
	if p.disarmed {
		return nil
	}
	if p.marker != nil && !p.markerRemoved {
		if err := p.stage.verify(); err != nil {
			return err
		}
		info, err := p.stage.root.Lstat(receiveOwnerMarker)
		if !errors.Is(err, os.ErrNotExist) {
			if err != nil || p.markerInfo == nil || !info.Mode().IsRegular() || !os.SameFile(p.markerInfo, info) {
				return ErrUnsafePath
			}
			// Identity suffices even when the token write or Sync failed partway.
			if err := p.stage.root.Remove(receiveOwnerMarker); err != nil {
				return err
			}
		}
		p.markerRemoved = true
	}
	// Depth, rather than creation order, also covers separate manifest branches.
	sort.SliceStable(p.directories, func(i, j int) bool { return p.directories[i].depth > p.directories[j].depth })
	for _, d := range p.directories {
		if d.removed {
			continue
		}
		if err := d.verify(); err != nil {
			return err
		}
		// Remove cannot recursively delete a directory containing unknown files.
		if err := d.parent.Remove(d.name); err != nil {
			return err
		}
		d.removed = true
	}
	p.disarmed = true
	p.close(false)
	return nil
}

func (p *preparedDestination) close(keepRoot bool) {
	if p.marker != nil {
		_ = p.marker.Close()
	}
	for i := len(p.directories) - 1; i >= 0; i-- {
		root := p.directories[i].root
		if root != nil && (!keepRoot || root != p.root) {
			_ = root.Close()
		}
	}
	if p.parent != nil {
		_ = p.parent.Close()
	}
}

func (p *preparedDestination) verifyBinding() error {
	if err := p.stage.verify(); err != nil {
		return err
	}
	if err := verifyPreparationParent(filepath.Dir(p.actual), p.parentIdentity); err != nil {
		return err
	}
	info, err := p.stage.root.Lstat(receiveOwnerMarker)
	if err != nil || p.markerInfo == nil || !os.SameFile(p.markerInfo, info) {
		return ErrUnsafePath
	}
	return validateReceiveOwnerMarker(p.stage.root, p.token)
}

func (p *preparedDestination) commit() {
	p.disarmed = true
	p.close(true)
}

func (p *preparedDestination) prepare(ctx context.Context, destination string, entries []Entry, space *diskspace.Guard, reserve int64, beforeCreate func(ReceivePreparation) error) (resultErr error) {
	defer func() { resultErr = diskspace.NormalizeError(resultErr) }()
	p.ctx = ctx
	if p.io == nil {
		p.io = defaultPreparationIO()
	}
	parent, err := openDestination(destination)
	if err != nil {
		return err
	}
	p.parent = parent
	if err := checkRootSpace(ctx, parent, space, reserve); err != nil {
		return err
	}
	p.parentIdentity, err = rootIdentity(parent)
	if err != nil {
		return err
	}
	name, err := randomName("sobalink-")
	if err != nil {
		return err
	}
	stage, err := randomName(".incoming-")
	if err != nil {
		return err
	}
	p.token, err = newOwnerToken()
	if err != nil {
		return err
	}
	p.actual = filepath.Join(destination, name)
	plan := ReceivePreparation{Destination: destination, DestinationIdentity: p.parentIdentity, Root: name, Stage: stage, OwnerToken: p.token}
	if err := ctx.Err(); err != nil {
		return err
	}
	if beforeCreate != nil {
		if err := beforeCreate(plan); err != nil {
			return err
		}
		p.plan = &plan
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := verifyMissingPreparation(ctx, &plan); err != nil {
		return err
	}
	d, err := p.mkdir(parent, nil, name)
	if err != nil {
		return err
	}
	p.root = d.root
	if err = d.verify(); err != nil {
		return err
	}
	if err = verifyPreparationParent(destination, p.parentIdentity); err != nil {
		return err
	}
	if err = p.io.protect(p.actual); err != nil {
		return err
	}
	if err = d.verify(); err != nil {
		return err
	}
	if err = verifyPreparationParent(destination, p.parentIdentity); err != nil {
		return err
	}
	if err = checkRootSpace(ctx, p.root, space, reserve); err != nil {
		return err
	}
	if err = d.verify(); err != nil {
		return err
	}
	p.stage, err = p.mkdir(p.root, d, stage)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := p.stage.verify(); err != nil {
		return err
	}
	p.marker, err = p.stage.root.OpenFile(receiveOwnerMarker, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	p.markerInfo, err = p.marker.Stat()
	if err != nil {
		return err
	}
	n, err := p.io.write(p.marker, []byte(p.token))
	if err != nil {
		return err
	}
	if n != len(p.token) {
		return io.ErrShortWrite
	}
	if err = p.io.sync(p.marker); err != nil {
		return err
	}
	// Retain an identity-bearing file handle after checking the writer's Close.
	marker, err := openAccountingFile(p.stage.root, receiveOwnerMarker)
	if err != nil {
		return err
	}
	opened, err := marker.Stat()
	if err != nil || !os.SameFile(p.markerInfo, opened) {
		_ = marker.Close()
		return ErrUnsafePath
	}
	closeErr := p.io.close(p.marker)
	p.marker = marker
	if closeErr != nil {
		return closeErr
	}
	directories := map[string]*preparedDirectory{".": d}
	for _, e := range entries {
		name := path.Dir(e.Path)
		if e.Kind == Directory {
			name = e.Path
		}
		if name == "." {
			continue
		}
		parts := strings.Split(name, "/")
		parent := d
		for i, component := range parts {
			part := strings.Join(parts[:i+1], "/")
			if err = parent.verify(); err != nil {
				return err
			}
			child := directories[part]
			if child == nil {
				if err = checkRootSpace(ctx, p.root, space, reserve); err != nil {
					return err
				}
				if err = parent.verify(); err != nil {
					return err
				}
				child, err = p.mkdir(parent.root, parent, component)
				if err != nil {
					return err
				}
				directories[part] = child
			}
			parent = child
		}
	}
	return nil
}

// Root confines all operations even when a component is exchanged concurrently.
// The checks additionally reject existing symlinks instead of following them.
// An attacker with write access as the same OS user can still modify received
// files after saving; this package is not a sandbox against that user.
func checkRootSpace(ctx context.Context, root *os.Root, space *diskspace.Guard, reserve int64) error {
	volume, err := root.Open(".")
	if err != nil {
		return diskspace.ErrUnknown
	}
	defer volume.Close()
	return space.Check(ctx, volume, reserve)
}

func ensureDirectories(root *os.Root, name string) error {
	return ensureDirectoriesWithSpace(context.Background(), root, name, diskspace.Process, diskspace.DefaultReserveBytes)
}

func ensureDirectoriesWithSpace(ctx context.Context, root *os.Root, name string, space *diskspace.Guard, reserve int64) error {
	if name == "." {
		return nil
	}
	parts := strings.Split(name, "/")
	for i := range parts {
		part := strings.Join(parts[:i+1], "/")
		info, err := root.Lstat(part)
		if errors.Is(err, fs.ErrNotExist) {
			if err = checkRootSpace(ctx, root, space, reserve); err != nil {
				return err
			}
			if err = root.Mkdir(part, 0700); err != nil && !errors.Is(err, fs.ErrExist) {
				return err
			}
			info, err = root.Lstat(part)
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return ErrUnsafePath
		}
	}
	return nil
}

// commitFile uses a same-filesystem hard link as a cross-platform atomic
// no-replace operation. Filesystems without hard-link support fail closed; it
// never falls back to a rename that could overwrite an existing destination.
func commitFile(root *os.Root, temp, name string, original fs.FileInfo) error {
	return commitFileWithSpace(context.Background(), root, temp, name, original, diskspace.Process, diskspace.DefaultReserveBytes, nil)
}

func commitFileWithSpace(ctx context.Context, root *os.Root, temp, name string, original fs.FileInfo, space *diskspace.Guard, reserve int64, beforeLink func() error) error {
	if err := ensureDirectoriesWithSpace(ctx, root, path.Dir(name), space, reserve); err != nil {
		return err
	}
	current, err := root.Lstat(temp)
	if err != nil {
		return err
	}
	if !current.Mode().IsRegular() || !os.SameFile(original, current) {
		return ErrUnsafePath
	}
	if err = checkRootSpace(ctx, root, space, reserve); err != nil {
		return err
	}
	if beforeLink != nil {
		if err := beforeLink(); err != nil {
			return err
		}
	}
	if err = root.Link(temp, name); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("%w: destination exists", ErrConflict)
		}
		return fmt.Errorf("save without overwrite: %w", err)
	}
	final, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if !final.Mode().IsRegular() || !os.SameFile(original, final) {
		return ErrUnsafePath
	}
	return nil
}

// Remove only a verified empty stage and its internal marker. Unknown files
// remain accounted and retain their marker for a later startup inventory.
func removeReceiveStage(root *os.Root, name, token string, original os.FileInfo) error {
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !os.SameFile(original, info) {
		return ErrUnsafePath
	}
	stage, err := root.OpenRoot(name)
	if err != nil {
		return err
	}
	defer stage.Close()
	opened, err := stage.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		return ErrUnsafePath
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
	for _, entry := range names {
		if entry != receiveOwnerMarker {
			return ErrReceiveRecovery
		}
	}
	if len(names) != 0 {
		if err := validateReceiveOwnerMarker(stage, token); err != nil {
			return err
		}
		if err := stage.Remove(receiveOwnerMarker); err != nil {
			return err
		}
	}
	current, err := root.Lstat(name)
	if err != nil || !os.SameFile(info, current) {
		return ErrUnsafePath
	}
	return root.Remove(name)
}
