package nearby

import (
	"errors"
	"net"
	"sync"
)

var (
	ErrCloseUncertain  = errors.New("nearby: socket cleanup is uncertain; retain the discovery owner and stop setup")
	ErrCloseUnresolved = errors.New("nearby: socket owners have not joined; retain the discovery owner and stop setup")
)

func normalizedCloseError(err error) error {
	if err == nil {
		return nil
	}
	// A multi-error containing ErrClosed can also contain a real failure.
	// Normalize only an ordinary wrapping chain ending in net.ErrClosed.
	for cause := err; cause != nil; cause = errors.Unwrap(cause) {
		if cause == net.ErrClosed {
			return nil
		}
		if _, joined := cause.(interface{ Unwrap() []error }); joined {
			break
		}
	}
	if errors.Is(err, ErrCloseUncertain) {
		return err
	}
	return errors.Join(ErrCloseUncertain, err)
}

// A failed post-bind constructor must return its owner if rollback cannot be
// confirmed. Callers must preserve the nonnil owner even when err is nonnil.
func rollbackTransport(owner Transport, cause error) (Transport, error) {
	if closeErr := owner.Close(); closeErr != nil {
		return owner, errors.Join(cause, normalizedCloseError(closeErr))
	}
	return nil, cause
}

type closeOnlyTransport struct {
	close func() error
	once  sync.Once
	err   error
}

func (t *closeOnlyTransport) ReadPacket() (Packet, error) { return Packet{}, ErrCloseUncertain }
func (t *closeOnlyTransport) WritePacket([]byte) error    { return ErrCloseUncertain }
func (t *closeOnlyTransport) Close() error {
	t.once.Do(func() { t.err = normalizedCloseError(t.close()) })
	return t.err
}
