package core

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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
		reply(w, http.StatusConflict, map[string]string{"code": "message_unknown_lease"})
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
