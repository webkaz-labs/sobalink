package core

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/resourcegrant"
	"github.com/webkaz-labs/sobalink/internal/resourcegroup"
)

func TestGroupCancellationAfterIntentMakesNoClientCallAndKeepsBarrier(t *testing.T) {
	record := groupStoreRecordFixture(t, 1, 1)
	record.Evidence.Members[0].Dispatch = resourcegroup.Dispatching
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c := &Core{ctx: context.Background()}
	active := &resourceGroupActivity{stop: resourcegroup.StopNone}
	row := record.Evidence.Members[0]
	attempt := c.resourceGroupDispatchAfterIntent(ctx, active, row.PeerKey, *row.Request, resourceManagementOrigin{})
	if attempt.clientInvoked || attempt.err == nil || attempt.stop != resourcegroup.StopUserCanceled {
		t.Fatal("cancellation after intent invoked a client")
	}
	record.Evidence.Members[0], _ = resourcegroup.ObserveDispatchUnknown(row, resourcegroup.LocalDurable)
	record.Admission = resourceGroupAdmissionFinished
	envelope, _ := newResourceGroupEnvelope(strings.Repeat("a", 32))
	envelope.Runs = []resourceGroupRecord{record}
	*envelope.HighWater = 1
	blockers, err := resourceGroupUnresolved(envelope, record.Review.Selection)
	if err != nil || len(blockers) != 1 {
		t.Fatal("post-intent cancellation became a repeat permission")
	}
}

func TestGroupCancelStopsKnownActiveEvenWhenStorageIsUnavailable(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	id := strings.Repeat("a", 32)
	active := &resourceGroupActivity{id: id, stop: resourcegroup.StopNone, cancel: cancel}
	c := &Core{ctx: context.Background(), resourceGroups: &resourceGroupCoordinator{active: active}}
	if result, err := c.resourceGroupCancel(resourcegroup.CancelInput{SchemaVersion: resourcegroup.SchemaVersion, ReviewID: strings.Repeat("b", 32)}); err == nil || result != nil || active.stop != resourcegroup.StopNone || ctx.Err() != nil {
		t.Fatal("nonmatching cancel reduced another operation")
	}
	if result, err := c.resourceGroupCancel(resourcegroup.CancelInput{SchemaVersion: resourcegroup.SchemaVersion, ReviewID: id}); err == nil || result != nil || active.stop != resourcegroup.StopUserCanceled || ctx.Err() == nil {
		t.Fatal("storage failure prevented exact local cancellation")
	}
	attempt := c.resourceGroupDispatchAfterIntent(context.Background(), active, "", resourcegrant.ManagementRequest{}, resourceManagementOrigin{})
	if attempt.clientInvoked {
		t.Fatal("new client call began after cancellation with failed storage")
	}
}

// This cohort uses only contexts, mutexes and a joined local goroutine. It does
// not call Core.Open, the group store, a Node, a grant, a provider or network.
func TestGroupActivityCanceledOrClosingAdmissionDoesNotPublish(t *testing.T) {
	for _, closing := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		c := &Core{ctx: context.Background(), closing: closing, resourceGroups: &resourceGroupCoordinator{}}
		if !closing {
			cancel()
		}
		c.op.Lock()
		_, active, finish, err := c.beginResourceGroupActivityLocked(ctx, "test", resourcegroup.ActivityApplying, 1)
		c.op.Unlock()
		cancel()
		if err == nil || active != nil || finish != nil || c.resourceGroups.active != nil {
			t.Fatal("canceled or closing request installed an activity")
		}
	}
}

func TestGroupActivityShutdownJoinsHistoricalCompletion(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	c := &Core{ctx: ctx, resourceGroups: &resourceGroupCoordinator{}}
	c.op.Lock()
	run, active, finish, err := c.beginResourceGroupActivityLocked(context.Background(), "test", resourcegroup.ActivityApplying, 1)
	c.op.Unlock()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	var once sync.Once
	complete := func() { once.Do(finish) }
	started, stopped := make(chan struct{}), make(chan struct{})
	var closeErr error
	t.Cleanup(func() {
		cancel()
		complete()
		select {
		case <-stopped:
		case <-time.After(3 * time.Second):
			t.Error("shutdown goroutine failed to join")
		}
	})
	go func() {
		close(started)
		closeErr = c.stopResourceGroups()
		close(stopped)
	}()
	wait := func(ch <-chan struct{}) {
		t.Helper()
		select {
		case <-ch:
		case <-time.After(3 * time.Second):
			t.Fatal("bounded activity wait failed")
		}
	}
	wait(started)
	wait(run.Done())
	select {
	case <-stopped:
		t.Fatal("shutdown returned before historical completion")
	default:
	}
	c.op.Lock()
	retained := c.resourceGroups.active == active
	c.op.Unlock()
	if !retained {
		t.Fatal("active handle disappeared before completion")
	}
	complete()
	wait(stopped)
	if closeErr != nil || c.resourceGroups.active != nil {
		t.Fatal("completion did not release shutdown handle")
	}
}

func TestGroupActivityStopReasonsRemainLocal(t *testing.T) {
	c := &Core{ctx: context.Background()}
	active := &resourceGroupActivity{stop: resourcegroup.StopNone}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if c.resourceGroupStopLocked(ctx, active) != resourcegroup.StopUserCanceled {
		t.Fatal("caller cancellation not represented as local stop")
	}
	active.stop = resourcegroup.StopContextChanged
	if c.resourceGroupStopLocked(context.Background(), active) != resourcegroup.StopContextChanged {
		t.Fatal("explicit local context stop lost")
	}
	c.ctx = ctx
	active.stop = resourcegroup.StopNone
	if c.resourceGroupStopLocked(context.Background(), active) != resourcegroup.StopContextChanged {
		t.Fatal("Core cancellation not represented as context stop")
	}
}

func TestGroupActivityCloseCannotMissConcurrentPublication(t *testing.T) {
	for i := 0; i < 8; i++ {
		t.Run(string(rune('a'+i)), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			c := &Core{ctx: ctx, resourceGroups: &resourceGroupCoordinator{}}
			closed := make(chan struct{})
			var finish func()
			var once sync.Once
			complete := func() {
				once.Do(func() {
					if finish != nil {
						finish()
					}
				})
			}
			t.Cleanup(func() {
				cancel()
				complete()
				select {
				case <-closed:
				case <-time.After(3 * time.Second):
					t.Error("concurrent close failed to join")
				}
			})
			go func() {
				c.mu.Lock()
				c.closing = true
				cancel()
				c.mu.Unlock()
				_ = c.stopResourceGroups()
				close(closed)
			}()
			c.op.Lock()
			_, _, end, err := c.beginResourceGroupActivityLocked(context.Background(), "test", resourcegroup.ActivityApplying, 1)
			finish = end
			c.op.Unlock()
			if err == nil && finish == nil {
				t.Fatal("admitted activity has no completion owner")
			}
			complete()
			select {
			case <-closed:
			case <-time.After(3 * time.Second):
				t.Fatal("close missed active publication")
			}
			if c.resourceGroups.active != nil {
				t.Fatal("activity survived completed close")
			}
		})
	}
}
