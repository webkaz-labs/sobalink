package core

import (
	"context"
	"errors"
	"io"
	"net"
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
	Reply, Attempt         int
	ElapsedMS, RemainingMS int64
	Error, Context         string
	Before, After          activationDiagnosticCounters
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
