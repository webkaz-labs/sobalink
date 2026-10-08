package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/directlan"
	"github.com/webkaz-labs/sobalink/internal/endpointmeta"
)

// Retry bookkeeping only. Callbacks return data/sentinels, never native owners,
// signed movement, sockets, a simulated transport, or publication receipts.
func TestEndpointDeliveryRetryBusyThenReply(t *testing.T) {
	for _, outcome := range []string{"already_applied", "review_required", "accepted_inactive"} {
		t.Run(outcome, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls, waits := 0, 0
			reply := &endpointmeta.UpdateReply{Outcome: outcome}
			got, attempts, err := runEndpointDeliveryAttempts(ctx, func(gotCtx context.Context) (*endpointmeta.UpdateReply, error) {
				if gotCtx != ctx {
					t.Fatal("original operation context replaced")
				}
				calls++
				if calls == 1 {
					return nil, directlan.ErrUnavailable
				}
				return reply, nil
			}, func(gotCtx context.Context, delay time.Duration) error {
				if gotCtx != ctx || delay != 250*time.Millisecond {
					t.Fatal("first wait/context changed")
				}
				waits++
				return nil
			})
			if err != nil || got != reply || calls != 2 || waits != 1 || attempts != 2 {
				t.Fatal("substantive reply retried or lost", calls, waits, attempts, err)
			}
		})
	}
}
func TestEndpointDeliveryRetryPersistentBusyBounded(t *testing.T) {
	calls := 0
	var waits []time.Duration
	_, attempts, err := runEndpointDeliveryAttempts(context.Background(), func(context.Context) (*endpointmeta.UpdateReply, error) {
		calls++
		return nil, directlan.ErrUnavailable
	}, func(_ context.Context, delay time.Duration) error { waits = append(waits, delay); return nil })
	if calls != 8 || attempts != 8 || len(waits) != 7 || !errors.Is(err, directlan.ErrUnavailable) {
		t.Fatal("retry bound changed", calls, attempts, len(waits), err)
	}
	var total time.Duration
	for i, delay := range waits {
		if delay != endpointDeliveryBackoff[i] {
			t.Fatal("backoff changed")
		}
		total += delay
	}
	if total != 15750*time.Millisecond {
		t.Fatal("unexpected total fixed wait", total)
	}
}
func TestEndpointDeliveryRetryCancellationAndEpoch(t *testing.T) {
	t.Run("cancelled-before", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		calls := 0
		_, attempts, err := runEndpointDeliveryAttempts(ctx, func(context.Context) (*endpointmeta.UpdateReply, error) { calls++; return nil, io.EOF }, func(context.Context, time.Duration) error { return nil })
		if !errors.Is(err, context.Canceled) || calls != 0 || attempts != 0 {
			t.Fatal("cancelled operation attempted")
		}
	})
	t.Run("cancelled-wait", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		calls := 0
		_, attempts, err := runEndpointDeliveryAttempts(ctx, func(context.Context) (*endpointmeta.UpdateReply, error) { calls++; return nil, io.EOF }, func(context.Context, time.Duration) error { cancel(); return ctx.Err() })
		if !errors.Is(err, context.Canceled) || calls != 1 || attempts != 1 {
			t.Fatal("cancelled wait retried")
		}
	})
	t.Run("original-deadline", func(t *testing.T) {
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancel()
		_, attempts, err := runEndpointDeliveryAttempts(ctx, func(context.Context) (*endpointmeta.UpdateReply, error) {
			t.Fatal("expired callback called")
			return nil, nil
		}, waitEndpointDeliveryRetry)
		if !errors.Is(err, context.DeadlineExceeded) || attempts != 0 {
			t.Fatal("original deadline renewed")
		}
	})
	t.Run("epoch-changed", func(t *testing.T) {
		epoch := directlan.NewContextEpoch()
		calls := 0
		_, attempts, err := runEndpointDeliveryAttempts(context.Background(), func(context.Context) (*endpointmeta.UpdateReply, error) {
			calls++
			if !epoch.Valid() {
				return nil, directlan.ErrUntrusted
			}
			return nil, io.EOF
		}, func(context.Context, time.Duration) error { epoch.Invalidate(); return nil })
		if !errors.Is(err, directlan.ErrUntrusted) || calls != 2 || attempts != 2 {
			t.Fatal("changed authority retried")
		}
	})
}
func TestEndpointDeliveryRetryErrorClassification(t *testing.T) {
	for _, err := range []error{directlan.ErrUnavailable, directlan.ErrCapacity, io.EOF, io.ErrUnexpectedEOF, fmt.Errorf("bounded wrapper: %w", io.EOF), errors.Join(io.EOF, directlan.ErrUnavailable)} {
		if !endpointDeliveryRetryable(err) {
			t.Fatal("transient classification lost")
		}
	}
	for _, err := range []error{nil, context.Canceled, context.DeadlineExceeded, endpointmeta.ErrExpired, endpointmeta.ErrReview, directlan.ErrUntrusted, directlan.ErrPolicy, directlan.ErrRecovery, directlan.ErrRetirementIncomplete, errors.Join(io.EOF, directlan.ErrRetirementIncomplete), errors.Join(directlan.ErrUnavailable, endpointmeta.ErrExpired), errors.New("unknown failure")} {
		if endpointDeliveryRetryable(err) {
			t.Fatal("terminal error retried")
		}
		if err != nil {
			calls := 0
			_, attempts, got := runEndpointDeliveryAttempts(context.Background(), func(context.Context) (*endpointmeta.UpdateReply, error) { calls++; return nil, err }, func(context.Context, time.Duration) error { t.Fatal("terminal error waited"); return nil })
			if got == nil || calls != 1 || attempts != 1 {
				t.Fatal("terminal error not stopped")
			}
		}
	}
}
