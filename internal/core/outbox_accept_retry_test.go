package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"testing/synctest"
	"time"
)

// The original route-process child gives each command 30 seconds. Bound all
// acceptance attempts together by that existing budget and the outer context;
// the invoking parent also uses this context to bound an in-flight response.
func retryOutboxProcessAccept(ctx context.Context, invoke func(context.Context) (json.RawMessage, string)) (json.RawMessage, string, error) {
	retry, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var value json.RawMessage
	var code string
	for {
		if err := retry.Err(); err != nil {
			return value, code, err
		}
		value, code = invoke(retry)
		if err := retry.Err(); err != nil {
			return value, code, err
		}
		if code != "transfer.accept: transfer_accept_busy" {
			return value, code, nil
		}
		timer := time.NewTimer(20 * time.Millisecond)
		select {
		case <-retry.Done():
			timer.Stop()
			return value, code, retry.Err()
		case <-timer.C:
		}
	}
}

func TestOutboxProcessAcceptRetryOnlyExactTypedBusy(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		calls := 0
		want := json.RawMessage(`{"state":"accepted"}`)
		var originalDeadline time.Time
		value, code, err := retryOutboxProcessAccept(context.Background(), func(ctx context.Context) (json.RawMessage, string) {
			calls++
			deadline, ok := ctx.Deadline()
			if !ok || calls > 1 && deadline != originalDeadline {
				t.Fatal("retry extended the original command budget")
			}
			originalDeadline = deadline
			if calls <= 2 {
				return nil, "transfer.accept: transfer_accept_busy"
			}
			return want, ""
		})
		if err != nil || code != "" || calls != 3 || !bytes.Equal(value, want) {
			t.Fatal("exact typed busy retry changed the successful reply")
		}
	})
	for _, code := range []string{"transfer.accept: busy", "transfer.accept: transfer_accept_unconfirmed", "transfer.accept: durable_recovery_required", "transfer.accept: disk_space_low", "transfer.accept: failed", "transfer.accept: transfer_accept_busy extra", "transfer.accept: transfer_accept_busy\nsynthetic", "transfer.cancel: transfer_accept_busy", "subprocess response unavailable", ""} {
		calls := 0
		want := json.RawMessage(`{"state":"pending"}`)
		value, actual, err := retryOutboxProcessAccept(context.Background(), func(context.Context) (json.RawMessage, string) { calls++; return want, code })
		if err != nil || actual != code || calls != 1 || !bytes.Equal(value, want) {
			t.Fatal("acceptance retried or changed a non-busy result")
		}
	}
}

func TestOutboxProcessAcceptRetryKeepsOriginalBudget(t *testing.T) {
	for _, outer := range []time.Duration{0, 50 * time.Millisecond} {
		synctest.Test(t, func(t *testing.T) {
			ctx := context.Background()
			limit := 30 * time.Second
			if outer != 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, outer)
				defer cancel()
				limit = outer
			}
			started, calls := time.Now(), 0
			_, _, err := retryOutboxProcessAccept(ctx, func(attempt context.Context) (json.RawMessage, string) {
				calls++
				if calls == 1 {
					return nil, "transfer.accept: transfer_accept_busy"
				}
				// Even an in-flight later attempt receives the first deadline.
				<-attempt.Done()
				return nil, "subprocess command context ended"
			})
			if !errors.Is(err, context.DeadlineExceeded) || calls != 2 || time.Since(started) != limit {
				t.Fatal("acceptance retry exceeded the original or outer budget")
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	if _, _, err := retryOutboxProcessAccept(ctx, func(context.Context) (json.RawMessage, string) { calls++; return nil, "" }); !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatal("cancelled acceptance issued a command")
	}
}
