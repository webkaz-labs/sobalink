package transport

import (
	"context"
	"errors"
	"net"
	"time"
)

type TCPConfig struct {
	ListenAddress string
	Target        string
	DialTimeout   time.Duration
}

// StartTCP binds a literal IPv4 loopback address and forwards streams to Target.
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
	l, err := (&net.ListenConfig{}).Listen(ctx, "tcp4", cfg.ListenAddress)
	if err != nil {
		return nil, err
	}
	return startServer(ctx, l, l.Addr(), func(s *Server) {
		acceptConnections(s, l, func(client net.Conn) {
			dialCtx, cancel := context.WithTimeout(s.ctx, timeout)
			remote, err := dialTracked(s, dialCtx, dial, "tcp", cfg.Target)
			cancel()
			if err != nil {
				return
			}
			defer s.release(remote)
			bridge(client, remote)
		})
	}), nil
}
