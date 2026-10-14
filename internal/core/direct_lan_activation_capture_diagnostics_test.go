package core

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/directlan"
)

// Inert owners only: terminal capture guards must return before any store or
// Node admission, without constructing a runtime, socket or authenticated proof.
func TestActivationCaptureTerminalGuardsRemainNonMutating(t *testing.T) {
	for _, mode := range []string{"deadline", "caller-cancelled", "owner-cancelled"} {
		t.Run(mode, func(t *testing.T) {
			ownerContext, cancelOwner := context.WithCancel(context.Background())
			defer cancelOwner()
			store := &directLANStore{}
			owner := &contextControlOwner{node: &directlan.Node{}, store: store, process: "synthetic-process", ctx: ownerContext, operations: make(map[*directlan.ContextAttempt]*contextExchangeOperation)}
			core := &Core{ctx: context.Background(), directLAN: store, lanStartNonce: owner.process, contextControl: owner}
			caller, cancel := context.WithCancel(context.Background())
			defer cancel()
			want := context.Canceled
			switch mode {
			case "deadline":
				cancel()
				caller, cancel = context.WithDeadline(context.Background(), time.Unix(1, 0))
				defer cancel()
				want = context.DeadlineExceeded
			case "caller-cancelled":
				cancel()
			case "owner-cancelled":
				cancelOwner()
				want = directlan.ErrUnavailable
			}
			core.op.Lock()
			attempt, err := core.prepareContextOutboundLocked(caller, owner, "synthetic-peer", directlan.ContextPrepare)
			core.op.Unlock()
			if !errors.Is(err, want) || attempt != nil || len(owner.operations) != 0 || store.contextEpoch != nil || store.contextPublication != nil || store.recovery || store.reviewRevision != 0 {
				t.Fatal("terminal capture crossed the inert admission boundary")
			}
		})
	}
}

func TestActivationCaptureFailureSnapshotPrecedesCleanup(t *testing.T) {
	f := newLocalContextFixture(t)
	f.advance(t, "prepared")
	owner := localContextRuntimeOwner(t, f)
	ctx, stop := context.WithCancel(context.Background())
	f.core.op.Lock()
	before := cloneDirectLANState(f.store.state)
	revision, writes, epoch, receipt := f.store.reviewRevision, f.writes, f.store.contextEpoch, f.store.contextPublication
	record := activationDiagnosticCaptureFailureLocked(f.core, owner, f.peer, ctx, context.Background(), directlan.ErrUnavailable)
	f.core.op.Unlock()
	stop()
	owner.cancel()
	if record.Phase != "capture" || record.Error != "unavailable" || record.Context != "success" || record.Workflow != "success" || record.OwnerContext != "success" || record.CoreContext != "success" || !record.OwnerCurrent || record.OwnerStopping || record.StoreRecovery || !record.ConfigurationCurrent || !record.Prepared || record.Committed || record.Confirmed {
		t.Fatal("capture snapshot did not retain its pre-cleanup observations")
	}
	if f.store.reviewRevision != revision || f.writes != writes || f.store.contextEpoch != epoch || f.store.contextPublication != receipt || !reflect.DeepEqual(before, f.store.state) {
		t.Fatal("diagnostic observation changed saved state or authority")
	}
}

func TestActivationCaptureFailureSnapshotClosedAndPrivate(t *testing.T) {
	f := newLocalContextFixture(t)
	owner := localContextRuntimeOwner(t, f)
	secret := "synthetic-sensitive-value"
	f.core.op.Lock()
	record := activationDiagnosticCaptureFailureLocked(f.core, owner, f.peer, context.Background(), context.Background(), &lanCommandError{code: secret, message: secret})
	f.core.op.Unlock()
	if record.Error != "other" || strings.Contains(fmt.Sprintf("%+v", record), secret) {
		t.Fatal("capture record retained arbitrary error content")
	}
	// No pointer, error, duration object, address, map or slice can be retained.
	typ := reflect.TypeOf(record)
	for i := 0; i < typ.NumField(); i++ {
		switch typ.Field(i).Type.Kind() {
		case reflect.Bool, reflect.Int, reflect.Int64, reflect.String:
		default:
			t.Fatal("capture record gained an unbounded or opaque field")
		}
	}
	for _, tc := range []struct {
		err  error
		want string
	}{
		{directlan.ErrRecovery, "recovery"},
		{directLANEndpointReviewChanged(), "file-review"},
		{directLANEndpointContextRequired(), "context-required"},
		{&lanCommandError{code: secret, message: secret}, "other"},
	} {
		if got := activationDiagnosticError(errors.Join(tc.err, errors.New(secret))); got != tc.want {
			t.Fatal("capture error escaped the closed classifier")
		}
	}
}
