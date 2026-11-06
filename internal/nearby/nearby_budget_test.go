package nearby

import (
	"context"
	"net/netip"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// Expose the second write while Session.run has not yet initialized lastAd.
type heldBudgetStartupTransport struct {
	*fakeTransport
	release chan struct{}
}

func (tr *heldBudgetStartupTransport) WritePacket(b []byte) error {
	if err := tr.fakeTransport.WritePacket(b); err != nil {
		return err
	}
	if tr.count() == 2 {
		<-tr.release
	}
	return nil
}

func TestQueryFloodRequiresStartupDrain(t *testing.T) {
	for _, premature := range []bool{true, false} {
		name := "drain_before_advance"
		if premature {
			name = "initial_writes_are_insufficient"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				tr := &heldBudgetStartupTransport{fakeTransport: newFakeTransport(), release: make(chan struct{})}
				release := sync.OnceFunc(func() { close(tr.release) })
				s, clock := startCloseTest(t, tr)
				t.Cleanup(func() { release(); _ = s.Close() })
				synctest.Wait()
				if tr.count() != 2 {
					t.Fatal("initial query and advertisement missing")
				}
				if premature {
					clock.advance(time.Second)
				}
				// The gate is durably blocked, not ready. Release it before
				// waiting for initialization and advancing the next window.
				release()
				synctest.Wait()
				if !premature {
					clock.advance(time.Second)
				}
				query := testPacket(t, testAd(1))
				query.Data = packQuery()
				tr.incoming <- query
				synctest.Wait()
				want := 3
				if premature {
					want = 2 // Initialization consumed the advanced interval.
				}
				if got := tr.count(); got != want || s.Err() != nil {
					t.Fatalf("writes = %d, want %d; error = %v", got, want, s.Err())
				}
				clock.advance(time.Second)
				tr.incoming <- query
				synctest.Wait()
				if tr.count() != want+1 || s.Err() != nil {
					t.Fatal("query did not respond in the next initialized window")
				}
			})
		})
	}
}

func TestQueryFloodRequiresConsumerDrain(t *testing.T) {
	for _, premature := range []bool{true, false} {
		name := "drain_before_advance"
		if premature {
			name = "periodic_writes_are_insufficient"
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
				t.Cleanup(func() { _ = s.Close() })
				synctest.Wait()
				before := tr.count()
				clock.advance(QueryInterval)
				synctest.Wait()
				entered, release := consumerClock.holdNext()
				defer release()
				query := testPacket(t, testAd(1))
				query.Data = packQuery()
				tr.incoming <- query
				synctest.Wait()
				select {
				case <-entered:
				default:
					t.Fatal("consumer did not reach the held query")
				}
				// Periodic writes satisfy the old progress predicate while
				// the transport is drained but the consumer still has work.
				if tr.count() <= before || tr.count() != 4 || len(tr.incoming) != 0 || s.Err() != nil {
					t.Fatal("did not reproduce the periodic-write boundary")
				}
				if premature {
					clock.advance(time.Second)
				}
				release()
				synctest.Wait()
				want := 4 // The query belongs to the periodic advertisement's window.
				if premature {
					want = 5 // An unfinished query incorrectly consumes the next window.
				}
				if got := tr.count(); got != want || s.Err() != nil {
					t.Fatalf("writes = %d, want %d; error = %v", got, want, s.Err())
				}
			})
		})
	}
}
