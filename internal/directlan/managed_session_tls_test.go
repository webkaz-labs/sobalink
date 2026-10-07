//go:build directlan_managed_session_tls

package directlan

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/endpointmeta"
)

// Opt-in in-memory TLS composition fixtures. Their presence is not execution
// authorization and does not supply durable Core publication authority.
func TestManagedInboundTLSBoundSessionOnly(t *testing.T) {
	for _, operation := range []string{"session", "pair-context-status"} {
		t.Run(operation, func(t *testing.T) {
			n, g, p, remote := managedFixtureOwner(t)
			serverRaw, clientRaw := net.Pipe()
			defer clientRaw.Close()
			work, err := g.acquireWork(nil, true)
			if err != nil {
				t.Fatal(err)
			}
			w := &wire{raw: newControlStream(g, serverRaw), control: true, g: g, work: work, contextDeadline: time.Now().Add(time.Second)}
			n.wires[w] = struct{}{}
			done := make(chan struct{})
			go func() { n.handle(w); close(done) }()
			cert, err := certificate(remote, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			client := tls.Client(clientRaw, tlsConfigProtocol(cert, n.PublicKey(), false, contextProtocolName))
			if err := client.SetDeadline(time.Now().Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			if err := client.HandshakeContext(context.Background()); err != nil {
				t.Fatal(err)
			}
			request, err := endpointmeta.Encode(endpointmeta.BoundRequest{Version: 2, Operation: operation, PairBinding: p.binding})
			if err != nil {
				t.Fatal(err)
			}
			if err := writeFrame(client, request, endpointmeta.MaxFrameBytes); err != nil {
				t.Fatal(err)
			}
			reply, readErr := readFrame(client, endpointmeta.MaxFrameBytes)
			if operation == "session" {
				if readErr != nil {
					t.Fatal(readErr)
				}
				if _, err := endpointmeta.ParseReply(reply, "session"); err != nil {
					t.Fatal(err)
				}
			} else if readErr == nil {
				t.Fatal("unsupported operation received success")
			}
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("handler did not join")
			}
			if g.applicationPeer(p) != (operation == "session") {
				t.Fatal("authentication differs from bound exchange")
			}
		})
	}
}

type managedFailedClose struct {
	net.Conn
	failure error
}

func (c *managedFailedClose) Close() error { _ = c.Conn.Close(); return c.failure }
func TestManagedFailedCloseRetainsAndSeals(t *testing.T) {
	n, g, p, _ := managedFixtureOwner(t)
	if err := n.commitManagedSession(context.Background(), n.captureManagedSession(p)); err != nil {
		t.Fatal(err)
	}
	a, b := net.Pipe()
	defer b.Close()
	failure := errors.New("synthetic close failure")
	owned := newControlStream(g, &managedFailedClose{a, failure})
	if !errors.Is(owned.Close(), failure) {
		t.Fatal("close error lost")
	}
	if g.applicationPeer(p) || g.open() {
		t.Fatal("cleanup failure left admission open")
	}
	g.mu.Lock()
	retained := g.failedControl[owned]
	count := g.controlCount
	g.mu.Unlock()
	if !errors.Is(retained, failure) || count != 1 {
		t.Fatal("failed close not retained and charged")
	}
	if !errors.Is(owned.Close(), failure) {
		t.Fatal("repeated close lost failure")
	}
}

func TestManagedTLSRefusesDowngradeAndWrongPin(t *testing.T) {
	for _, test := range []struct {
		name, protocol string
		wrongPin       bool
	}{
		{"v1", protocolName, false}, {"missing-alpn", "", false}, {"wrong-pin", contextProtocolName, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			n, g, p, remote := managedFixtureOwner(t)
			a, b := net.Pipe()
			defer b.Close()
			work, err := g.acquireWork(nil, true)
			if err != nil {
				t.Fatal(err)
			}
			w := &wire{raw: newControlStream(g, a), control: true, g: g, work: work, contextDeadline: time.Now().Add(time.Second)}
			n.wires[w] = struct{}{}
			done := make(chan struct{})
			go func() { n.handle(w); close(done) }()
			cert, err := certificate(remote, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			pin := n.PublicKey()
			if test.wrongPin {
				pin = remote.PublicKey()
			}
			config := tlsConfigProtocol(cert, pin, false, test.protocol)
			if test.protocol == "" {
				config.NextProtos = nil
			}
			client := tls.Client(b, config)
			if err := client.SetDeadline(time.Now().Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			// TLS 1.3 may return locally before the server verifies the client flight.
			// Either the handshake or the attempted bound exchange must be refused.
			handshakeErr := client.HandshakeContext(context.Background())
			if handshakeErr == nil {
				request, _ := endpointmeta.Encode(endpointmeta.BoundRequest{Version: 2, Operation: "session", PairBinding: p.binding})
				_ = writeFrame(client, request, endpointmeta.MaxFrameBytes)
				if _, err := readFrame(client, endpointmeta.MaxFrameBytes); err == nil {
					t.Fatal("disallowed TLS accepted a session reply")
				}
			}
			_ = b.Close()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("rejected handler did not join")
			}
			if g.applicationPeer(p) {
				t.Fatal("disallowed TLS authenticated")
			}
		})
	}
}

func TestManagedTLSRejectsInvalidFrames(t *testing.T) {
	for _, kind := range []string{"wrong-binding", "v1-frame", "noncanonical", "oversize", "truncated", "lost-reply"} {
		t.Run(kind, func(t *testing.T) {
			n, g, p, remote := managedFixtureOwner(t)
			a, b := net.Pipe()
			defer b.Close()
			work, err := g.acquireWork(nil, true)
			if err != nil {
				t.Fatal(err)
			}
			w := &wire{raw: newControlStream(g, a), control: true, g: g, work: work, contextDeadline: time.Now().Add(time.Second)}
			n.wires[w] = struct{}{}
			done := make(chan struct{})
			go func() { n.handle(w); close(done) }()
			cert, err := certificate(remote, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			client := tls.Client(b, tlsConfigProtocol(cert, n.PublicKey(), false, contextProtocolName))
			if err := client.SetDeadline(time.Now().Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			if err := client.HandshakeContext(context.Background()); err != nil {
				t.Fatal(err)
			}
			request, _ := endpointmeta.Encode(endpointmeta.BoundRequest{Version: 2, Operation: "session", PairBinding: p.binding})
			switch kind {
			case "wrong-binding":
				request, _ = endpointmeta.Encode(endpointmeta.BoundRequest{Version: 2, Operation: "session", PairBinding: string(makeHexBytes('0', 64))})
				_ = writeFrame(client, request, endpointmeta.MaxFrameBytes)
			case "v1-frame":
				_ = writeFrame(client, []byte(`{"version":1,"operation":"session"}`), endpointmeta.MaxFrameBytes)
			case "noncanonical":
				_ = writeFrame(client, append([]byte(" "), request...), endpointmeta.MaxFrameBytes)
			case "oversize":
				_ = writeAll(client, []byte{0x7f, 0xff, 0xff, 0xff})
			case "truncated":
				_ = writeAll(client, []byte{0, 0, 0, 20, '{'})
				_ = b.Close()
			case "lost-reply":
				_ = writeFrame(client, request, endpointmeta.MaxFrameBytes)
				_ = b.Close()
			}
			if kind != "truncated" && kind != "lost-reply" {
				if _, err := readFrame(client, endpointmeta.MaxFrameBytes); err == nil {
					t.Fatal("invalid request received reply")
				}
			}
			_ = b.Close()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("invalid exchange did not join")
			}
			if g.applicationPeer(p) {
				t.Fatal("failed reply or invalid request authenticated")
			}
		})
	}
}
func makeHexBytes(value byte, length int) []byte {
	out := make([]byte, length)
	for i := range out {
		out[i] = value
	}
	return out
}
