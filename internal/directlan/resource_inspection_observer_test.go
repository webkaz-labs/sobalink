//go:build resource_inspection_native

package directlan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/resourcegrant"
)

func TestInspectionDialResultClassifications(t *testing.T) {
	cases := []struct {
		name string
		err  error
		code string
	}{
		{"success", nil, "ok"},
		{"deadline", context.DeadlineExceeded, "deadline"},
		{"cancelled", context.Canceled, "cancelled"},
		{"untrusted", ErrUntrusted, "untrusted"},
		{"capacity", ErrCapacity, "capacity"},
		{"unavailable", ErrUnavailable, "unavailable"},
		{"recovery", ErrRecovery, "recovery"},
		{"closed", net.ErrClosed, "closed"},
		{"unknown", errors.New("synthetic-private-error"), "other"},
		{"joined", errors.Join(context.Canceled, net.ErrClosed), "multiple_causes"},
		{"joined_private", errors.Join(context.Canceled, inspectionUnformattableError{}), "multiple_causes"},
		{"single_join", errors.Join(context.Canceled, nil), "cancelled"},
		{"nested_join", errors.Join(errors.Join(context.Canceled, net.ErrClosed), nil), "multiple_causes"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, log := WithInspectionObservation(context.Background())
			err := tc.err
			if err != nil {
				if tc.name != "joined_private" {
					err = fmt.Errorf("synthetic-private-wrapper: %w", err)
				}
			}
			markInspectionDialResult(ctx, "tcp_dial", err, tc.err == nil)
			beginInspectionObservation(ctx).finish("dial", resourcegrant.ErrTransport, nil)
			got, seen := log.Result()
			if !seen || got.DialResultStage != "tcp_dial" || got.DialResultCode != tc.code || got.TCPConnected != (tc.err == nil) || got.Code != "transport" {
				t.Fatalf("unexpected fixed-category result: %+v", got)
			}
			encoded, err := json.Marshal(got)
			if err != nil || strings.Contains(string(encoded), "synthetic-private") {
				t.Fatal("diagnostic retained private error text")
			}
		})
	}
}

func TestInspectionDialResultStagesAndPrivacy(t *testing.T) {
	for _, stage := range []string{"initial_wake", "tcp_dial", "wake_worker", "context_done", "authentication", "complete", "synthetic-private-stage"} {
		ctx, log := WithInspectionObservation(context.Background())
		markInspectionDialResult(ctx, stage, errors.New("synthetic-private-error"), true)
		beginInspectionObservation(ctx).finish("dial", resourcegrant.ErrTransport, nil)
		got, _ := log.Result()
		want := stage
		if stage == "synthetic-private-stage" {
			want = "other"
		}
		if got.DialResultStage != want || got.DialResultCode != "other" || !got.TCPConnected {
			t.Fatal("stage or connected evidence changed")
		}
		encoded, _ := json.Marshal(got)
		if strings.Contains(string(encoded), "synthetic-private") {
			t.Fatal("diagnostic retained private input")
		}
	}
}

func TestInspectionDialResultFirstTerminalResult(t *testing.T) {
	ctx, log := WithInspectionObservation(context.Background())
	markInspectionDial(ctx, "ensure_enter", true, true)
	markInspectionDial(ctx, "tunnel_dial", false, false)
	markInspectionDialResult(ctx, "wake_worker", context.Canceled, true)
	markInspectionDialResult(ctx, "complete", nil, false)
	observation := beginInspectionObservation(ctx)
	observation.finish("dial", resourcegrant.ErrTransport, nil)
	got, seen := log.Result()
	if !seen || got.DialResultStage != "wake_worker" || got.DialResultCode != "cancelled" || !got.TCPConnected || got.DialPhase != "tunnel_dial" || !got.ReadyBeforeEnsure || !got.AuthenticatedBeforeEnsure {
		t.Fatal("finish lost the first result or readiness evidence")
	}
	markInspectionDialResult(ctx, "complete", nil, false)
	observation.finish("complete", nil, nil)
	after, _ := log.Result()
	if after != got {
		t.Fatal("late result changed terminal evidence")
	}
}

func TestInspectionDialResultNoObservation(t *testing.T) {
	markInspectionDialResult(nil, "tcp_dial", nil, true)
	markInspectionDialResult(context.Background(), "tcp_dial", nil, true)
	ctx, log := WithInspectionObservation(context.Background())
	beginInspectionObservation(ctx).finish("validate", resourcegrant.ErrInvalid, nil)
	markInspectionDialResult(ctx, "complete", nil, true)
	got, _ := log.Result()
	if got.DialResultStage != "" || got.DialResultCode != "" || got.TCPConnected {
		t.Fatal("post-finish marker invented dial evidence")
	}
}

func TestInspectionDialInitialWakeObservation(t *testing.T) {
	ctx, log := WithInspectionObservation(context.Background())
	n := &Node{}
	connection, err := n.dialInspectionPeer(ctx, nil, nil, nil)
	if connection != nil || !errors.Is(err, ErrUntrusted) {
		t.Fatal("missing authority reached application dial")
	}
	beginInspectionObservation(ctx).finish("dial", resourcegrant.ErrTransport, nil)
	got, seen := log.Result()
	if !seen || got.DialResultStage != "initial_wake" || got.DialResultCode != "untrusted" || got.TCPConnected {
		t.Fatal("real initial-wake failure was not distinguished")
	}
}

// A diagnostic must never format an unknown transport error, even incidentally.
type inspectionUnformattableError struct{}

func (inspectionUnformattableError) Error() string { panic("diagnostic formatted raw error") }

func TestInspectionDialResultNeverFormatsError(t *testing.T) {
	ctx, log := WithInspectionObservation(context.Background())
	markInspectionDialResult(ctx, "tcp_dial", inspectionUnformattableError{}, false)
	beginInspectionObservation(ctx).finish("dial", resourcegrant.ErrTransport, nil)
	got, _ := log.Result()
	if got.DialResultCode != "other" {
		t.Fatal("unknown error did not remain generic")
	}
}
