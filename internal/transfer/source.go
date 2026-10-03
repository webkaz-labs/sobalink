package transfer

import (
	"context"
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

// Open rechecks a source before staging. On Unix it cannot block opening a FIFO
// substituted for a regular file, and it does not follow a final symlink.
func (s Source) Open() (*os.File, error) {
	info, err := os.Lstat(s.LocalPath)
	if err != nil {
		return nil, err
	}
	return openSource(s.LocalPath, info)
}

// BuildManifest hashes selected regular files and directories using bounded
// buffers and directory pages. It rejects symlinks, special files, unsafe names,
// collisions and files that change while hashing. Only empty directories appear
// explicitly. No source executable attributes are included in the manifest.
func BuildManifest(id string, paths []string, limits Limits) (Manifest, []Source, error) {
	return BuildManifestContext(context.Background(), id, paths, limits)
}

// BuildManifestContext is BuildManifest with cancellation during enumeration and
// hashing. A failed or cancelled operation never returns a partial manifest.
func BuildManifestContext(ctx context.Context, id string, paths []string, limits Limits) (Manifest, []Source, error) {
	plan, err := PlanSources(ctx, id, paths, limits)
	if err != nil {
		return Manifest{}, nil, err
	}
	return plan.Hash(ctx)
}

// SourcePlan is a validated selection whose payload bytes have not been read.
// It lets callers reserve staging capacity before hashing or copying files.
type SourcePlan struct {
	manifest Manifest
	sources  []Source
	info     map[string]os.FileInfo
}

// Manifest returns a copy for admission only. Its placeholder digests must be
// replaced by Hash before the manifest is offered to a receiver.
func (p *SourcePlan) Manifest() Manifest {
	return Manifest{ID: p.manifest.ID, Entries: append([]Entry(nil), p.manifest.Entries...)}
}

// PlanSources validates the complete selection using metadata only, including
// entry counts, portable path collisions and byte limits.
func PlanSources(ctx context.Context, id string, paths []string, limits Limits) (*SourcePlan, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	lim, err := limitsOrDefault(limits)
	if err != nil {
		return nil, err
	}
	if !validID(id) || len(paths) == 0 {
		return nil, ErrInvalidManifest
	}
	if len(paths) > lim.MaxEntries {
		return nil, ErrLimit
	}
	b := sourceBuilder{ctx: ctx, manifest: Manifest{ID: id}, limits: lim, info: map[string]os.FileInfo{}}
	for _, selected := range paths {
		absolute, err := filepath.Abs(selected)
		if err != nil {
			return nil, err
		}
		base := filepath.Base(absolute)
		if err = validatePath(base, lim); err != nil {
			return nil, err
		}
		parent, err := os.OpenRoot(filepath.Dir(absolute))
		if err != nil {
			return nil, err
		}
		err = b.visit(parent, base, base, absolute)
		_ = parent.Close()
		if err != nil {
			return nil, err
		}
	}
	sort.Slice(b.manifest.Entries, func(i, j int) bool { return b.manifest.Entries[i].Path < b.manifest.Entries[j].Path })
	if _, err = validateManifest(b.manifest, lim); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &SourcePlan{manifest: b.manifest, sources: b.sources, info: b.info}, nil
}

type sourceBuilder struct {
	ctx       context.Context
	manifest  Manifest
	sources   []Source
	limits    Limits
	total     int64
	visited   int
	walkBytes int64
	info      map[string]os.FileInfo
}

func (b *sourceBuilder) visit(root *os.Root, local, wire, absolute string) error {
	if err := b.ctx.Err(); err != nil {
		return err
	}
	if err := validatePath(wire, b.limits); err != nil {
		return err
	}
	// Directory traversal retains one frame and its local/wire paths per
	// ancestor. Charge that live metadata before opening another directory so
	// raising logical depth cannot multiply memory outside the finite budget.
	frameBytes := int64(512) + int64(len(wire)) + int64(len(absolute))
	if frameBytes > b.limits.MaxManifestBytes-metadataSize(b.manifest)-b.walkBytes {
		return ErrMetadataLimit
	}
	b.walkBytes += frameBytes
	defer func() { b.walkBytes -= frameBytes }()
	b.visited++
	if (b.visited-1)/b.limits.MaxDepth >= b.limits.MaxEntries {
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
			if err := b.ctx.Err(); err != nil {
				return err
			}
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
	b.total += info.Size()
	if err := b.add(Entry{Path: wire, Kind: File, Size: info.Size(), SHA256: fmt.Sprintf("%064d", 0)}, absolute); err != nil {
		return err
	}
	b.info[b.manifest.Entries[len(b.manifest.Entries)-1].ID] = info
	return nil
}

// Hash reads only after the entire selection has passed validation. Sources are
// rechecked against their inspected identities, sizes and modification times.
func (p *SourcePlan) Hash(ctx context.Context) (Manifest, []Source, error) {
	manifest := p.Manifest()
	byID := make(map[string]int, len(manifest.Entries))
	for i, entry := range manifest.Entries {
		byID[entry.ID] = i
	}
	for _, source := range p.sources {
		if err := ctx.Err(); err != nil {
			return Manifest{}, nil, err
		}
		digest, err := hashSource(ctx, source.LocalPath, p.info[source.EntryID])
		if err != nil {
			return Manifest{}, nil, err
		}
		manifest.Entries[byID[source.EntryID]].SHA256 = digest
	}
	if err := ctx.Err(); err != nil {
		return Manifest{}, nil, err
	}
	return manifest, append([]Source(nil), p.sources...), nil
}

func hashSource(ctx context.Context, absolute string, info os.FileInfo) (string, error) {
	file, err := openSource(absolute, info)
	if err != nil {
		return "", err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return "", err
	}
	if opened.Size() != info.Size() || !opened.ModTime().Equal(info.ModTime()) {
		return "", fmt.Errorf("%w: source changed", ErrIntegrity)
	}
	digest := sha256.New()
	n, err := io.CopyBuffer(digest, io.LimitReader(sourceContextReader{ctx, file}, info.Size()+1), make([]byte, streamBufferBytes))
	if err != nil {
		return "", err
	}
	final, err := file.Stat()
	if err != nil {
		return "", err
	}
	if n != info.Size() || final.Size() != info.Size() || !final.ModTime().Equal(info.ModTime()) {
		return "", fmt.Errorf("%w: source changed", ErrIntegrity)
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func openSource(absolute string, info os.FileInfo) (*os.File, error) {
	if !info.Mode().IsRegular() {
		return nil, ErrUnsafePath
	}
	root, err := os.OpenRoot(filepath.Dir(absolute))
	if err != nil {
		return nil, err
	}
	defer root.Close()
	local := filepath.Base(absolute)
	file, err := root.OpenFile(local, sourceReadFlags(), 0)
	if err != nil {
		return nil, err
	}
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		file.Close()
		return nil, ErrUnsafePath
	}
	current, err := root.Lstat(local)
	if err != nil || !current.Mode().IsRegular() || !os.SameFile(opened, current) {
		file.Close()
		return nil, ErrUnsafePath
	}
	return file, nil
}

type sourceContextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r sourceContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.r.Read(p)
	if cancelled := r.ctx.Err(); cancelled != nil {
		return n, cancelled
	}
	return n, err
}

func (b *sourceBuilder) add(entry Entry, source string) error {
	if len(b.manifest.Entries) >= b.limits.MaxEntries {
		return ErrLimit
	}
	entry.ID = fmt.Sprintf("file-%d", len(b.manifest.Entries)+1)
	b.manifest.Entries = append(b.manifest.Entries, entry)
	if metadataSize(b.manifest) > b.limits.MaxManifestBytes {
		return ErrMetadataLimit
	}
	if entry.Kind == File {
		b.sources = append(b.sources, Source{EntryID: entry.ID, LocalPath: source})
	}
	return nil
}
