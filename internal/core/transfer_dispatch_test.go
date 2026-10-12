package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/identity"
	"github.com/webkaz-labs/sobalink/internal/policy"
)

func dispatchTrio(t *testing.T) (corePair, *Core) {
	t.Helper()
	pair := newCorePair(t)
	lock, err := config.AcquireLock(pair.a.dir)
	if err != nil {
		t.Fatal(err)
	}
	pair.a.op.Lock()
	pair.a.initializeResourceIdentity(lock)
	pair.a.op.Unlock()
	t.Cleanup(func() { _ = pair.a.Close(); _ = lock.Close() })
	thirdIP := netip.MustParseAddr("100.64.0.3")
	node := &pipeNode{hub: pair.hub, ip: thirdIP, who: map[netip.Addr]string{pair.na.ip: "peer-a"}, state: identity.State{IPs: []netip.Addr{thirdIP}, Backend: "Running", Snapshot: policy.Snapshot{Running: true, Peers: []policy.Peer{{ID: "peer-a", DNSName: "peer-a.invalid", IPs: []netip.Addr{pair.na.ip}, Online: true}}}}}
	third, err := Open(context.Background(), Options{Directory: t.TempDir(), Version: "test", NodeFactory: func(string, string) (NetworkBackend, error) { return node, nil }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := third.Close(); err != nil {
			t.Error(err)
		}
	})
	mustCommand(t, third, "network.configure", map[string]any{"mode": "tailnet"})
	third.op.Lock()
	err = third.startPeerServer([]netip.Addr{thirdIP})
	third.op.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	pair.na.mu.Lock()
	pair.na.state.Snapshot.Peers[0].Online = true
	pair.na.state.Snapshot.Peers = append(pair.na.state.Snapshot.Peers, policy.Peer{ID: "peer-c", DNSName: "peer-c.invalid", IPs: []netip.Addr{thirdIP}, Online: true})
	pair.na.who[thirdIP] = "peer-c"
	pair.na.mu.Unlock()
	trustPair(t, pair)
	mustCommand(t, pair.a, "peer.trust", map[string]any{"peerId": "peer-c", "trusted": true})
	mustCommand(t, third, "peer.trust", map[string]any{"peerId": "peer-a", "trusted": true})
	return pair, third
}
func prepareTestDispatch(t *testing.T, c *Core, id string, peers []string, paths []string) TransferDispatchReview {
	t.Helper()
	value, err := command(c, id, "transfer.dispatch.prepare", map[string]any{"processId": c.resourceCatalogProcessID(), "peerIds": peers, "paths": paths})
	if err != nil {
		t.Fatal(err)
	}
	return value.(TransferDispatchReview)
}
func commitTestDispatch(t *testing.T, c *Core, r TransferDispatchReview) TransferDispatchReceipt {
	t.Helper()
	value, err := c.commitDispatch(context.Background(), r.ProcessID, r.PreparationID, r.ReviewRevision, true)
	if err != nil {
		t.Fatal(err)
	}
	return value.(TransferDispatchReceipt)
}
func dispatchSource(t *testing.T) string {
	t.Helper()
	name := filepath.Join(t.TempDir(), "example.txt")
	if err := os.WriteFile(name, []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	return name
}
func dispatchBatch(t *testing.T, c *Core, id string) *outgoingBatch {
	t.Helper()
	c.mu.RLock()
	defer c.mu.RUnlock()
	b := c.outgoing[id]
	if b == nil {
		t.Fatal("missing outgoing child")
	}
	return b
}
func TestDispatchStagesOnceAndRetainsPayloadUntilFinalChild(t *testing.T) {
	p, third := dispatchTrio(t)
	source := dispatchSource(t)
	p.a.mu.Lock()
	p.a.capacity.Resources["transferSpoolBytes"] = capacity.Limited(4)
	p.a.mu.Unlock()
	p.hub.mu.Lock()
	before := p.hub.wire.Len()
	p.hub.mu.Unlock()
	review := prepareTestDispatch(t, p.a, "prepare-one", []string{"peer-b", "peer-c"}, []string{source})
	if review.TotalBytes != 4 || review.Status != "prepared" || len(review.Entries) != 1 {
		t.Fatalf("review=%+v", review)
	}
	p.hub.mu.Lock()
	traffic := p.hub.wire.Len() - before
	p.hub.mu.Unlock()
	if traffic != 0 || len(p.a.outgoing) != 0 {
		t.Fatal("preparation sent traffic or created child history")
	}
	entries, err := os.ReadDir(filepath.Join(p.a.dir, "outgoing"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("spools=%v %v", entries, err)
	}
	if err := os.WriteFile(source, []byte("new!"), 0600); err != nil {
		t.Fatal(err)
	}
	receipt := commitTestDispatch(t, p.a, review)
	if len(receipt.Children) != 2 || receipt.Children[0].TransferID == receipt.Children[1].TransferID {
		t.Fatal("child identities were not distinct")
	}
	first := dispatchBatch(t, p.a, receipt.Children[0].TransferID)
	second := dispatchBatch(t, p.a, receipt.Children[1].TransferID)
	first.mu.Lock()
	spool, owner := first.Spool, first.payload
	first.mu.Unlock()
	second.mu.Lock()
	same := second.Spool == spool && second.payload == owner
	second.mu.Unlock()
	if !same {
		t.Fatal("children did not share one payload")
	}
	waitIncomingOffer(t, p.b, first.ID)
	waitIncomingOffer(t, third, second.ID)
	dir1, dir2 := t.TempDir(), t.TempDir()
	mustCommand(t, p.b, "transfer.accept", map[string]string{"transferId": first.ID, "destination": dir1})
	waitOutgoingIdle(t, p.a, first.ID)
	if _, err := os.Stat(spool); err != nil {
		t.Fatalf("first completion removed another child's payload: %v", err)
	}
	if _, err := p.a.SendPaths(context.Background(), "peer-b", []string{source}); !errors.Is(err, errOutgoingStaging) {
		t.Fatalf("shared payload was not charged once: %v", err)
	}
	mustCommand(t, third, "transfer.accept", map[string]string{"transferId": second.ID, "destination": dir2})
	waitOutgoingIdle(t, p.a, second.ID)
	for i, receiver := range []*Core{p.b, third} {
		received, err := receiver.transfers.Get(receipt.Children[i].TransferID)
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filepath.Join(received.Destination, received.Files[0].StoredName))
		if err != nil || string(data) != "data" {
			t.Fatalf("frozen payload=%q %v", data, err)
		}
	}
	if _, err := os.Stat(spool); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("final completion retained payload: %v", err)
	}
	for _, b := range []*outgoingBatch{first, second} {
		if b.State != "completed" || b.payload != nil || b.reserved != 0 {
			t.Fatalf("terminal state=%s", b.State)
		}
	}
}
func TestDispatchCommitIsIdempotentAcrossConcurrentCallsAndTrustChanges(t *testing.T) {
	p, _ := dispatchTrio(t)
	review := prepareTestDispatch(t, p.a, "repeat-commit", []string{"peer-b", "peer-c"}, []string{dispatchSource(t)})
	var wg sync.WaitGroup
	values := make(chan any, 12)
	errs := make(chan error, 12)
	for range 12 {
		wg.Go(func() {
			v, e := p.a.commitDispatch(context.Background(), review.ProcessID, review.PreparationID, review.ReviewRevision, true)
			values <- v
			errs <- e
		})
	}
	wg.Wait()
	close(values)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var receipt TransferDispatchReceipt
	for value := range values {
		r := value.(TransferDispatchReceipt)
		if receipt.DispatchID != "" && !reflect.DeepEqual(receipt, r) {
			t.Fatal("duplicate commit changed receipt")
		}
		receipt = r
	}
	if len(p.a.outgoing) != 2 {
		t.Fatalf("duplicate children=%d", len(p.a.outgoing))
	}
	mustCommand(t, p.a, "peer.trust", map[string]any{"peerId": "peer-b", "trusted": false})
	repeated := commitTestDispatch(t, p.a, review)
	if !reflect.DeepEqual(receipt, repeated) {
		t.Fatal("lost response could not recover after trust change")
	}
	if _, err := p.a.commitDispatch(context.Background(), review.ProcessID, review.PreparationID, strings.Repeat("0", 64), true); networkErrorCode(err) != "transfer_dispatch_stale" {
		t.Fatalf("changed review=%v", err)
	}
	if _, err := p.a.preparationResult(review.ProcessID, review.PreparationID); err != nil {
		t.Fatal(err)
	}
}
func TestDispatchPrecommitChangesCreateNoChildren(t *testing.T) {
	for _, change := range []string{"generation", "paused", "backend", "process", "offline", "history", "metadata", "spool", "confirmation"} {
		t.Run(change, func(t *testing.T) {
			p, _ := dispatchTrio(t)
			review := prepareTestDispatch(t, p.a, "changed-review", []string{"peer-b", "peer-c"}, []string{dispatchSource(t)})
			p.a.mu.Lock()
			switch change {
			case "generation":
				p.a.profile.Peers[0].Generation++
			case "paused":
				p.a.profile.Peers[0].Paused = true
			case "backend":
				p.a.node = p.nb
			case "process":
				review.ProcessID = "different-process"
			case "history":
				p.a.capacity.Logical["transferHistoryEntries"] = capacity.Limited(2)
			case "metadata":
				p.a.capacity.Resources["transferMetadataBytes"] = capacity.Limited(512)
			case "spool":
				p.a.capacity.Resources["transferSpoolBytes"] = capacity.Limited(3)
			}
			p.a.mu.Unlock()
			if change == "offline" {
				p.na.mu.Lock()
				p.na.state.Snapshot.Peers[1].Online = false
				p.na.mu.Unlock()
			}
			_, err := p.a.commitDispatch(context.Background(), review.ProcessID, review.PreparationID, review.ReviewRevision, change != "confirmation")
			if err == nil || len(p.a.outgoing) != 0 {
				t.Fatalf("changed review admitted children: %v", err)
			}
			if change == "backend" {
				p.a.mu.Lock()
				p.a.node = p.na
				p.a.mu.Unlock()
			}
		})
	}
}
func TestDispatchRejectsDuplicateRecipientsAndDiscardCleansPreparedBytes(t *testing.T) {
	p, _ := dispatchTrio(t)
	source := dispatchSource(t)
	process := p.a.resourceCatalogProcessID()
	if _, err := p.a.prepareDispatchPaths(context.Background(), "duplicate", process, []string{"peer-b", "peer-b"}, []string{source}); networkErrorCode(err) != "transfer_dispatch_invalid" {
		t.Fatalf("duplicates=%v", err)
	}
	review := prepareTestDispatch(t, p.a, "discard-one", []string{"peer-b", "peer-c"}, []string{source})
	p.a.mu.RLock()
	batch := p.a.transferPreparations[review.PreparationID].batch
	p.a.mu.RUnlock()
	batch.mu.Lock()
	spool := batch.Spool
	batch.mu.Unlock()
	if err := p.a.discardPreparation(process, review.PreparationID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(spool); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("discard payload=%v", err)
	}
	if _, err := p.a.commitDispatch(context.Background(), process, review.PreparationID, review.ReviewRevision, true); networkErrorCode(err) != "transfer_dispatch_stale" {
		t.Fatal(err)
	}
	if len(p.a.outgoing) != 0 {
		t.Fatal("discard created children")
	}
}
func TestSharedPayloadCleanupFailureKeepsReservationAndOtherChild(t *testing.T) {
	c := outgoingCapacityCore()
	lim := transferLimits()
	lim.MaxReservedBytes = 4
	spool := t.TempDir()
	if err := os.WriteFile(filepath.Join(spool, "1"), []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	owner := &outgoingPayload{refs: 2, reserved: 4, spool: spool + string(os.PathSeparator) + "."}
	first, second := outgoingForCapacity("first", 4), outgoingForCapacity("second", 4)
	for _, b := range []*outgoingBatch{first, second} {
		b.payload = owner
		b.reserved = 4
		b.Spool = spool
		b.State = "failed"
		c.outgoing[b.ID] = b
	}
	first.stop()
	if first.payload != nil || owner.refs != 1 {
		t.Fatal("first reference was not released")
	}
	second.stop()
	if second.payload == nil || owner.refs != 1 || owner.reserved != 4 {
		t.Fatal("failed final delete lost its owner")
	}
	if _, err := c.reserveOutgoing(context.Background(), outgoingForCapacity("blocked", 1), lim); !errors.Is(err, errOutgoingStaging) {
		t.Fatalf("failed cleanup capacity=%v", err)
	}
	owner.mu.Lock()
	owner.spool = spool
	owner.mu.Unlock()
	second.stop()
	if owner.reserved != 0 || second.payload != nil {
		t.Fatal("successful cleanup did not release capacity")
	}
}
func TestOutgoingDispatchQueueBoundsAllBatchesAndCancelsWithoutSending(t *testing.T) {
	p, third := dispatchTrio(t)
	p.a.mu.Lock()
	p.a.capacity.Resources["transferConcurrentRecipients"] = capacity.Limited(1)
	p.a.mu.Unlock()
	source := dispatchSource(t)
	review := prepareTestDispatch(t, p.a, "queued-group", []string{"peer-b", "peer-c"}, []string{source})
	receipt := commitTestDispatch(t, p.a, review)
	first := dispatchBatch(t, p.a, receipt.Children[0].TransferID)
	second := dispatchBatch(t, p.a, receipt.Children[1].TransferID)
	waitIncomingOffer(t, p.b, first.ID)
	value, err := p.a.SendPaths(context.Background(), "peer-b", []string{source})
	if err != nil {
		t.Fatal(err)
	}
	single := dispatchBatch(t, p.a, value.(map[string]string)["id"])
	p.a.mu.RLock()
	active, queued := p.a.outgoingActive, len(p.a.outgoingQueue)
	p.a.mu.RUnlock()
	if active != 1 || queued != 2 {
		t.Fatalf("global active/queued=%d/%d", active, queued)
	}
	second.stop()
	waitOutgoingIdle(t, p.a, second.ID)
	if _, err := third.transfers.Get(second.ID); err == nil {
		t.Fatal("cancelled queued child contacted recipient")
	}
	first.stop()
	waitOutgoingIdle(t, p.a, first.ID)
	waitIncomingOffer(t, p.b, single.ID)
	single.stop()
	waitOutgoingIdle(t, p.a, single.ID)
	p.a.mu.RLock()
	active, queued = p.a.outgoingActive, len(p.a.outgoingQueue)
	p.a.mu.RUnlock()
	if active != 0 || queued != 0 {
		t.Fatalf("queue retained completed work %d/%d", active, queued)
	}
}
func TestUploadDispatchPreparesOnlyAndReturnsRecoverableReceipt(t *testing.T) {
	p, _ := dispatchTrio(t)
	var buffer bytes.Buffer
	w := multipart.NewWriter(&buffer)
	process := p.a.resourceCatalogProcessID()
	for _, field := range [][2]string{{"peerIds", `["peer-b","peer-c"]`}, {"processId", process}, {"requestId", "browser-prepare"}, {"manifest", `[{"path":"example.txt","size":4,"kind":"file"}]`}} {
		if err := w.WriteField(field[0], field[1]); err != nil {
			t.Fatal(err)
		}
	}
	f, err := w.CreateFormFile("files", "example.txt")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.Write([]byte("data"))
	_ = w.Close()
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/api/upload-dispatch", bytes.NewReader(buffer.Bytes()))
	request.Header.Set("Content-Type", w.FormDataContentType())
	response := httptest.NewRecorder()
	p.a.UploadDispatch(response, request)
	if response.Code != 200 {
		t.Fatalf("prepare=%d %s", response.Code, response.Body.String())
	}
	var reply struct {
		OK     bool                   `json:"ok"`
		Result TransferDispatchReview `json:"result"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	if !reply.OK || reply.Result.PreparationID != "browser-prepare" || len(p.a.outgoing) != 0 {
		t.Fatal("upload did not prepare only")
	}
	status, err := p.a.preparationResult(process, "browser-prepare")
	if err != nil || !reflect.DeepEqual(status, reply.Result) {
		t.Fatalf("status=%+v %v", status, err)
	}
}
func TestPreparedDispatchExpiresWithoutCreatingChild(t *testing.T) {
	p, _ := dispatchTrio(t)
	review := prepareTestDispatch(t, p.a, "expiry", []string{"peer-b", "peer-c"}, []string{dispatchSource(t)})
	p.a.mu.Lock()
	p.a.transferPreparations[review.PreparationID].expiry.Reset(time.Millisecond)
	p.a.mu.Unlock()
	until := time.Now().Add(time.Second)
	for {
		p.a.mu.RLock()
		exists := p.a.transferPreparations[review.PreparationID] != nil
		p.a.mu.RUnlock()
		if !exists {
			break
		}
		if time.Now().After(until) {
			t.Fatal("expired preparation remained")
		}
		time.Sleep(time.Millisecond)
	}
	if len(p.a.outgoing) != 0 {
		t.Fatal("expiry created children")
	}
}

func TestDispatchStaleOwnerCleanupDoesNotRemoveReplacement(t *testing.T) {
	p, _ := dispatchTrio(t)
	source := dispatchSource(t)
	oldReview := prepareTestDispatch(t, p.a, "reused-id", []string{"peer-b", "peer-c"}, []string{source})
	p.a.mu.RLock()
	old := p.a.transferPreparations[oldReview.PreparationID]
	p.a.mu.RUnlock()
	if err := p.a.discardPreparation(oldReview.ProcessID, oldReview.PreparationID, false); err != nil {
		t.Fatal(err)
	}
	nextReview := prepareTestDispatch(t, p.a, "reused-id", []string{"peer-b", "peer-c"}, []string{source})
	p.a.mu.RLock()
	next := p.a.transferPreparations[nextReview.PreparationID]
	p.a.mu.RUnlock()
	if nextReview.ReviewRevision == oldReview.ReviewRevision {
		t.Fatal("replacement reused the old review")
	}
	if err := p.a.discardPreparationOwner(old.processID, old.batch.ID, false, old); err != nil {
		t.Fatal(err)
	}
	if err := p.a.discardPreparationOwner(old.processID, old.batch.ID, true, old); err != nil {
		t.Fatal(err)
	}
	next.batch.mu.Lock()
	spool := next.batch.Spool
	next.batch.mu.Unlock()
	if _, err := os.Stat(spool); err != nil {
		t.Fatalf("old owner removed replacement: %v", err)
	}
	value, err := p.a.preparationResult(nextReview.ProcessID, nextReview.PreparationID)
	if err != nil || !reflect.DeepEqual(value, nextReview) {
		t.Fatalf("replacement changed=%+v %v", value, err)
	}
}
func TestDispatchFailedChildRetainsOriginalPayloadForExplicitRetry(t *testing.T) {
	p, third := dispatchTrio(t)
	source := dispatchSource(t)
	mustCommand(t, p.b, "peer.autosave", map[string]any{"peerId": "peer-a", "enabled": true, "directory": t.TempDir()})
	mustCommand(t, third, "peer.autosave", map[string]any{"peerId": "peer-a", "enabled": true, "directory": t.TempDir()})
	mustCommand(t, third, "peer.autosave", map[string]any{"peerId": "peer-a", "paused": true})
	review := prepareTestDispatch(t, p.a, "retry-child", []string{"peer-b", "peer-c"}, []string{source})
	receipt := commitTestDispatch(t, p.a, review)
	completed := waitOutgoingIdle(t, p.a, receipt.Children[0].TransferID)
	failed := waitOutgoingIdle(t, p.a, receipt.Children[1].TransferID)
	if completed.State != "completed" || failed.State != "failed" || failed.payload == nil || failed.reserved != 4 {
		t.Fatalf("mixed result=%s/%s %s", completed.State, failed.State, failed.Error)
	}
	if err := os.WriteFile(source, []byte("new!"), 0600); err != nil {
		t.Fatal(err)
	}
	mustCommand(t, third, "peer.autosave", map[string]any{"peerId": "peer-a", "paused": false})
	mustCommand(t, p.a, "transfer.retry", map[string]string{"transferId": failed.ID})
	waitOutgoingIdle(t, p.a, failed.ID)
	if failed.State != "completed" || failed.reserved != 0 {
		t.Fatalf("retry=%s %s", failed.State, failed.Error)
	}
	received, err := third.transfers.Get(failed.ID)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(filepath.Join(received.Destination, received.Files[0].StoredName))
	if err != nil || string(saved) != "data" {
		t.Fatalf("retry changed content: %q %v", saved, err)
	}
}

type dispatchBlockedCloseFile struct {
	*os.File
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (f *dispatchBlockedCloseFile) Close() error {
	f.once.Do(func() { close(f.entered) })
	<-f.release
	return f.File.Close()
}
func TestSharedPayloadWaitsForPhysicalSourceAfterSiblingCompletion(t *testing.T) {
	c := outgoingCapacityCore()
	lifetime, stop := context.WithCancel(context.Background())
	defer stop()
	c.ctx = lifetime
	spool := t.TempDir()
	path := filepath.Join(spool, "1")
	if err := os.WriteFile(path, []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	source := &dispatchBlockedCloseFile{File: file, entered: make(chan struct{}), release: make(chan struct{})}
	var unblock sync.Once
	defer unblock.Do(func() { close(source.release) })
	owner := &outgoingPayload{refs: 2, reserved: 4, spool: spool}
	first, second := outgoingForCapacity("first", 4), outgoingForCapacity("second", 4)
	for _, b := range []*outgoingBatch{first, second} {
		b.payload = owner
		b.Spool = spool
		b.reserved = 4
		b.running = true
		b.runDone = make(chan struct{})
		c.outgoing[b.ID] = b
	}
	caller, cancel := context.WithCancel(lifetime)
	first.cancel = cancel
	appCtx, application, err := c.peerHTTPApplication(caller)
	if err != nil {
		t.Fatal(err)
	}
	request, err := c.newPeerHTTPRequest(appCtx, "peer", &peerHTTPRoute{backend: "tailnet"}, source)
	if err != nil {
		t.Fatal(err)
	}
	request.finishOnce.Do(func() { close(request.appDone) })
	cancel()
	select {
	case <-source.entered:
	case <-time.After(time.Second):
		t.Fatal("source owner did not begin physical close")
	}
	finished := make(chan struct{})
	go func() {
		application.waitRequests()
		c.finishOutgoingRun(&outgoingRun{ctx: caller, batch: first, stop: func() bool { return true }, done: func() { close(finished) }}, application, context.Canceled, false)
	}()
	c.finishOutgoingRun(&outgoingRun{ctx: lifetime, batch: second, stop: func() bool { return true }, done: func() {}}, nil, nil, false)
	select {
	case <-finished:
		t.Fatal("cancelled child escaped physical reader join")
	default:
	}
	owner.mu.Lock()
	refs, reserved := owner.refs, owner.reserved
	owner.mu.Unlock()
	if refs != 1 || reserved != 4 {
		t.Fatalf("reader lost payload reference: %d/%d", refs, reserved)
	}
	if _, err := os.Stat(spool); err != nil {
		t.Fatalf("sibling completion removed active reader payload: %v", err)
	}
	unblock.Do(func() { close(source.release) })
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("joined reader did not finish")
	}
	c.wg.Wait()
	if _, err := os.Stat(spool); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("final physical release retained spool: %v", err)
	}
}

func TestDispatchCancellationDuringStagingCreatesNoChildren(t *testing.T) {
	for _, action := range []string{"request", "discard", "revoke", "close"} {
		t.Run(action, func(t *testing.T) {
			p, _ := dispatchTrio(t)
			original, blocked, _ := blockCommandStaging(t, p.a)
			var input struct {
				Paths []string `json:"paths"`
			}
			if err := json.Unmarshal(original.Payload, &input); err != nil {
				t.Fatal(err)
			}
			process := p.a.resourceCatalogProcessID()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result := make(chan error, 1)
			go func() {
				_, err := p.a.prepareDispatchPaths(ctx, "blocked-prepare", process, []string{"peer-b", "peer-c"}, input.Paths)
				result <- err
			}()
			select {
			case <-blocked.entered:
			case <-time.After(time.Second):
				t.Fatal("preparation never reached staging")
			}
			if value, err := p.a.preparationResult(process, "blocked-prepare"); value != nil || networkErrorCode(err) != "transfer_dispatch_preparing" {
				t.Fatalf("pending status=%+v %v", value, err)
			}
			switch action {
			case "request":
				cancel()
			case "discard":
				if err := p.a.discardPreparation(process, "blocked-prepare", false); err != nil {
					t.Fatal(err)
				}
			case "revoke":
				mustCommand(t, p.a, "peer.trust", map[string]any{"peerId": "peer-b", "trusted": false})
			case "close":
				if err := p.a.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if err := awaitCommandError(t, result); err == nil {
				t.Fatal("cancelled preparation succeeded")
			}
			p.a.mu.RLock()
			children, preparations := len(p.a.outgoing), len(p.a.transferPreparations)
			p.a.mu.RUnlock()
			if children != 0 || preparations != 0 {
				t.Fatalf("cancelled preparation retained %d children/%d preparations", children, preparations)
			}
		})
	}
}
func TestOutgoingRetryUsesGlobalDispatchQueue(t *testing.T) {
	p, _ := dispatchTrio(t)
	source := dispatchSource(t)
	if err := p.b.transfers.PausePeer("peer-a", true); err != nil {
		t.Fatal(err)
	}
	value, err := p.a.SendPaths(context.Background(), "peer-b", []string{source})
	if err != nil {
		t.Fatal(err)
	}
	failed := waitOutgoingIdle(t, p.a, value.(map[string]string)["id"])
	if failed.State != "failed" {
		t.Fatalf("expected retryable failure: %s %s", failed.State, failed.Error)
	}
	if err := p.b.transfers.PausePeer("peer-a", false); err != nil {
		t.Fatal(err)
	}
	p.a.mu.Lock()
	p.a.capacity.Resources["transferConcurrentRecipients"] = capacity.Limited(1)
	p.a.mu.Unlock()
	value, err = p.a.SendPaths(context.Background(), "peer-b", []string{source})
	if err != nil {
		t.Fatal(err)
	}
	active := dispatchBatch(t, p.a, value.(map[string]string)["id"])
	waitIncomingOffer(t, p.b, active.ID)
	mustCommand(t, p.a, "transfer.retry", map[string]string{"transferId": failed.ID})
	p.a.mu.RLock()
	running, queued := p.a.outgoingActive, len(p.a.outgoingQueue)
	p.a.mu.RUnlock()
	if running != 1 || queued != 1 {
		t.Fatalf("retry bypassed global budget: %d/%d", running, queued)
	}
	if _, err := p.b.transfers.Get(failed.ID); err == nil {
		t.Fatal("queued retry offered before a global slot was free")
	}
	active.stop()
	waitOutgoingIdle(t, p.a, active.ID)
	waitIncomingOffer(t, p.b, failed.ID)
	failed.stop()
	waitOutgoingIdle(t, p.a, failed.ID)
}
