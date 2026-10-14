package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/webkaz-labs/sobalink/internal/transfer"
)

func runMessageQueueAttempt(t *testing.T, c *Core, id string) error {
	t.Helper()
	c.mu.Lock()
	run := messageQueueRunFixture(c, id)
	c.mu.Unlock()
	ctx, app, err := c.peerHTTPApplication(run.ctx)
	if err == nil {
		err = c.deliverMessageQueue(ctx, run)
		app.waitRequests()
		app.finish()
	}
	run.cancel()
	c.mu.Lock()
	completion := c.messageQueue.completions[id]
	if completion == nil {
		completion = &messageQueueCompletion{}
		c.messageQueue.completions[id] = completion
	}
	completion.joined = true
	completion.code = messageQueueCompletionCode(err)
	close(run.joined)
	drainErr := c.drainMessageQueueCompletionLocked(id)
	c.mu.Unlock()
	if drainErr != nil {
		t.Fatal(drainErr)
	}
	return err
}
func TestMessageQueueExactBodyFreeOfferRetryAfterLostACK(t *testing.T) {
	c := messageQueueFixture(t)
	view := messageQueueCommitFixture(t, c, messageQueuePrepareFixture(t, c, "peer-b", "synthetic exact retry text"))
	row := cloneMessageOutgoing(c.messageStore.state.Outgoing[view.OutboxID])
	lease := messageQueueLeaseFixture(&row)
	var offers []DurableTextOffer
	var mu sync.Mutex
	var delivered atomic.Int64
	outboxPipePeer(t, c, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/hello":
			reply(w, http.StatusOK, map[string]any{"protocol": 1, "product": "sobalink", "capabilities": []string{DurableTextCapability}})
		case DurableTextOfferPath:
			var offer DurableTextOffer
			if json.NewDecoder(r.Body).Decode(&offer) != nil {
				http.Error(w, "bad offer", 400)
				return
			}
			mu.Lock()
			offers = append(offers, offer)
			first := len(offers) == 1
			mu.Unlock()
			if first {
				hijacker, ok := w.(http.Hijacker)
				if !ok {
					t.Error("fixture lacks connection close")
					return
				}
				connection, _, err := hijacker.Hijack()
				if err == nil {
					_ = connection.Close()
				}
				return
			}
			reply(w, http.StatusOK, DurableTextStatus{Lease: lease, State: "offered", ReceiverNow: time.Now()})
		case DurableTextDeliverPath:
			var delivery DurableTextDeliver
			if json.NewDecoder(r.Body).Decode(&delivery) != nil || delivery.Text != view.Text || !sameTextLease(delivery.Lease, lease) {
				http.Error(w, "changed delivery", 409)
				return
			}
			c.mu.RLock()
			current := c.messageStore.state.Outgoing[view.OutboxID]
			fenced := current.ExposurePossible && current.View.DeliveryOutcome == "unknown" && current.Lease != nil
			c.mu.RUnlock()
			if !fenced {
				t.Error("text exposed before durable exposure/lease fence")
			}
			delivered.Add(1)
			receipt := messageQueueReceiptFixture(&row, lease)
			reply(w, http.StatusOK, DurableTextStatus{Lease: lease, State: "received", Receipt: &receipt, ReceiverNow: time.Now()})
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
			http.Error(w, "unexpected route", 409)
		}
	}))
	if err := runMessageQueueAttempt(t, c, view.OutboxID); err == nil {
		t.Fatal("lost offer ACK reported success")
	}
	current := c.messageStore.state.Outgoing[view.OutboxID]
	if current.Lease != nil || current.ExposurePossible || current.View.DeliveryOutcome != "not_attempted" || delivered.Load() != 0 {
		t.Fatal("body-free ACK loss became possible text exposure")
	}
	if err := runMessageQueueAttempt(t, c, view.OutboxID); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(offers) != 2 {
		t.Fatalf("wrong offer count: %d", len(offers))
	}
	first, second := offers[0], offers[1]
	first.SentAt = time.Time{}
	second.SentAt = time.Time{}
	if !reflect.DeepEqual(first, second) || delivered.Load() != 1 || c.messageStore.state.Outgoing[view.OutboxID].View.DeliveryOutcome != "confirmed" {
		t.Fatal("retry changed original offer or lost exact receipt")
	}
}
func TestMessageQueuePersistedLeaseUnknownNeverReoffersOrDowngrades(t *testing.T) {
	c := messageQueueFixture(t)
	view := messageQueueCommitFixture(t, c, messageQueuePrepareFixture(t, c, "peer-b", "synthetic unknown lease text"))
	lease := messageQueueLeaseFixture(c.messageStore.state.Outgoing[view.OutboxID])
	c.mu.Lock()
	if err := c.mutateMessageStoreLocked(func(next *messageEnvelope) error {
		row := next.Outgoing[view.OutboxID]
		row.Lease = &lease
		row.Attempted = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	c.mu.Unlock()
	var paths []string
	var mu sync.Mutex
	outboxPipePeer(t, c, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		replyMessageQueueUnknownLease(t, w, r, lease)
	}))
	if err := runMessageQueueAttempt(t, c, view.OutboxID); networkErrorCode(err) != "message_unknown_lease" {
		t.Fatalf("lost lease was not fail closed: %v", err)
	}
	c.pumpMessageQueue()
	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(paths, []string{DurableTextStatusPath}) {
		t.Fatalf("persisted lease was reoffered or downgraded: %v", paths)
	}
	current := c.messageStore.state.Outgoing[view.OutboxID]
	if current.Lease == nil || !sameTextLease(*current.Lease, lease) || current.View.Status != "needs_review" || !current.NoMoreSend {
		t.Fatal("missing remote lease was cleared or granted send authority")
	}
}

// Match the production receiver: inspect the complete exact status request
// before rejecting its retained lease. Replying with an unread POST body can
// close the in-memory stream while its writer is still finishing, which tests
// transport interruption instead of the intended coded lease rejection.
func replyMessageQueueUnknownLease(t *testing.T, w http.ResponseWriter, r *http.Request, expected transfer.DurableLease) {
	t.Helper()
	var actual transfer.DurableLease
	if r.Method != http.MethodPost || r.URL.Path != DurableTextStatusPath || readJSON(r, 8192, &actual) != nil || !sameTextLease(actual, expected) {
		t.Error("unknown-lease fixture received a different or incomplete status request")
		http.Error(w, "invalid synthetic status request", http.StatusBadRequest)
		return
	}
	reply(w, http.StatusConflict, map[string]string{"code": "message_unknown_lease"})
}

type messageQueueGatedLeaseBody struct {
	*bytes.Reader
	release, started chan struct{}
	startOnce        sync.Once
	eof              atomic.Bool
}

func (b *messageQueueGatedLeaseBody) Read(p []byte) (int, error) {
	b.startOnce.Do(func() { close(b.started) })
	<-b.release
	n, err := b.Reader.Read(p)
	if err == io.EOF {
		b.eof.Store(true)
	}
	return n, err
}
func (*messageQueueGatedLeaseBody) Close() error { return nil }

type messageQueueLeaseResponse struct {
	*httptest.ResponseRecorder
	body      *messageQueueGatedLeaseBody
	beforeEOF atomic.Bool
}

func (w *messageQueueLeaseResponse) WriteHeader(code int) {
	if !w.body.eof.Load() {
		w.beforeEOF.Store(true)
	}
	w.ResponseRecorder.WriteHeader(code)
}

// The old and corrected response order is deterministic, without depending on
// which side wins the net.Pipe close race or on a real-time scheduling delay.
func TestMessageQueueUnknownLeaseFixtureReadsBeforeReply(t *testing.T) {
	for _, decoded := range []bool{false, true} {
		name := "unread-control"
		if decoded {
			name = "exact-request-and-eof"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				lease := transfer.DurableLease{Version: DurableTextVersion, DeliveryID: "synthetic-delivery", RecipientID: "synthetic-peer", Token: "synthetic-token"}
				raw, err := json.Marshal(lease)
				if err != nil {
					t.Fatal(err)
				}
				body := &messageQueueGatedLeaseBody{Reader: bytes.NewReader(raw), release: make(chan struct{}), started: make(chan struct{})}
				request := httptest.NewRequest(http.MethodPost, DurableTextStatusPath, body)
				request.Header.Set("Content-Type", "application/json")
				response := &messageQueueLeaseResponse{ResponseRecorder: httptest.NewRecorder(), body: body}
				done := make(chan struct{})
				go func() {
					defer close(done)
					if decoded {
						replyMessageQueueUnknownLease(t, response, request, lease)
					} else {
						reply(response, http.StatusConflict, map[string]string{"code": "message_unknown_lease"})
					}
				}()
				synctest.Wait()
				responded, reading := channelClosed(done), channelClosed(body.started)
				// Always release the reader before reporting a failed assertion.
				close(body.release)
				synctest.Wait()
				<-done
				if decoded && (responded || !reading || !body.eof.Load()) || !decoded && (!responded || reading || body.eof.Load()) {
					t.Fatal("fixture response order did not distinguish unread and complete requests")
				}
				if response.beforeEOF.Load() == decoded {
					t.Fatal("fixture reply crossed the wrong request EOF boundary")
				}
				var problem map[string]string
				if response.Code != http.StatusConflict || json.Unmarshal(response.Body.Bytes(), &problem) != nil || problem["code"] != "message_unknown_lease" {
					t.Fatal("fixture changed the exact unknown-lease rejection")
				}
				if !decoded {
					// Later draining and a duplicate header cannot erase the original
					// observation that this response was emitted before request EOF.
					if _, err := io.Copy(io.Discard, io.LimitReader(body, int64(len(raw))+1)); err != nil || !body.eof.Load() {
						t.Fatal("unread control did not subsequently reach EOF", err)
					}
					response.WriteHeader(http.StatusConflict)
					if !response.beforeEOF.Load() {
						t.Fatal("late header erased the early-response observation")
					}
				}
			})
		})
	}
}

func TestMessageQueueDecodedReceiptSurvivesCancelAndPhysicalRelease(t *testing.T) {
	c := messageQueueFixture(t)
	view := messageQueueCommitFixture(t, c, messageQueuePrepareFixture(t, c, "peer-b", "synthetic physical completion text"))
	c.mu.Lock()
	row := c.messageStore.state.Outgoing[view.OutboxID]
	lease := messageQueueLeaseFixture(row)
	receipt := messageQueueReceiptFixture(row, lease)
	run := messageQueueRunFixture(c, view.OutboxID)
	c.outgoingActive++
	c.wg.Add(1)
	c.mu.Unlock()
	if err := c.recordMessageQueueStatus(view.OutboxID, DurableTextStatus{Lease: lease, State: "received", Receipt: &receipt, ReceiverNow: time.Now()}, true); err != nil {
		t.Fatal(err)
	}
	atRelease, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithCancel(c.ctx)
	application := &peerHTTPApplication{ctx: ctx, cancel: cancel, release: func() { close(atRelease); <-release }, requests: map[*peerHTTPRequest]struct{}{}, requestChanged: make(chan struct{})}
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	go func() {
		c.finishMessageQueueRun(run, application, errors.New("synthetic cleanup interrupted"))
		close(finished)
	}()
	select {
	case <-atRelease:
	case <-time.After(3 * time.Second):
		t.Fatal("physical completion did not reach release")
	}
	cancelled, cancelCall := context.WithCancel(context.Background())
	cancelCall()
	if _, err := c.cancelMessageQueue(cancelled, view.OutboxID); err != nil {
		t.Fatal(err)
	}
	c.mu.RLock()
	current := c.messageStore.state.Outgoing[view.OutboxID]
	retained := current.Active && current.View.DeliveryOutcome == "confirmed" && current.Receipt != nil && c.messageQueue.runs[view.OutboxID] == run && !c.messageQueue.completions[view.OutboxID].joined && c.outgoingActive == 1
	c.mu.RUnlock()
	if !retained {
		t.Fatal("cancel or terminal callback lost receipt/physical ownership")
	}
	unblock()
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("physical release failed to finish")
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	current = c.messageStore.state.Outgoing[view.OutboxID]
	if current.Active || current.View.DeliveryOutcome != "confirmed" || current.Receipt == nil || !reflect.DeepEqual(*current.Receipt, receipt) || c.messageQueue.runs[view.OutboxID] != nil || c.outgoingActive != 0 {
		t.Fatal("physical completion did not preserve confirmed receipt and retire capacity")
	}
}
func TestMessageQueueSharedFiniteDispatchBudget(t *testing.T) {
	c := messageQueueFixture(t)
	view := messageQueueCommitFixture(t, c, messageQueuePrepareFixture(t, c, "peer-b", "synthetic bounded dispatch text"))
	c.mu.Lock()
	limit := finiteInt(policyOrDefault(c.capacity).Number("resources", "transferConcurrentRecipients"))
	c.outgoingActive = limit
	c.mu.Unlock()
	c.pumpMessageQueue()
	c.mu.RLock()
	defer c.mu.RUnlock()
	if len(c.messageQueue.runs) != 0 || c.outgoingActive != limit || !c.messageStore.state.Outgoing[view.OutboxID].Active {
		t.Fatal("text bypassed occupied file-transfer concurrency slots")
	}
}
