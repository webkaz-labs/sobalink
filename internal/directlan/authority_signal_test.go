package directlan

import "testing"

// Signal-only tests: no Node constructor, Start, transport, or publisher.
func TestOrdinaryAuthoritySignalClosesNodeAndGenerationAdmission(t *testing.T) {
	epoch := NewContextEpoch()
	cfg := Config{AuthorityCurrent: epoch.Valid}
	n := &Node{cfg: cfg, started: true}
	g := &runtimeGeneration{cfg: cfg}
	if n.readyLocked() != nil || !g.admit(func() bool { return true }) {
		t.Fatal("live signal rejected")
	}
	epoch.Invalidate()
	called := false
	if n.readyLocked() == nil || g.admit(func() bool { called = true; return true }) || called {
		t.Fatal("stale publication admitted work")
	}
}
