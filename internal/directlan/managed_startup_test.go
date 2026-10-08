package directlan

import (
	"context"
	"testing"
	"time"
)

// Opaque owner bookkeeping only. No Node constructor, Start, native generation,
// publication, listener, signed movement or application traffic is executed.
func startupOwnerData(t *testing.T) Config {
	t.Helper()
	base, model, _, now := currentProjectionFixture(t)
	cfg, err := ProjectCurrentEndpointConfig(base, model, now)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ReplacementOwner = NewManagedTransportOwner()
	cfg.AuthorityCurrent = func() bool { return true }
	cfg.CompletionAdmission = func(context.Context, *ManagedCompletionRequest) (ContextResponse, error) {
		return ContextResponse{}, ErrUnavailable
	}
	return cfg
}

func TestManagedStartupOwnerTakesExactCopiedConfigOnce(t *testing.T) {
	cfg := startupOwnerData(t)
	original := cfg.Peers[0]
	checks := 0
	owner := NewManagedStartupOwner(cfg, func() bool { checks++; return true })
	cfg.Peers[0].Name = "changed-after-capture"
	got, err := owner.take()
	if err != nil || got.Peers[0] != original || checks != 1 {
		t.Fatal("one-use copied startup data", err)
	}
	got.Peers[0].Name = "changed-return"
	if owner.cfg.Peers[0] != original {
		t.Fatal("returned data aliases opaque owner")
	}
	if _, err := owner.take(); err == nil || checks != 1 {
		t.Fatal("startup owner reused")
	}
}

func TestManagedStartupOwnerRejectsUnownedOrStaleData(t *testing.T) {
	for _, change := range []string{"zero", "copied-identity", "missing-projection", "missing-replacement", "missing-authority", "missing-completion", "stale"} {
		t.Run(change, func(t *testing.T) {
			cfg := startupOwnerData(t)
			current := true
			switch change {
			case "missing-projection":
				cfg.currentEndpoints = nil
			case "missing-replacement":
				cfg.ReplacementOwner = nil
			case "missing-authority":
				cfg.AuthorityCurrent = nil
			case "missing-completion":
				cfg.CompletionAdmission = nil
			case "stale":
				current = false
			}
			owner := NewManagedStartupOwner(cfg, func() bool { return current })
			if change == "zero" {
				owner = &ManagedStartupOwner{}
			} else if change == "copied-identity" {
				owner = &ManagedStartupOwner{self: owner.self, cfg: owner.cfg, current: owner.current}
			}
			if _, err := owner.take(); err == nil {
				t.Fatal("unowned or stale startup data accepted")
			}
			current = true
			if _, err := owner.take(); err == nil {
				t.Fatal("failed attempt reused")
			}
		})
	}
}

func TestManagedStartupPreservesSealedPeerOrder(t *testing.T) {
	projected := []Peer{{Key: "second", Name: "synthetic-b"}, {Key: "first", Name: "synthetic-a"}}
	snapshot := []Peer{projected[1], projected[0]}
	if !currentStartupPeersMatch(projected, snapshot) || projected[0].Key != "second" {
		t.Fatal("transport sorting changed projected order")
	}
	snapshot[0].Name = "changed"
	if currentStartupPeersMatch(projected, snapshot) || currentStartupPeersMatch(projected, []Peer{projected[0], projected[0]}) || currentStartupPeersMatch(projected, projected[:1]) {
		t.Fatal("changed membership or identity accepted")
	}
}

func TestManagedStartupAllInactiveClonePreservesProjectionSeal(t *testing.T) {
	base, model, remote, now := currentProjectionFixture(t)
	model = currentProjectionSet(t, model, remote, "7", "127.0.0.2:45107", now, false)
	cfg, err := ProjectCurrentEndpointConfig(currentProjectionBase(base, model), model, now.Add(2*time.Hour))
	if err != nil || len(cfg.Peers) != 0 || len(cfg.InactiveEndpointPeerKeys()) != 1 {
		t.Fatal("inert expired projection", err)
	}
	copy := cloneGenerationConfig(cfg)
	if copy.Validate() != nil || (copy.Peers == nil) != (cfg.Peers == nil) {
		t.Fatal("clone changed empty projection seal")
	}
	cfg.ReplacementOwner = NewManagedTransportOwner()
	cfg.AuthorityCurrent = func() bool { return true }
	cfg.CompletionAdmission = func(context.Context, *ManagedCompletionRequest) (ContextResponse, error) {
		return ContextResponse{}, ErrUnavailable
	}
	owner := NewManagedStartupOwner(cfg, func() bool { return true })
	if got, err := owner.take(); err != nil || got.Validate() != nil || len(got.Peers) != 0 {
		t.Fatal("empty startup data rejected", err)
	}
}
