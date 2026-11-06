package lanlink

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
)

// Keep this helper and its socket-free privacy tests in the ordinary test build
// so required package race coverage runs them without integration opt-in tags.
// The integration calls it only after its single paired TCP dial fails. Never
// format the error, addresses, keys or arbitrary operation/phase strings.
func pairedTCPDialDiagnostic(ctx context.Context, completedDirections int, elapsed time.Duration, err error) string {
	completedDirections = max(0, min(completedDirections, 1))
	direction := "client_to_host"
	if completedDirections == 1 {
		direction = "host_to_client"
	}
	// Saturate at the existing parent budget; this does not change any timeout.
	elapsedMS := max(int64(0), min(elapsed.Milliseconds(), (4*time.Minute).Milliseconds()))
	parentErr := ctx.Err()
	// As reports the first matching typed observation, including in joins.
	// The independent Is flags below can describe more than one joined cause.
	var networkError net.Error
	isNetworkError := errors.As(err, &networkError)
	networkTimeout := isNetworkError && networkError.Timeout()
	networkOp := "unclassified"
	var op *net.OpError
	if errors.As(err, &op) && op != nil {
		switch op.Op {
		case "connect":
			networkOp = "connect"
		case "dial":
			networkOp = "dial"
		case "read":
			networkOp = "read"
		case "write":
			networkOp = "write"
		case "bind":
			networkOp = "bind"
		case "listen":
			networkOp = "listen"
		}
	}
	phase := "none"
	var staged interface{ DERPFailurePhase() string }
	if errors.As(err, &staged) {
		phase = "unclassified"
		switch staged.DERPFailurePhase() {
		case "dial":
			phase = "dial"
		case "tls":
			phase = "tls"
		case "protocol":
			phase = "protocol"
		case "transport":
			phase = "transport"
		}
	}
	return fmt.Sprintf("paired TCP dial diagnostics: direction=%s completed_directions=%d dial_elapsed_ms=%d parent_done=%t parent_canceled=%t parent_deadline=%t error_canceled=%t error_deadline=%t error_closed=%t error_untrusted=%t error_route_permission=%t error_atomic_recovery=%t error_eof=%t net_error=%t net_timeout=%t net_op=%s derp_phase=%s",
		direction, completedDirections, elapsedMS, parentErr != nil, errors.Is(parentErr, context.Canceled), errors.Is(parentErr, context.DeadlineExceeded),
		errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded), errors.Is(err, net.ErrClosed), errors.Is(err, ErrUntrusted), errors.Is(err, ErrRoutePermission), errors.Is(err, config.ErrAtomicRecovery), errors.Is(err, io.EOF),
		isNetworkError, networkTimeout, networkOp, phase)
}

const pairedDialBaseline = "paired TCP dial diagnostics: direction=client_to_host completed_directions=0 dial_elapsed_ms=0 parent_done=false parent_canceled=false parent_deadline=false error_canceled=false error_deadline=false error_closed=false error_untrusted=false error_route_permission=false error_atomic_recovery=false error_eof=false net_error=false net_timeout=false net_op=unclassified derp_phase=none"

func requirePairedDialDiagnostic(t *testing.T, got string, replacements ...string) {
	t.Helper()
	if got != strings.NewReplacer(replacements...).Replace(pairedDialBaseline) {
		t.Fatal("paired dial diagnostic lost a fixed field or retained unexpected data")
	}
}

func TestPairedTCPDialDiagnosticTypedErrors(t *testing.T) {
	for _, tt := range []struct {
		name string
		err  error
		want []string
	}{
		{"canceled", context.Canceled, []string{"error_canceled=false", "error_canceled=true"}},
		{"deadline", context.DeadlineExceeded, []string{"error_deadline=false", "error_deadline=true", "net_error=false", "net_error=true", "net_timeout=false", "net_timeout=true"}},
		{"closed", net.ErrClosed, []string{"error_closed=false", "error_closed=true", "net_error=false", "net_error=true"}},
		{"untrusted", ErrUntrusted, []string{"error_untrusted=false", "error_untrusted=true"}},
		{"route-permission", ErrRoutePermission, []string{"error_route_permission=false", "error_route_permission=true"}},
		{"atomic-recovery", config.ErrAtomicRecovery, []string{"error_atomic_recovery=false", "error_atomic_recovery=true"}},
		{"eof", io.EOF, []string{"error_eof=false", "error_eof=true"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			for _, err := range []error{tt.err, fmt.Errorf("synthetic-private-wrapper: %w", tt.err), errors.Join(errors.New("synthetic-private-join"), fmt.Errorf("synthetic-private-wrapper: %w", tt.err))} {
				requirePairedDialDiagnostic(t, pairedTCPDialDiagnostic(context.Background(), 0, 0, err), tt.want...)
			}
		})
	}
	joined := errors.Join(context.Canceled, context.DeadlineExceeded, net.ErrClosed, ErrUntrusted, ErrRoutePermission, config.ErrAtomicRecovery, io.EOF)
	requirePairedDialDiagnostic(t, pairedTCPDialDiagnostic(context.Background(), 0, 0, joined),
		"error_canceled=false", "error_canceled=true", "error_deadline=false", "error_deadline=true", "error_closed=false", "error_closed=true",
		"error_untrusted=false", "error_untrusted=true", "error_route_permission=false", "error_route_permission=true", "error_atomic_recovery=false", "error_atomic_recovery=true", "error_eof=false", "error_eof=true",
		"net_error=false", "net_error=true", "net_timeout=false", "net_timeout=true")
	// Familiar text is not evidence of a typed cause.
	for _, err := range []error{nil, errors.New("context canceled"), errors.New("context deadline exceeded"), errors.New("use of closed network connection"), errors.New("peer is not currently approved"), errors.New("no locally approved current route"), errors.New("atomic persistence inventory requires recovery"), errors.New("EOF")} {
		requirePairedDialDiagnostic(t, pairedTCPDialDiagnostic(context.Background(), 0, 0, err))
	}
}

func TestPairedTCPDialDiagnosticBoundsAndParentState(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, stop := context.WithDeadline(context.Background(), time.Unix(0, 0))
	defer stop()
	for _, tt := range []struct {
		name      string
		ctx       context.Context
		completed int
		elapsed   time.Duration
		want      []string
	}{
		{"negative", context.Background(), -1, -time.Second, nil},
		{"milliseconds", context.Background(), 0, 1500 * time.Microsecond, []string{"dial_elapsed_ms=0", "dial_elapsed_ms=1"}},
		{"second-direction", context.Background(), 1, 12 * time.Millisecond, []string{"direction=client_to_host", "direction=host_to_client", "completed_directions=0", "completed_directions=1", "dial_elapsed_ms=0", "dial_elapsed_ms=12"}},
		{"saturated", context.Background(), 2, time.Duration(1<<63 - 1), []string{"direction=client_to_host", "direction=host_to_client", "completed_directions=0", "completed_directions=1", "dial_elapsed_ms=0", "dial_elapsed_ms=240000"}},
		{"canceled", canceled, 0, 0, []string{"parent_done=false", "parent_done=true", "parent_canceled=false", "parent_canceled=true"}},
		{"deadline", expired, 0, 0, []string{"parent_done=false", "parent_done=true", "parent_deadline=false", "parent_deadline=true"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			requirePairedDialDiagnostic(t, pairedTCPDialDiagnostic(tt.ctx, tt.completed, tt.elapsed, nil), tt.want...)
		})
	}
}

func TestPairedTCPDialDiagnosticAllowlistsAndPrivacy(t *testing.T) {
	const private = "synthetic-private-body-address-source-key-operation-phase"
	for _, operation := range []string{"connect", "dial", "read", "write", "bind", "listen", "", "Dial", private} {
		for _, phase := range []string{"dial", "tls", "protocol", "transport", "", "TLS", private} {
			err := fmt.Errorf("%s: %w", private, errors.Join(errors.New(private), &net.OpError{
				Op: operation, Net: private, Source: &net.UnixAddr{Name: private, Net: private}, Addr: &net.UnixAddr{Name: private, Net: private},
				Err: testDERPPhaseError{phase, errors.New(private)},
			}))
			wantOp := "unclassified"
			if operation == "connect" || operation == "dial" || operation == "read" || operation == "write" || operation == "bind" || operation == "listen" {
				wantOp = operation
			}
			wantPhase := "unclassified"
			if phase == "dial" || phase == "tls" || phase == "protocol" || phase == "transport" {
				wantPhase = phase
			}
			requirePairedDialDiagnostic(t, pairedTCPDialDiagnostic(context.Background(), 0, 0, err),
				"net_error=false", "net_error=true", "net_op=unclassified", "net_op="+wantOp, "derp_phase=none", "derp_phase="+wantPhase)
		}
	}
	// No Error, Addr.String or Addr.Network method is needed for classification.
	requirePairedDialDiagnostic(t, pairedTCPDialDiagnostic(context.Background(), 0, 0, &net.OpError{
		Op: private, Net: private, Source: pairedDialUnformattable{}, Addr: pairedDialUnformattable{}, Err: pairedDialUnformattable{},
	}), "net_error=false", "net_error=true")
}

type pairedDialUnformattable struct{}

func (pairedDialUnformattable) Error() string   { panic("diagnostic formatted an error") }
func (pairedDialUnformattable) String() string  { panic("diagnostic formatted an address") }
func (pairedDialUnformattable) Network() string { panic("diagnostic read an address network") }
