// Package transportorigin carries immutable transport ownership through wrappers.
// An origin is attribution and completion accounting, not peer authorization.
package transportorigin

import (
	"context"
	"errors"
	"net"
)

var (
	ErrMissingOrigin = errors.New("transport origin unavailable")
	ErrStopped       = errors.New("transport origin stopped")
)

// Token is opaque comparable identity only. It contains no owner, hooks, source,
// context or authority. Its nonzero size gives separately allocated identities
// distinct addresses. Allocate once per generation and propagate its pointer.
type Token struct{ marker byte }

func NewToken() *Token { return new(Token) }

// Origin attributes an operation to one immutable disposable transport owner.
// Compare Identity pointers, never arbitrary Origin interface values. Nil or
// missing origins are not permission to rediscover a current backend.
type Origin interface {
	Identity() *Token
	StopRequested() <-chan struct{}
	// Acquire counts ownership; callers separately reserve their applicable
	// finite operation/body budget and validate authority before admission.
	Acquire(context.Context) (Lease, error)
	// AcquirePublication is a distinct counted permit. The caller already holds its
	// authority/commit mutex and has checked cancellation/permissions. Successful
	// acquisition is the publication linearization point against the same terminal
	// fence as stop. A pre-seal permit may finish only its precomputed bounded copy
	// under that authority mutex; retirement waits for Release before completion or
	// replacement. Do not hold WG/generation locks during that copy, perform I/O,
	// invoke application callbacks or use the permit to admit later work. Always
	// release after the copy, including failure paths. No application callback is
	// passed into an owner admission gate.
	AcquirePublication(context.Context) (Lease, error)
}

// Lease owns admitted work through its actual cleanup. Release is idempotent;
// the implementation must detach its context/owner references on release.
// Participant contexts are cancelled by terminal stop, without manufacturing
// completion. A caller timeout never releases the owner's reservation.
type Lease interface {
	Context() context.Context
	Release()
}

// Carrier must be delegated explicitly; embedding net.Conn erases methods
// outside that interface. Address/key equality never reconstructs an origin.
type Carrier interface{ TransportOrigin() Origin }

// Connection is the connection-wrapper spelling of Carrier.
type Connection = Carrier

// TerminalConnection distinguishes a close request from physical completion.
// WaitClosed does not initiate cleanup or stop cleanup when its context expires.
type TerminalConnection interface {
	Carrier
	WaitClosed(context.Context) error
}

// ConnectionLifetime retains exact connection identity/index ownership for an
// enclosing application callback lifetime. It grants no I/O or peer authority.
// A successful hold returns an idempotent release. The caller must finish every
// numeric identity consumer before releasing it, then join terminal cleanup:
// WaitClosed may itself need the hold released. Stop seals new holds immediately
// but does not release an existing hold or prevent the raw close request.
type ConnectionLifetime interface {
	HoldConnection() (release func(), err error)
}

// DialCapability fixes the selected backend and destination before network work.
// Dial must use that captured selection and may never resolve a current backend.
type DialCapability interface {
	Origin() Origin
	Dial(context.Context) (net.Conn, error)
}
