package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/transfer"
)

func outgoingForCapacity(id string, size int64) *outgoingBatch {
	return &outgoingBatch{ID: id, State: "queued", Manifest: transfer.Manifest{ID: id, Entries: []transfer.Entry{{ID: "1", Path: "note.txt", Kind: transfer.File, Size: size}}}}
}

func TestOutgoingCapacityCountsRetainedSpoolsInsteadOfHistory(t *testing.T) {
	c := &Core{ctx: context.Background(), outgoing: map[string]*outgoingBatch{}}
	lim := transferLimits()
	lim.MaxReservedBytes = 4
	b := outgoingForCapacity("first", 4)
	if _, err := c.reserveOutgoing(context.Background(), b, lim); err != nil {
		t.Fatal(err)
	}
	b.Spool = t.TempDir()
	if err := os.WriteFile(filepath.Join(b.Spool, "1"), []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	b.staging = false
	b.State = "failed"
	spool := b.Spool
	if _, err := c.reserveOutgoing(context.Background(), outgoingForCapacity("blocked", 1), lim); !errors.Is(err, errOutgoingStaging) {
		t.Fatalf("failed retryable spool did not consume capacity: %v", err)
	}
	b.stop()
	b.stop()
	if b.reserved != 0 || b.State != "cancelled" {
		t.Fatalf("cancellation did not release reservation exactly once: %+v", b)
	}
	if _, err := os.Stat(spool); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled spool remains: %v", err)
	}
	if _, err := c.reserveOutgoing(context.Background(), outgoingForCapacity("second", 4), lim); err != nil {
		t.Fatalf("released history consumed staging capacity: %v", err)
	}
	if len(c.outgoing) != 2 || len(b.Manifest.Entries) != 1 {
		t.Fatal("releasing capacity lost transfer history")
	}
}

func TestConcurrentOutgoingAdmissionCannotOversubscribe(t *testing.T) {
	c := &Core{ctx: context.Background(), outgoing: map[string]*outgoingBatch{}}
	lim := transferLimits()
	lim.MaxReservedBytes = 4
	var wg sync.WaitGroup
	results := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := c.reserveOutgoing(context.Background(), outgoingForCapacity(fmt.Sprintf("batch-%d", i), 1), lim)
			results <- err
		}(i)
	}
	wg.Wait()
	close(results)
	admitted := 0
	for err := range results {
		if err == nil {
			admitted++
		} else if !errors.Is(err, errOutgoingStaging) {
			t.Fatal(err)
		}
	}
	if admitted != 4 || len(c.outgoing) != 4 {
		t.Fatalf("admitted %d requests into a four-byte budget", admitted)
	}
}

type cancelStagingReader struct {
	cancel context.CancelFunc
	reads  int
}

func (r *cancelStagingReader) Read(p []byte) (int, error) {
	r.reads++
	r.cancel()
	return copy(p, "data"), nil
}

func TestStagingCopyStopsAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	source := &cancelStagingReader{cancel: cancel}
	var copied bytes.Buffer
	_, err := copyStaging(ctx, &copied, source, 4)
	if !errors.Is(err, context.Canceled) || source.reads != 1 {
		t.Fatalf("cancelled staging continued reading: %d reads, %v", source.reads, err)
	}
}

func TestStagingCopyDoesNotWriteBeyondReservation(t *testing.T) {
	var copied bytes.Buffer
	n, err := copyStaging(context.Background(), &copied, strings.NewReader("extra"), 4)
	if !errors.Is(err, transfer.ErrIntegrity) || n != 4 || copied.String() != "extr" {
		t.Fatalf("copy exceeded reservation or accepted excess input: %d, %q, %v", n, copied.String(), err)
	}
}

func multipartTransfer(t *testing.T, id string, entries []transfer.Entry, data string, blocked bool) (*http.Request, *uploadBarrierReader) {
	t.Helper()
	selected := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		selected = append(selected, map[string]any{"path": entry.Path, "size": entry.Size, "kind": entry.Kind})
	}
	manifest, err := json.Marshal(selected)
	if err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	for _, field := range []struct{ name, value string }{{"peerId", "peer-b"}, {"requestId", id}, {"manifest", string(manifest)}} {
		if err := w.WriteField(field.name, field.value); err != nil {
			t.Fatal(err)
		}
	}
	cut := 0
	for _, entry := range entries {
		if entry.Kind != transfer.File {
			continue
		}
		file, err := w.CreateFormFile("files", entry.Path)
		if err != nil {
			t.Fatal(err)
		}
		cut = body.Len() + 1
		if _, err := io.WriteString(file, data); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	barrier := &uploadBarrierReader{body: bytes.NewReader(body.Bytes()), before: cut, ready: make(chan struct{}), release: make(chan struct{}), closed: make(chan struct{})}
	if !blocked {
		close(barrier.release)
	}
	r := httptest.NewRequest("POST", "http://127.0.0.1/api/upload", barrier)
	r.Header.Set("Content-Type", w.FormDataContentType())
	return r, barrier
}

func waitOutgoingIdle(t *testing.T, c *Core, id string) *outgoingBatch {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		c.mu.RLock()
		b := c.outgoing[id]
		c.mu.RUnlock()
		if b == nil {
			t.Fatalf("missing outgoing transfer %s", id)
		}
		b.mu.Lock()
		running, staging := b.running, b.staging
		b.mu.Unlock()
		if !running && !staging {
			return b
		}
		if time.Now().After(deadline) {
			t.Fatal("outgoing transfer did not finish")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestBrowserAndCLIShareStagingQuotaAndCancellation(t *testing.T) {
	for _, cancelRequest := range []bool{false, true} {
		t.Run(fmt.Sprintf("request=%t", cancelRequest), func(t *testing.T) {
			p := newCorePair(t)
			trustPair(t, p)
			lim := transferLimits()
			lim.MaxReservedBytes = 4
			entry := transfer.Entry{ID: "1", Path: "note.txt", Kind: transfer.File, Size: 4}
			r, barrier := multipartTransfer(t, "staging", []transfer.Entry{entry}, "data", true)
			defer barrier.Close()
			requestCtx, cancel := context.WithCancel(r.Context())
			defer cancel()
			r = r.WithContext(requestCtx)
			response := httptest.NewRecorder()
			done := make(chan struct{})
			go func() { p.a.upload(response, r, lim); close(done) }()
			select {
			case <-barrier.ready:
			case <-done:
				t.Fatalf("upload did not stage: %d %s", response.Code, response.Body.String())
			case <-time.After(3 * time.Second):
				t.Fatal("upload did not reach payload barrier")
			}
			source := filepath.Join(t.TempDir(), "cli.txt")
			if err := os.WriteFile(source, []byte("x"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := p.a.sendPaths(context.Background(), "peer-b", []string{source}, lim); !errors.Is(err, errOutgoingStaging) {
				t.Fatalf("CLI bypassed browser reservation: %v", err)
			}
			p.a.mu.RLock()
			b := p.a.outgoing["staging"]
			p.a.mu.RUnlock()
			if b == nil {
				t.Fatal("upload read payload without a reservation")
			}
			b.mu.Lock()
			spool := b.Spool
			b.mu.Unlock()
			if cancelRequest {
				cancel()
			} else {
				b.stop()
			}
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("cancelled staging kept reading its blocked body")
			}
			if response.Code < 400 || len(p.b.transfers.List()) != 0 {
				t.Fatalf("cancelled upload was accepted or offered: %d", response.Code)
			}
			p.a.mu.RLock()
			remaining := len(p.a.outgoing)
			p.a.mu.RUnlock()
			if remaining != 0 || b.reserved != 0 {
				t.Fatal("cancelled staging retained its reservation")
			}
			if _, err := os.Stat(spool); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("cancelled staging retained files: %v", err)
			}
		})
	}
}

func TestCLIHistoryCapacityBeforeStagingAndCancelledAdmission(t *testing.T) {
	p := newCorePair(t)
	trustPair(t, p)
	lim := transferLimits()
	lim.MaxBatches = 1
	old := outgoingForCapacity("history", 4)
	old.State = "completed"
	p.a.mu.Lock()
	p.a.outgoing[old.ID] = old
	p.a.mu.Unlock()
	source := filepath.Join(t.TempDir(), "note.txt")
	if err := os.WriteFile(source, []byte("payload"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := p.a.sendPaths(context.Background(), "peer-b", []string{source}, lim); !errors.Is(err, errOutgoingHistory) {
		t.Fatalf("history capacity error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(p.a.dir, "outgoing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("full history allowed staging directory creation: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.a.sendPaths(ctx, "peer-b", []string{source}, lim); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled CLI request = %v", err)
	}
	b := outgoingForCapacity("cancelled", 1)
	if err := p.a.runStagedOutgoing(context.Background(), ctx, b); !errors.Is(err, context.Canceled) || b.running {
		t.Fatalf("cancelled staging launched a transfer: %v", err)
	}
}

func TestBrowserCountsFailedCLIStagingAndReusesCompletedCapacity(t *testing.T) {
	p := newCorePair(t)
	trustPair(t, p)
	lim := transferLimits()
	lim.MaxReservedBytes = 4
	source := filepath.Join(t.TempDir(), "note.txt")
	if err := os.WriteFile(source, []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	// A paused receiver makes delivery fail after staging.
	if err := p.b.transfers.PausePeer("peer-a", true); err != nil {
		t.Fatal(err)
	}
	value, err := p.a.sendPaths(context.Background(), "peer-b", []string{source}, lim)
	if err != nil {
		t.Fatal(err)
	}
	b := waitOutgoingIdle(t, p.a, value.(map[string]string)["id"])
	if b.State != "failed" || b.reserved != 4 {
		t.Fatalf("retryable CLI spool = %s, %d", b.State, b.reserved)
	}
	entry := transfer.Entry{ID: "1", Path: "next.txt", Kind: transfer.File, Size: 1}
	r, body := multipartTransfer(t, "blocked-browser", []transfer.Entry{entry}, "x", false)
	defer body.Close()
	response := httptest.NewRecorder()
	p.a.upload(response, r, lim)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "staging capacity") {
		t.Fatalf("browser ignored failed CLI staging: %d %s", response.Code, response.Body.String())
	}
	b.stop()
	if err := p.b.transfers.PausePeer("peer-a", false); err != nil {
		t.Fatal(err)
	}
	mustCommand(t, p.b, "peer.autosave", map[string]any{"peerId": "peer-a", "enabled": true, "directory": t.TempDir()})
	value, err = p.a.sendPaths(context.Background(), "peer-b", []string{source}, lim)
	if err != nil {
		t.Fatalf("cancelled history consumed capacity: %v", err)
	}
	completed := waitOutgoingIdle(t, p.a, value.(map[string]string)["id"])
	if completed.State != "completed" || completed.reserved != 0 {
		t.Fatalf("completion retained staging capacity: %s %d", completed.State, completed.reserved)
	}
	r, body = multipartTransfer(t, "after-completion", []transfer.Entry{entry}, "x", false)
	defer body.Close()
	response = httptest.NewRecorder()
	p.a.upload(response, r, lim)
	if response.Code != http.StatusOK {
		t.Fatalf("completed history consumed browser capacity: %d %s", response.Code, response.Body.String())
	}
}

func TestTransferManifestLimitsFit256EscapedLeafNames(t *testing.T) {
	manifest := transfer.Manifest{ID: strings.Repeat("b", 128)}
	for i := 0; i < 256; i++ {
		manifest.Entries = append(manifest.Entries, transfer.Entry{ID: fmt.Sprintf("file-%d", i), Path: fmt.Sprintf("%03d", i) + strings.Repeat("&", 252), Kind: transfer.File, Size: 1, SHA256: strings.Repeat("0", 64)})
	}
	if err := transfer.ValidateManifest(manifest, transferLimits()); err != nil {
		t.Fatalf("advertised ordinary 256-entry selection rejected: %v", err)
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) <= 256<<10 || len(encoded) > transferManifestJSONBytes {
		t.Fatalf("escaped JSON envelope does not cover valid metadata: %d", len(encoded))
	}
	deep := transfer.Manifest{ID: "deep-paths", Entries: append([]transfer.Entry(nil), manifest.Entries[:128]...)}
	for i := range deep.Entries {
		deep.Entries[i].Path = fmt.Sprintf("%03d/", i) + strings.Repeat(strings.Repeat("&", 255)+"/", 8) + "note"
	}
	if err := transfer.ValidateManifest(deep, transferLimits()); !errors.Is(err, transfer.ErrMetadataLimit) {
		t.Fatalf("long nested paths escaped the separate metadata budget: %v", err)
	}
	p := newCorePair(t)
	trustPair(t, p)
	// The exact batch-byte ceiling still leaves room for the escaped manifest
	// and multipart headers; no large payload is needed to exercise this bound.
	lim := transferLimits()
	lim.MaxBatchBytes = 256
	lim.MaxReservedBytes = 256
	r, body := multipartTransfer(t, manifest.ID, manifest.Entries, "x", false)
	defer body.Close()
	response := httptest.NewRecorder()
	p.a.upload(response, r, lim)
	if response.Code != http.StatusOK {
		t.Fatalf("browser rejected valid escaped manifest/framing: %d %s", response.Code, response.Body.String())
	}
	manifest.ID = "peer-encoded-manifest"
	assertPeerStatus(t, p.na, p.nb, "POST", "/v1/offers", manifest, http.StatusOK)
}

func TestUploadMetadataBudgetErrorDoesNotBlamePaths(t *testing.T) {
	p := newCorePair(t)
	trustPair(t, p)
	lim := transferLimits()
	lim.MaxManifestBytes = 1000
	entry := transfer.Entry{ID: "1", Path: "note.txt", Kind: transfer.File, Size: 1}
	r, body := multipartTransfer(t, "metadata-limit", []transfer.Entry{entry}, "x", false)
	defer body.Close()
	response := httptest.NewRecorder()
	p.a.upload(response, r, lim)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "metadata budget") || strings.Contains(response.Body.String(), "file paths") {
		t.Fatalf("wrong metadata limit error: %d %s", response.Code, response.Body.String())
	}
}
