package control

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/webkaz-labs/sobalink/internal/messageframe"
	"github.com/webkaz-labs/sobalink/internal/webui"
)

func boundedMemoryServer(limits Limits, handler Handler) (*Server, net.Conn) {
	ln := newMemoryListener()
	server, client := net.Pipe()
	ln.incoming <- server
	return serveListenerWithLimits(context.Background(), ln, handler, func() Limits { return limits }), client
}

func TestDynamicControlEscapedUTF8AndNegotiation(t *testing.T) {
	for _, value := range []string{"&", "\u2028", "界", "😀"} {
		t.Run(value, func(t *testing.T) {
			const textBytes = 128 << 10
			bounds, err := messageframe.ForText(textBytes)
			if err != nil {
				t.Fatal(err)
			}
			limits := Limits{bounds.CommandBytes, bounds.ControlRequestBytes, 2 << 20}
			text := strings.Repeat(value, textBytes/len(value))
			payload, _ := json.Marshal(map[string]string{"peerId": "peer", "text": text})
			command, _ := json.Marshal(webui.Command{RequestID: "large", Name: "message.send", Payload: payload})
			if len(command) <= messageframe.CommandBytes {
				t.Fatal("fixture does not exceed legacy budget")
			}
			s, client := boundedMemoryServer(limits, func(_ context.Context, got string) (any, error) {
				if got != string(command) {
					t.Error("escaped UTF-8 command changed")
				}
				return true, nil
			})
			t.Cleanup(func() {
				client.Close()
				if err := s.Close(); err != nil {
					t.Error(err)
				}
			})
			encoded, _ := json.Marshal(Request{Command: string(command)})
			var accepted bool
			if err := callConn(t.Context(), client, encoded, &accepted, limits.ResponseBytes); err != nil || !accepted {
				t.Fatalf("raised command rejected: %v", err)
			}
		})
	}
	limits := Limits{2 << 20, 4 << 20, 48 << 20}
	s, client := boundedMemoryServer(limits, func(context.Context, string) (any, error) {
		t.Error("limits reached application handler")
		return nil, nil
	})
	defer s.Close()
	defer client.Close()
	encoded, _ := json.Marshal(Request{Command: "control.limits"})
	var got Limits
	if err := callConn(t.Context(), client, encoded, &got, maxResponseBytes); err != nil || got != limits {
		t.Fatalf("negotiation failed: %+v %v", got, err)
	}
}

func TestDynamicControlRejectsOversizeAndTrailingRequest(t *testing.T) {
	limits := Limits{128, 256, 1024}
	for _, request := range []string{
		`{"command":"` + strings.Repeat("x", 129) + `"}` + "\n",
		`{"command":"` + strings.Repeat("x", 300) + `"}` + "\n",
		`{"command":"status"}{"command":"stop"}` + "\n",
		`{"command":"status"}` + "\n" + `{"command":"stop"}` + "\n",
	} {
		s, client := boundedMemoryServer(limits, func(context.Context, string) (any, error) {
			t.Error("invalid request reached handler")
			return nil, nil
		})
		written := make(chan struct{})
		go func() { defer close(written); _, _ = io.WriteString(client, request) }()
		if err := readResponseWithLimit(client, nil, limits.ResponseBytes); err == nil {
			t.Error("invalid request accepted")
		}
		client.Close()
		<-written
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDynamicControlPartialReadTimesOut(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, client := boundedMemoryServer(DefaultLimits(), func(context.Context, string) (any, error) {
			t.Error("partial request reached handler")
			return nil, nil
		})
		defer s.Close()
		defer client.Close()
		start := time.Now()
		if _, err := io.WriteString(client, `{"command":"status"`); err != nil {
			t.Fatal(err)
		}
		if _, err := client.Read(make([]byte, 1)); err == nil {
			t.Fatal("partial request stayed open")
		}
		if time.Since(start) != initialReadTimeout {
			t.Fatalf("partial request timeout %s", time.Since(start))
		}
	})
}

func TestDynamicControlLongStagingUsesOperationContext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, client := boundedMemoryServer(DefaultLimits(), func(ctx context.Context, _ string) (any, error) {
			select {
			case <-time.After(2 * time.Minute):
				return true, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		})
		defer s.Close()
		defer client.Close()
		encoded, _ := json.Marshal(Request{Command: "transfer.send"})
		var accepted bool
		start := time.Now()
		if err := callConn(t.Context(), client, encoded, &accepted, maxResponseBytes); err != nil || !accepted {
			t.Fatalf("long operation failed: %v", err)
		}
		if time.Since(start) != 2*time.Minute {
			t.Fatalf("operation interrupted at %s", time.Since(start))
		}
	})
}

func TestDynamicControlClientCancelReachesHandler(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered, canceled := make(chan struct{}), make(chan struct{})
		s, client := boundedMemoryServer(DefaultLimits(), func(ctx context.Context, _ string) (any, error) {
			close(entered)
			<-ctx.Done()
			close(canceled)
			return nil, ctx.Err()
		})
		defer s.Close()
		defer client.Close()
		ctx, cancel := context.WithCancel(t.Context())
		encoded, _ := json.Marshal(Request{Command: "transfer.send"})
		done := make(chan error, 1)
		go func() { done <- callConn(ctx, client, encoded, nil, maxResponseBytes) }()
		<-entered
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("wrong cancellation error: %v", err)
		}
		<-canceled
	})
}

func TestDynamicResponseBudgetAndTrailingJSON(t *testing.T) {
	for _, raw := range []string{`{"data":true} {"data":false}`, `{"data":true}` + strings.Repeat(" ", 100)} {
		if err := readResponseWithLimit(strings.NewReader(raw), nil, 64); err == nil {
			t.Fatal("invalid response accepted")
		}
	}
	if err := readResponseWithLimit(strings.NewReader(`{"data":true}`), nil, 0); err == nil {
		t.Fatal("zero response budget accepted")
	}
}
