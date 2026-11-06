package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/webui"
	"os"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func TestOutgoingMessageLifecycleBusyPreservesConfirmedDelivery(t *testing.T) {
	p := newCorePair(t)
	trustPair(t, p)
	entered, release, ownerDone := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		ownerDone <- p.a.resourceLock.WithOwnership(p.a.dir, func() error { close(entered); <-release; return nil })
	}()
	<-entered
	defer func() {
		close(release)
		if err := <-ownerDone; err != nil {
			t.Error(err)
		}
	}()
	result, err := command(p.a, randomID(), "message.send", map[string]string{"peerId": "peer-b", "text": "Fictional lifecycle admission contention."})
	if networkErrorCode(err) != "message_history_unavailable" || !strings.Contains(err.Error(), "peer acknowledged receipt") {
		t.Fatalf("unexpected outgoing busy outcome: %v", err)
	}
	sent := result.(Message)
	if sent.Status != "sent" {
		t.Fatal("confirmed delivery was lost")
	}
	p.a.mu.RLock()
	localCount, frozen, candidate := len(p.a.messages), p.a.messageStore.frozen, p.a.messageStore.candidate != nil
	p.a.mu.RUnlock()
	p.b.mu.RLock()
	received := append([]Message(nil), p.b.messages...)
	p.b.mu.RUnlock()
	if localCount != 0 || frozen != "" || candidate || len(received) != 1 || received[0].ID != sent.ID {
		t.Fatal("busy admission changed local history or duplicated/lost receiver delivery")
	}
	t.Log("sender lifecycle admission alone reproduced ACK followed by local-history failure; receiver has exactly one acknowledged message")
}

// The held lifecycle owner and synctest.Wait put execution exactly between
// receipt and local publication. No scheduler sleep selects the collision.
func TestOutgoingMessageLifecycleBusyHTTPRecovery(t *testing.T) {
	for _, outcome := range []string{"release", "cancel", "trust-removed", "close", "receiver-storage-failure"} {
		t.Run(outcome, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				p := newCorePair(t)
				trustPair(t, p)
				wantStatus, wantReceived := "sent", 1
				if outcome == "receiver-storage-failure" {
					p.b.mu.Lock()
					p.b.messageStore.write = func(string, []byte) error { return os.ErrPermission }
					p.b.mu.Unlock()
					wantStatus, wantReceived = "failed", 0
				}
				writes := 0
				p.a.mu.Lock()
				p.a.messageStore.afterWrite = func() error { writes++; return nil }
				p.a.mu.Unlock()
				entered, release, ownerDone := make(chan struct{}), make(chan struct{}), make(chan error, 1)
				go func() {
					ownerDone <- p.a.resourceLock.WithOwnership(p.a.dir, func() error { close(entered); <-release; return nil })
				}()
				<-entered
				released := false
				defer func() {
					if !released {
						close(release)
						<-ownerDone
					}
				}()
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				id := randomID()
				payload := json.RawMessage(`{"peerId":"peer-b","text":"Fictional local-completion recovery."}`)
				cmd := webui.Command{RequestID: id, Name: "message.send", Payload: payload}
				var value any
				var err error
				returned := make(chan struct{})
				go func() {
					if outcome == "close" {
						// Exercise the admitted message obligation without Command's
						// outer mutation mutex, so shutdown must join beginWork itself.
						value, err = p.a.sendMessageCommand(ctx, cmd.Payload)
					} else {
						value, err = p.a.Command(ctx, cmd)
					}
					close(returned)
				}()
				synctest.Wait()
				select {
				case <-returned:
					t.Fatal("message returned before local lifecycle admission cleared")
				default:
				}
				p.b.mu.RLock()
				received := append([]Message(nil), p.b.messages...)
				p.b.mu.RUnlock()
				if len(received) != wantReceived {
					t.Fatal("receiver outcome changed before the local completion wait")
				}
				// Acquiring Core.mu also proves the wait does not retain the state lock.
				p.a.mu.Lock()
				if len(p.a.messages) != 0 || p.a.messageStore.candidate != nil || p.a.messageStore.frozen != "" || writes != 0 {
					t.Fatal("pre-admission refusal changed sender storage")
				}
				if outcome == "trust-removed" {
					p.a.profile.Peers = nil
				}
				p.a.mu.Unlock()
				if outcome == "cancel" {
					cancel()
				}
				var closed chan error
				if outcome == "close" {
					closed = make(chan error, 1)
					go func() { closed <- p.a.Close() }()
					<-p.a.Done()
					select {
					case <-closed:
						t.Fatal("shutdown did not join the pending local completion")
					default:
					}
					p.a.mu.RLock()
					storeOpen := p.a.messageStore != nil && p.a.messageStore.root != nil
					p.a.mu.RUnlock()
					if !storeOpen {
						t.Fatal("shutdown closed the store before local completion")
					}
				}
				close(release)
				released = true
				if e := <-ownerDone; e != nil {
					t.Fatal(e)
				}
				<-returned
				if closed != nil {
					if e := <-closed; e != nil {
						t.Fatal(e)
					}
				}
				if outcome == "receiver-storage-failure" {
					if networkErrorCode(err) != "message_peer_storage_unavailable" {
						t.Fatalf("original receiver storage error was lost: %v", err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
				sent := value.(Message)
				if sent.Status != wantStatus || sent.ID == "" || (wantReceived == 1 && sent.ID != received[0].ID) {
					t.Fatal("local completion changed the delivery outcome")
				}
				if writes != 1 {
					t.Fatal("local completion did not perform exactly one admitted write")
				}
				for i, app := range []*Core{p.a, p.b} {
					loaded := &Core{dir: app.dir}
					if e := loaded.loadMessages(); e != nil {
						t.Fatal(e)
					}
					want := 1
					if i == 1 {
						want = wantReceived
					}
					if len(loaded.messages) != want || (want == 1 && loaded.messages[0].ID != sent.ID) {
						t.Fatal("local commit replayed or lost the message")
					}
					if i == 0 && loaded.messages[0].Status != wantStatus {
						t.Fatal("local history changed the delivery status")
					}
				}
				// A repeated command reads the same completion, without another delivery.
				if outcome != "close" {
					repeated, e := p.a.Command(t.Context(), cmd)
					if e != err || repeated.(Message).ID != sent.ID {
						t.Fatal("same-ID command lost its exact completion")
					}
				}
				p.b.mu.RLock()
				count := len(p.b.messages)
				p.b.mu.RUnlock()
				if count != wantReceived {
					t.Fatal("local admission recovery caused another delivery")
				}
				p.hub.mu.Lock()
				sends := bytes.Count(p.hub.wire.Bytes(), []byte("POST /v1/messages "))
				p.hub.mu.Unlock()
				if sends != 1 {
					t.Fatalf("local admission recovery caused %d network sends", sends)
				}
			})
		})
	}
}

func TestOutgoingMessageBusyAdmissionBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := messageStoreFixture(t)
		entered, release, ownerDone := make(chan struct{}), make(chan struct{}), make(chan error, 1)
		go func() {
			ownerDone <- c.resourceLock.WithOwnership(c.dir, func() error { close(entered); <-release; return nil })
		}()
		<-entered
		defer func() {
			close(release)
			if err := <-ownerDone; err != nil {
				t.Error(err)
			}
		}()
		start := time.Now()
		err := new(peerMessageOperation).commit(c, Message{ID: "synthetic-completion", PeerID: "peer", Direction: "outgoing", Text: "Fictional message.", CreatedAt: start.UTC(), Status: "sent"})
		if _, busy := err.(*messageStoreAdmissionBusy); !busy {
			t.Fatalf("wrong exhaustion: %v", err)
		}
		if elapsed := time.Since(start); elapsed != 250*time.Millisecond {
			t.Fatalf("admission budget %v", elapsed)
		}
		if len(c.messages) != 0 || c.messageStore.candidate != nil || c.messageStore.frozen != "" {
			t.Fatal("exhausted admission mutated storage")
		}
	})
}

func TestOutgoingMessageDoesNotRetryStorageOrUncertainty(t *testing.T) {
	for _, failure := range []error{os.ErrPermission, config.ErrLifecycleBusy, errors.Join(config.ErrLifecycleBusy, config.ErrAtomicCommitted)} {
		t.Run(failure.Error(), func(t *testing.T) {
			c := messageStoreFixture(t)
			calls := 0
			c.messageStore.write = func(string, []byte) error { calls++; return failure }
			err := new(peerMessageOperation).commit(c, Message{ID: "synthetic-completion", PeerID: "peer", Direction: "outgoing", Text: "Fictional message.", CreatedAt: time.Now().UTC(), Status: "sent"})
			if err == nil || calls != 1 {
				t.Fatalf("post-admission error was retried: calls=%d err=%v", calls, err)
			}
			if errors.Is(failure, config.ErrAtomicCommitted) != errors.Is(err, config.ErrAtomicCommitted) {
				t.Fatal("uncertain atomic publication was hidden")
			}
		})
	}
}
