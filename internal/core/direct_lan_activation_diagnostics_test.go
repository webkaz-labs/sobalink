package core

import (
	"context"
	"errors"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/directlan"
	"github.com/webkaz-labs/sobalink/internal/endpointmeta"
)

// Closed test-only labels: never format an error, address, identity or proof.
func activationDiagnosticError(err error) string {
	switch {
	case err == nil:
		return "success"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, directlan.ErrUnavailable):
		return "unavailable"
	case errors.Is(err, endpointmeta.ErrReview):
		return "review"
	case errors.Is(err, directlan.ErrRecovery):
		return "recovery"
	case errors.Is(err, endpointmeta.ErrInvalid):
		return "invalid"
	case networkErrorCode(err) == "direct_lan_endpoint_review_changed":
		return "file-review"
	case networkErrorCode(err) == "direct_lan_endpoint_context_required":
		return "context-required"
	case errors.Is(err, endpointmeta.ErrIdentity), errors.Is(err, directlan.ErrUntrusted):
		return "identity"
	case errors.Is(err, directlan.ErrCapacity), errors.Is(err, endpointmeta.ErrCapacity):
		return "capacity"
	case errors.Is(err, io.EOF):
		return "eof"
	case errors.Is(err, io.ErrUnexpectedEOF):
		return "unexpected-eof"
	case errors.Is(err, net.ErrClosed):
		return "closed"
	default:
		var network net.Error
		if errors.As(err, &network) && network.Timeout() {
			return "network-timeout"
		}
		return "other"
	}
}

type activationDiagnosticCounters struct {
	Callbacks, Completed, Rejected, Admitted, Denied int32
	ResponseContention                               uint64
}

type activationDiagnosticAttempt struct {
	Reply, Attempt                                                          int
	ElapsedMS, RemainingMS                                                  int64
	ResponderLockWaitMS, InitiatorLockWaitMS, CaptureMS, CaptureRemainingMS int64
	Error, Context                                                          string
	Before, After                                                           activationDiagnosticCounters
}

// Snapshots are independently atomic observations, not a coherent transport
// history or a cleanup receipt. The inbound peer can still finish after After.
type activationDiagnosticAttempts struct {
	Records        [16]activationDiagnosticAttempt
	Count, Omitted int
}

func (d *activationDiagnosticAttempts) add(record activationDiagnosticAttempt) {
	if d.Count == len(d.Records) {
		d.Omitted++
		return
	}
	d.Records[d.Count] = record
	d.Count++
}

func activationDiagnosticMillis(duration time.Duration) int64 {
	if duration < 0 {
		return 0
	}
	return duration.Milliseconds()
}

type activationDiagnosticTimeout struct{}

func (activationDiagnosticTimeout) Error() string   { return "synthetic timeout" }
func (activationDiagnosticTimeout) Timeout() bool   { return true }
func (activationDiagnosticTimeout) Temporary() bool { return false }

func TestActivationDiagnosticClosedErrorClasses(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{nil, "success"}, {context.DeadlineExceeded, "deadline"}, {context.Canceled, "cancelled"},
		{directlan.ErrUnavailable, "unavailable"}, {endpointmeta.ErrReview, "review"},
		{directlan.ErrRecovery, "recovery"}, {endpointmeta.ErrInvalid, "invalid"},
		{directLANEndpointReviewChanged(), "file-review"}, {directLANEndpointContextRequired(), "context-required"},
		{endpointmeta.ErrIdentity, "identity"}, {directlan.ErrUntrusted, "identity"},
		{directlan.ErrCapacity, "capacity"}, {endpointmeta.ErrCapacity, "capacity"},
		{io.EOF, "eof"}, {io.ErrUnexpectedEOF, "unexpected-eof"}, {net.ErrClosed, "closed"},
		{activationDiagnosticTimeout{}, "network-timeout"}, {errors.New("synthetic opaque error"), "other"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			if got := activationDiagnosticError(tc.err); got != tc.want {
				t.Fatalf("class=%s want=%s", got, tc.want)
			}
			if tc.err != nil {
				if got := activationDiagnosticError(errors.Join(tc.err, errors.New("synthetic wrapper"))); got != tc.want {
					t.Fatalf("wrapped class=%s want=%s", got, tc.want)
				}
			}
		})
	}
}

func TestActivationDiagnosticAttemptRecordsBounded(t *testing.T) {
	var records activationDiagnosticAttempts
	for i := 0; i < 20; i++ {
		records.add(activationDiagnosticAttempt{Attempt: i})
	}
	if records.Count != 16 || records.Omitted != 4 || records.Records[0].Attempt != 0 || records.Records[15].Attempt != 15 {
		t.Fatal("attempt diagnostics lost their fixed bound or original records")
	}
	if activationDiagnosticMillis(-time.Second) != 0 || activationDiagnosticMillis(1500*time.Millisecond) != 1500 {
		t.Fatal("relative timing conversion changed")
	}
}

// One failure-only value snapshot. Closed labels and relative durations only;
// no error, owner pointer, peer data, path, digest or absolute timestamp escapes.
// This does not identify the exact internal return site or join remote work.
type activationDiagnosticCapture struct {
	Phase, Error, Context, Workflow, OwnerContext, CoreContext       string
	Reply, WireAttempts                                              int
	ResponderLockWaitMS, InitiatorLockWaitMS, CaptureMS, RemainingMS int64
	OwnerCurrent, OwnerStopping, StoreRecovery, EpochValid           bool
	ConfigurationCurrent, PublicationCurrent                         bool
	Prepared, Committed, Confirmed                                   bool
}

// Core.op must be held, and the caller must snapshot before cancelling its run.
// Read only retained state: do not call model/admission/readiness methods here.
func activationDiagnosticCaptureFailureLocked(c *Core, owner *contextControlOwner, key string, run, workflow context.Context, err error) activationDiagnosticCapture {
	r := activationDiagnosticCapture{Phase: "capture", Error: activationDiagnosticError(err),
		Context: activationDiagnosticError(run.Err()), Workflow: activationDiagnosticError(workflow.Err()),
		CoreContext: activationDiagnosticError(c.ctx.Err()), OwnerCurrent: c.currentContextOwnerLocked(owner) == nil,
		OwnerContext: "unavailable"}
	if owner == nil {
		return r
	}
	r.OwnerStopping = owner.stopping.Load()
	if owner.ctx != nil {
		r.OwnerContext = activationDiagnosticError(owner.ctx.Err())
	}
	s := owner.store
	if s == nil {
		return r
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r.StoreRecovery, r.EpochValid = s.recovery, s.contextEpoch.Valid()
	r.ConfigurationCurrent = owner.configuration == contextConfigurationDigest(s.state)
	r.PublicationCurrent = s.contextPublicationCurrentLocked(owner.process)
	if s.state.Metadata != nil {
		for _, peer := range s.state.Metadata.Peers {
			if peer.Peer.Key == key {
				r.Prepared, r.Committed, r.Confirmed = contextSavedPair(peer) != nil, peer.PairContext != nil, peer.ContextConfirmed
				break
			}
		}
	}
	return r
}

// Completed callback-local values only. Slots are bounded, append-only and
// atomic: concurrent callbacks cannot overwrite or tear one another's result.
// Ordinals describe completion-call entry order, not correlation with sender wires.
type activationDiagnosticCompletion struct {
	Ordinal uint32
	Result  contextCompletionObservation
}
type activationDiagnosticCompletions struct {
	started atomic.Uint32
	records [32]atomic.Pointer[activationDiagnosticCompletion]
}

func (d *activationDiagnosticCompletions) begin() uint32 { return d.started.Add(1) }
func (d *activationDiagnosticCompletions) finish(ordinal uint32, result contextCompletionObservation) {
	if ordinal > 0 && ordinal <= uint32(len(d.records)) {
		d.records[ordinal-1].Store(&activationDiagnosticCompletion{Ordinal: ordinal, Result: result})
	}
}
func (d *activationDiagnosticCompletions) snapshot() (records []activationDiagnosticCompletion, omitted, unobserved uint32) {
	started := d.started.Load()
	if started > uint32(len(d.records)) {
		omitted = started - uint32(len(d.records))
	}
	for i := range min(started, uint32(len(d.records))) {
		if record := d.records[i].Load(); record != nil {
			records = append(records, *record)
		}
	}
	unobserved = min(started, uint32(len(d.records))) - uint32(len(records))
	return records, omitted, unobserved
}
