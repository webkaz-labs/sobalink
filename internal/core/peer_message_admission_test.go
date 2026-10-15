package core

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing/synctest"
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func TestIncomingMessageLifecycleBusyHTTPExhaustion(t *testing.T) {
	p := newCorePair(t)
	trustPair(t, p)
	c := p.b
	c.mu.Lock()
	before := append([]byte(nil), c.messageStore.confirmedDisk...)
	c.mu.Unlock()
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- c.resourceLock.WithOwnership(c.dir, func() error { close(entered); <-release; return nil })
	}()
	<-entered
	defer func() {
		close(release)
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	r := httptest.NewRequest("POST", "http://peer.invalid/v1/messages", strings.NewReader(`{"id":"held-owner","text":"synthetic message"}`))
	r.RemoteAddr = netip.AddrPortFrom(p.na.ip, 32123).String()
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	c.peerHTTP(c.peerServer, w, r)
	c.mu.RLock()
	defer c.mu.RUnlock()
	if w.Code != http.StatusInsufficientStorage || !strings.Contains(w.Body.String(), "message_history_unavailable") || len(c.messages) != 0 || c.messageStore.frozen != "" || c.messageStore.candidate != nil || !bytes.Equal(before, c.messageStore.confirmedDisk) {
		t.Fatalf("held owner outcome: status=%d body=%s messages=%d frozen=%q candidate=%t", w.Code, w.Body.String(), len(c.messages), c.messageStore.frozen, c.messageStore.candidate != nil)
	}
}

// synctest.Wait observes the request asleep after the certified refusal, with
// the owner still held. No elapsed-time race selects whether contention occurs.
func TestIncomingMessageLifecycleBusyHTTPRecovery(t *testing.T) {
	for _, outcome := range []string{"release", "cancel", "revoke", "generation", "close", "competing", "conflict"} {
		t.Run(outcome, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				p := newCorePair(t)
				trustPair(t, p)
				c := p.b
				entered, release, ownerDone := make(chan struct{}), make(chan struct{}), make(chan error, 1)
				go func() {
					ownerDone <- c.resourceLock.WithOwnership(c.dir, func() error { close(entered); <-release; return nil })
				}()
				<-entered
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				r := httptest.NewRequest("POST", "http://peer.invalid/v1/messages", strings.NewReader(`{"id":"held-owner","text":"synthetic message"}`)).WithContext(ctx)
				r.RemoteAddr = netip.AddrPortFrom(p.na.ip, 32123).String()
				r.Header.Set("Content-Type", "application/json")
				w := httptest.NewRecorder()
				returned := make(chan struct{})
				go func() { c.peerHTTP(c.peerServer, w, r); close(returned) }()
				synctest.Wait()
				select {
				case <-returned:
					t.Fatal("HTTP returned before lifecycle contention cleared")
				default:
				}
				// This acquisition also proves the retry wait holds no Core.mu.
				c.mu.Lock()
				if len(c.messages) != 0 || c.messageStore.candidate != nil || c.messageStore.frozen != "" {
					t.Fatal("busy admission changed storage")
				}
				switch outcome {
				case "cancel":
					cancel()
				case "revoke":
					c.profile.Peers = nil
				case "generation":
					c.profile.Peers[0].Generation++
				case "close":
					c.closing = true
				}
				c.mu.Unlock()
				close(release)
				if err := <-ownerDone; err != nil {
					t.Fatal(err)
				}
				if outcome == "competing" || outcome == "conflict" {
					text := "synthetic message"
					if outcome == "conflict" {
						text = "different synthetic message"
					}
					trust := c.profile.Peers[0]
					status, _ := c.commitIncomingPeerMessage(ctx, trust, trust.ID, "held-owner", text)
					if status != http.StatusOK {
						t.Fatalf("competing commit: %d", status)
					}
				}
				<-returned
				want := http.StatusForbidden
				if outcome == "release" || outcome == "competing" {
					want = http.StatusOK
				}
				if outcome == "conflict" {
					want = http.StatusConflict
				}
				if w.Code != want {
					t.Fatalf("HTTP status %d, want %d: %s", w.Code, want, w.Body.String())
				}
				c.mu.Lock()
				defer c.mu.Unlock()
				expected := 0
				if outcome == "release" || outcome == "competing" || outcome == "conflict" {
					expected = 1
				}
				if len(c.messages) != expected || c.messageStore.candidate != nil || c.messageStore.frozen != "" {
					t.Fatal("wrong final storage state")
				}
				c.closing = false
			})
		})
	}
}

func TestIncomingMessageDoesNotRetryStorageOrUncertainty(t *testing.T) {
	for _, failure := range []error{os.ErrPermission, config.ErrLifecycleBusy, errors.Join(config.ErrLifecycleBusy, config.ErrAtomicCommitted)} {
		t.Run(failure.Error(), func(t *testing.T) {
			c := messageStoreFixture(t)
			trust := Trust{ID: "peer", Network: "tailnet", Generation: 1}
			c.profile.Settings.Network = "tailnet"
			c.profile.Peers = []Trust{trust}
			calls := 0
			c.messageStore.write = func(string, []byte) error { calls++; return failure }
			status, _ := c.commitIncomingPeerMessage(t.Context(), trust, "peer", "storage-failure", "synthetic message")
			if status != http.StatusInsufficientStorage || calls != 1 {
				t.Fatalf("storage error retried or acknowledged: status=%d calls=%d", status, calls)
			}
		})
	}
}

func TestIncomingMessageBusyDuplicateRemainsIdempotent(t *testing.T) {
	c := messageStoreFixture(t)
	trust := Trust{ID: "peer", Network: "tailnet", Generation: 1}
	c.profile.Settings.Network = "tailnet"
	c.profile.Peers = []Trust{trust}
	if status, _ := c.commitIncomingPeerMessage(t.Context(), trust, "peer", "duplicate", "synthetic message"); status != http.StatusOK {
		t.Fatal(status)
	}
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- c.resourceLock.WithOwnership(c.dir, func() error { close(entered); <-release; return nil })
	}()
	<-entered
	defer func() {
		close(release)
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	start := time.Now()
	if status, _ := c.commitIncomingPeerMessage(t.Context(), trust, "peer", "duplicate", "synthetic message"); status != http.StatusOK {
		t.Fatal(status)
	}
	if len(c.messages) != 1 {
		t.Fatal("duplicate appended")
	}
	t.Logf("existing receipt returned while ownership remained held in %s", time.Since(start))
}

func TestIncomingMessageBusyAdmissionBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := messageStoreFixture(t)
		trust := Trust{ID: "peer", Network: "tailnet", Generation: 1}
		c.profile.Settings.Network = "tailnet"
		c.profile.Peers = []Trust{trust}
		entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
		go func() {
			done <- c.resourceLock.WithOwnership(c.dir, func() error { close(entered); <-release; return nil })
		}()
		<-entered
		defer func() {
			close(release)
			if err := <-done; err != nil {
				t.Error(err)
			}
		}()
		started := time.Now()
		status, _ := c.commitIncomingPeerMessage(t.Context(), trust, "peer", "bounded", "synthetic message")
		if status != http.StatusInsufficientStorage || time.Since(started) != 250*time.Millisecond {
			t.Fatalf("busy admission budget: status=%d elapsed=%s", status, time.Since(started))
		}
		if len(c.messages) != 0 || c.messageStore.candidate != nil || c.messageStore.frozen != "" {
			t.Fatal("budget exhaustion changed storage")
		}
	})
}
