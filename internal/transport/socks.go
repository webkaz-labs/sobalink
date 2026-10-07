package transport

import (
	"context"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strconv"
	"time"
)

const defaultHandshakeTimeout = 10 * time.Second

type SOCKSConfig struct {
	Controller       *Controller
	PolicyID         string
	ListenAddress    string
	Username         string
	Password         string
	HandshakeTimeout time.Duration
	DialTimeout      time.Duration
}

// StartSOCKS serves authenticated SOCKS5 TCP CONNECT only. BIND and UDP
// ASSOCIATE are rejected as soon as the command header is received.
func StartSOCKS(ctx context.Context, cfg SOCKSConfig, dial Dialer) (*Server, error) {
	if err := validateListenAddress(cfg.ListenAddress); err != nil {
		return nil, err
	}
	if dial == nil {
		return nil, errors.New("dialer is required")
	}
	if len(cfg.Username) == 0 || len(cfg.Username) > 255 || len(cfg.Password) == 0 || len(cfg.Password) > 255 {
		return nil, errors.New("SOCKS credentials must each contain 1 to 255 bytes")
	}
	if cfg.HandshakeTimeout < 0 {
		return nil, errors.New("handshake timeout must be positive")
	}
	if cfg.HandshakeTimeout == 0 {
		cfg.HandshakeTimeout = defaultHandshakeTimeout
	}
	var err error
	cfg.DialTimeout, err = normalizeTimeout(cfg.DialTimeout)
	if err != nil {
		return nil, err
	}
	l, err := (&net.ListenConfig{}).Listen(ctx, loopbackNetwork("tcp", cfg.ListenAddress), cfg.ListenAddress)
	if err != nil {
		return nil, err
	}
	return startServer(ctx, l, l.Addr(), func(s *Server) {
		acceptConnections(s, l, func(client net.Conn) { serveSOCKS(s, client, cfg, dial) }, TCPConfig{Controller: cfg.Controller, PolicyID: cfg.PolicyID})
	}), nil
}

func serveSOCKS(s *Server, client net.Conn, cfg SOCKSConfig, dial Dialer) {
	deadline := time.Now().Add(cfg.HandshakeTimeout)
	if client.SetDeadline(deadline) != nil {
		return
	}
	if !authenticateSOCKS(client, cfg.Username, cfg.Password) {
		return
	}
	var header [4]byte
	if _, err := io.ReadFull(client, header[:]); err != nil {
		return
	}
	if header[0] != 5 || header[2] != 0 {
		_ = socksReply(client, 1)
		return
	}
	if header[1] != 1 {
		_ = socksReply(client, 7)
		return
	}
	host, err := readSOCKSHost(client, header[3])
	if err != nil {
		_ = socksReply(client, 8)
		return
	}
	var portBytes [2]byte
	if _, err := io.ReadFull(client, portBytes[:]); err != nil {
		return
	}
	port := binary.BigEndian.Uint16(portBytes[:])
	if port == 0 {
		_ = socksReply(client, 2)
		return
	}
	address := net.JoinHostPort(host, strconv.Itoa(int(port)))
	handshakeCtx, cancelHandshake := context.WithDeadline(sessionContext(client, s.ctx), deadline)
	defer cancelHandshake()
	dialCtx, cancel := context.WithTimeout(handshakeCtx, cfg.DialTimeout)
	remote, err := dialTracked(s, dialCtx, dial, "tcp", address)
	cancel()
	if err != nil {
		_ = socksReply(client, 2)
		return
	}
	defer s.release(remote)
	if err := socksReply(client, 0); err != nil {
		return
	}
	if client.SetDeadline(time.Time{}) != nil {
		return
	}
	bridge(client, remote)
}

func authenticateSOCKS(c net.Conn, username, password string) bool {
	var header [2]byte
	if _, err := io.ReadFull(c, header[:]); err != nil || header[0] != 5 || header[1] == 0 {
		return false
	}
	methods := make([]byte, int(header[1]))
	if _, err := io.ReadFull(c, methods); err != nil {
		return false
	}
	found := false
	for _, m := range methods {
		if m == 2 {
			found = true
		}
	}
	if !found {
		_ = writeFull(c, []byte{5, 255})
		return false
	}
	if writeFull(c, []byte{5, 2}) != nil {
		return false
	}
	if _, err := io.ReadFull(c, header[:]); err != nil || header[0] != 1 || header[1] == 0 {
		return false
	}
	u := make([]byte, int(header[1]))
	if _, err := io.ReadFull(c, u); err != nil {
		return false
	}
	var length [1]byte
	if _, err := io.ReadFull(c, length[:]); err != nil || length[0] == 0 {
		return false
	}
	p := make([]byte, int(length[0]))
	if _, err := io.ReadFull(c, p); err != nil {
		return false
	}
	valid := subtle.ConstantTimeCompare(u, []byte(username)) & subtle.ConstantTimeCompare(p, []byte(password))
	if valid != 1 {
		_ = writeFull(c, []byte{1, 1})
		return false
	}
	return writeFull(c, []byte{1, 0}) == nil
}

func readSOCKSHost(r io.Reader, atyp byte) (string, error) {
	var n int
	switch atyp {
	case 1:
		n = net.IPv4len
	case 4:
		n = net.IPv6len
	case 3:
		var length [1]byte
		if _, err := io.ReadFull(r, length[:]); err != nil {
			return "", err
		}
		if length[0] == 0 {
			return "", errors.New("empty hostname")
		}
		name := make([]byte, int(length[0]))
		if _, err := io.ReadFull(r, name); err != nil {
			return "", err
		}
		return string(name), nil
	default:
		return "", errors.New("unsupported address type")
	}
	address := make([]byte, n)
	if _, err := io.ReadFull(r, address); err != nil {
		return "", err
	}
	return net.IP(address).String(), nil
}

func socksReply(w io.Writer, status byte) error {
	return writeFull(w, []byte{5, status, 0, 1, 0, 0, 0, 0, 0, 0})
}
