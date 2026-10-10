//go:build resource_process_native && linux && (amd64 || arm64)

package resourceacceptance

import (
	"context"
	"testing"
	"time"
)

// This registers real signal notification, but sends no actual OS signal.
func TestProcessEntrySignalRegistrationJoinNativeSignal(t *testing.T) {
	cutoff := time.Now().Add(3 * time.Second)
	s := newProcessEntrySignal()
	t.Cleanup(func() {
		if !s.stopBefore(cutoff) {
			t.Error("signal producer/Stop cleanup unjoined")
		}
	})
	if s.ctx.Err() != nil {
		t.Fatal("context canceled before stopping")
	}
	if !s.stopBefore(cutoff) || context.Cause(s.ctx) != context.Canceled || s.ctx.Err() != context.Canceled {
		t.Fatal("normal signal stop/cause failed")
	}
	if !processWait(s.producerDone, cutoff) || !processWait(s.stopDone, cutoff) {
		t.Fatal("owned signal tasks not observed")
	}
	// This says nothing about the process-lifetime stdlib watchSignalLoop.
}
