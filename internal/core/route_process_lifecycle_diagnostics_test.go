//go:build lanlink_integration

package core

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Use only immediately before terminating the test: after a timeout the child
// stream must not be reused. The existing fixture cleanup owns pipe closure
// and process shutdown; diagnostic collection never kills or mutates the child.
func observeRouteLifecycle(ctx context.Context, p *routeCoreProcess) routeLifecycleEvidence {
	if ctx.Err() != nil {
		return routeLifecycleEvidence{}
	}
	done := make(chan routeLifecycleEvidence, 1)
	go func() {
		var v routeLifecycleEvidence
		if json.NewEncoder(p.input).Encode(struct{ Name string }{"fixture.lifecycle-evidence"}) == nil {
			var reply processReply
			if p.output.Decode(&reply) == nil && reply.Error == "" {
				if json.Unmarshal(reply.Value, &v) != nil {
					v = routeLifecycleEvidence{}
				}
			}
		}
		done <- v
	}()
	select {
	case v := <-done:
		return v
	case <-ctx.Done():
		return routeLifecycleEvidence{}
	}
}

// A failure-only, process-wide observation after commit retries stop. This is
// not an ownership capability or proof of the holder at an earlier refusal.
// Only fixed booleans leave the child. Stack text, arguments and source paths
// stay in this bounded private buffer and are never logged or returned.
type routeLifecycleEvidence struct {
	Observed       bool `json:"observed"`
	Truncated      bool `json:"truncated"`
	OwnershipFrame bool `json:"ownershipFrame"`
	Outbox         bool `json:"outbox"`
	Receiver       bool `json:"receiver"`
	Messages       bool `json:"messages"`
	Resources      bool `json:"resources"`
	Profile        bool `json:"profile"`
	MutexWait      bool `json:"mutexWait"`
	IOWait         bool `json:"ioWait"`
	Syscall        bool `json:"syscall"`
}

func captureRouteLifecycleEvidence() routeLifecycleEvidence {
	stack := make([]byte, 256<<10)
	n := runtime.Stack(stack, true)
	return classifyRouteLifecycleEvidence(stack[:n], n == len(stack))
}

func classifyRouteLifecycleEvidence(stack []byte, truncated bool) routeLifecycleEvidence {
	v := routeLifecycleEvidence{Observed: true, Truncated: truncated}
	blocks := bytes.Split(stack, []byte("\n\n"))
	if truncated {
		// A partial block cannot establish which call path owns the mutex.
		blocks = blocks[:len(blocks)-1]
	}
	for _, block := range blocks {
		if !bytes.Contains(block, []byte("\ngithub.com/webkaz-labs/sobalink/internal/config.(*Lock).withOwnershipInfoLocked(")) {
			continue
		}
		v.OwnershipFrame = true
		header, _, _ := bytes.Cut(block, []byte("\n"))
		v.MutexWait = v.MutexWait || bytes.Contains(header, []byte("[sync.Mutex.Lock"))
		v.IOWait = v.IOWait || bytes.Contains(header, []byte("[IO wait"))
		v.Syscall = v.Syscall || bytes.Contains(header, []byte("[syscall"))
		for _, frame := range bytes.Split(block, []byte("\n")) {
			const core = "github.com/webkaz-labs/sobalink/internal/core."
			if !bytes.HasPrefix(frame, []byte(core)) {
				continue
			}
			frame = bytes.TrimPrefix(frame, []byte(core))
			has := func(prefix string) bool { return bytes.HasPrefix(frame, []byte(prefix)) }
			v.Outbox = v.Outbox || has("(*outboxCoordinator).") || has("(*Core).initializeOutbox")
			v.Receiver = v.Receiver || has("(*Core).initializeDurableReceiver")
			v.Messages = v.Messages || has("(*Core).initializeMessageStore") || has("(*Core).mutateMessageStoreLocked") || has("(*Core).reconcileMessageStoreLocked")
			v.Resources = v.Resources || has("(*Core).withResource") || has("(*Core).resource") || has("(*Core).initializeResource") || has("(*Core).initializeCatalogViews") || has("(*Core).catalogView") || has("(*resourceGroupStore).")
			v.Profile = v.Profile || has("(*Core).persistDurableProfile") || has("openExistingProfile")
		}
	}
	return v
}

func TestRouteLifecycleEvidenceIsFixedAndBounded(t *testing.T) {
	const ownership = "github.com/webkaz-labs/sobalink/internal/config.(*Lock).withOwnershipInfoLocked(0x123, private-argument)\n\t/synthetic-private-profile/lock_ownership.go:1 +0x42\n"
	const caller = "github.com/webkaz-labs/sobalink/internal/core.(*Core).reconcileMessageStoreLocked(0x456)\n"
	stack := "goroutine 999 [sync.Mutex.Lock, 2 minutes]:\n" + ownership + caller + "\n"
	v := classifyRouteLifecycleEvidence([]byte(stack), false)
	if !v.Observed || !v.OwnershipFrame || !v.Messages || !v.MutexWait || v.Truncated || v.Outbox || v.Receiver || v.Resources || v.Profile || v.IOWait || v.Syscall {
		t.Fatal("lifecycle observation lost a fixed ownership/caller class")
	}
	raw, err := json.Marshal(v)
	if err != nil || len(raw) > 256 {
		t.Fatal("lifecycle observation is not bounded")
	}
	for _, private := range []string{"synthetic-private", "private-argument", "0x123", "0x456", "999", "2 minutes", ".go:", "reconcileMessageStoreLocked"} {
		if strings.Contains(string(raw), private) {
			t.Fatal("raw stack data escaped lifecycle observation")
		}
	}
	if v := classifyRouteLifecycleEvidence([]byte("goroutine 1 [IO wait]:\n"+caller), false); v.OwnershipFrame || v.Messages || v.IOWait {
		t.Fatal("unowned call path was attributed to an ownership callback")
	}
	if v := classifyRouteLifecycleEvidence([]byte(strings.TrimSuffix(stack, "\n\n")), true); !v.Truncated || v.OwnershipFrame {
		t.Fatal("truncated stack block became ownership evidence")
	}
	// Exercise the real bounded capture without requiring a specific schedule.
	if v := captureRouteLifecycleEvidence(); !v.Observed {
		t.Fatal("native lifecycle snapshot was not captured")
	}
}

func TestRouteLifecycleEvidenceObservesHeldOwnership(t *testing.T) {
	c := outboxFixture(t)
	release := holdOutboxLifecycle(t, c)
	defer release()
	if v := captureRouteLifecycleEvidence(); !v.Observed || !v.OwnershipFrame {
		t.Fatal("live held lifecycle callback was not observed")
	}
}

type routeLifecycleObservedWriter struct {
	*io.PipeWriter
	entered chan struct{}
}

func (w routeLifecycleObservedWriter) Write(p []byte) (int, error) {
	close(w.entered)
	return w.PipeWriter.Write(p)
}

func TestRouteLifecycleObservationTimeoutDoesNotClosePipes(t *testing.T) {
	for _, stage := range []string{"write", "response"} {
		t.Run(stage, func(t *testing.T) {
			input, writer := io.Pipe()
			reader, output := io.Pipe()
			defer input.Close()
			defer writer.Close()
			defer reader.Close()
			defer output.Close()
			entered := make(chan struct{})
			p := &routeCoreProcess{input: routeLifecycleObservedWriter{writer, entered}, output: json.NewDecoder(reader)}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan routeLifecycleEvidence, 1)
			go func() { done <- observeRouteLifecycle(ctx, p) }()
			<-entered
			readRequest := func() {
				var request struct{ Name string }
				if json.NewDecoder(input).Decode(&request) != nil || request.Name != "fixture.lifecycle-evidence" {
					t.Fatal("diagnostic observer closed its input or changed command")
				}
			}
			if stage == "response" {
				readRequest()
			}
			cancel()
			select {
			case got := <-done:
				if got != (routeLifecycleEvidence{}) {
					t.Fatal("canceled observation invented lifecycle evidence")
				}
			case <-time.After(time.Second):
				t.Fatal("diagnostic observation did not honor cancellation")
			}
			// Both pipes remain usable. Complete the outstanding read so the
			// observer exits; production fixture cleanup owns this on failure.
			if stage == "write" {
				readRequest()
			}
			if _, err := output.Write([]byte("{\"Value\":{\"observed\":true}}\n")); err != nil {
				t.Fatal("diagnostic observer closed its response pipe")
			}
		})
	}
}
