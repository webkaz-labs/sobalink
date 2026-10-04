package core

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/identity"
	"github.com/webkaz-labs/sobalink/internal/policy"
)

func refreshFixture(count int) identity.State {
	var state identity.State
	for i := count - 1; i >= 0; i-- {
		state.Snapshot.Peers = append(state.Snapshot.Peers, policy.Peer{ID: fmt.Sprintf("peer-%04d", i)})
	}
	return state
}

func TestBackgroundPeerRefreshHealthyInitialCapacity(t *testing.T) {
	for _, peers := range []int{128, 400} {
		t.Run(fmt.Sprint(peers), func(t *testing.T) {
			c := &Core{confirmed: map[string]time.Time{}}
			state := refreshFixture(peers)
			seen := map[string]bool{}
			var mu sync.Mutex
			now := time.Unix(4_000, 0)
			for remaining := peers; remaining > 0; remaining -= 128 {
				var probes atomic.Int64
				c.refreshPeerBatchWithBackoff(context.Background(), state, func(_ context.Context, id string) error {
					probes.Add(1)
					mu.Lock()
					seen[id] = true
					mu.Unlock()
					c.mu.Lock()
					c.confirmed[id] = now
					c.mu.Unlock()
					return nil
				}, now)
				if got, want := probes.Load(), int64(min(128, remaining)); got != want {
					t.Fatalf("healthy background probes = %d, want %d", got, want)
				}
			}
			if len(seen) != peers {
				t.Fatalf("only %d of %d healthy peers examined", len(seen), peers)
			}
		})
	}
}

func TestPeerRefreshRotatesStableBatchesBeyond128(t *testing.T) {
	c := &Core{confirmed: map[string]time.Time{}}
	state := refreshFixture(400)
	var mu sync.Mutex
	seen := map[string]int{}
	probe := func(_ context.Context, id string) error { mu.Lock(); seen[id]++; mu.Unlock(); return nil }
	for i := 0; i < 4; i++ {
		c.refreshPeerBatch(context.Background(), state, probe)
		// Changing source order must not change fair scheduling.
		state.Snapshot.Peers = append(state.Snapshot.Peers[1:], state.Snapshot.Peers[0])
	}
	if len(seen) != 400 {
		t.Fatalf("only %d of 400 peers examined", len(seen))
	}
	for _, count := range seen {
		if count < 1 || count > 2 {
			t.Fatal("peer starvation or duplicate scheduling")
		}
	}
}

func TestPeerRefreshDeadlineKeepsUnexaminedCandidates(t *testing.T) {
	c := &Core{confirmed: map[string]time.Time{}}
	state := refreshFixture(20)
	ctx, cancel := context.WithCancel(context.Background())
	var active, maximum, started atomic.Int64
	fourStarted := make(chan struct{})
	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		c.refreshPeerBatch(ctx, state, func(ctx context.Context, id string) error {
			current := active.Add(1)
			for previous := maximum.Load(); current > previous && !maximum.CompareAndSwap(previous, current); previous = maximum.Load() {
			}
			if started.Add(1) == 4 {
				close(fourStarted)
			}
			<-ctx.Done()
			active.Add(-1)
			return ctx.Err()
		})
	}()
	select {
	case <-fourStarted:
	case <-time.After(time.Second):
		t.Fatal("four probes did not start")
	}
	cancel()
	select {
	case <-firstDone:
	case <-time.After(time.Second):
		t.Fatal("refresh did not cancel")
	}
	if maximum.Load() != 4 || started.Load() != 4 || c.refreshCursor != "peer-0003" {
		t.Fatalf("wrong canceled progress: active=%d started=%d cursor=%s", maximum.Load(), started.Load(), c.refreshCursor)
	}
	var mu sync.Mutex
	seen := map[string]bool{}
	c.refreshPeerBatch(context.Background(), state, func(_ context.Context, id string) error { mu.Lock(); seen[id] = true; mu.Unlock(); return nil })
	if !seen["peer-0004"] || !seen["peer-0019"] {
		t.Fatal("unexamined candidates were skipped")
	}
}

func TestPeerRefreshSkipsExpiredAndRecentlyConfirmedWithoutStarvation(t *testing.T) {
	c := &Core{confirmed: map[string]time.Time{}}
	state := refreshFixture(260)
	for i := 0; i < 128; i++ {
		c.confirmed[fmt.Sprintf("peer-%04d", i)] = time.Now()
	}
	state.Snapshot.Peers = append(state.Snapshot.Peers, policy.Peer{ID: "expired", Expired: true}, policy.Peer{}, policy.Peer{ID: "peer-0259"})
	var mu sync.Mutex
	seen := map[string]bool{}
	probe := func(_ context.Context, id string) error { mu.Lock(); seen[id] = true; mu.Unlock(); return nil }
	for i := 0; i < 3; i++ {
		c.refreshPeerBatch(context.Background(), state, probe)
	}
	if seen[""] || seen["expired"] || seen["peer-0000"] || !seen["peer-0259"] {
		t.Fatal("eligibility filter or rotating schedule failed")
	}
}

func TestBackgroundPeerRefreshBackoffUsesInjectedLoopbackBackend(t *testing.T) {
	p := newCorePair(t)
	var accepted, requests atomic.Int64
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.NotFound(w, r)
	}))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server.Listener = &countingListener{Listener: listener, accepted: &accepted}
	server.Start()
	t.Cleanup(server.Close)
	injectBackendDialIP(p.a, func(ctx context.Context, _ string, _ netip.AddrPort) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", server.Listener.Addr().String())
	})

	state := refreshFixture(1)
	state.Snapshot.Peers[0].ID = "peer-b"
	base := time.Unix(1_000, 0)
	for _, offset := range []time.Duration{0, 2, 4, 6, 8} {
		p.a.refreshPeerBatchWithBackoff(context.Background(), state, p.a.probePeer, base.Add(offset*time.Second))
	}
	if got := requests.Load(); got != 3 {
		t.Fatalf("HTTP requests by t=8s = %d, want 3", got)
	}
	if got := accepted.Load(); got != requests.Load() {
		t.Fatalf("accepted sockets = %d, requests = %d; peer transport should open one socket per request", got, requests.Load())
	}
	t.Logf("logical ticks 0,2,4,6,8s: requests=%d accepted sockets=%d", requests.Load(), accepted.Load())
}

func TestPeerRefreshCancellationDoesNotRecordFailure(t *testing.T) {
	c := &Core{confirmed: map[string]time.Time{}, peerRefreshRetries: newPeerRefreshScheduler()}
	state := refreshFixture(1)
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.refreshPeerBatchWithBackoff(ctx, state, func(ctx context.Context, _ string) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		}, time.Unix(2_000, 0))
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("probe did not start")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("canceled refresh did not join its worker")
	}
	if len(c.peerRefreshRetries.entries) != 0 {
		t.Fatal("batch cancellation poisoned failed-peer backoff")
	}
}

func TestBackgroundPeerRefreshAdmissionEventuallyCovers400Peers(t *testing.T) {
	c := &Core{confirmed: map[string]time.Time{}, peerRefreshRetries: newPeerRefreshScheduler()}
	state := refreshFixture(400)
	seen := make(map[string]bool, 400)
	var seenMu sync.Mutex
	base := time.Unix(3_000, 0)
	for batch := 0; batch <= (400-peerRefreshCacheLimit)/peerRefreshOverflowPerBatch; batch++ {
		now := base
		if batch > 0 {
			now = base.Add(time.Second) // Protected retries are not yet due or expired.
		}
		var probes atomic.Int64
		c.refreshPeerBatchWithBackoff(context.Background(), state, func(_ context.Context, id string) error {
			probes.Add(1)
			seenMu.Lock()
			seen[id] = true
			seenMu.Unlock()
			return errors.New("simulated failed hello")
		}, now)
		want := int64(peerRefreshCacheLimit)
		if batch > 0 {
			want = peerRefreshOverflowPerBatch
		}
		if probes.Load() != want || len(c.peerRefreshRetries.entries) != peerRefreshCacheLimit || len(c.peerRefreshRetries.live) != 0 || len(c.peerRefreshRetries.reservations) != 0 {
			t.Fatalf("batch %d: probes=%d, want %d; cache=%d live=%d reservations=%d", batch, probes.Load(), want, len(c.peerRefreshRetries.entries), len(c.peerRefreshRetries.live), len(c.peerRefreshRetries.reservations))
		}
		state.Snapshot.Peers = append(state.Snapshot.Peers[1:], state.Snapshot.Peers[0])
	}
	if len(seen) != 400 {
		t.Fatalf("fair admission reached %d of 400 peers", len(seen))
	}
}

func TestBackgroundPeerRefreshFourWorkersRejectForgottenCompletion(t *testing.T) {
	c := &Core{confirmed: map[string]time.Time{}, peerRefreshRetries: newPeerRefreshScheduler()}
	state := refreshFixture(128)
	now := time.Unix(5_000, 0)
	failure := errors.New("failed hello")
	c.refreshPeerBatchWithBackoff(context.Background(), state, func(context.Context, string) error { return failure }, now)
	now = now.Add(2 * time.Second)
	started := make(chan string, 4)
	release, done := make(chan struct{}), make(chan struct{})
	var active, maximum, probes atomic.Int64
	var releaseOnce sync.Once
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); releaseOnce.Do(func() { close(release) }); <-done })
	go func() {
		defer close(done)
		c.refreshPeerBatchWithBackoff(ctx, state, func(_ context.Context, id string) error {
			current := active.Add(1)
			defer active.Add(-1)
			for previous := maximum.Load(); current > previous && !maximum.CompareAndSwap(previous, current); previous = maximum.Load() {
			}
			if probes.Add(1) <= 4 {
				started <- id
				<-release
			}
			return failure
		}, now)
	}()
	var forgotten string
	for i := 0; i < 4; i++ {
		select {
		case id := <-started:
			forgotten = id
		case <-time.After(time.Second):
			t.Fatal("four background workers did not reach the barrier")
		}
	}
	s := c.peerRefreshRetries
	s.mu.Lock()
	live, reserved := len(s.live), len(s.entries)+len(s.reservations)
	s.mu.Unlock()
	if live != 4 || reserved != 128 {
		t.Fatalf("live=%d reserved capacity=%d, want 4 and 128", live, reserved)
	}
	s.forget(forgotten)
	replacement := s.admit("replacement", now)
	if replacement == nil || !replacement.retained {
		t.Fatal("forgotten slot was not reusable")
	}
	s.complete(replacement, now, false)
	releaseOnce.Do(func() { close(release) })
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("background batch did not join")
	}
	if maximum.Load() != 4 || active.Load() != 0 || probes.Load() != 128 || len(s.entries) != 128 || len(s.live) != 0 || len(s.reservations) != 0 {
		t.Fatalf("max=%d active=%d probes=%d entries=%d live=%d reservations=%d", maximum.Load(), active.Load(), probes.Load(), len(s.entries), len(s.live), len(s.reservations))
	}
	if _, restored := s.entries[forgotten]; restored {
		t.Fatal("forgotten worker restored its retry entry")
	}
}

func TestBackgroundPeerRefreshLateProbeCannotRestoreCaches(t *testing.T) {
	for _, success := range []bool{false, true} {
		for _, invalidate := range []string{"revoke", "remove"} {
			t.Run(fmt.Sprintf("success=%t/%s", success, invalidate), func(t *testing.T) {
				p := newCorePair(t)
				started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
				var releaseOnce sync.Once
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if success && r.URL.Path == "/v1/hello" {
						_, _ = fmt.Fprint(w, `{"protocol":1,"product":"sobalink"}`)
						return
					}
					close(started)
					<-release
					if success {
						_, _ = fmt.Fprint(w, `{"version":2,"services":[]}`)
					} else {
						http.NotFound(w, r)
					}
				}))
				injectBackendDialIP(p.a, func(ctx context.Context, _ string, _ netip.AddrPort) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, "tcp", server.Listener.Addr().String())
				})
				s := p.a.peerRefreshRetries
				now := time.Unix(6_000, 0)
				s.complete(s.admit("peer-b", now), now, false)
				now = now.Add(2 * time.Second)
				state, err := p.na.State(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithCancel(context.Background())
				t.Cleanup(func() { cancel(); releaseOnce.Do(func() { close(release) }); <-done; server.Close() })
				go func() {
					defer close(done)
					p.a.refreshPeerBatchWithBackoff(ctx, state, p.a.probePeer, now)
				}()
				select {
				case <-started:
				case <-time.After(time.Second):
					t.Fatal("peer request did not reach the response barrier")
				}
				if invalidate == "revoke" {
					p.a.revokePeer("peer-b")
					fresh := s.admit("peer-b", now)
					if fresh == nil {
						t.Fatal("revoked ticket retained its reservation")
					}
					s.complete(fresh, now, false)
				} else {
					p.na.mu.Lock()
					p.na.state.Snapshot.Peers = nil
					p.na.mu.Unlock()
					s.prune(map[string]struct{}{}, now)
				}
				releaseOnce.Do(func() { close(release) })
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Fatal("late probe did not join")
				}
				p.a.mu.RLock()
				_, confirmed := p.a.confirmed["peer-b"]
				_, discovered := p.a.discovered["peer-b"]
				_, observed := p.a.discoveryObservations["peer-b"]
				p.a.mu.RUnlock()
				if confirmed || discovered || observed {
					t.Fatalf("late probe restored caches: confirmed=%v discovered=%v observed=%v", confirmed, discovered, observed)
				}
				if invalidate == "revoke" && s.entries["peer-b"].failures != 1 || invalidate == "remove" && len(s.entries) != 0 {
					t.Fatal("late probe altered invalidated retry state")
				}
			})
		}
	}
}

func TestPeerRefreshManualCommandsBypassBackgroundBackoff(t *testing.T) {
	p := newCorePair(t)
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	injectBackendDialIP(p.a, func(ctx context.Context, _ string, _ netip.AddrPort) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", server.Listener.Addr().String())
	})
	now := time.Now()
	s := p.a.peerRefreshRetries
	s.complete(s.admit("peer-b", now), now, false)
	state, err := p.na.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	p.a.refreshPeerBatchWithBackoff(context.Background(), state, p.a.probePeer, now)
	if requests.Load() != 0 {
		t.Fatal("background refresh ignored backoff")
	}
	for i, payload := range []map[string]string{{"peerId": "peer-b"}, {}} {
		mustCommand(t, p.a, "discovery.refresh", payload)
		if got := requests.Load(); got != int64(i+1) {
			t.Fatalf("manual refresh requests = %d, want %d", got, i+1)
		}
	}
}

type injectedDialIPBackend struct {
	NetworkBackend
	dial func(context.Context, string, netip.AddrPort) (net.Conn, error)
}

func (b injectedDialIPBackend) DialIP(ctx context.Context, network string, target netip.AddrPort) (net.Conn, error) {
	return b.dial(ctx, network, target)
}

func injectBackendDialIP(c *Core, dial func(context.Context, string, netip.AddrPort) (net.Conn, error)) {
	c.mu.Lock()
	c.node = injectedDialIPBackend{NetworkBackend: c.node, dial: dial}
	c.mu.Unlock()
}

type countingListener struct {
	net.Listener
	accepted *atomic.Int64
}

func (l *countingListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err == nil {
		l.accepted.Add(1)
	}
	return conn, err
}
