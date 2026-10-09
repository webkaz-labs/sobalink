package directlan

import (
	"context"
	"net"
	"time"

	"github.com/tailscale/wireguard-go/device"
)

// dialInspectionPeer refreshes the existing managed control proof and schedules
// at most one rate-limited wakeup while ONE application dial is pending. A saved
// local ready bit does not prove that the remote still retains its session keys.
// Actual TCP completion, followed by the original capability checks, is required.
func (n *Node) dialInspectionPeer(ctx context.Context, g *runtimeGeneration, p *peerState, authentication *managedAuthentication) (net.Conn, error) {
	dialCtx, cancelDial := context.WithTimeout(ctx, handshakeTimeout)
	defer cancelDial()
	if err := n.wakeInspectionSession(dialCtx, authentication); err != nil {
		return nil, err
	}
	// Space our second attempt from completion of the first, not from the
	// earlier start of its control I/O. The fixed dial deadline is unchanged;
	// slow initial control can leave no time for this one additional wakeup.
	// Other callers and the engine's rate limiter remain authoritative.
	wake := time.NewTimer(device.RekeyTimeout)
	defer wake.Stop()
	workerCtx, stopWorker := context.WithCancel(dialCtx)
	done := make(chan struct{})
	var workerErr error // published by close(done), read only after joining
	go func() {
		defer close(done)
		select {
		case <-workerCtx.Done():
			return
		case <-wake.C:
		}
		if err := n.wakeInspectionSession(workerCtx, authentication); err != nil && workerCtx.Err() == nil {
			// Record a real wakeup failure before cancelling the pending dial.
			// The caller's normal stop-after-success is only cleanup.
			workerErr = err
			cancelDial()
		}
	}()
	connection, err := n.dialCapturedPeer(dialCtx, g, p, "tcp", ResourceInspectPort)
	stopWorker()
	<-done                      // no Node/generation/session lock; join before exposing the flow
	contextErr := dialCtx.Err() // Cleanup cannot carry success past the fixed cutoff.
	if workerErr != nil {
		err = workerErr
	} else if err == nil && contextErr != nil {
		err = contextErr
	} else if err == nil && !n.inspectionAuthenticationCurrent(authentication, true) {
		err = ErrUntrusted
	}
	if err != nil {
		if connection != nil {
			_ = connection.Close()
		}
		return nil, err
	}
	return connection, nil
}

func (n *Node) inspectionAuthenticationCurrent(authentication *managedAuthentication, authenticated bool) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.readyLocked() != nil || authentication == nil || !authentication.current() || n.peers[authentication.peer.peer.Key] != authentication.peer {
		return false
	}
	current := authentication.peer.authenticated.Load()
	return !authenticated || current != nil && *current == *authentication
}

func (n *Node) wakeInspectionSession(ctx context.Context, authentication *managedAuthentication) error {
	if authentication == nil || authentication.peer == nil || authentication.peer.session == nil {
		return ErrUntrusted
	}
	p := authentication.peer
	select {
	case <-ctx.Done():
		return ctx.Err()
	case p.session.gate <- struct{}{}:
	}
	defer func() { <-p.session.gate }()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if !n.inspectionAuthenticationCurrent(authentication, false) {
		return ErrUntrusted
	}
	if err := n.requestCapturedManagedSession(ctx, authentication); err != nil {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if !n.inspectionAuthenticationCurrent(authentication, true) {
		return ErrUntrusted
	}
	if p.session.initiator {
		// Existing engine admission and RekeyTimeout remain authoritative.
		return n.initiateSession(p)
	}
	return nil
}
