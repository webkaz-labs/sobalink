package nearby

import (
	"errors"
	"io"
	"net"
	"sync"
	"time"
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
	closed := make(chan error, 1)
	go func() { closed <- owner.Close() }()
	timer := time.NewTimer(closeJoinTimeout)
	defer timer.Stop()
	select {
	case closeErr := <-closed:
		if closeErr != nil {
			return owner, errors.Join(cause, normalizedCloseError(closeErr))
		}
		return nil, cause
	case <-timer.C:
		// Retain the original in-flight attempt. Close on this owner does not
		// finish until the actual close worker joins, and never retries an FD.
		unresolved := errors.Join(ErrCloseUncertain, ErrCloseUnresolved)
		retained := &closeOnlyTransport{close: func() error {
			return errors.Join(unresolved, normalizedCloseError(<-closed))
		}}
		return retained, errors.Join(cause, unresolved)
	}
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

// Both distinct resources begin closing even if either Close blocks. This
// worker returns only after both physical close calls join. Public Session and
// constructor rollback boundaries own finite waits and retain unresolved work.
func pairedCloseOwner(first, second io.Closer) Transport {
	return &closeOnlyTransport{close: func() error {
		done := make(chan error, 2)
		go func() { done <- normalizedCloseError(first.Close()) }()
		go func() { done <- normalizedCloseError(second.Close()) }()
		return errors.Join(<-done, <-done)
	}}
}
