//go:build lanlink_integration

package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/webkaz-labs/sobalink/internal/transfer"
)

// Only the inherited private fixture pipe can select these test controls. No
// production route, option, backend, persistence operation or receipt is faked.
type durableTextProcessFixture struct {
	core            *Core
	mu              sync.Mutex
	target          string
	drop            bool
	fault           durableTextProcessFault
	wireObservation textWireObservation
}

type durableTextProcessFault struct {
	Installed         bool
	OfferRequests     int
	DeliverRequests   int
	StatusRequests    int
	SavedACKDrops     int
	ReceiptReplyDrops int
	InvalidCapture    bool
}

type durableTextProcessEvidence struct {
	Fault             durableTextProcessFault
	PeerReady         bool
	IncomingRows      int
	OutgoingRows      int
	VisibleRows       int
	BodyRetained      bool
	BodyExact         bool
	RetainedTextBytes int
	ExposurePossible  bool
	RunActive         bool
	Lease             *transfer.DurableLease
	Receipt           *DurableTextReceipt
}

func (f *durableTextProcessFixture) command(ctx context.Context, name string, raw json.RawMessage) (any, error) {
	var in struct {
		MessageID string `json:"messageId"`
		Text      string `json:"text"`
		Operation string `json:"operation"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, errors.New("invalid synthetic text control")
	}
	if _, ok := textRequestCreated(in.MessageID); !ok {
		return nil, errors.New("invalid synthetic text locator")
	}
	switch name {
	case "fixture.text-fault":
		if in.Operation != "observe" && in.Operation != "drop-saved-ack" {
			return nil, errors.New("unknown synthetic response mode")
		}
		return true, f.install(in.MessageID, in.Operation == "drop-saved-ack")
	case "fixture.text-evidence":
		f.mu.Lock()
		view := durableTextProcessEvidence{Fault: f.fault}
		f.mu.Unlock()
		c := f.core
		c.mu.RLock()
		defer c.mu.RUnlock()
		if c.messageStore == nil || c.messageStore.frozen != "" {
			return nil, errors.New("certified text evidence unavailable")
		}
		view.IncomingRows, view.OutgoingRows = len(c.messageStore.state.Incoming), len(c.messageStore.state.Outgoing)
		view.VisibleRows = len(c.messages)
		view.PeerReady = c.peerServer != nil && c.node != nil && c.networkReady.Load()
		for _, message := range c.messages {
			if message.ID == in.MessageID {
				view.BodyExact = message.Text == in.Text
			}
		}
		if row := c.messageStore.state.Incoming[in.MessageID]; row != nil {
			lease := row.Lease
			view.Lease, view.BodyRetained = &lease, row.BodyRetained
			view.RetainedTextBytes = len(row.Text)
			if row.Receipt != nil {
				receipt := *row.Receipt
				view.Receipt = &receipt
			}
		}
		if row := c.messageStore.state.Outgoing[in.MessageID]; row != nil {
			copy := cloneMessageOutgoing(row)
			view.Lease, view.Receipt = copy.Lease, copy.Receipt
			view.BodyRetained, view.ExposurePossible = row.BodyRetained, row.ExposurePossible
			view.RetainedTextBytes = len(row.Text)
		}
		view.RunActive = c.messageQueue != nil && c.messageQueue.runs[in.MessageID] != nil
		return view, nil
	case "fixture.text-wire":
		return f.wire(ctx, in.MessageID, in.Text, in.Operation)
	}
	return nil, errors.New("unknown synthetic text control")
}

// Install only while the sending process is stopped. Replace the server rather
// than mutating a live http.Server.Handler. Preserve both owned listener gates,
// genuine backend streams, admission contexts and production server callbacks.
func (f *durableTextProcessFixture) install(id string, drop bool) error {
	c := f.core
	c.op.Lock()
	defer c.op.Unlock()
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fault.Installed {
		return errors.New("synthetic response fault already installed")
	}
	c.mu.RLock()
	p, backend := c.peerServer, c.node
	c.mu.RUnlock()
	if p == nil || p.http == nil || backend == nil || len(p.listeners) != 2 {
		return errors.New("native peer listeners unavailable")
	}
	original := p.http
	owned := make([]peerIncomingListener, len(p.listeners))
	addresses := make([]string, len(p.listeners))
	for i, listener := range p.listeners {
		value, ok := listener.Listener.(*peerIncomingListener)
		if !ok {
			return errors.New("owned native listener required")
		}
		owned[i], addresses[i] = *value, value.Addr().String()
	}
	if err := original.Close(); err != nil {
		return errors.New("native response interceptor close failed")
	}
	listeners := make([]*peerServerListener, 0, len(owned))
	for i := range owned {
		listener, err := backend.Listen("tcp", addresses[i])
		if err != nil {
			for _, opened := range listeners {
				_ = opened.Close()
			}
			return errors.New("native response interceptor listen failed")
		}
		owned[i].Listener = listener
		listeners = append(listeners, &peerServerListener{Listener: &owned[i]})
	}
	server := &http.Server{
		Handler:           http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { f.serve(w, r, original.Handler) }),
		ReadHeaderTimeout: original.ReadHeaderTimeout, ReadTimeout: original.ReadTimeout,
		WriteTimeout: original.WriteTimeout, IdleTimeout: original.IdleTimeout,
		MaxHeaderBytes: original.MaxHeaderBytes, BaseContext: original.BaseContext,
		ConnContext: original.ConnContext, ConnState: original.ConnState,
	}
	f.target, f.drop, f.fault.Installed = id, drop, true
	p.http, p.listeners = server, listeners
	for _, listener := range listeners {
		go func() { _ = server.Serve(listener) }()
	}
	return nil
}

// Capture only the bounded production response. The real handler first reads
// the actual authenticated request and atomically saves body plus receipt.
// A successful received response is checked against that certified row before
// closing its actual connection, without writing any ACK bytes to the sender.
func (f *durableTextProcessFixture) serve(w http.ResponseWriter, r *http.Request, next http.Handler) {
	f.mu.Lock()
	switch r.URL.Path {
	case DurableTextOfferPath:
		f.fault.OfferRequests++
	case DurableTextDeliverPath:
		f.fault.DeliverRequests++
	case DurableTextStatusPath:
		f.fault.StatusRequests++
	default:
		f.mu.Unlock()
		next.ServeHTTP(w, r)
		return
	}
	target, drop := f.target, f.drop
	f.mu.Unlock()
	capture := &durableTextProcessResponse{header: make(http.Header)}
	next.ServeHTTP(capture, r)
	var status DurableTextStatus
	decoded := !capture.overflow && capture.code == http.StatusOK && json.Unmarshal(capture.body.Bytes(), &status) == nil
	if decoded && status.State == "received" && status.Lease.DeliveryID == target {
		valid := status.Receipt != nil
		c := f.core
		c.mu.RLock()
		row := c.messageStore.state.Incoming[target]
		valid = valid && c.messageStore.frozen == "" && row != nil && row.Receipt != nil
		if valid {
			valid = row.State == "received" && sameTextLease(row.Lease, status.Lease) && *row.Receipt == *status.Receipt && validateTextReceipt(row.Offer, &row.Lease, status.Receipt) == nil
		}
		c.mu.RUnlock()
		conn, connected := r.Context().Value(connectionKey{}).(net.Conn)
		f.mu.Lock()
		if !valid || !connected {
			f.fault.InvalidCapture = true
		} else if drop && r.URL.Path == DurableTextDeliverPath {
			f.fault.SavedACKDrops++
		} else if drop {
			// Keep sender evidence unknown until the parent restarts it. These
			// responses also came from the real admitted status/offer handler.
			f.fault.ReceiptReplyDrops++
		}
		f.mu.Unlock()
		if valid && connected && drop {
			_ = conn.Close()
			panic(http.ErrAbortHandler)
		}
	}
	if capture.overflow || capture.code == 0 {
		f.mu.Lock()
		f.fault.InvalidCapture = true
		f.mu.Unlock()
		panic(http.ErrAbortHandler)
	}
	for key, values := range capture.header {
		w.Header()[key] = append([]string(nil), values...)
	}
	w.WriteHeader(capture.code)
	_, _ = w.Write(capture.body.Bytes())
}

type durableTextProcessResponse struct {
	header   http.Header
	body     bytes.Buffer
	code     int
	overflow bool
}

func (w *durableTextProcessResponse) Header() http.Header { return w.header }
func (w *durableTextProcessResponse) WriteHeader(code int) {
	if w.code == 0 {
		w.code = code
	}
}
func (w *durableTextProcessResponse) Write(p []byte) (int, error) {
	if w.code == 0 {
		w.code = http.StatusOK
	}
	if w.body.Len()+len(p) > 16<<10 {
		w.overflow = true
		return 0, errors.New("synthetic response capture limit")
	}
	return w.body.Write(p)
}

// Replays go through native peerJSON and the receiver's production HTTP route,
// never direct receiver methods. Only the test pipe can request these adversarial
// same-ID inputs; retained sender state supplies the original peer and lease.
func (f *durableTextProcessFixture) wire(ctx context.Context, id, text, operation string) (DurableTextStatus, error) {
	f.wireObservation = textWireObservation{Operation: textWireOperation(operation), Stage: "lease_lookup"}
	var result DurableTextStatus
	c := f.core
	c.mu.RLock()
	stored := c.messageStore.state.Outgoing[id]
	if stored == nil || stored.Lease == nil {
		c.mu.RUnlock()
		return result, errors.New("original synthetic lease unavailable")
	}
	row := cloneMessageOutgoing(stored)
	c.mu.RUnlock()
	path := DurableTextStatusPath
	var input any = *row.Lease
	switch operation {
	case "status":
	case "deliver":
		path, input = DurableTextDeliverPath, DurableTextDeliver{Lease: *row.Lease, Text: text}
	case "offer":
		row.Offer.SentAt = time.Now().UTC()
		path, input = DurableTextOfferPath, row.Offer
	case "wrong-epoch":
		if row.Lease.ReceiverEpoch[0] == '0' {
			row.Lease.ReceiverEpoch = "1" + row.Lease.ReceiverEpoch[1:]
		} else {
			row.Lease.ReceiverEpoch = "0" + row.Lease.ReceiverEpoch[1:]
		}
		input = *row.Lease
	default:
		return result, errors.New("unknown synthetic replay")
	}
	call, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	f.wireObservation.Stage = "peer_json"
	err := c.peerJSON(call, row.View.PeerID, http.MethodPost, path, input, &result)
	f.wireObservation.Stage = "peer_json_returned"
	f.wireObservation.Category = textWireErrorCategory(err)
	f.wireObservation.Context = textWireErrorCategory(call.Err())
	return result, err
}

// Fixed diagnostics only: never serialize errors, addresses, IDs, text or leases.
type textWireObservation struct{ Operation, Stage, Category, Context string }

func textWireOperation(op string) string {
	switch op {
	case "status", "deliver", "offer", "wrong-epoch":
		return op
	}
	return "other"
}
func textWireErrorCategory(err error) string {
	if err == nil {
		return "none"
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, io.EOF):
		return "eof"
	case errors.Is(err, io.ErrUnexpectedEOF):
		return "unexpected_eof"
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return "network_timeout"
	}
	switch networkErrorCode(err) {
	case "message_conflict":
		return "message_conflict"
	case "message_unknown_lease":
		return "message_unknown_lease"
	case "message_storage_busy":
		return "message_storage_busy"
	case "message_permission":
		return "message_permission"
	}
	return "other"
}
