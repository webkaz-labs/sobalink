package transfer

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
)

// Source records a regular local file selected for sending. LocalPath is private
// local state and must never be included in the wire manifest. The receiver
// verifies bytes again, so a changed source cannot silently alter a transfer.
type Source struct {
	EntryID   string `json:"entryID"`
	LocalPath string `json:"-"`
}

// BuildManifest hashes selected regular files and directories using bounded
// buffers and directory pages. It rejects symlinks, special files, unsafe names,
// collisions and files that change while hashing. Only empty directories appear
// explicitly. No source executable attributes are included in the manifest.
func BuildManifest(id string, paths []string, limits Limits) (Manifest, []Source, error) {
	lim, err := limitsOrDefault(limits)
	if err != nil {
		return Manifest{}, nil, err
	}
	if !validID(id) || len(paths) == 0 {
		return Manifest{}, nil, ErrInvalidManifest
	}
	if len(paths) > lim.MaxEntries {
		return Manifest{}, nil, ErrLimit
	}
	b := sourceBuilder{manifest: Manifest{ID: id}, limits: lim}
	for _, selected := range paths {
		absolute, err := filepath.Abs(selected)
		if err != nil {
			return Manifest{}, nil, err
		}
		base := filepath.Base(absolute)
		if err = validatePath(base, lim); err != nil {
			return Manifest{}, nil, err
		}
		parent, err := os.OpenRoot(filepath.Dir(absolute))
		if err != nil {
			return Manifest{}, nil, err
		}
		err = b.visit(parent, base, base, absolute)
		_ = parent.Close()
		if err != nil {
			return Manifest{}, nil, err
		}
	}
	sort.Slice(b.manifest.Entries, func(i, j int) bool { return b.manifest.Entries[i].Path < b.manifest.Entries[j].Path })
	if _, err = validateManifest(b.manifest, lim); err != nil {
		return Manifest{}, nil, err
	}
	return b.manifest, b.sources, nil
}

type sourceBuilder struct {
	manifest Manifest
	sources  []Source
	limits   Limits
	total    int64
	visited  int
}

func (b *sourceBuilder) visit(root *os.Root, local, wire, absolute string) error {
	if err := validatePath(wire, b.limits); err != nil {
		return err
	}
	b.visited++
	if b.visited > b.limits.MaxEntries*b.limits.MaxDepth {
		return ErrLimit
	}
	info, err := root.Lstat(local)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() && !info.Mode().IsRegular() {
		return ErrUnsafePath
	}
	if info.IsDir() {
		directory, err := root.OpenRoot(local)
		if err != nil {
			return err
		}
		defer directory.Close()
		opened, err := directory.Stat(".")
		if err != nil || !os.SameFile(info, opened) {
			return ErrUnsafePath
		}
		current, err := root.Lstat(local)
		if err != nil || !current.IsDir() || current.Mode()&os.ModeSymlink != 0 || !os.SameFile(opened, current) {
			return ErrUnsafePath
		}
		file, err := directory.Open(".")
		if err != nil {
			return err
		}
		defer file.Close()
		empty := true
		for {
			children, readErr := file.ReadDir(128)
			for _, child := range children {
				empty = false
				if err := b.visit(directory, child.Name(), path.Join(wire, child.Name()), filepath.Join(absolute, child.Name())); err != nil {
					return err
				}
			}
			if readErr == io.EOF {
				break
			}
			if readErr != nil {
				return readErr
			}
		}
		if empty {
			return b.add(Entry{Path: wire, Kind: Directory}, "")
		}
		return nil
	}
	if info.Size() > b.limits.MaxFileBytes || info.Size() > b.limits.MaxBatchBytes-b.total {
		return ErrLimit
	}
	file, err := root.OpenFile(local, sourceReadFlags(), 0)
	if err != nil {
		return err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return ErrUnsafePath
	}
	current, err := root.Lstat(local)
	if err != nil || !current.Mode().IsRegular() || !os.SameFile(opened, current) {
		return ErrUnsafePath
	}
	digest := sha256.New()
	n, err := io.CopyBuffer(digest, io.LimitReader(file, info.Size()+1), make([]byte, streamBufferBytes))
	if err != nil {
		return err
	}
	final, err := file.Stat()
	if err != nil {
		return err
	}
	if n != info.Size() || final.Size() != info.Size() || !final.ModTime().Equal(info.ModTime()) {
		return fmt.Errorf("%w: source changed", ErrIntegrity)
	}
	b.total += n
	return b.add(Entry{Path: wire, Kind: File, Size: n, SHA256: hex.EncodeToString(digest.Sum(nil))}, absolute)
}

func (b *sourceBuilder) add(entry Entry, source string) error {
	if len(b.manifest.Entries) >= b.limits.MaxEntries {
		return ErrLimit
	}
	entry.ID = fmt.Sprintf("file-%d", len(b.manifest.Entries)+1)
	b.manifest.Entries = append(b.manifest.Entries, entry)
	if metadataSize(b.manifest) > b.limits.MaxManifestBytes {
		return ErrLimit
	}
	if entry.Kind == File {
		b.sources = append(b.sources, Source{EntryID: entry.ID, LocalPath: source})
	}
	return nil
}
