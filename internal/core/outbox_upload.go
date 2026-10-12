package core

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strconv"
	"time"

	"github.com/webkaz-labs/sobalink/internal/transfer"
	"github.com/webkaz-labs/sobalink/internal/webui"
)

// UploadOutbox stages only a process-scoped review. It never authorizes replay.
func (c *Core) UploadOutbox(w http.ResponseWriter, r *http.Request) {
	lim := c.transferLimits()
	r.Body = http.MaxBytesReader(w, r.Body, lim.MaxBatchBytes+2*transferJSONBytes(lim.MaxManifestBytes)+transferJSONBytes(lim.MaxMetadataBytes)+(256<<10))
	body, err := webui.BoundBody(w, r, 30*time.Second, true)
	if err != nil {
		reply(w, 503, map[string]string{"code": "outbox_upload_unavailable", "error": "upload deadline unavailable"})
		return
	}
	defer body.Finish(false)
	fail := func(err error) {
		code := networkErrorCode(err)
		if code == "" {
			code = "outbox_upload_failed"
		}
		reply(w, 409, map[string]string{"code": code, "error": "outbox preparation was not confirmed; inspect the original request"})
	}
	done, err := c.beginWork()
	if err != nil {
		fail(err)
		return
	}
	defer done()
	if err = body.Watch(c.ctx); err != nil {
		fail(err)
		return
	}
	reader, err := r.MultipartReader()
	if err != nil {
		fail(err)
		return
	}
	field := func(name string, max int64) (string, error) {
		part, e := reader.NextPart()
		if e != nil {
			return "", e
		}
		defer part.Close()
		if part.FormName() != name || part.FileName() != "" {
			return "", outboxError("outbox_invalid")
		}
		b, e := io.ReadAll(io.LimitReader(part, max+1))
		if e != nil || int64(len(b)) > max {
			return "", outboxError("outbox_invalid")
		}
		return string(b), nil
	}
	peerJSON, err := field("peerIds", transferJSONBytes(lim.MaxMetadataBytes))
	if err != nil {
		fail(err)
		return
	}
	var peers []string
	if json.Unmarshal([]byte(peerJSON), &peers) != nil {
		fail(outboxError("outbox_invalid"))
		return
	}
	process, err := field("processId", 128)
	if err != nil {
		fail(err)
		return
	}
	expiryRaw, err := field("expiresAt", 128)
	if err != nil {
		fail(err)
		return
	}
	var expiry time.Time
	if expiryRaw != "" {
		expiry, err = time.Parse(time.RFC3339, expiryRaw)
		if err != nil {
			fail(err)
			return
		}
	}
	id, err := field("requestId", 128)
	if err != nil {
		fail(err)
		return
	}
	selectedRaw, err := field("manifest", transferJSONBytes(lim.MaxManifestBytes))
	if err != nil {
		fail(err)
		return
	}
	var selected []struct {
		Path string        `json:"path"`
		Kind transfer.Kind `json:"kind"`
		Size int64         `json:"size"`
	}
	if json.Unmarshal([]byte(selectedRaw), &selected) != nil || len(selected) == 0 || len(selected) > lim.MaxEntries {
		fail(outboxError("outbox_invalid"))
		return
	}
	manifest := transfer.Manifest{ID: id}
	for i, item := range selected {
		entry := transfer.Entry{ID: strconv.Itoa(i + 1), Path: item.Path, Kind: item.Kind, Size: item.Size}
		if item.Kind == transfer.File {
			entry.SHA256 = "0000000000000000000000000000000000000000000000000000000000000000"
		}
		manifest.Entries = append(manifest.Entries, entry)
	}
	if err = transfer.ValidateManifest(manifest, lim); err != nil {
		fail(err)
		return
	}
	p, old, err := c.reserveOutbox(r.Context(), id, process, peers, manifest, expiry)
	if err != nil {
		fail(err)
		return
	}
	if old != nil {
		reply(w, 200, map[string]any{"ok": true, "result": old})
		return
	}
	complete := false
	defer func() {
		if !complete {
			c.abortOutboxPreparation(p)
		}
	}()
	stage, cancel := c.operationContext(r.Context(), "stagingSeconds")
	defer cancel()
	if err = body.Watch(stage); err == nil {
		err = body.Watch(p.ctx)
	}
	if err != nil {
		fail(err)
		return
	}
	o := c.outboxCopy()
	o.mu.Lock()
	payload := o.state.Payloads[p.intent.PayloadID]
	root, err := o.payloadRootLocked(payload)
	if err == nil && o.stagingRootOpened != nil {
		o.stagingRootOpened(root)
	}
	o.mu.Unlock()
	if err != nil {
		fail(err)
		return
	}
	rootOpen := true
	defer func() {
		if rootOpen {
			_ = root.Close()
		}
	}()
	for i, entry := range manifest.Entries {
		if entry.Kind != transfer.File {
			continue
		}
		part, err := reader.NextPart()
		if err != nil {
			fail(err)
			return
		}
		_, params, err := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
		if err != nil || part.FormName() != "files" || params["filename"] != entry.Path {
			fail(outboxError("outbox_invalid"))
			return
		}
		hash := sha256.New()
		entry.SHA256 = ""
		if err = c.stageOutboxFile(stage, root, payload, entry, io.TeeReader(part, hash)); err != nil {
			fail(err)
			return
		}
		manifest.Entries[i].SHA256 = hex.EncodeToString(hash.Sum(nil))
		part.Close()
	}
	if _, err = reader.NextPart(); err != io.EOF {
		fail(outboxError("outbox_invalid"))
		return
	}
	if _, err = io.Copy(io.Discard, r.Body); err != nil {
		fail(err)
		return
	}
	if err = body.Finish(true); err != nil {
		fail(err)
		return
	}
	if err = outboxSyncDirectory(root); err != nil {
		fail(err)
		return
	}
	// Readiness means the physical staging owner has joined, including this
	// directory handle. On failure, the earlier defer closes before abort cleanup.
	rootOpen = false
	if err = root.Close(); err != nil {
		fail(err)
		return
	}
	o.mu.Lock()
	err = o.withOwnedLocked(func() error {
		payload.Manifest = manifest
		payload.Ready = true
		p.intent.View.Entries = append([]transfer.Entry{}, manifest.Entries...)
		return o.finishPreparationLocked(p)
	})
	result := cloneOutboxRecord(p.intent.View)
	o.mu.Unlock()
	if err != nil {
		fail(err)
		return
	}
	complete = true
	reply(w, 200, map[string]any{"ok": true, "result": result})
}
func (c *Core) abortOutboxPreparation(p *outboxPreparation) {
	o := c.outboxCopy()
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	p.discarded = true
	p.staging = false
	p.cancel()
	p.intent.View.Status = "discarded"
	for i := range p.intent.View.Children {
		p.intent.View.Children[i].Status = "discarded"
	}
	_ = o.cleanupPayloadLocked(o.state.Payloads[p.intent.PayloadID])
}
