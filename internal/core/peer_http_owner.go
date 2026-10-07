package core

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"reflect"
	"sync"

	"github.com/webkaz-labs/sobalink/internal/transportorigin"
)

type peerHTTPRequest struct {
	mu            sync.Mutex
	front         *peerHTTPFront
	backend       string // immutable actual selected backend, including mixed routes
	ctx           context.Context
	cancel        context.CancelFunc
	token         *peerHTTPToken
	authority     *peerHTTPAuthority
	origin        transportorigin.Origin
	participant   transportorigin.Lease
	application   *peerHTTPApplication
	conn          *peerHTTPConn
	source        *peerHTTPBody
	response      *peerHTTPBody
	responseLease func()
	appRelease    func()
	workDone      func()
	stopping      bool
	appDone       chan struct{}
	done          chan struct{}
	finishOnce    sync.Once
	limits        peerHTTPLimits
}

type peerHTTPLimits struct{ total, perPeer, window int64 }

// adoptPeerHTTPSource accepts only private encoded bytes or an opened staged
// regular file. Files transfer ownership even when admission later fails.
// Arbitrary readers/callbacks and replayable GetBody closures are excluded.
func adoptPeerHTTPSource(body io.Reader) (io.ReadCloser, int64, error) {
	switch source := body.(type) {
	case nil:
		return nil, 0, nil
	case *bytes.Reader:
		// peerJSON owns these encoded bytes; copying prevents later mutation or
		// repositioning by its caller from changing the exposed source.
		copyOf := *source
		raw, err := io.ReadAll(&copyOf)
		if err != nil {
			return nil, 0, err
		}
		return io.NopCloser(bytes.NewReader(raw)), int64(len(raw)), nil
	case *os.File:
		info, err := source.Stat()
		if err != nil || !info.Mode().IsRegular() {
			_ = source.Close()
			return nil, 0, errors.New("peer upload requires a staged regular file")
		}
		return source, info.Size(), nil
	default:
		return nil, 0, errors.New("unsupported peer request source")
	}
}

func (c *Core) newPeerHTTPRequest(caller context.Context, peer string, route *peerHTTPRoute, source io.ReadCloser) (*peerHTTPRequest, error) {
	closeSource := func() {
		if source != nil {
			_ = source.Close()
		}
	}
	application, _ := caller.Value(peerHTTPApplicationKey{}).(*peerHTTPApplication)
	if application != nil {
		if err := application.check(route); err != nil {
			closeSource()
			return nil, err
		}
	}
	front, err := c.peerHTTPFront()
	if err != nil {
		closeSource()
		return nil, err
	}
	p := c.capacityPolicy()
	limits := peerHTTPLimits{p.Number("resources", "tcpConnections"), p.Number("resources", "tcpPerPeer"), p.Number("resources", "tcpPerPolicy")}
	appRelease, err := front.reserve(peerHTTPRequestLease, limits.total, limits.perPeer, peer)
	if err != nil {
		closeSource()
		return nil, err
	}
	responseLease, err := front.reserve(peerHTTPBodyLease, 2*limits.total, 0, "")
	if err != nil {
		appRelease()
		closeSource()
		return nil, err
	}
	var sourceLease func()
	if source != nil {
		sourceLease, err = front.reserve(peerHTTPBodyLease, 2*limits.total, 0, "")
		if err != nil {
			responseLease()
			appRelease()
			closeSource()
			return nil, err
		}
	}
	workDone, err := c.beginWork()
	if err != nil {
		if sourceLease != nil {
			sourceLease()
		}
		responseLease()
		appRelease()
		closeSource()
		return nil, err
	}
	// Do not inherit caller values: httptrace and arbitrary callback/source
	// chains must not become part of net/http's retained dial context.
	ctx, cancel := context.WithCancel(context.Background())
	deadline, hasDeadline := caller.Deadline()
	if hasDeadline {
		cancel()
		ctx, cancel = context.WithDeadline(context.Background(), deadline)
	}
	h := &peerHTTPRequest{front: front, backend: route.backend, ctx: ctx, cancel: cancel, origin: route.origin, application: application, responseLease: responseLease, appRelease: appRelease, workDone: workDone, limits: limits, appDone: make(chan struct{}), done: make(chan struct{})}
	var originDone <-chan struct{}
	if route.origin != nil {
		originDone = route.origin.StopRequested()
	}
	h.authority = &peerHTTPAuthority{deadline: deadline, caller: caller.Done(), front: front.done, origin: originDone, stopped: ctx.Done()}
	h.token = &peerHTTPToken{authority: h.authority}
	if source != nil {
		h.source = ownPeerHTTPBody(h.authority, source, sourceLease)
	}
	if application != nil {
		if err = application.registerRequest(h); err != nil {
			h.stop()
			close(h.appDone)
			h.cleanup()
			return nil, err
		}
	}
	if route.origin != nil {
		h.participant, err = route.origin.Acquire(caller)
		if err != nil {
			h.stop()
			close(h.appDone)
			h.cleanup()
			return nil, err
		}
	}
	go func() {
		select {
		case <-caller.Done():
		case <-front.done:
		case <-originDone:
		case <-ctx.Done():
		case <-h.appDone:
		}
		h.stop()
		h.cleanup()
	}()
	return h, nil
}

func (c *Core) preparePeerHTTPRequest(ctx context.Context, peer string, input io.Reader) (*peerHTTPRequest, int64, error) {
	source, size, err := adoptPeerHTTPSource(input)
	if err != nil {
		return nil, 0, err
	}
	defer func() {
		if source != nil {
			_ = source.Close()
		}
	}()
	attempted := make(map[string]bool)
	for {
		route, err := c.capturePeerHTTPRoute(ctx, peer, attempted)
		if err != nil {
			return nil, 0, err
		}
		attempted[route.backend] = true
		// Source exposure starts only after a successful prepared stream. An
		// unavailable unopened mixed route can still select another backend,
		// preserving the existing fallback without replaying any request bytes.
		h, err := c.newPeerHTTPRequest(ctx, peer, route, nil)
		if err != nil {
			return nil, 0, err
		}
		err = h.prepare(c, peer, route)
		if err != nil {
			h.mu.Lock()
			unopened := h.conn == nil
			h.mu.Unlock()
			h.finish(ctx)
			if route.mixed && unopened && ctx.Err() == nil && mixedUnavailable(err) {
				continue
			}
			return nil, 0, err
		}
		if source != nil {
			release, err := h.front.reserve(peerHTTPBodyLease, 2*h.limits.total, 0, "")
			if err != nil {
				h.finish(ctx)
				return nil, 0, err
			}
			h.mu.Lock()
			h.source = ownPeerHTTPBody(h.authority, source, release)
			h.mu.Unlock()
			source = nil
		}
		return h, size, nil
	}
}

func (h *peerHTTPRequest) stop() {
	h.mu.Lock()
	h.stopping = true
	conn, source := h.conn, h.source
	h.mu.Unlock()
	h.token.seal()
	h.cancel()
	if conn != nil {
		_ = conn.Close()
	}
	// Closing the physical connection is signalled first. A file close can
	// wake a source read, but it cannot substitute for that connection wake.
	if source != nil {
		_ = source.Close()
	}
}

func (h *peerHTTPRequest) cleanup() {
	<-h.appDone
	h.mu.Lock()
	conn, source, response := h.conn, h.source, h.response
	h.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
	if source != nil {
		_ = source.Close()
	}
	if response != nil {
		_ = response.Close()
	} else {
		h.responseLease()
	}
	if conn != nil {
		<-conn.done
	}
	if h.participant != nil {
		h.participant.Release()
	}
	h.mu.Lock()
	h.conn, h.source, h.response = nil, nil, nil
	h.origin, h.participant = nil, nil
	application := h.application
	h.application = nil
	h.responseLease = nil
	h.mu.Unlock()
	if application != nil {
		application.requestCleaned(h)
	}
	h.appRelease()
	h.workDone()
	h.appRelease, h.workDone = nil, nil
	close(h.done)
}

func (h *peerHTTPRequest) finish(ctx context.Context) error {
	h.finishOnce.Do(func() { close(h.appDone) })
	select {
	case <-h.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-h.front.done:
		return net.ErrClosed
	}
}

func (h *peerHTTPRequest) prepare(c *Core, peer string, route *peerHTTPRoute) error {
	if !h.authority.open() {
		return net.ErrClosed
	}
	// Existing c.dial/policy and backend flow limits do not reserve this Core
	// Controller. This is the one physical Core reservation, made before Dial.
	release, ok := c.serviceResources().AdmitTCP("peer-http", peer)
	if !ok {
		return errPeerHTTPCapacity
	}
	conn, err := route.dial(h.ctx)
	if conn == nil {
		release()
		if err == nil {
			err = errors.New("peer dial returned no connection")
		}
		return err
	}
	owned := ownPeerHTTPConn(h.authority, conn, h.origin != nil, release)
	h.mu.Lock()
	h.conn = owned
	stopped := h.stopping || !h.authority.open()
	h.mu.Unlock()
	if err != nil || stopped {
		_ = owned.Close()
		if err == nil {
			err = net.ErrClosed
		}
		return err
	}
	if deadline, ok := h.ctx.Deadline(); ok {
		if err := owned.SetDeadline(deadline); err != nil {
			return err
		}
	}
	if h.application != nil {
		// Failed unopened attempts belong only to H. Durable operation
		// attribution begins once the selected stream exists, before exposure.
		if err := h.application.bind(route); err != nil {
			return err
		}
	}
	h.token.mu.Lock()
	if h.token.closed {
		err = net.ErrClosed
	} else {
		h.token.conn = owned
	}
	h.token.mu.Unlock()
	return err
}

type peerHTTPRoundTripper struct {
	owner  *peerHTTPRequest
	upload bool
}

func (t peerHTTPRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	h := t.owner
	if req.GetBody != nil || req.Header.Get("Upgrade") != "" || req.Header.Get("Connection") != "" || req.URL.Scheme != "http" || req.URL.Host != "peer.invalid" || req.Method == "CONNECT" || req.Trailer != nil {
		return nil, errors.New("unsupported peer HTTP transport shape")
	}
	slot, err := h.front.submit(t.upload, h.limits.total, h.limits.window)
	if err != nil {
		return nil, err
	}
	transport := h.front.normal
	if t.upload {
		transport = h.front.upload
	}
	resp, err := transport.RoundTrip(req)
	// The fixed Transport.RoundTrip has returned, including its defers. Adopt
	// a body before Client can inspect/close a rejected redirect response.
	if resp != nil && resp.Body != nil {
		body := ownPeerHTTPBody(h.authority, resp.Body, h.responseLease)
		h.mu.Lock()
		h.response = body
		stopping := h.stopping
		h.mu.Unlock()
		resp.Body = body
		if stopping {
			_ = body.Close()
		}
	}
	slot.finish()
	return resp, err
}

func (h *peerHTTPRequest) request(method string, target string, contentType string, size int64, upload bool, redirectError error) (*http.Response, error) {
	ctx := context.WithValue(h.ctx, peerHTTPTokenKey{}, h.token)
	var body io.Reader
	if h.source != nil {
		body = h.source
	}
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, err
	}
	req.GetBody = nil
	if h.source != nil {
		req.ContentLength = size
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if redirectError == nil {
		redirectError = errors.New("peer redirects are refused")
	}
	client := &http.Client{Transport: peerHTTPRoundTripper{h, upload}, CheckRedirect: func(*http.Request, []*http.Request) error { return redirectError }}
	return client.Do(req)
}

func privatePeerHTTPResult(out any) (any, func(), error) {
	if out == nil {
		return nil, func() {}, nil
	}
	v := reflect.ValueOf(out)
	if v.Kind() != reflect.Pointer || v.IsNil() {
		return nil, nil, errors.New("peer response requires a nonnil output pointer")
	}
	temporary := reflect.New(v.Elem().Type())
	return temporary.Interface(), func() { v.Elem().Set(temporary.Elem()) }, nil
}

func (h *peerHTTPRequest) publish(commit func()) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	var permit transportorigin.Lease
	var err error
	if h.origin != nil {
		permit, err = h.origin.AcquirePublication(h.ctx)
		if err != nil {
			return err
		}
		defer permit.Release()
	}
	if h.stopping || !h.authority.open() {
		return net.ErrClosed
	}
	commit() // a precomputed bounded copy only; no I/O or arbitrary user callback
	return nil
}
