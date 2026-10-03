package transfer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/webkaz-labs/sobalink/internal/diskspace"
)

const streamBufferBytes = 32 << 10

// ReceiveFile verifies the authenticated caller for every stream. The reader is
// consumed with fixed-size memory, and at most declared size+1 bytes are read.
// Cancellation closes readers implementing io.Closer, including HTTP bodies.
// An arbitrary non-closable blocking Reader must be interrupted by its caller;
// it still cannot commit after cancellation or revocation has taken effect.
func (m *Manager) ReceiveFile(ctx context.Context, peer Peer, id, fileID string, reader io.Reader) (ack FileAck, resultErr error) {
	if ctx == nil || reader == nil {
		return FileAck{}, ErrState
	}
	m.mu.Lock()
	p, err := m.checkPeerLocked(peer)
	if err != nil {
		m.mu.Unlock()
		return FileAck{}, err
	}
	b, ok := m.batches[id]
	if !ok {
		m.mu.Unlock()
		return FileAck{}, ErrNotFound
	}
	if b.value.Peer != peer {
		m.mu.Unlock()
		return FileAck{}, ErrPeerChanged
	}
	index := fileIndex(b, fileID)
	if index < 0 {
		m.mu.Unlock()
		return FileAck{}, ErrNotFound
	}
	f := &b.value.Files[index]
	if f.Kind != File {
		m.mu.Unlock()
		return FileAck{}, ErrState
	}
	if f.State == FileSaved {
		ack := savedAck(b, f)
		m.mu.Unlock()
		return ack, nil
	}
	if terminal(b.value.State) || b.value.State == Pending || f.State != FilePending || b.root == nil {
		m.mu.Unlock()
		return FileAck{}, ErrState
	}
	if ctx.Err() != nil {
		m.mu.Unlock()
		return FileAck{}, transferError(ctx, ctx.Err())
	}
	if b.ctx.Err() != nil {
		m.mu.Unlock()
		return FileAck{}, ErrCancelled
	}
	if m.active >= m.limits.MaxConcurrentFiles || p.active >= m.limits.MaxConcurrentPerPeer {
		m.mu.Unlock()
		return FileAck{}, ErrBusy
	}
	streamCtx, cancel := context.WithCancel(b.ctx)
	stopCancel := context.AfterFunc(ctx, cancel)
	stopClose := func() bool { return true }
	if closer, ok := reader.(io.Closer); ok {
		stopClose = context.AfterFunc(streamCtx, func() { _ = closer.Close() })
	}
	f.State, f.Error = FileReceiving, ""
	b.active++
	p.active++
	m.active++
	root, stage, entry := b.root, b.stage, f.Entry
	m.updateStateLocked(b)
	m.mu.Unlock()
	var tempName string
	var temp *os.File
	defer func() {
		resultErr = diskspace.NormalizeError(resultErr)
		stopCancel()
		stopClose()
		cancel()
		if temp != nil {
			_ = temp.Close()
		}
		if tempName != "" {
			_ = root.Remove(tempName)
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		b.active--
		m.active--
		m.peers[peer.ID].active--
		if resultErr != nil && f.State == FileReceiving {
			f.State, f.Error = FileFailed, ErrorCode(resultErr)
		}
		if b.active == 0 && b.ctx.Err() != nil && !terminal(b.value.State) {
			if state := m.peers[peer.ID]; !state.paused && !state.revoked && state.peer == peer {
				b.ctx, b.cancel = context.WithCancel(context.Background())
			}
		}
		m.updateStateLocked(b)
		if terminal(b.value.State) {
			m.releaseLocked(b)
		}
		m.closeRootLocked(b)
	}()
	volume, err := root.Open(stage)
	if err != nil {
		return FileAck{}, err
	}
	err = m.diskSpace.Check(streamCtx, volume, m.diskReserve())
	_ = volume.Close()
	if err != nil {
		return FileAck{}, transferError(streamCtx, err)
	}
	name, err := randomName("part-")
	if err != nil {
		return FileAck{}, err
	}
	tempName = stage + "/" + name
	// Exclusive creation rejects an existing symlink, including one planted in a
	// confined staging directory. Neither sender names nor source modes are used.
	temp, err = root.OpenFile(tempName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return FileAck{}, err
	}
	digest := sha256.New()
	buffer := make([]byte, streamBufferBytes)
	var received int64
	emptyReads := 0
	for {
		if err = receiveCancellation(ctx, streamCtx); err != nil {
			return FileAck{}, err
		}
		want := int64(len(buffer))
		if remaining := entry.Size - received + 1; want > remaining {
			want = remaining
		}
		n, readErr := reader.Read(buffer[:want])
		if n < 0 || n > int(want) {
			return FileAck{}, fmt.Errorf("invalid reader count")
		}
		if err = receiveCancellation(ctx, streamCtx); err != nil {
			return FileAck{}, err
		}
		if int64(n) > entry.Size-received {
			return FileAck{}, ErrIntegrity
		}
		if n > 0 {
			emptyReads = 0
			written, writeErr := m.diskSpace.Write(streamCtx, temp, buffer[:n], m.diskReserve())
			if writeErr != nil {
				return FileAck{}, transferError(streamCtx, writeErr)
			}
			if written != n {
				return FileAck{}, io.ErrShortWrite
			}
			_, _ = digest.Write(buffer[:n])
			received += int64(n)
			m.mu.Lock()
			b.value.CompletedBytes += int64(n)
			f.CompletedBytes += int64(n)
			m.mu.Unlock()
		} else {
			emptyReads++
			if emptyReads >= 100 && readErr == nil {
				return FileAck{}, io.ErrNoProgress
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return FileAck{}, transferError(streamCtx, readErr)
		}
	}
	if received != entry.Size || hex.EncodeToString(digest.Sum(nil)) != entry.SHA256 {
		return FileAck{}, ErrIntegrity
	}
	if err = m.diskSpace.Check(streamCtx, temp, m.diskReserve()); err != nil {
		return FileAck{}, transferError(streamCtx, err)
	}
	if err = temp.Sync(); err != nil {
		return FileAck{}, err
	}
	original, err := temp.Stat()
	if err != nil {
		return FileAck{}, err
	}
	if err = temp.Close(); err != nil {
		return FileAck{}, err
	}
	temp = nil
	m.mu.Lock()
	defer m.mu.Unlock()
	// Commit is serialized with Cancel, PausePeer, RevokePeer and BindPeer.
	if err = receiveCancellation(ctx, streamCtx); err != nil {
		return FileAck{}, err
	}
	if _, err = m.checkPeerLocked(peer); err != nil {
		return FileAck{}, err
	}
	if f.State != FileReceiving || terminal(b.value.State) {
		return FileAck{}, ErrCancelled
	}
	if err = commitFileWithSpace(streamCtx, root, tempName, entry.Path, original, m.diskSpace, m.limits.DiskReserveBytes, func() error { return receiveCancellation(ctx, streamCtx) }); err != nil {
		return FileAck{}, transferError(streamCtx, err)
	}
	f.State, f.StoredName, f.Error = FileSaved, entry.Path, ""
	return savedAck(b, f), nil
}

func (m *Manager) diskReserve() int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.limits.DiskReserveBytes
}

func receiveCancellation(request, stream context.Context) error {
	// AfterFunc is asynchronous. Check the original request directly as well as
	// the peer/batch cancellation so a just-cancelled request cannot commit while
	// its callback is still waiting to run.
	if err := request.Err(); err != nil {
		return transferError(request, err)
	}
	if err := stream.Err(); err != nil {
		return transferError(stream, err)
	}
	return nil
}

func savedAck(b *batchState, f *FileStatus) FileAck {
	return FileAck{BatchID: b.value.ID, FileID: f.ID, StoredName: f.StoredName, Size: f.Size, SHA256: f.SHA256}
}

// ErrorCode is safe for a peer response or localized interface. Raw storage and
// reader errors can contain private absolute paths or untrusted remote text and
// should only be used in appropriate local diagnostics.
func ErrorCode(err error) string {
	if err == nil {
		return ""
	}
	for _, known := range []struct {
		err  error
		code string
	}{
		{ErrInvalidManifest, "invalid_manifest"}, {ErrLimit, "limit_exceeded"},
		{ErrUnknownPeer, "unknown_peer"}, {ErrPeerChanged, "peer_changed"},
		{ErrPeerPaused, "peer_paused"}, {ErrNotFound, "not_found"},
		{ErrConflict, "destination_conflict"}, {ErrState, "invalid_state"},
		{diskspace.ErrLow, "disk_space_low"}, {diskspace.ErrUnknown, "disk_space_unknown"},
		{ErrBusy, "busy"}, {ErrIntegrity, "integrity_mismatch"},
		{ErrCancelled, "cancelled"}, {ErrClosed, "closed"}, {ErrUnsafePath, "unsafe_path"},
	} {
		if errors.Is(err, known.err) {
			return known.code
		}
	}
	return "transfer_failed"
}
