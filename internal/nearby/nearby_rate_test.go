package nearby

import (
	"context"
	"net/netip"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// heldSessionClock pauses one consumer observation while the raw provider uses
// the same underlying fake time without the pause. It controls the queueing
// schedule without changing either production rate limit.
type heldSessionClock struct {
	*fakeClock
	mu      sync.Mutex
	entered chan struct{}
	release chan struct{}
}

func (c *heldSessionClock) holdNext() (<-chan struct{}, func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entered, release := make(chan struct{}), make(chan struct{})
	c.entered, c.release = entered, release
	return entered, sync.OnceFunc(func() { close(release) })
}

func (c *heldSessionClock) Now() time.Time {
	c.mu.Lock()
	entered, release := c.entered, c.release
	c.entered, c.release = nil, nil
	c.mu.Unlock()
	if entered != nil {
		close(entered)
		<-release
	}
	return c.fakeClock.Now()
}

func TestSessionRateWindowRequiresObservationDrain(t *testing.T) {
	for _, premature := range []bool{true, false} {
		name := "drain_before_advance"
		if premature {
			name = "transport_drain_is_insufficient"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				clock, tr := newFakeClock(), newFakeTransport()
				consumerClock := &heldSessionClock{fakeClock: clock}
				local := testAd(999)
				local.Bootstrap = netip.AddrPortFrom(testScope().LocalAddress, 42000)
				s, err := startProvider(context.Background(), Config{testScope(), local, consumerClock}, func(ctx context.Context, cfg Config, policy discoveryPolicy) (recordProvider, error) {
					cfg.Clock = clock
					return newRawRecordProvider(ctx, cfg, policy, func(Scope) (Transport, error) { return tr, nil })
				}, discoveryPolicy{})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := s.Close(); err != nil {
						t.Error(err)
					}
				})
				synctest.Wait()
				if tr.count() != 2 {
					t.Fatal("initial query and advertisement missing")
				}
				for i := 1; i <= MaxParsedPerSecond; i++ {
					tr.incoming <- testPacket(t, testAd(i))
				}
				synctest.Wait()
				entered, release := consumerClock.holdNext()
				defer release()
				for i := 1; i <= MaxParsedPerSecond; i++ {
					tr.incoming <- testPacket(t, testAd(MaxParsedPerSecond+i))
				}
				synctest.Wait()
				select {
				case <-entered:
				default:
					t.Fatal("consumer did not reach the held observation")
				}
				// Both of the old fixture's predicates hold even though its
				// second half is still buffered between provider and consumer.
				if len(tr.incoming) != 0 || len(s.Candidates()) != MaxParsedPerSecond {
					t.Fatal("did not reproduce the transport-only drain boundary")
				}
				if premature {
					clock.advance(time.Second)
				}
				release()
				synctest.Wait()
				if !premature {
					clock.advance(time.Second)
				}
				query := testPacket(t, testAd(100))
				query.Data = packQuery()
				for i := 0; i < 32; i++ {
					tr.incoming <- query
				}
				synctest.Wait()
				want := 3
				if premature {
					want = 2 // Prior observations consumed the new consumer window.
				}
				if got := tr.count(); got != want {
					t.Fatalf("writes = %d, want %d", got, want)
				}
				if len(s.Candidates()) != MaxParsedPerSecond || s.Err() != nil {
					t.Fatalf("unexpected candidate/session change: %v", s.Err())
				}
			})
		})
	}
}
