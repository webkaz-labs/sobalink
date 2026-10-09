//go:build resource_inspection_native

package directlan

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/webkaz-labs/sobalink/internal/resourcegrant"
)

// InspectionObservation is a single context-local, read-only diagnostic. It
// retains only finite labels, never selectors, identities, endpoints or errors.
// It cannot alter transport/grant authority or invoke an application callback.
type InspectionObservation struct {
	mu    sync.Mutex
	event InspectionObservationEvent
	seen  bool
}
type InspectionObservationEvent struct {
	Phase, Code, Elapsed, DialPhase              string
	ReadyBeforeEnsure, AuthenticatedBeforeEnsure bool
}
type inspectionObservationKey struct{}

func WithInspectionObservation(ctx context.Context) (context.Context, *InspectionObservation) {
	log := &InspectionObservation{}
	return context.WithValue(ctx, inspectionObservationKey{}, log), log
}
func (l *InspectionObservation) Result() (InspectionObservationEvent, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.event, l.seen
}

type inspectionObservation struct {
	log     *InspectionObservation
	started time.Time
}

func beginInspectionObservation(ctx context.Context) inspectionObservation {
	if ctx == nil {
		return inspectionObservation{}
	}
	log, _ := ctx.Value(inspectionObservationKey{}).(*InspectionObservation)
	if log == nil {
		return inspectionObservation{}
	}
	return inspectionObservation{log: log, started: time.Now()}
}
func (o inspectionObservation) finish(phase string, result, contextErr error) {
	if o.log == nil {
		return
	}
	code := "other"
	switch {
	case result == nil:
		code = "ok"
	case errors.Is(contextErr, context.DeadlineExceeded):
		code = "deadline"
	case errors.Is(contextErr, context.Canceled):
		code = "cancelled"
	case errors.Is(result, ErrUntrusted):
		code = "untrusted"
	case errors.Is(result, resourcegrant.ErrTransport):
		code = "transport"
	case errors.Is(result, resourcegrant.ErrProtocol):
		code = "protocol"
	case errors.Is(result, resourcegrant.ErrUnsupported):
		code = "unsupported"
	case errors.Is(result, resourcegrant.ErrInvalid):
		code = "invalid"
	}
	elapsed := "under_1s"
	if d := time.Since(o.started); d >= 5*time.Second {
		elapsed = "at_least_5s"
	} else if d >= time.Second {
		elapsed = "1_to_5s"
	}
	o.log.mu.Lock()
	defer o.log.mu.Unlock()
	if !o.log.seen {
		o.log.event, o.log.seen = InspectionObservationEvent{Phase: phase, Code: code, Elapsed: elapsed, DialPhase: o.log.event.DialPhase, ReadyBeforeEnsure: o.log.event.ReadyBeforeEnsure, AuthenticatedBeforeEnsure: o.log.event.AuthenticatedBeforeEnsure}, true
	}
}

// Called outside transport locks; immutable labels and atomic readiness snapshots
// provide diagnosis only and cannot change session admission.
func markInspectionDial(ctx context.Context, phase string, ready, authenticated bool) {
	log, _ := ctx.Value(inspectionObservationKey{}).(*InspectionObservation)
	if log == nil {
		return
	}
	log.mu.Lock()
	defer log.mu.Unlock()
	if log.seen {
		return
	}
	log.event.DialPhase = phase
	if phase == "ensure_enter" {
		log.event.ReadyBeforeEnsure, log.event.AuthenticatedBeforeEnsure = ready, authenticated
	}
}
