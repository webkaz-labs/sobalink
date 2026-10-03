package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/transfer"
)

const transferManifestMetadataBytes = 256 << 10

// A JSON character can expand to six bytes (for example, & becomes \u0026).
// The conservative metadata estimate also includes ample per-entry framing.
const transferManifestJSONBytes = 6 * transferManifestMetadataBytes

func transferLimits() transfer.Limits {
	return transfer.Limits{MaxEntries: 256, MaxManifestBytes: transferManifestMetadataBytes, MaxFileBytes: 1 << 30, MaxBatchBytes: 1 << 30, MaxReservedBytes: 4 << 30, MaxBatches: 32, MaxMetadataBytes: 1 << 20}
}

// wireBatch deliberately excludes local destination paths and trust state.
type wireBatch struct {
	ID             string                `json:"id"`
	State          transfer.BatchState   `json:"state"`
	Files          []transfer.FileStatus `json:"files"`
	TotalBytes     int64                 `json:"totalBytes"`
	CompletedBytes int64                 `json:"completedBytes"`
}

func batchWire(b transfer.Batch) wireBatch {
	return wireBatch{ID: b.ID, State: b.State, Files: b.Files, TotalBytes: b.TotalBytes, CompletedBytes: b.CompletedBytes}
}

type outgoingBatch struct {
	mu           sync.Mutex
	ID, PeerID   string
	Generation   uint64
	Manifest     transfer.Manifest
	Files        map[string]string
	Spool        string
	State, Error string
	Completed    int64
	Created      time.Time
	cancel       context.CancelFunc
	running      bool
	staging      bool
	reserved     int64 // Payload bytes retained or reserved, independent of history.
}

func (b *outgoingBatch) stop() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.cancel != nil {
		b.cancel()
	}
	if !b.running && !b.staging && b.State != "completed" {
		b.State = "cancelled"
		if err := b.releaseSpoolLocked(); err != nil {
			b.Error = "could not remove staged files; clear this transfer to retry cleanup"
		}
	}
}

// Keep the reservation until removal succeeds. Failed retryable batches retain
// their spool and reservation; terminal history with a released spool does not.
func (b *outgoingBatch) releaseSpoolLocked() error {
	if b.Spool != "" {
		if err := os.RemoveAll(b.Spool); err != nil {
			return err
		}
		b.Spool = ""
	}
	b.reserved = 0
	return nil
}

var (
	errOutgoingHistory = errors.New("transfer history is full; finish or clear existing transfers")
	errOutgoingStaging = errors.New("outgoing staging capacity reached")
)

// Both browser and CLI staging use the same atomic admission. Existing IDs are
// returned to the caller for idempotency validation without consuming capacity.
func (c *Core) reserveOutgoing(ctx context.Context, b *outgoingBatch, lim transfer.Limits) (*outgoingBatch, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c.closing || c.ctx.Err() != nil {
		return nil, errors.New("application is stopping")
	}
	if existing := c.outgoing[b.ID]; existing != nil {
		return existing, nil
	}
	if len(c.outgoing) >= lim.MaxBatches {
		return nil, errOutgoingHistory
	}
	var total, reserved int64
	for _, entry := range b.Manifest.Entries {
		total += entry.Size
	}
	for _, other := range c.outgoing {
		other.mu.Lock()
		reserved += other.reserved
		other.mu.Unlock()
	}
	if total > lim.MaxReservedBytes || reserved > lim.MaxReservedBytes-total {
		return nil, errOutgoingStaging
	}
	b.reserved = total
	b.staging = true
	c.outgoing[b.ID] = b
	return nil, nil
}

// A failed staging attempt has no retryable manifest. If cleanup itself fails,
// retain the record and capacity so those bytes cannot become unaccounted for.
func (c *Core) discardOutgoing(b *outgoingBatch) {
	b.mu.Lock()
	if b.cancel != nil {
		b.cancel()
	}
	err := b.releaseSpoolLocked()
	b.staging = false
	if err != nil {
		b.State = "cancelled"
		b.Error = "could not remove staged files; clear this transfer to retry cleanup"
	}
	b.mu.Unlock()
	if err == nil {
		c.mu.Lock()
		if c.outgoing[b.ID] == b {
			delete(c.outgoing, b.ID)
		}
		c.mu.Unlock()
	}
}

func (c *Core) Upload(w http.ResponseWriter, r *http.Request) {
	c.upload(w, r, transferLimits())
}

func (c *Core) upload(w http.ResponseWriter, r *http.Request, lim transfer.Limits) {
	done, err := c.beginWork()
	if err != nil {
		reply(w, 503, map[string]string{"error": err.Error()})
		return
	}
	defer done()
	r.Body = http.MaxBytesReader(w, r.Body, lim.MaxBatchBytes+6*lim.MaxManifestBytes+(256<<10))
	body := r.Body
	stopBody := context.AfterFunc(c.ctx, func() { _ = body.Close() })
	defer stopBody()
	stopRequestBody := context.AfterFunc(r.Context(), func() { _ = body.Close() })
	defer stopRequestBody()
	reader, e := r.MultipartReader()
	if e != nil {
		reply(w, 400, map[string]string{"error": "invalid multipart upload"})
		return
	}
	readField := func(want string, limit int64) (string, error) {
		part, e := reader.NextPart()
		if e != nil {
			return "", e
		}
		defer part.Close()
		if part.FormName() != want || part.FileName() != "" {
			return "", errors.New("upload fields are out of order")
		}
		b, e := io.ReadAll(io.LimitReader(part, limit+1))
		if e != nil || int64(len(b)) > limit {
			return "", errors.New("upload field exceeds limit")
		}
		return string(b), nil
	}
	peerID, e := readField("peerId", 128)
	if e != nil {
		reply(w, 400, map[string]string{"error": e.Error()})
		return
	}
	id, e := readField("requestId", 128)
	if e != nil || !config.ValidPeerID(id) {
		reply(w, 400, map[string]string{"error": "invalid upload request ID"})
		return
	}
	trust, ok := c.trust(peerID)
	if !ok || trust.Paused {
		reply(w, 403, map[string]string{"error": "approve this exact peer and resume reception first"})
		return
	}
	if _, e := c.currentPeer(r.Context(), peerID); e != nil {
		reply(w, 409, map[string]string{"error": e.Error()})
		return
	}
	manifestJSON, e := readField("manifest", 6*lim.MaxManifestBytes)
	if e != nil {
		reply(w, 400, map[string]string{"error": e.Error()})
		return
	}
	var selected []struct {
		Path string        `json:"path"`
		Size int64         `json:"size"`
		Kind transfer.Kind `json:"kind"`
	}
	if json.Unmarshal([]byte(manifestJSON), &selected) != nil || len(selected) == 0 || len(selected) > 256 {
		reply(w, 400, map[string]string{"error": "select 1..256 files or empty folders"})
		return
	}
	manifest := transfer.Manifest{ID: id}
	var total int64
	for i, s := range selected {
		entry := transfer.Entry{ID: strconv.Itoa(i + 1), Path: s.Path, Kind: s.Kind, Size: s.Size}
		if s.Kind == transfer.File {
			entry.SHA256 = "0000000000000000000000000000000000000000000000000000000000000000"
		}
		manifest.Entries = append(manifest.Entries, entry)
		if s.Size < 0 || s.Size > (1<<30)-total {
			reply(w, 400, map[string]string{"error": "batch exceeds 1 GiB"})
			return
		}
		total += s.Size
	}
	if e := transfer.ValidateManifest(manifest, lim); e != nil {
		reason := "invalid or conflicting file paths"
		if errors.Is(e, transfer.ErrMetadataLimit) {
			reason = "transfer manifest metadata budget exceeded"
		} else if errors.Is(e, transfer.ErrLimit) {
			reason = "transfer entry, depth or size limit exceeded"
		}
		reply(w, 400, map[string]string{"error": reason})
		return
	}
	// Reserve staging capacity before reading payload bytes. The reservation is
	// represented by the entry even while HTTP staging is still in progress.
	life, cancel := context.WithCancel(c.ctx)
	b := &outgoingBatch{ID: id, PeerID: peerID, Generation: trust.Generation, Manifest: manifest, Files: map[string]string{}, State: "queued", Created: time.Now().UTC(), cancel: cancel}
	existing, e := c.reserveOutgoing(r.Context(), b, lim)
	if e != nil {
		cancel()
		reply(w, 409, map[string]string{"error": e.Error()})
		return
	}
	if existing != nil {
		cancel()
		existing.mu.Lock()
		same := existing.PeerID == peerID && existing.Generation == trust.Generation && len(existing.Manifest.Entries) == len(manifest.Entries)
		for i, entry := range manifest.Entries {
			if !same {
				break
			}
			old := existing.Manifest.Entries[i]
			same = old.Path == entry.Path && old.Size == entry.Size && old.Kind == entry.Kind
		}
		state := existing.State
		existing.mu.Unlock()
		if !same {
			reply(w, 409, map[string]string{"error": "upload request ID belongs to a different selection"})
			return
		}
		// A retry acknowledges the original staged operation. It never reads new
		// bytes under an already committed idempotency key or silently resends.
		reply(w, 200, map[string]any{"ok": true, "result": map[string]string{"id": id, "status": state}})
		return
	}
	ctx, finishStaging := context.WithCancel(r.Context())
	defer finishStaging()
	stopLifetime := context.AfterFunc(life, finishStaging)
	defer stopLifetime()
	stopCancelledBody := context.AfterFunc(ctx, func() { _ = body.Close() })
	defer stopCancelledBody()
	stagedOK := false
	defer func() {
		if !stagedOK {
			c.discardOutgoing(b)
		}
	}()
	spoolRoot := filepath.Join(c.dir, "outgoing")
	if e = config.SecureDir(spoolRoot); e != nil {
		reply(w, 507, map[string]string{"error": "private staging directory unavailable"})
		return
	}
	spool, e := os.MkdirTemp(spoolRoot, "batch-")
	b.mu.Lock()
	b.Spool = spool
	b.mu.Unlock()
	if e != nil {
		reply(w, 507, map[string]string{"error": "could not stage this batch"})
		return
	}
	if e = config.Protect(b.Spool, true); e != nil {
		reply(w, 507, map[string]string{"error": "private staging permissions unavailable"})
		return
	}
	for i, entry := range manifest.Entries {
		if ctx.Err() != nil || r.Context().Err() != nil {
			reply(w, 400, map[string]string{"error": "upload was interrupted"})
			return
		}
		if entry.Kind != transfer.File {
			continue
		}
		part, e := reader.NextPart()
		if e != nil {
			reply(w, 400, map[string]string{"error": "upload is missing a file"})
			return
		}
		_, params, e := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
		if e != nil || part.FormName() != "files" || params["filename"] != entry.Path {
			part.Close()
			reason := "file order or path changed during upload"
			if e != nil {
				reason = "invalid multipart file disposition"
			} else if part.FormName() != "files" {
				reason = "unexpected multipart field"
			} else {
				reason = "multipart filename does not match the selected path"
			}
			reply(w, 400, map[string]string{"error": reason})
			return
		}
		filePath := filepath.Join(b.Spool, entry.ID)
		f, e := os.OpenFile(filePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			part.Close()
			reply(w, 507, map[string]string{"error": "could not stage file"})
			return
		}
		hash := sha256.New()
		n, copyErr := copyStaging(ctx, io.MultiWriter(f, hash), part, entry.Size)
		closeErr := f.Close()
		_ = part.Close()
		if copyErr != nil || closeErr != nil || n != entry.Size || r.Context().Err() != nil || ctx.Err() != nil {
			reply(w, 400, map[string]string{"error": "upload was interrupted or its size changed"})
			return
		}
		b.mu.Lock()
		b.Manifest.Entries[i].SHA256 = hex.EncodeToString(hash.Sum(nil))
		b.mu.Unlock()
		b.Files[entry.ID] = filePath
	}
	if part, e := reader.NextPart(); e != io.EOF {
		if part != nil {
			part.Close()
		}
		reply(w, 400, map[string]string{"error": "unexpected extra upload data"})
		return
	}
	if e := transfer.ValidateManifest(b.Manifest, lim); e != nil {
		reply(w, 400, map[string]string{"error": "invalid transfer manifest"})
		return
	}
	if ctx.Err() != nil || r.Context().Err() != nil {
		reply(w, 400, map[string]string{"error": "upload was interrupted"})
		return
	}
	if err := c.runStagedOutgoing(life, ctx, b); err != nil {
		reply(w, 503, map[string]string{"error": err.Error()})
		return
	}
	stagedOK = true
	reply(w, 200, map[string]any{"ok": true, "result": map[string]string{"id": id}})
}

func (c *Core) runOutgoing(ctx context.Context, b *outgoingBatch) error {
	return c.runStagedOutgoing(ctx, ctx, b)
}

func (c *Core) runStagedOutgoing(ctx, staging context.Context, b *outgoingBatch) error {
	done, err := c.beginWork()
	if err != nil {
		return err
	}
	b.mu.Lock()
	if err := staging.Err(); err != nil {
		b.mu.Unlock()
		done()
		return err
	}
	if err := ctx.Err(); err != nil {
		b.mu.Unlock()
		done()
		return err
	}
	if b.running {
		b.mu.Unlock()
		done()
		return nil
	}
	b.running = true
	b.staging = false
	b.State = "awaiting-acceptance"
	b.Error = ""
	b.mu.Unlock()
	go func() {
		defer done()
		err := c.deliver(ctx, b)
		b.mu.Lock()
		if err != nil {
			if ctx.Err() != nil {
				b.State = "cancelled"
				b.Error = "Transfer cancelled. Already saved files remain on the receiver."
			} else {
				b.State = "failed"
				b.Error = err.Error()
			}
		}
		finished := b.State == "completed" || b.State == "cancelled"
		if finished {
			if cleanupErr := b.releaseSpoolLocked(); cleanupErr != nil {
				b.Error = "could not remove staged files; clear this transfer to retry cleanup"
			}
		}
		b.running = false
		b.mu.Unlock()
	}()
	return nil
}
func (c *Core) deliver(ctx context.Context, b *outgoingBatch) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	peer, ok := c.trust(b.PeerID)
	if !ok || peer.Generation != b.Generation || peer.Paused {
		return errors.New("peer permission changed")
	}
	var remote wireBatch
	call, cancel := context.WithTimeout(ctx, 10*time.Second)
	e := c.peerJSON(call, b.PeerID, "POST", "/v1/offers", b.Manifest, &remote)
	cancel()
	if e != nil {
		return e
	}
	deadline := time.Now().Add(10 * time.Minute)
	for remote.State == transfer.Pending {
		if !time.Now().Before(deadline) {
			return errors.New("receiver did not accept within ten minutes; retry when ready")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
		call, cancel = context.WithTimeout(ctx, 8*time.Second)
		e = c.peerJSON(call, b.PeerID, "GET", "/v1/batches/"+b.ID, nil, &remote)
		cancel()
		if e != nil {
			return e
		}
	}
	if remote.ID != b.ID {
		return errors.New("peer returned a different transfer ID")
	}
	if remote.State == transfer.Cancelled || remote.State == transfer.Rejected {
		b.mu.Lock()
		b.State = "declined"
		b.mu.Unlock()
		return errors.New("receiver declined or cancelled this batch")
	}
	b.mu.Lock()
	b.State = "transferring"
	b.Completed = remote.CompletedBytes
	b.mu.Unlock()
	for _, entry := range b.Manifest.Entries {
		if entry.Kind != transfer.File {
			continue
		}
		saved := false
		failed := false
		for _, file := range remote.Files {
			if file.ID == entry.ID {
				saved = file.State == transfer.FileSaved
				failed = file.State == transfer.FileFailed
			}
		}
		if saved {
			continue
		}
		if failed {
			call, cancel = context.WithTimeout(ctx, 8*time.Second)
			e = c.peerJSON(call, b.PeerID, "POST", "/v1/batches/"+b.ID+"/retry/"+entry.ID, nil, &remote)
			cancel()
			if e != nil {
				return e
			}
		}
		peer, ok := c.trust(b.PeerID)
		if !ok || peer.Generation != b.Generation || peer.Paused {
			return errors.New("peer permission changed")
		}
		f, e := os.Open(b.Files[entry.ID])
		if e != nil {
			return errors.New("staged file is unavailable; select it again")
		}
		var ack transfer.FileAck
		call, cancel = context.WithTimeout(ctx, 10*time.Minute)
		e = c.peerRequest(call, b.PeerID, "PUT", "/v1/batches/"+b.ID+"/files/"+entry.ID, f, "application/octet-stream", &ack)
		cancel()
		f.Close()
		if e != nil {
			return e
		}
		if ack.BatchID != b.ID || ack.FileID != entry.ID || ack.Size != entry.Size || ack.SHA256 != entry.SHA256 {
			return errors.New("receiver save confirmation did not match the file")
		}
		b.mu.Lock()
		b.Completed += entry.Size
		b.mu.Unlock()
	}
	b.mu.Lock()
	b.State = "saving"
	b.mu.Unlock()
	call, cancel = context.WithTimeout(ctx, 8*time.Second)
	e = c.peerJSON(call, b.PeerID, "GET", "/v1/batches/"+b.ID, nil, &remote)
	cancel()
	if e != nil {
		return e
	}
	if remote.ID != b.ID || remote.State != transfer.Completed {
		return errors.New("receiver has not confirmed the complete batch")
	}
	b.mu.Lock()
	b.State = "completed"
	b.Completed = remote.CompletedBytes
	b.mu.Unlock()
	return nil
}

func (c *Core) transferCommand(ctx context.Context, name string, raw json.RawMessage) (any, error) {
	var v struct {
		TransferID  string `json:"transferId"`
		Destination string `json:"destination"`
	}
	if e := decodePayload(raw, &v); e != nil {
		return nil, e
	}
	c.mu.RLock()
	out := c.outgoing[v.TransferID]
	c.mu.RUnlock()
	if out != nil {
		switch name {
		case "transfer.cancel":
			out.stop()
			call, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			_ = c.peerJSON(call, out.PeerID, "POST", "/v1/batches/"+out.ID+"/cancel", nil, nil)
			return nil, nil
		case "transfer.retry":
			out.mu.Lock()
			if out.running || out.State != "failed" {
				out.mu.Unlock()
				return nil, errors.New("only a failed inactive transfer can be retried")
			}
			ctx, cancel := context.WithCancel(c.ctx)
			out.cancel = cancel
			out.mu.Unlock()
			return nil, c.runOutgoing(ctx, out)
		case "transfer.forget":
			c.mu.Lock()
			defer c.mu.Unlock()
			out.mu.Lock()
			defer out.mu.Unlock()
			if out.running || out.staging || !(out.State == "completed" || out.State == "cancelled" || out.State == "failed" || out.State == "declined") {
				return nil, errors.New("cancel or finish the transfer before clearing it")
			}
			if e := out.releaseSpoolLocked(); e != nil {
				return nil, e
			}
			if c.outgoing[out.ID] == out {
				delete(c.outgoing, out.ID)
			}
			return nil, nil
		default:
			return nil, errors.New("this operation is only for received transfers")
		}
	}
	switch name {
	case "transfer.accept":
		if v.Destination == "" {
			v.Destination = c.profileCopy().Settings.ReceiveDirectory
		}
		if v.Destination == "" {
			return nil, errors.New("choose a receive directory before accepting this batch")
		}
		return c.transfers.Accept(v.TransferID, v.Destination)
	case "transfer.decline":
		return c.transfers.Reject(v.TransferID)
	case "transfer.cancel":
		return c.transfers.Cancel(v.TransferID)
	case "transfer.forget":
		return nil, c.transfers.Forget(v.TransferID)
	case "transfer.retry":
		batch, e := c.transfers.Get(v.TransferID)
		if e != nil {
			return nil, e
		}
		for _, file := range batch.Files {
			if file.State == transfer.FileFailed {
				if _, e = c.transfers.RetryFile(batch.ID, file.ID); e != nil {
					return nil, e
				}
			}
		}
		return c.transfers.Get(v.TransferID)
	}
	return nil, errors.New("unsupported transfer action")
}

func (c *Core) transferViews() []map[string]any {
	out := []map[string]any{}
	for _, b := range c.transfers.List() {
		status := map[transfer.BatchState]string{transfer.Pending: "awaiting-acceptance", transfer.Accepted: "queued", transfer.Receiving: "transferring", transfer.Partial: "failed", transfer.Completed: "completed", transfer.Cancelled: "cancelled", transfer.Rejected: "declined"}[b.State]
		entries := []map[string]any{}
		for _, f := range b.Files {
			entries = append(entries, map[string]any{"id": f.ID, "path": f.Path, "size": f.Size, "kind": f.Kind, "status": f.State, "error": f.Error, "storedPath": f.StoredName})
		}
		out = append(out, map[string]any{"id": b.ID, "peerId": b.Peer.ID, "direction": "incoming", "name": "Batch " + b.ID[:min(8, len(b.ID))], "entries": entries, "totalBytes": b.TotalBytes, "completedBytes": b.CompletedBytes, "status": status, "createdAt": b.CreatedAt})
	}
	c.mu.RLock()
	batches := make([]*outgoingBatch, 0, len(c.outgoing))
	for _, b := range c.outgoing {
		batches = append(batches, b)
	}
	c.mu.RUnlock()
	for _, b := range batches {
		b.mu.Lock()
		entries := []map[string]any{}
		var total int64
		for _, entry := range b.Manifest.Entries {
			total += entry.Size
			entries = append(entries, map[string]any{"id": entry.ID, "path": entry.Path, "size": entry.Size, "kind": entry.Kind})
		}
		out = append(out, map[string]any{"id": b.ID, "peerId": b.PeerID, "direction": "outgoing", "name": "Batch " + b.ID[:min(8, len(b.ID))], "entries": entries, "totalBytes": total, "completedBytes": b.Completed, "status": b.State, "createdAt": b.Created, "error": b.Error})
		b.mu.Unlock()
	}
	sort.Slice(out, func(i, j int) bool { return out[i]["createdAt"].(time.Time).Before(out[j]["createdAt"].(time.Time)) })
	return out
}

// File selection from the agent CLI uses the same staging and manifest protocol.
func (c *Core) SendPaths(ctx context.Context, peerID string, paths []string) (any, error) {
	return c.sendPaths(ctx, peerID, paths, transferLimits())
}

func (c *Core) sendPaths(ctx context.Context, peerID string, paths []string, lim transfer.Limits) (any, error) {
	done, err := c.beginWork()
	if err != nil {
		return nil, err
	}
	defer done()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	t, ok := c.trust(peerID)
	if !ok || t.Paused {
		return nil, errors.New("approve this exact peer first")
	}
	life, cancel := context.WithCancel(c.ctx)
	stageCtx, finishStaging := context.WithCancel(ctx)
	defer finishStaging()
	stopLifetime := context.AfterFunc(life, finishStaging)
	defer stopLifetime()
	plan, e := transfer.PlanSources(stageCtx, randomID(), paths, lim)
	if e != nil {
		cancel()
		return nil, e
	}
	planned := plan.Manifest()
	b := &outgoingBatch{ID: planned.ID, PeerID: peerID, Generation: t.Generation, Manifest: planned, Files: map[string]string{}, Created: time.Now().UTC(), State: "queued", cancel: cancel}
	if existing, e := c.reserveOutgoing(stageCtx, b, lim); e != nil || existing != nil {
		cancel()
		if e != nil {
			return nil, e
		}
		return nil, errors.New("transfer request ID already exists")
	}
	stagedOK := false
	defer func() {
		if !stagedOK {
			c.discardOutgoing(b)
		}
	}()
	manifest, sources, e := plan.Hash(stageCtx)
	if e != nil {
		return nil, e
	}
	b.mu.Lock()
	b.Manifest = manifest
	b.mu.Unlock()
	spoolRoot := filepath.Join(c.dir, "outgoing")
	if e := config.SecureDir(spoolRoot); e != nil {
		return nil, e
	}
	spool, e := os.MkdirTemp(spoolRoot, "batch-")
	if e != nil {
		return nil, e
	}
	b.mu.Lock()
	b.Spool = spool
	b.mu.Unlock()
	if e := config.Protect(spool, true); e != nil {
		return nil, e
	}
	files := map[string]string{}
	for _, source := range sources {
		if err := stageCtx.Err(); err != nil {
			return nil, err
		}
		from, e := source.Open()
		if e != nil {
			return nil, e
		}
		target := filepath.Join(spool, source.EntryID)
		to, e := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			from.Close()
			return nil, e
		}
		var entry transfer.Entry
		for _, item := range manifest.Entries {
			if item.ID == source.EntryID {
				entry = item
				break
			}
		}
		hash := sha256.New()
		n, e := copyStaging(stageCtx, io.MultiWriter(to, hash), from, entry.Size)
		from.Close()
		closeErr := to.Close()
		if err := stageCtx.Err(); err != nil {
			return nil, err
		}
		if e != nil || closeErr != nil || n != entry.Size || hex.EncodeToString(hash.Sum(nil)) != entry.SHA256 {
			return nil, errors.New("selected file changed while staging")
		}
		files[source.EntryID] = target
	}
	b.mu.Lock()
	b.Files = files
	b.mu.Unlock()
	if err := stageCtx.Err(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if e := c.runStagedOutgoing(life, stageCtx, b); e != nil {
		return nil, e
	}
	stagedOK = true
	return map[string]string{"id": b.ID}, nil
}

type stagingContextReader struct {
	ctx context.Context
	r   io.Reader
}

// Probe excess input without writing beyond the admitted disk reservation.
func copyStaging(ctx context.Context, dst io.Writer, src io.Reader, size int64) (int64, error) {
	r := stagingContextReader{ctx, src}
	n, err := io.CopyBuffer(dst, io.LimitReader(r, size), make([]byte, 32<<10))
	if err != nil {
		return n, err
	}
	if n != size {
		return n, transfer.ErrIntegrity
	}
	var extra [1]byte
	if count, err := io.ReadFull(r, extra[:]); count != 0 || err != io.EOF {
		if err != nil {
			return n, err
		}
		return n, transfer.ErrIntegrity
	}
	return n, nil
}

func (r stagingContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.r.Read(p)
	if cancelled := r.ctx.Err(); cancelled != nil {
		return n, cancelled
	}
	return n, err
}
