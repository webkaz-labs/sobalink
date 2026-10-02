package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/webkaz-labs/tsnet-bridge/internal/config"
)

type blockedProcessReader struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (r *blockedProcessReader) Read([]byte) (int, error) {
	r.once.Do(func() { close(r.started) })
	<-r.release
	return 0, io.EOF
}

func blockingProcessInput(t *testing.T) (context.Context, context.CancelFunc, *processInput, <-chan struct{}) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	reader := &blockedProcessReader{started: make(chan struct{}), release: make(chan struct{})}
	input := newProcessInput(ctx, reader)
	t.Cleanup(func() {
		cancel()
		close(reader.release)
		select {
		case <-reader.started:
			select {
			case <-input.done:
			case <-time.After(5 * time.Second):
				t.Error("input worker did not stop after its OS read returned")
			}
		default:
		}
	})
	return ctx, cancel, input, reader.started
}

func TestProcessInputCancelsBlockedPromptWithoutWaitingForRead(t *testing.T) {
	_, cancel, input, started := blockingProcessInput(t)
	done := make(chan error, 1)
	go func() { _, err := newPrompts(input, io.Discard).ask("Name: "); done <- err }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("input read did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("prompt did not cancel")
	}
}

func TestProcessInputCancellationDoesNotSaveOrConfirm(t *testing.T) {
	dir := saveRules(t)
	fake := newFake(t, dir)
	ctx, cancel, input, started := blockingProcessInput(t)
	done := make(chan error, 1)
	var out bytes.Buffer
	go func() { done <- configureRule(ctx, dir, "forward", []string{"--manual", "--save-only"}, input, &out) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("wizard did not request input")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("wizard did not cancel")
	}
	c, err := config.Load(dir)
	if err != nil || len(c.Rules) != 0 || len(fake.actions()) != 0 || strings.Contains(out.String(), "Save disabled without connecting?") {
		t.Fatalf("canceled wizard mutated state or asked confirmation: config %v, actions %v, err %v", c.Rules, fake.actions(), err)
	}
}

func TestProcessInputCancellationRejectsBufferedConfirmation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	input := newProcessInput(ctx, strings.NewReader("first\ny\n"))
	var out bytes.Buffer
	p := newPrompts(input, &out)
	if got, err := p.ask("Name: "); err != nil || got != "first" {
		t.Fatalf("%q, %v", got, err)
	}
	cancel()
	out.Reset()
	if err := p.confirm("Save?", false); !errors.Is(err, context.Canceled) || out.Len() != 0 {
		t.Fatalf("accepted buffered confirmation after cancellation: err %v, output %q", err, out.String())
	}
	select {
	case <-input.done:
	case <-time.After(5 * time.Second):
		t.Fatal("idle reader did not stop")
	}
}

func TestProcessInputKeepsChildStdinAndDoesNotReadAhead(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	original := strings.NewReader("abc")
	input := newProcessInput(ctx, original)
	if unwrapProcessInput(input) != original || unwrapProcessInput(original) != original {
		t.Fatal("child stdin changed")
	}
	// Each request asks for one byte. An eager copy loop would consume the
	// remainder; a process reader must leave it for a subsequent child command.
	for _, want := range []byte{'a', 'b'} {
		b := make([]byte, 1)
		if n, err := input.Read(b); n != 1 || err != nil || b[0] != want {
			t.Fatalf("read %q, %d, %v", b, n, err)
		}
	}
	cancel()
	select {
	case <-input.done:
	case <-time.After(5 * time.Second):
		t.Fatal("reader kept reading without a request")
	}
	if rest, err := io.ReadAll(original); err != nil || string(rest) != "c" {
		t.Fatalf("read-ahead consumed stdin: %q, %v", rest, err)
	}
}

func TestCanceledOfflineSaveNeverFallsBackToDisk(t *testing.T) {
	dir := saveRules(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	rule := config.Rule{Name: "canceled", Purpose: "web", Direction: "forward", Network: "tcp", ListenPort: 18080, TargetHost: "server.example.ts.net", TargetPort: 8080, PeerID: "peer1"}
	installRequest(t, func(context.Context, string, string, any) error {
		t.Fatal("canceled save contacted service")
		return nil
	})
	if err := saveRule(ctx, dir, rule, false); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	saved, err := config.Load(dir)
	if err != nil || len(saved.Rules) != 0 {
		t.Fatal("canceled save changed profile", saved, err)
	}
}
