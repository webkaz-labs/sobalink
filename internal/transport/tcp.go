package transport

import (
	"context"
	"errors"
	"net"
	"time"
)

type TCPConfig struct {
	// Controller shares live finite budgets across all ports and directions.
	Controller       *Controller
	PolicyID, PeerID string
	ListenAddress    string
	Target           string
	DialTimeout      time.Duration
}

// StartTCP binds an exact numeric loopback address and forwards streams to Target.
func StartTCP(ctx context.Context, cfg TCPConfig, dial Dialer) (*Server, error) {
	if err := validateListenAddress(cfg.ListenAddress); err != nil {
		return nil, err
	}
	if dial == nil {
		return nil, errors.New("dialer is required")
	}
	if cfg.Target == "" {
		return nil, errors.New("target is required")
	}
	timeout, err := normalizeTimeout(cfg.DialTimeout)
	if err != nil {
		return nil, err
	}
	l, err := (&net.ListenConfig{}).Listen(ctx, loopbackNetwork("tcp", cfg.ListenAddress), cfg.ListenAddress)
	if err != nil {
		return nil, err
	}
	return startServer(ctx, l, l.Addr(), func(s *Server) {
		acceptConnections(s, l, func(client net.Conn) {
			serveTCP(s, client, cfg.Target, timeout, dial)
		}, cfg)
	}), nil
}

// serveTCP keeps the connection lifecycle shared by the listener and tests.
func serveTCP(s *Server, client net.Conn, target string, timeout time.Duration, dial Dialer) {
	dialCtx, cancel := context.WithTimeout(s.ctx, timeout)
	remote, err := dialTracked(s, dialCtx, dial, "tcp", target)
	cancel()
	if err != nil {
		return
	}
	defer s.release(remote)
	bridge(client, remote)
}
