package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"testing/synctest"
	"time"
)

// A lifecycle owner can refuse an initial or already-admitted commit before
// entering its callback. Retry only that closed pre-admission code, through the same
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

func TestOutboxProcessCommitRetryInitialAdmission(t *testing.T) {
	// Hold ownership until two commands have been refused, independent of
	// scheduler timing. Pure helper tests below cover the exact retry deadlines.
	c := outboxFixture(t)
	review := outboxPrepareFixture(t, c, "busy-initial-admission")
	o := c.outbox
	now := o.now()
	o.now = func() time.Time { return now }
	prepared := o.prepared[review.OutboxID]
	if prepared == nil || len(o.state.Intents) != 0 || len(o.state.Payloads) != 1 {
		t.Fatal("initial retry fixture already admitted or replaced its preparation")
	}
	marshal := func(value any) []byte {
		t.Helper()
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal("synthetic initial retry state could not be encoded")
		}
		return raw
	}
	read := func(path string) []byte {
		t.Helper()
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal("synthetic initial retry state could not be read")
		}
		return raw
	}
	payload := marshal(map[string]any{"processId": review.ProcessID, "outboxId": review.OutboxID, "reviewRevision": review.ReviewRevision, "confirm": true})
	originalPayload := append([]byte(nil), payload...)
	before, beforeReview := marshal(o.state), marshal(prepared.intent)
	statePath := filepath.Join(c.dir, outboxNamespace, "state.json")
	stagedPath := filepath.Join(c.dir, outboxNamespace, "payloads", prepared.intent.PayloadID, review.Entries[0].ID)
	disk, staged := read(statePath), read(stagedPath)
	certified := append([]byte(nil), o.confirmedDisk...)
	release := holdOutboxLifecycle(t, c)
	defer func() {
		if release != nil {
			release()
		}
	}()
	entered := false
	o.mu.Lock()
	err := o.withOwnedLocked(func() error { entered = true; return nil })
	o.mu.Unlock()
	if entered || networkErrorCode(err) != "outbox_storage_busy" {
		t.Fatal("busy initial ownership entered its callback")
	}
	calls := 0
	invoke := func() (json.RawMessage, string) {
		calls++
		if !bytes.Equal(payload, originalPayload) {
			t.Fatal("initial retry changed the original command payload")
		}
		value, err := c.outboxCommand(context.Background(), "outbox.commit", "synthetic-commit", payload)
		if calls <= 2 {
			if value != nil || networkErrorCode(err) != "outbox_storage_busy" {
				t.Fatal("held lifecycle did not refuse initial admission")
			}
			if !bytes.Equal(before, marshal(o.state)) || !bytes.Equal(beforeReview, marshal(prepared.intent)) || !bytes.Equal(disk, read(statePath)) || !bytes.Equal(staged, read(stagedPath)) || !bytes.Equal(certified, o.confirmedDisk) || o.prepared[review.OutboxID] != prepared || prepared.ctx.Err() != nil || prepared.staging || prepared.discarded || o.frozen != "" {
				t.Fatal("busy initial admission changed preparation, payload or durable evidence")
			}
			if calls == 2 {
				release()
				release = nil
			}
		}
		if err != nil {
			return nil, "outbox.commit: " + networkErrorCode(err)
		}
		return marshal(value), ""
	}
	reply, code, err := retryOutboxProcessCommit(context.Background(), invoke)
	if err != nil || code != "" || calls != 3 {
		t.Fatal("initial admission did not use the bounded exact-busy retry")
	}
	want := cloneOutboxRecord(review)
	want.ProcessID, want.Admission, want.Status, want.UpdatedAt = "", "enqueued", "queued", now.UTC()
	for i := range want.Children {
		want.Children[i].Status = "queued"
	}
	var got OutboxRecord
	if json.Unmarshal(reply, &got) != nil || !reflect.DeepEqual(got, want) || len(o.state.Intents) != 1 || len(o.state.Payloads) != 1 || len(o.prepared) != 0 || o.state.Intents[review.OutboxID] != prepared.intent {
		t.Fatal("released initial retry changed the original review or duplicated admission")
	}
	admitted, admittedDisk := marshal(o.state), read(statePath)
	repeated, code, err := retryOutboxProcessCommit(context.Background(), invoke)
	if err != nil || code != "" || calls != 4 || !bytes.Equal(reply, repeated) || !bytes.Equal(payload, originalPayload) || !bytes.Equal(admitted, marshal(o.state)) || !bytes.Equal(admittedDisk, read(statePath)) || !bytes.Equal(staged, read(stagedPath)) {
		t.Fatal("same-payload replay changed initial admission or its staged bytes")
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
