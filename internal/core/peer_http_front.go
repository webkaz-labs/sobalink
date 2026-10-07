package core

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
)

var errPeerHTTPCapacity = errors.New("peer HTTP capacity reached; retry after current operations finish")

// peerHTTPFront belongs to Core, not to a peer or a disposable generation. The
// two transports are configured once and are never rebound or modified.
// This bounds Sobalink admissions; it makes no hard RSS or private net/http
// goroutine/tombstone completion claim.
type peerHTTPFront struct {
	mu        sync.Mutex
	normal    *http.Transport
	upload    *http.Transport
	windows   [2][]*peerHTTPSubmission
	leases    [4]int64
	submitted int64
	requests  map[string]int64
	closing   bool
	done      chan struct{}
}

func newPeerHTTPFront() *peerHTTPFront {
	f := &peerHTTPFront{done: make(chan struct{}), requests: make(map[string]int64)}
	f.normal = peerHTTPTransport(5 * time.Second)
	f.upload = peerHTTPTransport(0)
	return f
}

func peerHTTPTransport(headerTimeout time.Duration) *http.Transport {
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	return &http.Transport{
		Proxy: nil, DisableKeepAlives: true,
		ResponseHeaderTimeout: headerTimeout, MaxResponseHeaderBytes: 8 << 10,
		MaxConnsPerHost: 0, Protocols: protocols,
		DialContext: handoffPeerHTTP,
	}
}

func (c *Core) peerHTTPFront() (*peerHTTPFront, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closing || c.ctx.Err() != nil {
		return nil, net.ErrClosed
	}
	if c.peerHTTPTransport == nil {
		c.peerHTTPTransport = newPeerHTTPFront()
	}
	return c.peerHTTPTransport, nil
}

func (f *peerHTTPFront) close() {
	f.mu.Lock()
	if !f.closing {
		f.closing = true
		close(f.done)
	}
	f.mu.Unlock()
	// Best effort only. Request owners supply their own acknowledged completion.
	f.normal.CloseIdleConnections()
	f.upload.CloseIdleConnections()
}

type peerHTTPLeaseKind uint8

const (
	peerHTTPRequestLease peerHTTPLeaseKind = iota
	peerHTTPBodyLease
	peerHTTPApplicationLease
	peerHTTPPreparingLease
)

// Each resource family is aggregate across normal and upload requests. The
// connection-count policy permits N simultaneous request owners and N enclosing
// operations; their two possible bodies are bounded by 2N. Submission history
// has its independent shared N limit below. These are distinct count dimensions,
// not a claim that a body or map entry costs a physical connection's memory.
// N=1 still permits one request with its source, response and submission.
// Physical sockets separately use Controller.AdmitTCP. Exhaustion queues nobody.
func (f *peerHTTPFront) reserve(kind peerHTTPLeaseKind, limit, perPeer int64, peer string) (func(), error) {
	f.mu.Lock()
	if f.closing || limit <= 0 || f.leases[kind] >= limit || peer != "" && f.requests[peer] >= perPeer {
		f.mu.Unlock()
		return nil, errPeerHTTPCapacity
	}
	f.leases[kind]++
	if peer != "" {
		f.requests[peer]++
	}
	f.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			f.mu.Lock()
			f.leases[kind]--
			if peer != "" {
				f.requests[peer]--
				if f.requests[peer] == 0 {
					delete(f.requests, peer)
				}
			}
			f.mu.Unlock()
		})
	}, nil
}

type peerHTTPEntranceKey struct{}
type peerHTTPEntrance struct{}

// Admission precedes private JSON encoding/copying and route/State lookups.
// Nested helpers share this one ticket; it never reaches the library context.
func (c *Core) enterPeerHTTP(ctx context.Context) (context.Context, func(), error) {
	if _, ok := ctx.Value(peerHTTPEntranceKey{}).(*peerHTTPEntrance); ok {
		return ctx, func() {}, nil
	}
	front, err := c.peerHTTPFront()
	if err != nil {
		return nil, nil, err
	}
	release, err := front.reserve(peerHTTPPreparingLease, c.limit("resources", "tcpConnections"), 0, "")
	if err != nil {
		return nil, nil, err
	}
	return context.WithValue(ctx, peerHTTPEntranceKey{}, &peerHTTPEntrance{}), release, nil
}

type peerHTTPSubmission struct {
	front    *peerHTTPFront
	index    int
	terminal bool // protected by front.mu
}

func (f *peerHTTPFront) submit(upload bool, total, window int64) (*peerHTTPSubmission, error) {
	index := 0
	if upload {
		index = 1
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closing || total <= 0 || f.submitted >= total || window <= 0 || int64(len(f.windows[index])) >= window {
		return nil, errPeerHTTPCapacity
	}
	s := &peerHTTPSubmission{front: f, index: index}
	f.submitted++
	f.windows[index] = append(f.windows[index], s)
	return s, nil
}

func (s *peerHTTPSubmission) finish() {
	f := s.front
	f.mu.Lock()
	s.terminal = true
	queue := f.windows[s.index]
	count := 0
	for count < len(queue) && queue[count].terminal {
		queue[count] = nil
		count++
	}
	// Only the contiguous terminal prefix is reusable. A hung earlier
	// RoundTrip therefore cannot create an unbounded tail of later attempts.
	if count != 0 {
		copy(queue, queue[count:])
		clear(queue[len(queue)-count:])
		f.windows[s.index] = queue[:len(queue)-count]
		f.submitted -= int64(count)
	}
	f.mu.Unlock()
}

type peerHTTPTokenKey struct{}

// The library sees only this once-only handoff token and a context derived
// from Background. It cannot reach a caller context, dial closure or Core.
type peerHTTPToken struct {
	mu        sync.Mutex
	conn      *peerHTTPConn
	closed    bool
	authority *peerHTTPAuthority
}

// Cancellation channels and an absolute deadline are the complete authority
// visible to retained library wrappers. No channel points back to its owner.
type peerHTTPAuthority struct {
	deadline                       time.Time
	caller, front, origin, stopped <-chan struct{}
}

func (a *peerHTTPAuthority) open() bool {
	return a != nil && !channelClosed(a.caller) && !channelClosed(a.front) && !channelClosed(a.origin) && !channelClosed(a.stopped) && (a.deadline.IsZero() || time.Now().Before(a.deadline))
}

func channelClosed(done <-chan struct{}) bool {
	select {
	case <-done:
		return true
	default:
		return false
	}
}

func handoffPeerHTTP(ctx context.Context, network, address string) (net.Conn, error) {
	token, ok := ctx.Value(peerHTTPTokenKey{}).(*peerHTTPToken)
	if !ok || token == nil || network != "tcp" || address != "peer.invalid:80" {
		return nil, net.ErrClosed
	}
	token.mu.Lock()
	defer token.mu.Unlock()
	if token.closed || token.conn == nil || !token.authority.open() {
		return nil, net.ErrClosed
	}
	conn := token.conn
	token.conn = nil
	return conn, nil
}

func (t *peerHTTPToken) seal() {
	t.mu.Lock()
	t.closed = true
	t.conn = nil
	t.authority = nil
	t.mu.Unlock()
}

func validatePeerHTTPRequest(method, path string, proof bool) (*url.URL, bool, error) {
	u, err := url.Parse("http://peer.invalid" + path)
	if err != nil || u.Scheme != "http" || u.Host != "peer.invalid" || u.User != nil || u.Fragment != "" || u.Opaque != "" || u.RawPath != "" || !strings.HasPrefix(path, "/") {
		return nil, false, errors.New("invalid peer HTTP authority or path")
	}
	allowed, upload := false, false
	if proof {
		allowed = method == "GET" && u.Path == mixedIdentityPath || method == "POST" && u.Path == mixedProofPath
	} else {
		switch u.Path {
		case "/v1/hello", "/.well-known/sobalink/services/v2", discoveryV3Path:
			allowed = method == "GET"
		case "/v1/messages", "/v1/offers":
			allowed = method == "POST"
		default:
			parts := strings.Split(strings.TrimPrefix(u.Path, "/v1/batches/"), "/")
			if strings.HasPrefix(u.Path, "/v1/batches/") && len(parts) > 0 && config.ValidPeerID(parts[0]) {
				allowed = len(parts) == 1 && method == "GET" || len(parts) == 2 && parts[1] == "cancel" && method == "POST"
				if len(parts) == 3 && config.ValidPeerID(parts[2]) {
					upload = parts[1] == "files" && method == "PUT"
					allowed = upload || parts[1] == "retry" && method == "POST"
				}
			}
		}
	}
	if !allowed || u.RawQuery != "" && u.Path != discoveryV3Path {
		return nil, false, errors.New("unsupported peer HTTP request")
	}
	if u.RawQuery != "" {
		values, e := url.ParseQuery(u.RawQuery)
		if e != nil {
			return nil, false, errors.New("invalid peer discovery query")
		}
		for key, value := range values {
			if key != "after" && key != "revision" || len(value) != 1 {
				return nil, false, errors.New("invalid peer discovery query")
			}
		}
	}
	return u, upload, nil
}
