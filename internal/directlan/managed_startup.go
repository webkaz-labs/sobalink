package directlan

import "sync/atomic"

// ManagedStartupOwner captures one copied config and Core's receipt gate.
// Exported Go visibility bridges two trusted internal packages only. It is not
// a public wire/CLI/Web API or a substitute for the private Core sole-publisher
// admission. External values never select a callback or construct this owner.
// A self identity and one-use latch reject accidental copy/retry/rebinding.
type ManagedStartupOwner struct {
	self    *ManagedStartupOwner
	cfg     Config
	current func() bool
	used    atomic.Bool
}

// NewManagedStartupOwner is called only by the private Core startup coordinator
// after exact successful whole-state publication and context-owner join. The
// callback must reobserve that same receipt/file/process/original-deadline gate.
// Creation is data-only and cannot itself publish or start a transport.
func NewManagedStartupOwner(cfg Config, current func() bool) *ManagedStartupOwner {
	o := &ManagedStartupOwner{cfg: cloneGenerationConfig(cfg.withDefaults()), current: current}
	o.self = o
	return o
}

// take grants at most one construction attempt, including failed attempts.
// The opaque owner exposes no accessor or way to substitute later config.
func (o *ManagedStartupOwner) take() (Config, error) {
	if o == nil || o.self != o || !o.used.CompareAndSwap(false, true) || o.current == nil {
		return Config{}, ErrUnavailable
	}
	cfg := o.cfg
	if cfg.currentEndpoints == nil || cfg.ReplacementOwner == nil || cfg.ReplacementOwner.self != cfg.ReplacementOwner || cfg.AuthorityCurrent == nil || cfg.CompletionAdmission == nil || !o.current() {
		return Config{}, ErrUnavailable
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cloneGenerationConfig(cfg), nil
}

// NewManagedStartupNode leaves public NewNode closed to current projections.
// Core's exact gate is checked again after inert allocation, before returning
// any Node. Ordinary Core startup additionally rechecks before/after Start.
func NewManagedStartupNode(o *ManagedStartupOwner) (*Node, error) {
	cfg, err := o.take()
	if err != nil {
		return nil, err
	}
	n, err := newNode(cfg)
	if err != nil {
		return nil, err
	}
	if !o.current() {
		n.cancel()
		return nil, ErrUnavailable
	}
	return n, nil
}

func currentStartupPeersMatch(projected, snapshot []Peer) bool {
	if len(projected) != len(snapshot) {
		return false
	}
	peers := make(map[string]Peer, len(projected))
	for _, peer := range projected {
		peers[peer.Key] = peer
	}
	if len(peers) != len(projected) {
		return false
	}
	for _, peer := range snapshot {
		if expected, ok := peers[peer.Key]; !ok || expected != peer {
			return false
		}
		delete(peers, peer.Key)
	}
	return len(peers) == 0
}
