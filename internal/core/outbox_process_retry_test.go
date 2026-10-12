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

// A lifecycle owner can refuse even an already-admitted commit before entering
// its callback. Retry only that closed pre-admission code, through the same
// serial command stream and immutable payload. This bounds retry admission;
// each command retains the existing process timeout and the outer context wins.
func retryOutboxProcessCommit(ctx context.Context, invoke func() (json.RawMessage, string)) (json.RawMessage, string, error) {
	retry, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var reply json.RawMessage
	var code string
	for {
		if err := retry.Err(); err != nil {
			return reply, code, err
		}
		reply, code = invoke()
		if err := ctx.Err(); err != nil {
			return reply, code, err
		}
		if code != "outbox.commit: outbox_storage_busy" {
			return reply, code, nil
		}
		timer := time.NewTimer(20 * time.Millisecond)
		select {
		case <-retry.Done():
			timer.Stop()
			return reply, code, retry.Err()
		case <-timer.C:
		}
	}
}

func TestOutboxProcessCommitRetryPreservesReply(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start, calls := time.Now(), 0
		want := json.RawMessage(`{"original":"synthetic-record"}`)
		got, code, err := retryOutboxProcessCommit(context.Background(), func() (json.RawMessage, string) {
			calls++
			if calls < 3 {
				return nil, "outbox.commit: outbox_storage_busy"
			}
			return want, ""
		})
		if err != nil || code != "" || calls != 3 || !bytes.Equal(got, want) || time.Since(start) != 40*time.Millisecond {
			t.Fatal("bounded busy retry changed the successful original reply")
		}
	})
}

func TestOutboxProcessCommitRetryRejectsOtherErrors(t *testing.T) {
	for _, code := range []string{
		"outbox.commit: outbox_storage_uncertain", "outbox.commit: outbox_stale", "outbox.commit: outbox_conflict",
		"outbox.cancel: outbox_storage_busy", "outbox_storage_busy", "outbox.commit: outbox_storage_busy extra",
		"subprocess response unavailable",
	} {
		t.Run(code, func(t *testing.T) {
			calls := 0
			want := json.RawMessage(`null`)
			got, gotCode, err := retryOutboxProcessCommit(context.Background(), func() (json.RawMessage, string) { calls++; return want, code })
			if err != nil || calls != 1 || !bytes.Equal(got, want) || gotCode != code {
				t.Fatal("replay retried or changed a non-busy failure")
			}
		})
	}
}

func TestOutboxProcessCommitRetryStops(t *testing.T) {
	for _, outer := range []time.Duration{0, 50 * time.Millisecond} {
		t.Run(outer.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx := context.Background()
				wantDuration := 2 * time.Second
				if outer != 0 {
					var cancel context.CancelFunc
					ctx, cancel = context.WithTimeout(ctx, outer)
					defer cancel()
					wantDuration = outer
				}
				start, calls := time.Now(), 0
				want := "outbox.commit: outbox_storage_busy"
				got, code, err := retryOutboxProcessCommit(ctx, func() (json.RawMessage, string) { calls++; return nil, want })
				if !errors.Is(err, context.DeadlineExceeded) || got != nil || code != want || calls < 2 || time.Since(start) != wantDuration {
					t.Fatal("persistent busy escaped the retry or outer deadline")
				}
			})
		})
	}
	for _, before := range []bool{true, false} {
		t.Run(map[bool]string{true: "cancel-before", false: "cancel-during"}[before], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			wantCalls := 1
			if before {
				cancel()
				wantCalls = 0
			}
			calls := 0
			_, _, err := retryOutboxProcessCommit(ctx, func() (json.RawMessage, string) {
				calls++
				cancel()
				return nil, "outbox.commit: outbox_storage_busy"
			})
			if !errors.Is(err, context.Canceled) || calls != wantCalls {
				t.Fatal("cancelled replay issued another command")
			}
		})
	}
}
