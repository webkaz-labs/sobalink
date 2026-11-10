package core

import (
	"context"
	"encoding/json"
	"testing"
	"testing/synctest"

	_ "embed"
	"github.com/webkaz-labs/sobalink/internal/webui"
	"go/ast"
	"go/parser"
	"go/token"
	"runtime"
	"sync"
	"time"
)

// The observer is compiled only into test executables. A fixed number of
// bounded profiles becomes closed counters; PCs, source names, arguments and
// goroutine identities are never returned. Sampling perturbs scheduling and is
// evidence of observed pre-admission waits, not their total duration or ownership.
// The runtime profile is synchronous and cannot be cancelled once started: the
// sample count and record capacity are bounded, not its wall-clock duration.
//
//go:embed outbox_commands.go
var outboxCommitObservationSource string

const outboxWaitProfileLimit = 4096

type outboxProcessWaitObservation struct {
	Samples, Unavailable, CommandMissing, CoreOp, CoreMu, OutboxMu, Other int
	MaintenanceRefresh, ReceiverMaintenance                               int
	SamplerUS, JoinUS                                                     int64
}

type outboxCommitLockLines struct{ op, outbox int }

func outboxCommitWaitLines() outboxCommitLockLines {
	set := token.NewFileSet()
	file, err := parser.ParseFile(set, "source.go", outboxCommitObservationSource, 0)
	if err != nil {
		return outboxCommitLockLines{}
	}
	var lines outboxCommitLockLines
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "commitOutbox" || fn.Body == nil {
			continue
		}
		for _, statement := range fn.Body.List {
			expression, ok := statement.(*ast.ExprStmt)
			if !ok {
				continue
			}
			call, ok := expression.X.(*ast.CallExpr)
			if !ok {
				continue
			}
			method, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || method.Sel.Name != "Lock" {
				continue
			}
			field, ok := method.X.(*ast.SelectorExpr)
			if !ok {
				continue
			}
			owner, ok := field.X.(*ast.Ident)
			if !ok {
				continue
			}
			switch {
			case owner.Name == "c" && field.Sel.Name == "op":
				lines.op = set.Position(call.Pos()).Line
			case owner.Name == "o" && field.Sel.Name == "mu":
				lines.outbox = set.Position(call.Pos()).Line
			}
		}
	}
	return lines
}

func outboxProcessWaitSample(records []runtime.StackRecord, available bool, lines outboxCommitLockLines) outboxProcessWaitObservation {
	var sample outboxProcessWaitObservation
	sample.Samples = 1
	if !available || lines.op == 0 || lines.outbox == 0 {
		sample.Unavailable = 1
		return sample
	}
	const core = "github.com/webkaz-labs/sobalink/internal/core.(*Core)."
	commands := 0
	for _, record := range records {
		frames := runtime.CallersFrames(record.Stack())
		command, outbox, mutex, op, mu, coordinator, maintenance, refresh, receiver := false, false, false, false, false, false, false, false, false
		for {
			frame, more := frames.Next()
			switch frame.Function {
			case core + "Command":
				command = true
			case core + "outboxCommand":
				outbox = true
			case "sync.runtime_SemacquireMutex", "sync.runtime_SemacquireRWMutexR", "sync.runtime_SemacquireRWMutex", "internal/sync.runtime_SemacquireMutex":
				mutex = true
			case core + "commitOutbox":
				op = frame.Line == lines.op
				coordinator = frame.Line == lines.outbox
			// Only known pre-admission Core.mu sites are attributed. Deeper
			// callback waits remain Other rather than guessed ownership.
			case core + "outboxCopy", core + "durableProfileReady", core + "beginWork":
				mu = true
			case core + "maintain":
				maintenance = true
			case core + "refreshPeers":
				refresh = true
			case "github.com/webkaz-labs/sobalink/internal/transfer.(*Manager).MaintainDurable":
				receiver = true
			}
			if !more {
				break
			}
		}
		if maintenance && refresh {
			sample.MaintenanceRefresh = 1
		}
		if maintenance && receiver {
			sample.ReceiverMaintenance = 1
		}
		if !command || !outbox {
			continue
		}
		commands++
		switch {
		case mutex && op:
			sample.CoreOp++
		case mutex && mu:
			sample.CoreMu++
		case mutex && coordinator:
			sample.OutboxMu++
		default:
			sample.Other++
		}
	}
	if commands != 1 {
		sample.CoreOp, sample.CoreMu, sample.OutboxMu, sample.Other = 0, 0, 0, 0
		if commands == 0 {
			sample.CommandMissing = 1
		} else {
			sample.Unavailable = 1
		}
	}
	return sample
}

func (o *outboxProcessWaitObservation) add(sample outboxProcessWaitObservation) {
	o.Samples += sample.Samples
	o.Unavailable += sample.Unavailable
	o.CommandMissing += sample.CommandMissing
	o.CoreOp += sample.CoreOp
	o.CoreMu += sample.CoreMu
	o.OutboxMu += sample.OutboxMu
	o.Other += sample.Other
	o.MaintenanceRefresh += sample.MaintenanceRefresh
	o.ReceiverMaintenance += sample.ReceiverMaintenance
	o.SamplerUS += sample.SamplerUS
	o.JoinUS += sample.JoinUS
}

func observeOutboxProcessWait() func() outboxProcessWaitObservation {
	return observeOutboxProcessWaitUsing(runtime.GoroutineProfile)
}

func observeOutboxProcessWaitUsing(profile func([]runtime.StackRecord) (int, bool)) func() outboxProcessWaitObservation {
	stop, done := make(chan struct{}), make(chan struct{})
	var once sync.Once
	var result outboxProcessWaitObservation
	go func() {
		defer close(done)
		// Stop before a sample avoids source parsing and profile allocation.
		var lines outboxCommitLockLines
		var records []runtime.StackRecord
		for _, delay := range []time.Duration{100 * time.Millisecond, 400 * time.Millisecond, 500 * time.Millisecond, 500 * time.Millisecond} {
			timer := time.NewTimer(delay)
			select {
			case <-stop:
				timer.Stop()
				return
			case <-timer.C:
			}
			// Prefer a completed command when the timer and stop race. An active
			// runtime profile still has to finish before the observer can join.
			select {
			case <-stop:
				return
			default:
			}
			started := time.Now()
			if records == nil {
				lines = outboxCommitWaitLines()
				records = make([]runtime.StackRecord, outboxWaitProfileLimit)
			}
			n, ok := profile(records)
			if n > len(records) {
				n, ok = 0, false
			}
			sample := outboxProcessWaitSample(records[:n], ok, lines)
			sample.SamplerUS = time.Since(started).Microseconds()
			result.add(sample)
		}
	}()
	return func() outboxProcessWaitObservation {
		once.Do(func() { close(stop) })
		<-done
		return result
	}
}

func TestOutboxProcessWaitObservedRealLocks(t *testing.T) {
	for _, kind := range []string{"core-op", "core-mu", "outbox-mu"} {
		t.Run(kind, func(t *testing.T) {
			c := outboxFixture(t)
			review := outboxPrepareFixture(t, c, "synthetic-wait")
			payload, err := json.Marshal(map[string]any{"processId": review.ProcessID, "outboxId": review.OutboxID, "reviewRevision": review.ReviewRevision, "confirm": true})
			if err != nil {
				t.Fatal("synthetic command encoding failed")
			}
			var unlock func()
			switch kind {
			case "core-op":
				c.op.Lock()
				unlock = c.op.Unlock
			case "core-mu":
				c.mu.Lock()
				unlock = c.mu.Unlock
			case "outbox-mu":
				c.outbox.mu.Lock()
				unlock = c.outbox.mu.Unlock
			}
			done := make(chan error, 1)
			go func() {
				_, err := c.Command(context.Background(), webui.Command{RequestID: "synthetic", Name: "outbox.commit", Payload: payload})
				done <- err
			}()
			records := make([]runtime.StackRecord, outboxWaitProfileLimit)
			lines := outboxCommitWaitLines()
			found := false
			until := time.Now().Add(2 * time.Second)
			for time.Now().Before(until) {
				n, ok := runtime.GoroutineProfile(records)
				if !ok || n > len(records) {
					break
				}
				sample := outboxProcessWaitSample(records[:n], ok, lines)
				found = kind == "core-op" && sample.CoreOp == 1 || kind == "core-mu" && sample.CoreMu == 1 || kind == "outbox-mu" && sample.OutboxMu == 1
				if found {
					break
				}
				runtime.Gosched()
			}
			unlock()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal("released original command failed")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("released original command did not finish")
			}
			if !found {
				t.Fatal("held real lock was not classified")
			}
			if len(c.outbox.state.Intents) != 1 || len(c.outbox.prepared) != 0 {
				t.Fatal("observer changed original admission")
			}
		})
	}
}

func TestOutboxProcessWaitUnavailableAndMissing(t *testing.T) {
	lines := outboxCommitWaitLines()
	if lines.op == 0 || lines.outbox == 0 || lines.op == lines.outbox {
		t.Fatal("source lock mapping unavailable")
	}
	for _, sample := range []outboxProcessWaitObservation{
		outboxProcessWaitSample(nil, false, lines),
		outboxProcessWaitSample(nil, true, outboxCommitLockLines{}),
	} {
		if sample.Samples != 1 || sample.Unavailable != 1 || sample.CoreOp+sample.CoreMu+sample.OutboxMu+sample.Other != 0 {
			t.Fatal("unavailable sample fabricated a wait")
		}
	}
	sample := outboxProcessWaitSample(nil, true, lines)
	if sample.CommandMissing != 1 || sample.Unavailable != 0 {
		t.Fatal("missing command became a wait")
	}
}

func TestOutboxProcessWaitQuickStop(t *testing.T) {
	stop := observeOutboxProcessWait()
	got := stop()
	if got.Samples != 0 || got != stop() {
		t.Fatal("quick or repeated stop sampled or changed results")
	}
}

func TestOutboxProcessWaitSampleLimit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		calls, fourth := 0, make(chan struct{})
		stop := observeOutboxProcessWaitUsing(func(_ []runtime.StackRecord) (int, bool) {
			calls++
			if calls == 4 {
				close(fourth)
			}
			return 0, true
		})
		defer stop()
		select {
		case <-fourth:
		case <-time.After(3 * time.Second):
			t.Fatal("four fixed samples did not finish")
		}
		got := stop()
		if calls != 4 || got.Samples != 4 || got.CommandMissing != 4 {
			t.Fatal("fixed sample limit or missing classification changed")
		}
	})
}

func TestOutboxProcessWaitStopJoinsActiveProfile(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered, release := make(chan struct{}), make(chan struct{})
		stop := observeOutboxProcessWaitUsing(func(records []runtime.StackRecord) (int, bool) {
			close(entered)
			<-release
			return len(records) + 1, true
		})
		select {
		case <-entered:
		case <-time.After(2 * time.Second):
			close(release)
			stop()
			t.Fatal("profile did not start")
		}
		done := make(chan outboxProcessWaitObservation, 1)
		go func() { done <- stop() }()
		synctest.Wait()
		select {
		case <-done:
			close(release)
			t.Fatal("stop failed to join active profile")
		default:
		}
		close(release)
		select {
		case got := <-done:
			if got.Samples != 1 || got.Unavailable != 1 || got.CoreOp+got.CoreMu+got.OutboxMu+got.Other != 0 {
				t.Fatal("capacity overflow fabricated a wait or another sample")
			}
		case <-time.After(2 * time.Second):
			t.Fatal("released profile did not join")
		}
	})
}

func TestOutboxProcessWaitRejectsMultipleCommands(t *testing.T) {
	c := outboxFixture(t)
	review := outboxPrepareFixture(t, c, "synthetic-ambiguity")
	payload, err := json.Marshal(map[string]any{"processId": review.ProcessID, "outboxId": review.OutboxID, "reviewRevision": review.ReviewRevision, "confirm": true})
	if err != nil {
		t.Fatal("synthetic command encoding failed")
	}
	c.op.Lock()
	done := make(chan error, 2)
	for range 2 {
		go func() {
			_, err := c.Command(context.Background(), webui.Command{RequestID: "synthetic", Name: "outbox.commit", Payload: payload})
			done <- err
		}()
	}
	records := make([]runtime.StackRecord, outboxWaitProfileLimit)
	lines := outboxCommitWaitLines()
	found := false
	until := time.Now().Add(2 * time.Second)
	for time.Now().Before(until) {
		n, ok := runtime.GoroutineProfile(records)
		if !ok || n > len(records) {
			break
		}
		sample := outboxProcessWaitSample(records[:n], ok, lines)
		found = sample.Unavailable == 1 && sample.CoreOp+sample.CoreMu+sample.OutboxMu+sample.Other == 0
		if found {
			break
		}
		runtime.Gosched()
	}
	c.op.Unlock()
	for range 2 {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal("released duplicate command failed")
			}
		case <-time.After(2 * time.Second):
			t.Fatal("duplicate command did not finish")
		}
	}
	if !found || len(c.outbox.state.Intents) != 1 {
		t.Fatal("ambiguous commands fabricated a wait or changed admission")
	}
}

func TestOutboxProcessWaitRealSamplerCost(t *testing.T) {
	c := outboxFixture(t)
	review := outboxPrepareFixture(t, c, "synthetic-sampled-wait")
	payload, err := json.Marshal(map[string]any{"processId": review.ProcessID, "outboxId": review.OutboxID, "reviewRevision": review.ReviewRevision, "confirm": true})
	if err != nil {
		t.Fatal("synthetic command encoding failed")
	}
	c.op.Lock()
	done := make(chan error, 1)
	go func() {
		_, err := c.Command(context.Background(), webui.Command{RequestID: "synthetic", Name: "outbox.commit", Payload: payload})
		done <- err
	}()
	captured := make(chan struct{})
	var once sync.Once
	stop := observeOutboxProcessWaitUsing(func(records []runtime.StackRecord) (int, bool) {
		n, ok := runtime.GoroutineProfile(records)
		once.Do(func() { close(captured) })
		return n, ok
	})
	select {
	case <-captured:
	case <-time.After(2 * time.Second):
		c.op.Unlock()
		stop()
		<-done
		t.Fatal("real sample did not complete")
	}
	joinStarted := time.Now()
	got := stop()
	joinUS := time.Since(joinStarted).Microseconds()
	c.op.Unlock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal("observed original command failed")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("observed original command did not finish")
	}
	if got.Samples != 1 || got.CoreOp != 1 || got.Unavailable != 0 {
		t.Fatal("real sampled lock was not classified")
	}
	t.Logf("synthetic observer cost: sample_count=%d sampler_work_us=%d sampler_join_us=%d", got.Samples, got.SamplerUS, joinUS)
}
