package identity

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"sync/atomic"
	"tailscale.com/logtail"
	"testing"
	"time"
)

func TestNetstackOnlyDispatch(t *testing.T) {
	tcpCalls, udpCalls := 0, 0
	fail := errors.New("unroutable in netstack")
	tcp := func(context.Context, netip.AddrPort) (net.Conn, error) { tcpCalls++; return nil, fail }
	udp := func(context.Context, netip.AddrPort) (net.Conn, error) { udpCalls++; return nil, fail }
	a := netip.MustParseAddrPort("100.64.1.2:21116")
	for _, n := range []string{"tcp", "udp"} {
		if _, e := dialNetstack(context.Background(), n, a, tcp, udp); !errors.Is(e, fail) {
			t.Fatal(e)
		}
	}
	if tcpCalls != 1 || udpCalls != 1 {
		t.Fatal(tcpCalls, udpCalls)
	}
	for _, a := range []netip.AddrPort{netip.MustParseAddrPort("8.8.8.8:443"), netip.MustParseAddrPort("127.0.0.1:1234")} {
		if _, e := dialNetstack(context.Background(), "tcp", a, tcp, udp); e == nil {
			t.Fatal("non-tailnet IP")
		}
	}
	if tcpCalls != 1 {
		t.Fatal("OS/other fallback attempted")
	}
	if _, e := dialNetstack(context.Background(), "tcp", netip.MustParseAddrPort("100.64.1.2:80"), nil, nil); e == nil {
		t.Fatal("nil dialer accepted")
	}
}
func TestInheritedCredentialsRejectedBeforeStart(t *testing.T) {
	for _, k := range []string{"TS_AUTHKEY", "TS_AUTH_KEY", "TS_CLIENT_SECRET", "TS_CLIENT_ID", "TS_ID_TOKEN", "TS_AUDIENCE", "TS_CONTROL_URL", "TSNET_FORCE_LOGIN"} {
		t.Run(k, func(t *testing.T) {
			t.Setenv(k, "redacted-test-value")
			if _, e := New(t.TempDir(), "test"); e == nil {
				t.Fatal("inherited authentication accepted")
			}
		})
	}
}

type countingLogBuffer struct{ writes atomic.Int64 }

func (b *countingLogBuffer) Write(p []byte) (int, error)  { b.writes.Add(1); return len(p), nil }
func (b *countingLogBuffer) TryReadLine() ([]byte, error) { return nil, nil }

type forbiddenLogTransport struct{ calls atomic.Int64 }

func (t *forbiddenLogTransport) RoundTrip(*http.Request) (*http.Response, error) {
	t.calls.Add(1)
	return nil, errors.New("test forbids network")
}
func TestNewDisablesUpstreamPrivateLogBuffering(t *testing.T) {
	if _, err := New(t.TempDir(), "test-node"); err != nil {
		t.Fatal(err)
	}
	buffer := &countingLogBuffer{}
	transport := &forbiddenLogTransport{}
	logger := logtail.NewLogger(logtail.Config{Buffer: buffer, Stderr: io.Discard, HTTPC: &http.Client{Transport: transport}}, func(string, ...any) {})
	logger.Logf("sign-in URL: %s", "https://login.tailscale.com/a/synthetic-not-a-real-login")
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := logger.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if buffer.writes.Load() != 0 {
		t.Fatal("private auth logs were written to buffer")
	}
	if transport.calls.Load() != 0 {
		t.Fatal("diagnostic upload attempted")
	}
}
