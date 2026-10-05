package backendworker

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/netip"
	"os"
	"os/exec"
	"testing"
	"time"
)

type processTestEngine struct{ pipeEngine }

func (e *processTestEngine) State(context.Context) (json.RawMessage, error) {
	return json.Marshal(map[string]any{"process": os.Getpid(), "running": true})
}
func TestWorkerProcessHelper(t *testing.T) {
	if os.Getenv("SOBALINK_SYNTHETIC_WORKER_TEST") != "1" {
		return
	}
	_ = ServeEngine(context.Background(), os.Stdin, os.Stdout, &processTestEngine{})
	os.Exit(0)
}
func TestTwoIsolatedWorkerProcesses(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	type process struct {
		cmd    *exec.Cmd
		engine *RemoteEngine
		pid    int
	}
	start := func() process {
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestWorkerProcessHelper$")
		cmd.Env = append(os.Environ(), "SOBALINK_SYNTHETIC_WORKER_TEST=1")
		in, e := cmd.StdinPipe()
		if e != nil {
			t.Fatal(e)
		}
		out, e := cmd.StdoutPipe()
		if e != nil {
			t.Fatal(e)
		}
		if e = cmd.Start(); e != nil {
			t.Fatal(e)
		}
		engine := NewRemoteEngine(ctx, out, in)
		raw, e := engine.State(ctx)
		if e != nil {
			t.Fatal(e)
		}
		var state struct {
			Process int `json:"process"`
		}
		if e = json.Unmarshal(raw, &state); e != nil {
			t.Fatal(e)
		}
		return process{cmd, engine, state.Process}
	}
	a, b := start(), start()
	defer func() { _ = a.engine.Close(); _ = b.engine.Close(); _ = a.cmd.Wait(); _ = b.cmd.Wait() }()
	if a.pid == b.pid || a.pid == os.Getpid() || b.pid == os.Getpid() {
		t.Fatal("engines were not process isolated")
	}
	_ = a.engine.Close()
	payload := bytes.Repeat([]byte("synthetic-payload"), 20000)
	conn, e := b.engine.DialIP(ctx, "tcp", netip.MustParseAddrPort("100.64.0.2:1234"))
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	written := make(chan error, 1)
	go func() { _, e := conn.Write(payload); written <- e }()
	got := make([]byte, len(payload))
	if _, e = io.ReadFull(conn, got); e != nil {
		t.Fatal(e)
	}
	if e = <-written; e != nil {
		t.Fatal(e)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("worker stream did not preserve payload across frames")
	}
}
