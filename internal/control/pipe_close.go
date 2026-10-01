package control

import (
	"errors"
	"net"
	"sync"
	"sync/atomic"
)

// pipeCloseListener repairs a lost-close-notification path in go-winio v0.6.2.
// If ConnectNamedPipe returns an unexpected error while consuming closeCh,
// makeConnectedServerPipe can return that error instead of ErrPipeListenerClosed.
// Accept then returns, but listenerRoutine resumes waiting and Close waits for
// doneCh indefinitely. Reissue the notification once from that exact path.
//
// This adapter is installed only by the Windows listen implementation. It does
// not alter connections, security descriptors, or peer identity verification.
// The enclosing Server.Close still applies its total shutdown budget if either
// native close cannot finish. There is no timer-based retry or error suppression.
// Upstream: github.com/microsoft/go-winio/blob/v0.6.2/pipe.go#L427-L491
type pipeCloseListener struct {
	net.Listener
	closing   atomic.Bool
	retryOnce sync.Once
}

func (l *pipeCloseListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	// Match the upstream listenerRoutine's exact sentinel comparison; a wrapped
	// closed error also needs recovery if upstream did not recognize it as closed.
	if err != nil && l.closing.Load() && err != net.ErrClosed {
		l.retryOnce.Do(func() {
			// go-winio's Close is concurrent-safe and all callers wait on the
			// same doneCh. Keep this operation in the accept worker so the
			// enclosing server also observes its completion before success.
			err = errors.Join(err, l.Listener.Close())
		})
	}
	return c, err
}

func (l *pipeCloseListener) Close() error {
	l.closing.Store(true)
	return l.Listener.Close()
}
