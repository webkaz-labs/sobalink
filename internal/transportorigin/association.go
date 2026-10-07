package transportorigin

import (
	"context"
	"net"
)

// PacketAssociation is the immutable return address for one admitted datagram
// flow. A formatted address is not a replacement for this capability. Identity
// is allocated once per association, even when its network tuple is reused.
// Close stops only this association; WaitClosed joins its packet reader, queued
// references, and endpoint cleanup. Neither method closes its retained listener.
type PacketAssociation interface {
	net.Addr
	Carrier
	AssociationIdentity() *Token
	PeerIdentity() (string, bool)
	Close() error
	WaitClosed(context.Context) error
}

// Binder installs an already selected origin before a dial can start. It does
// not select routes, grant permission, or rediscover a current transport owner.
type Binder interface {
	BindOrigin(Origin) error
}

type binderContextKey struct{}

func WithBinder(ctx context.Context, binder Binder) context.Context {
	return context.WithValue(ctx, binderContextKey{}, binder)
}

// BindSelected is optional for callers without an enclosing transport session.
// Dial paths must still use the same captured capability after this returns.
func BindSelected(ctx context.Context, origin Origin) error {
	if binder, ok := ctx.Value(binderContextKey{}).(Binder); ok {
		return binder.BindOrigin(origin)
	}
	return ctx.Err()
}

// Selected reports a committed enclosing session selection, including a bind
// that lost terminal admission. Such an operation must never try another route.
func Selected(ctx context.Context) bool {
	if binder, ok := ctx.Value(binderContextKey{}).(interface{ BoundOrigin() Origin }); ok {
		return binder.BoundOrigin() != nil
	}
	return false
}

// AdoptSelected transfers a newly returned connection before intermediate
// policy validation can close it or hide it from the enclosing owner.
func AdoptSelected(ctx context.Context, conn net.Conn) (net.Conn, error) {
	if owner, ok := ctx.Value(binderContextKey{}).(interface {
		Adopt(net.Conn) (net.Conn, error)
	}); ok {
		return owner.Adopt(conn)
	}
	return conn, nil
}

// RetainRelease keeps a finite reservation until the enclosing session's real
// cleanup. It returns false when the caller must retain its own release owner.
func RetainRelease(ctx context.Context, release func()) bool {
	if owner, ok := ctx.Value(binderContextKey{}).(interface{ HoldRelease(func()) bool }); ok {
		return owner.HoldRelease(release)
	}
	return false
}
