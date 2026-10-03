package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/transfer"
)

func TestTransferPolicyRaisesDefaultsWithinFiniteBudgets(t *testing.T) {
	p := capacity.Defaults()
	p.Logical["batchEntries"] = capacity.Unlimited()
	p.Logical["fileBytes"] = capacity.Unlimited()
	p.Logical["batchBytes"] = capacity.Unlimited()
	p.Logical["transferHistoryEntries"] = capacity.Unlimited()
	p.Resources["transferManifestBytes"] = capacity.Limited(2 << 20)
	p.Resources["transferMetadataBytes"] = capacity.Limited(4 << 20)
	p.Resources["transferSpoolBytes"] = capacity.Limited(12 << 30)
	limits := transferLimitsFor(p, "transferSpoolBytes")
	manifest := transfer.Manifest{ID: "selection"}
	for i := range 1100 {
		manifest.Entries = append(manifest.Entries, transfer.Entry{ID: fmt.Sprint(i), Path: fmt.Sprintf("folder-%d", i), Kind: transfer.Directory})
	}
	if err := transfer.ValidateManifest(manifest, limits); err != nil {
		t.Fatalf("explicit metadata and count budgets rejected ordinary selection: %v", err)
	}
	manifest.Entries = []transfer.Entry{{ID: "file", Path: "large", Kind: transfer.File, Size: 9 << 30, SHA256: strings.Repeat("a", 64)}}
	if err := transfer.ValidateManifest(manifest, limits); err != nil {
		t.Fatalf("selected finite payload budget remained clamped: %v", err)
	}
	manifest.Entries[0].Size = 13 << 30
	if err := transfer.ValidateManifest(manifest, limits); !errors.Is(err, transfer.ErrLimit) {
		t.Fatalf("unlimited logical choice escaped finite spool budget: %v", err)
	}
}

func TestTransferPathPolicyRemovesConvenienceCeilingsWithinMetadataBudget(t *testing.T) {
	p := capacity.Defaults()
	defaults := transferLimitsFor(p, "transferSpoolBytes")
	if defaults.MaxDepth != 16 || defaults.MaxPathBytes != 4096 {
		t.Fatalf("path defaults changed: %+v", defaults)
	}
	manifest := transfer.Manifest{ID: "long-path", Entries: []transfer.Entry{{ID: "folder", Path: strings.Repeat(strings.Repeat("x", 240)+"/", 20) + "leaf", Kind: transfer.Directory}}}
	for _, choice := range []capacity.Choice{capacity.Limited(8192), capacity.Unlimited()} {
		p.Logical["pathDepth"], p.Logical["pathBytes"] = choice, choice
		if err := validateSupportedCapacity(p); err != nil {
			t.Fatal(err)
		}
		for _, reserved := range []string{"transferSpoolBytes", "receiveReservedBytes"} {
			limits := transferLimitsFor(p, reserved)
			if err := transfer.ValidateManifest(manifest, limits); err != nil {
				t.Fatalf("%s retained default path ceiling: %v", reserved, err)
			}
			if int64(limits.MaxPathBytes) > limits.MaxManifestBytes || limits.MaxDepth > (limits.MaxPathBytes/2+limits.MaxPathBytes%2) {
				t.Fatalf("path choices escaped finite metadata bound: %+v", limits)
			}
		}
	}
	p.Resources["transferManifestBytes"] = capacity.Limited(transfer.ManifestMetadataBytes(manifest) - 1)
	if err := transfer.ValidateManifest(manifest, transferLimitsFor(p, "transferSpoolBytes")); !errors.Is(err, transfer.ErrMetadataLimit) {
		t.Fatalf("unlimited path escaped metadata admission: %v", err)
	}
	p.Resources["transferManifestBytes"] = capacity.Default()
	p.Logical["pathDepth"] = capacity.Limited(1)
	if err := transfer.ValidateManifest(manifest, transferLimitsFor(p, "transferSpoolBytes")); !errors.Is(err, transfer.ErrLimit) {
		t.Fatalf("lower depth did not reject new admission: %v", err)
	}
	p.Logical["pathDepth"] = capacity.Unlimited()
	p.Logical["pathBytes"] = capacity.Limited(4096)
	if err := transfer.ValidateManifest(manifest, transferLimitsFor(p, "transferSpoolBytes")); err == nil {
		t.Fatal("lower path bytes did not reject new admission")
	}
}

func TestPathPolicyApplyPreservesEscapedRetainedOffers(t *testing.T) {
	pair := newCorePair(t)
	trustPair(t, pair)
	policy := capacity.Defaults()
	policy.Logical["pathDepth"] = capacity.Unlimited()
	policy.Logical["pathBytes"] = capacity.Unlimited()
	manifest := transfer.Manifest{ID: "long-escaped", Entries: []transfer.Entry{{ID: "folder", Path: strings.Repeat(strings.Repeat("&", 240)+"/", 20) + "leaf", Kind: transfer.Directory}}}
	policy.Resources["transferManifestBytes"] = capacity.Limited(transfer.ManifestMetadataBytes(manifest))
	apply := func() {
		preview := mustCommand(t, pair.b, "policy.preview", map[string]any{"policy": policy}).(map[string]any)
		mustCommand(t, pair.b, "policy.apply", map[string]any{"policy": policy, "expectedRevision": preview["revision"]})
	}
	apply()
	offer := func(want int) {
		t.Helper()
		data, err := json.Marshal(manifest)
		if err != nil {
			t.Fatal(err)
		}
		if int64(len(data)) > pair.b.receiveManifestJSONBytes() {
			t.Fatal("JSON escaping exceeded selected transport framing")
		}
		r := httptest.NewRequest(http.MethodPost, "http://peer.invalid/v1/offers", strings.NewReader(string(data)))
		r.Header.Set("Content-Type", "application/json")
		r.RemoteAddr = netip.AddrPortFrom(pair.na.ip, 32123).String()
		w := httptest.NewRecorder()
		pair.b.peerHTTP(pair.b.peerServer, w, r)
		if w.Code != want {
			t.Fatalf("offer status %d, want %d: %s", w.Code, want, w.Body.String())
		}
	}
	offer(http.StatusOK)
	policy.Logical["pathDepth"] = capacity.Limited(1)
	policy.Logical["pathBytes"] = capacity.Limited(1)
	policy.Resources["transferManifestBytes"] = capacity.Limited(1024)
	apply()
	offer(http.StatusOK)
	manifest.ID = "new-escaped"
	offer(http.StatusBadRequest)
}

func TestTransferPolicyWaitsRespectSelectionAndCancellation(t *testing.T) {
	c := &Core{capacity: capacity.Defaults()}
	for _, key := range []string{"stagingSeconds", "receiveWaitSeconds", "fileTransferSeconds"} {
		c.capacity.Logical[key] = capacity.Limited(7200)
		ctx, cancel := c.operationContext(context.Background(), key)
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) < 119*time.Minute {
			t.Fatalf("%s retained a shorter hidden deadline", key)
		}
		cancel()
		c.capacity.Logical[key] = capacity.Unlimited()
		parent, stop := context.WithCancel(context.Background())
		ctx, cancel = c.operationContext(parent, key)
		if _, ok := ctx.Deadline(); ok {
			t.Fatalf("%s unlimited acquired a deadline", key)
		}
		stop()
		if !errors.Is(ctx.Err(), context.Canceled) {
			t.Fatalf("%s ignored caller cancellation", key)
		}
		cancel()
	}
}

func TestStartupInventoriesRetainedStagingWithoutFollowingLinks(t *testing.T) {
	dir := t.TempDir()
	spool := filepath.Join(dir, "outgoing", "batch-retained")
	if err := os.MkdirAll(spool, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(spool, "1")
	if err := os.WriteFile(file, []byte("retained"), 0600); err != nil {
		t.Fatal(err)
	}
	external := t.TempDir()
	if err := os.WriteFile(filepath.Join(external, "private"), make([]byte, 4096), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(spool, "link")
	if err := os.Symlink(external, link); err != nil {
		t.Skip(err)
	}
	linkInfo, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	c := outgoingCapacityCore()
	c.dir = dir
	c.inventoryOutgoingSpool()
	if c.orphanSpoolError != "" || c.orphanSpoolBytes != 8+linkInfo.Size() || c.orphanSpoolEntries != 3 {
		t.Fatalf("inventory followed a link or lost retained bytes: %d, %d, %s", c.orphanSpoolBytes, c.orphanSpoolEntries, c.orphanSpoolError)
	}
	limits := transferLimits()
	limits.MaxReservedBytes = c.orphanSpoolBytes
	if _, err := c.reserveOutgoing(context.Background(), outgoingForCapacity("blocked", 1), limits); !errors.Is(err, errOutgoingStaging) {
		t.Fatalf("orphaned payload escaped admission accounting: %v", err)
	}
	if data, err := os.ReadFile(file); err != nil || string(data) != "retained" {
		t.Fatalf("inventory removed data: %q %v", data, err)
	}
}

func TestMessageRetentionRequiresSeparateFreshCleanup(t *testing.T) {
	c := &Core{dir: t.TempDir(), capacity: capacity.Defaults()}
	now := time.Now().UTC()
	message := func(id string) Message {
		return Message{ID: id, PeerID: "peer", Direction: "incoming", Text: "saved", Status: "received", CreatedAt: now}
	}
	for _, id := range []string{"one", "two", "three"} {
		if err := c.appendMessageLocked(message(id)); err != nil {
			t.Fatal(err)
		}
	}
	c.capacity.Logical["messageHistoryEntries"] = capacity.Limited(1)
	c.capacity.Logical["messageHistoryBytes"] = capacity.Unlimited()
	c.capacity.Logical["messageHistoryAgeSeconds"] = capacity.Unlimited()
	if err := c.appendMessageLocked(message("four")); err != nil {
		t.Fatal(err)
	}
	if len(c.messages) != 4 {
		t.Fatal("policy reduction or append silently pruned history")
	}
	preview, err := c.messageHistoryCommand("message.history.preview", json.RawMessage(`{}`))
	if err != nil || preview.(historyCleanup).Remove != 3 || len(c.messages) != 4 {
		t.Fatalf("preview changed history or lost candidates: %v %v", preview, err)
	}
	raw, _ := json.Marshal(map[string]string{"expectedRevision": preview.(historyCleanup).Revision})
	if err := c.appendMessageLocked(message("five")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.messageHistoryCommand("message.history.cleanup", raw); err == nil || len(c.messages) != 5 {
		t.Fatal("stale review removed newer data")
	}
	preview, err = c.messageHistoryCommand("message.history.preview", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(map[string]string{"expectedRevision": preview.(historyCleanup).Revision})
	if _, err := c.messageHistoryCommand("message.history.cleanup", raw); err != nil {
		t.Fatal(err)
	}
	if len(c.messages) != 1 || c.messages[0].ID != "five" {
		t.Fatal("reviewed cleanup removed the wrong records")
	}
	if err := c.loadMessages(); err != nil || len(c.messages) != 1 {
		t.Fatalf("cleanup was not durable: %v", err)
	}
}

func TestMessageStorageFullPreservesSavedRecords(t *testing.T) {
	c := &Core{dir: t.TempDir(), capacity: capacity.Defaults()}
	m := Message{ID: "first", PeerID: "peer", Text: "saved", Direction: "incoming", Status: "received", CreatedAt: time.Now().UTC()}
	if err := c.appendMessageLocked(m); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(c.dir, "messages.json"))
	if err != nil {
		t.Fatal(err)
	}
	c.capacity.Resources["messageStorageBytes"] = capacity.Limited(int64(len(before)))
	m.ID = "next"
	if err := c.appendMessageLocked(m); err == nil || len(c.messages) != 1 {
		t.Fatal("full storage silently discarded records")
	}
	after, err := os.ReadFile(filepath.Join(c.dir, "messages.json"))
	if err != nil || string(after) != string(before) {
		t.Fatal("failed append changed durable history")
	}
}

func TestRaisedMessagePolicyRoundTripsAndLoweringPreservesHistory(t *testing.T) {
	pair := newCorePair(t)
	trustPair(t, pair)
	for _, c := range []*Core{pair.a, pair.b} {
		c.mu.Lock()
		c.capacity.Logical["messageBytes"] = capacity.Unlimited()
		c.capacity.Resources["messageTextBytes"] = capacity.Limited(32 << 10)
		c.mu.Unlock()
	}
	text := strings.Repeat("&", 32<<10)
	message := mustCommand(t, pair.a, "message.send", map[string]string{"peerId": "peer-b", "text": text}).(Message)
	if message.Status != "sent" {
		t.Fatal("larger message was not acknowledged")
	}
	for _, c := range []*Core{pair.a, pair.b} {
		c.mu.Lock()
		c.capacity.Logical["messageBytes"] = capacity.Limited(1)
		c.mu.Unlock()
		if err := c.loadMessages(); err != nil || len(c.messages) != 1 || c.messages[0].Text != text {
			t.Fatalf("lower admission setting stranded retained message: %v", err)
		}
	}
}

type transferDeadlineRecorder struct {
	*httptest.ResponseRecorder
	read, write       time.Time
	readSet, writeSet bool
}

func (w *transferDeadlineRecorder) SetReadDeadline(d time.Time) error {
	w.read, w.readSet = d, true
	return nil
}
func (w *transferDeadlineRecorder) SetWriteDeadline(d time.Time) error {
	w.write, w.writeSet = d, true
	return nil
}

func TestReceiverFilePolicyOverridesServerDeadlines(t *testing.T) {
	pair := newCorePair(t)
	trustPair(t, pair)
	trust, _ := pair.b.trust("peer-a")
	peer := transfer.Peer{ID: trust.ID, Generation: trust.Generation}
	digest := sha256.Sum256(nil)
	for _, unlimited := range []bool{false, true} {
		id := fmt.Sprintf("deadline-%v", unlimited)
		manifest := transfer.Manifest{ID: id, Entries: []transfer.Entry{{ID: "file", Path: "file", Kind: transfer.File, SHA256: hex.EncodeToString(digest[:])}}}
		if _, err := pair.b.transfers.Offer(peer, manifest); err != nil {
			t.Fatal(err)
		}
		if _, err := pair.b.transfers.Accept(id, t.TempDir()); err != nil {
			t.Fatal(err)
		}
		pair.b.mu.Lock()
		pair.b.capacity.Logical["fileTransferSeconds"] = capacity.Limited(7200)
		if unlimited {
			pair.b.capacity.Logical["fileTransferSeconds"] = capacity.Unlimited()
		}
		pair.b.mu.Unlock()
		r := httptest.NewRequest(http.MethodPut, "http://peer.invalid/v1/batches/"+id+"/files/file", strings.NewReader(""))
		r.RemoteAddr = netip.AddrPortFrom(pair.na.ip, 32123).String()
		w := &transferDeadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
		pair.b.peerHTTP(pair.b.peerServer, w, r)
		if w.Code != http.StatusOK || !w.readSet || !w.writeSet {
			t.Fatalf("file request did not replace transport deadlines: %d %s", w.Code, w.Body.String())
		}
		if unlimited && (!w.read.IsZero() || !w.write.IsZero()) {
			t.Fatal("unlimited transfer retained a server deadline")
		}
		if !unlimited && (time.Until(w.read) < 119*time.Minute || time.Until(w.write) < 119*time.Minute) {
			t.Fatal("selected transfer retained a shorter server deadline")
		}
	}
}

func TestHistoryPaginationUsesDistinctMessageKeys(t *testing.T) {
	c := &Core{capacity: capacity.Defaults()}
	c.capacity.Resources["pageEntries"] = capacity.Limited(1)
	for _, peer := range []string{"a", "b"} {
		c.messages = append(c.messages, Message{ID: "same", PeerID: peer, Direction: "incoming", Text: peer})
	}
	first, err := c.listHistory("message.list", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	page := first.(localPage)
	raw, _ := json.Marshal(map[string]string{"cursor": page.NextCursor, "revision": page.Revision})
	second, err := c.listHistory("message.list", raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.(localPage).Items) != 1 || page.Items[0]["peerId"] == second.(localPage).Items[0]["peerId"] {
		t.Fatal("duplicate remote message IDs broke pagination")
	}
}
