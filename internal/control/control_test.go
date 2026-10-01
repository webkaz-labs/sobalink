package control

import (
	"context"
	"github.com/webkaz-labs/tsnet-bridge/internal/config"
	"os"
	"testing"
	"time"
)

func TestRoundTripAndShutdown(t *testing.T) {
	ctx := context.Background()
	d := shortDir(t)
	if e := config.SecureDir(d); e != nil {
		t.Fatal(e)
	}
	s, e := Serve(ctx, d, func(ctx context.Context, n string) (any, error) { return map[string]string{"command": n}, nil })
	if e != nil {
		t.Fatal(e)
	}
	var out map[string]string
	if e = Call(ctx, d, "status", &out); e != nil || out["command"] != "status" {
		t.Fatal(out, e)
	}
	slow, e := dial(ctx, d)
	if e != nil {
		t.Fatal(e)
	}
	defer slow.Close()
	done := make(chan struct{})
	go func() { s.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("shutdown stuck on unauthenticated slow client")
	}
}
func TestUnavailable(t *testing.T) {
	ctx, c := context.WithTimeout(context.Background(), time.Second)
	defer c()
	e := Call(ctx, shortDir(t), "status", nil)
	if e == nil || !Unavailable(e) {
		t.Fatal(e)
	}
}

func shortDir(t *testing.T) string {
	t.Helper()
	d, e := os.MkdirTemp("", "tb-ipc-")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return d
}
