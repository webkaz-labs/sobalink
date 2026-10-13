package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/webkaz-labs/sobalink/internal/control"
	"github.com/webkaz-labs/sobalink/internal/core"
	"github.com/webkaz-labs/sobalink/internal/identity"
	"github.com/webkaz-labs/sobalink/internal/webui"
)

type outboxIPCDialCategory uint8

const (
	outboxIPCDialOther outboxIPCDialCategory = iota
	outboxIPCDialDiscovery
	outboxIPCDialOutbox
	outboxIPCDialBound  = 64
	outboxIPCProbeFrame = "github.com/webkaz-labs/sobalink/internal/core.(*Core).probePeer"
	outboxIPCSendFrame  = "github.com/webkaz-labs/sobalink/internal/core.(*Core).deliverOutbox"
)

// The production request owner deliberately strips caller context values. Its
// backend dial is synchronous, so this test-only classifier can distinguish the
// two real callers even though both start with GET /v1/hello. Renamed, missing,
// or truncated caller evidence fails closed; no raw frames are retained/logged.
func outboxIPCClassifyDial(frames []string, truncated bool) outboxIPCDialCategory {
	if truncated || len(frames) > outboxIPCDialBound {
		return outboxIPCDialOther
	}
	discovery := false
	for _, frame := range frames {
		if frame == outboxIPCSendFrame {
			return outboxIPCDialOutbox // Forbidden activity dominates discovery.
		}
		discovery = discovery || frame == outboxIPCProbeFrame
	}
	if discovery {
		return outboxIPCDialDiscovery
	}
	return outboxIPCDialOther
}

func outboxIPCCurrentDialCategory() outboxIPCDialCategory {
	var pcs [outboxIPCDialBound]uintptr
	n := runtime.Callers(2, pcs[:])
	if n == len(pcs) {
		return outboxIPCDialOther
	}
	frames := runtime.CallersFrames(pcs[:n])
	var names [outboxIPCDialBound]string
	for i := range names {
		frame, more := frames.Next()
		names[i] = frame.Function
		if !more {
			return outboxIPCClassifyDial(names[:i+1], false)
		}
	}
	return outboxIPCDialOther
}

type outboxIPCDialCounts struct {
	Total, Discovery, Outbox, Other uint32
	Overflow                        bool
}

func (c outboxIPCDialCounts) discoveryOnly() bool {
	return !c.Overflow && c.Outbox == 0 && c.Other == 0 && c.Total == c.Discovery
}

type outboxIPCDialLog struct {
	mu      sync.Mutex
	counts  outboxIPCDialCounts
	changed chan struct{}
}

func (d *outboxIPCDialLog) record(category outboxIPCDialCategory) {
	d.mu.Lock()
	if d.counts.Total == outboxIPCDialBound {
		d.counts.Overflow = true
	} else {
		d.counts.Total++
		switch category {
		case outboxIPCDialDiscovery:
			d.counts.Discovery++
		case outboxIPCDialOutbox:
			d.counts.Outbox++
		default:
			d.counts.Other++
		}
	}
	d.mu.Unlock()
	select {
	case d.changed <- struct{}{}:
	default:
	}
}

func (d *outboxIPCDialLog) snapshot() outboxIPCDialCounts {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.counts
}

func (d *outboxIPCDialLog) requireDiscoveryOnly(t *testing.T) {
	t.Helper()
	if counts := d.snapshot(); !counts.discoveryOnly() {
		t.Fatalf("unexpected offline dial activity: %+v", counts)
	}
}

func (d *outboxIPCDialLog) wait(t *testing.T, ctx context.Context, ready func(outboxIPCDialCounts) bool) {
	t.Helper()
	for !ready(d.snapshot()) {
		select {
		case <-d.changed:
		case <-ctx.Done():
			t.Fatalf("dial observation deadline: %+v", d.snapshot())
		}
	}
}

func TestOutboxCLIDialClassifierFailsClosed(t *testing.T) {
	for _, tt := range []struct {
		name      string
		frames    []string
		truncated bool
		want      outboxIPCDialCategory
	}{
		{"discovery", []string{outboxIPCProbeFrame}, false, outboxIPCDialDiscovery},
		{"outbox", []string{outboxIPCSendFrame}, false, outboxIPCDialOutbox},
		{"mixed-discovery-first", []string{outboxIPCProbeFrame, outboxIPCSendFrame}, false, outboxIPCDialOutbox},
		{"mixed-outbox-first", []string{outboxIPCSendFrame, outboxIPCProbeFrame}, false, outboxIPCDialOutbox},
		{"unknown", []string{"synthetic.unknown"}, false, outboxIPCDialOther},
		{"near-name", []string{outboxIPCProbeFrame + "Unknown"}, false, outboxIPCDialOther},
		{"missing", nil, false, outboxIPCDialOther},
		{"truncated-discovery", []string{outboxIPCProbeFrame}, true, outboxIPCDialOther},
		{"truncated-outbox", []string{outboxIPCSendFrame}, true, outboxIPCDialOther},
		{"overflow", make([]string, outboxIPCDialBound+1), false, outboxIPCDialOther},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := outboxIPCClassifyDial(tt.frames, tt.truncated)
			var d outboxIPCDialLog
			d.record(got)
			if got != tt.want || d.snapshot().discoveryOnly() != (tt.want == outboxIPCDialDiscovery) {
				t.Fatal("dial category or offline assertion failed closed classification")
			}
		})
	}
	var d outboxIPCDialLog
	for range outboxIPCDialBound + 1 {
		d.record(outboxIPCDialDiscovery)
	}
	if counts := d.snapshot(); !counts.Overflow || counts.Total != outboxIPCDialBound || counts.discoveryOnly() {
		t.Fatal("bounded dial accounting accepted overflow")
	}
	if (outboxIPCDialCounts{Total: 1}).discoveryOnly() {
		t.Fatal("offline assertion ignored an unaccounted dial")
	}
}

func TestOutboxCLIDialTrapRejectsUnknown(t *testing.T) {
	backend := &outboxIPCBackend{}
	conn, err := backend.DialIP(context.Background(), "tcp", netip.MustParseAddrPort("100.64.0.2:54543"))
	counts := backend.dials.snapshot()
	if conn != nil || err == nil || counts.Total != 1 || counts.Other != 1 || counts.discoveryOnly() {
		t.Fatal("direct unknown caller escaped the offline dial assertion")
	}
}

// This is only for the new online trap control. A closed pre-admission busy
// refusal can overlap maintenance; retry the same encoded commit, never a new
// preparation or another error. The original CLI contract keeps exact counts.
func outboxIPCTestCommit(ctx context.Context, invoke func(context.Context) (any, error)) (any, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		value, err := invoke(ctx)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var coded interface{ ErrorCode() string }
		if !errors.As(err, &coded) || coded.ErrorCode() != "outbox_storage_busy" {
			return value, err
		}
		timer := time.NewTimer(20 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func TestOutboxCLIDialCommitControlRetry(t *testing.T) {
	busy := &control.RemoteError{Code: "outbox_storage_busy"}
	t.Run("exact-busy", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			calls, want := 0, new(int)
			got, err := outboxIPCTestCommit(context.Background(), func(context.Context) (any, error) {
				calls++
				if calls <= 2 {
					return nil, busy
				}
				return want, nil
			})
			if err != nil || got != want || calls != 3 {
				t.Fatal("exact busy retry lost the original successful result")
			}
		})
	})
	t.Run("other-errors", func(t *testing.T) {
		for _, want := range []error{errors.New("outbox_storage_busy"), &control.RemoteError{Code: "outbox_storage_uncertain"}, &control.RemoteError{Code: "outbox_stale"}, &control.RemoteError{Code: "outbox_storage_busy_extra"}} {
			calls := 0
			_, got := outboxIPCTestCommit(context.Background(), func(context.Context) (any, error) { calls++; return nil, want })
			if got != want || calls != 1 {
				t.Fatal("commit control retried an unapproved error")
			}
		}
	})
	for _, outer := range []time.Duration{10 * time.Millisecond, 5 * time.Second} {
		t.Run("deadline", func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), outer)
				defer cancel()
				start, calls := time.Now(), 0
				_, err := outboxIPCTestCommit(ctx, func(context.Context) (any, error) { calls++; return nil, busy })
				if !errors.Is(err, context.DeadlineExceeded) || calls == 0 || time.Since(start) != min(outer, 2*time.Second) {
					t.Fatal("busy retry escaped the earlier deadline")
				}
			})
		})
	}
}

// Only this positive control advertises online peers. Its inherited DialIP is
// still a hard-deny stub; actual delivery cannot occur.
type outboxIPCOnlineBackend struct{ *outboxIPCBackend }

func (b *outboxIPCOnlineBackend) State(ctx context.Context) (identity.State, error) {
	state, err := b.outboxIPCBackend.State(ctx)
	for i := range state.Snapshot.Peers {
		state.Snapshot.Peers[i].Online = true
	}
	return state, err
}

func TestOutboxCLIDialTrapRejectsRealOutbox(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	backend := &outboxIPCOnlineBackend{&outboxIPCBackend{}}
	backend.dials.changed = make(chan struct{}, 1)
	_, app := dispatchContractCore(t, ctx, func(profile *core.Profile) core.NetworkBackend {
		backend.configure(profile)
		return backend
	})
	snapshot, err := app.Snapshot(ctx)
	if err != nil {
		t.Fatal("online control snapshot unavailable")
	}
	source := filepath.Join(t.TempDir(), "example.txt")
	if os.WriteFile(source, []byte("synthetic payload"), 0600) != nil {
		t.Fatal("online control source unavailable")
	}
	id, err := newOutboxCLIRequestID(time.Now())
	if err != nil {
		t.Fatal("online control request unavailable")
	}
	command := func(name string, payload map[string]any) core.OutboxRecord {
		t.Helper()
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal("online control payload unavailable")
		}
		cmd := webui.Command{Name: name, RequestID: id, Payload: raw}
		invoke := func(ctx context.Context) (any, error) { return app.Command(ctx, cmd) }
		var value any
		if name == "outbox.commit" {
			value, err = outboxIPCTestCommit(ctx, invoke)
		} else {
			value, err = invoke(ctx)
		}
		if err != nil {
			t.Fatal("online control command failed")
		}
		return value.(core.OutboxRecord)
	}
	review := command("outbox.prepare", map[string]any{"processId": snapshot["resourceCatalogProcessId"], "peerIds": []string{"peer-a"}, "paths": []string{source}})
	command("outbox.commit", map[string]any{"outboxId": id, "processId": review.ProcessID, "reviewRevision": review.ReviewRevision, "confirm": true})
	backend.dials.wait(t, ctx, func(counts outboxIPCDialCounts) bool {
		return counts.Outbox > 0 || counts.Other > 0 || counts.Overflow
	})
	counts := backend.dials.snapshot()
	if counts.Outbox == 0 || counts.Other != 0 || counts.Overflow || counts.Total != counts.Discovery+counts.Outbox || counts.discoveryOnly() {
		t.Fatalf("real outbox caller escaped the offline assertion: %+v", counts)
	}
	t.Logf("online outbox rejected by offline assertion: %+v", counts)
}
