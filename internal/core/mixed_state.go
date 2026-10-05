package core

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"

	"github.com/webkaz-labs/sobalink/internal/connectionroute"
)

// MixedSelection is an explicit application-wide communication boundary. It is
// separate from strict LAN configuration and never silently starts Tailnet.
type MixedSelection struct {
	Backends []string `json:"backends"`
}
type mixedState struct {
	Version   int                       `json:"version"`
	Seed      string                    `json:"seed"`
	Selection MixedSelection            `json:"selection"`
	Bindings  []connectionroute.Binding `json:"bindings"`
}

func validateMixedSelection(s MixedSelection) error {
	if len(s.Backends) < 2 || len(s.Backends) > 3 {
		return errors.New("choose two or three exact connection backends")
	}
	seen := map[string]bool{}
	for _, name := range s.Backends {
		if (name != "tailnet" && name != "lan" && name != "direct-lan") || seen[name] {
			return errors.New("invalid or duplicate mixed backend")
		}
		seen[name] = true
	}
	return nil
}
func validateMixedState(s mixedState) error {
	seed, e := hex.DecodeString(s.Seed)
	if s.Version != 1 || e != nil || len(seed) != ed25519.SeedSize || hex.EncodeToString(seed) != s.Seed {
		return errors.New("invalid private mixed identity")
	}
	if e := validateMixedSelection(s.Selection); e != nil {
		return e
	}
	ids := map[string]bool{}
	claims := map[connectionroute.TransportIdentity]bool{}
	for _, b := range s.Bindings {
		id, e := connectionroute.StablePeerID(b.PublicKey)
		if e != nil || id != b.PeerID || ids[id] || len(b.Identities) < 2 || len(b.Identities) > 3 {
			return errors.New("invalid saved mixed binding")
		}
		ids[id] = true
		seenBackends := map[string]bool{}
		for _, claim := range b.Identities {
			enabled := false
			for _, name := range s.Selection.Backends {
				if name == claim.Backend {
					enabled = true
				}
			}
			if !enabled || claim.ID == "" || claims[claim] || seenBackends[claim.Backend] {
				return errors.New("ambiguous saved transport identity")
			}
			claims[claim] = true
			seenBackends[claim.Backend] = true
		}
	}
	return nil
}
func (c *Core) readMixedState() (mixedState, error) {
	var s mixedState
	e := readBoundedPrivateJSON(filepath.Join(c.dir, "mixed.json"), c.limit("resources", "lanStateBytes"), &s)
	if e != nil {
		return s, e
	}
	return s, validateMixedState(s)
}
func (c *Core) configureMixed(selection *MixedSelection) error {
	if selection == nil {
		return errors.New("explicit mixed backend selection required")
	}
	if e := validateMixedSelection(*selection); e != nil {
		return e
	}
	if c.nodeCopy() != nil {
		return errors.New("stop networking before changing mixed backends")
	}
	for _, name := range selection.Backends {
		switch name {
		case "lan":
			store := c.lanStoreCopy()
			if store == nil {
				return errors.New("configure and pair the LAN backend before mixed mode")
			}
			if store.copy().DestinationPolicy.Strict() {
				return errors.New("strict LAN destinations cannot silently enable a mixed application boundary; review LAN policy first")
			}
		case "direct-lan":
			if c.directLANStoreCopy() == nil {
				return errors.New("configure and pair direct LAN before mixed mode")
			}
		}
	}
	s, e := c.readMixedState()
	if e != nil && !errors.Is(e, os.ErrNotExist) {
		return e
	}
	if e == nil {
		if reflect.DeepEqual(s.Selection, *selection) {
			return nil
		}
		if len(s.Bindings) > 0 {
			return errors.New("remove mixed identity bindings before changing enabled backends")
		}
	} else {
		seed := make([]byte, ed25519.SeedSize)
		if _, e = rand.Read(seed); e != nil {
			return e
		}
		s = mixedState{Version: 1, Seed: hex.EncodeToString(seed), Bindings: []connectionroute.Binding{}}
	}
	s.Selection = MixedSelection{Backends: append([]string(nil), selection.Backends...)}
	if e = validateMixedState(s); e != nil {
		return e
	}
	raw, e := json.Marshal(s)
	if e != nil {
		return e
	}
	return c.writeAtomic(filepath.Join(c.dir, "mixed.json"), raw)
}
func (c *Core) newMixedBackend() (NetworkBackend, error) {
	return c.newMixedBackendUsing(func(ctx context.Context, dir, mode, hostname string) (NetworkBackend, error) {
		return startProcessBackend(ctx, dir, mode, hostname)
	})
}
func (c *Core) newMixedBackendUsing(makeWorker func(context.Context, string, string, string) (NetworkBackend, error)) (NetworkBackend, error) {
	s, e := c.readMixedState()
	if e != nil {
		return nil, e
	}
	seed, _ := hex.DecodeString(s.Seed)
	private := ed25519.NewKeyFromSeed(seed)
	id, _ := connectionroute.StablePeerID(private.Public().(ed25519.PublicKey))
	limits, e := selectedWorkerLimits(c.capacityPolicy())
	if e != nil {
		return nil, e
	}
	// Validate every selected boundary before any worker can start external I/O.
	for _, name := range s.Selection.Backends {
		switch name {
		case "lan":
			store := c.lanStoreCopy()
			if store == nil || store.copy().DestinationPolicy.Strict() || store.routesNeedRecovery() {
				return nil, errors.New("mixed networking is outside current LAN permission")
			}
			if e := validateLANState(store.copy()); e != nil {
				return nil, e
			}
		case "direct-lan":
			store := c.directLANStoreCopy()
			if store == nil || store.needsRecovery() {
				return nil, errors.New("direct LAN requires reviewed durable state")
			}
			if _, e := directLANConfig(store.copy()); e != nil {
				return nil, e
			}
		}
	}
	n := &mixedBackend{sourceLimit: limits.Handles, packetQueueLimit: int(c.limit("resources", "udpQueuePackets")), workerLimits: limits, ctx: c.ctx, self: mixedIP(id), nodes: map[string]NetworkBackend{}, order: append([]string(nil), s.Selection.Backends...), bindings: s.Bindings, sources: map[netip.AddrPort]mixedSource{}}
	for _, name := range n.order {
		var node NetworkBackend
		switch name {
		case "tailnet", "lan":
			if name == "lan" {
				store := c.lanStoreCopy()
				if store == nil || store.copy().DestinationPolicy.Strict() {
					_ = n.Close()
					return nil, errors.New("mixed networking is outside strict LAN permission")
				}
			}
			node, e = makeWorker(c.ctx, c.dir, name, c.profileCopy().Settings.Hostname)
		case "direct-lan":
			store := c.directLANStoreCopy()
			if store == nil {
				_ = n.Close()
				return nil, errors.New("direct LAN setup required")
			}
			node, e = c.newDirectLANBackend(store)
		}
		if e != nil {
			_ = n.Close()
			return nil, e
		}
		n.nodes[name] = node
	}
	return n, nil
}
func (c *Core) mixedStatus() map[string]any {
	s, e := c.readMixedState()
	if errors.Is(e, os.ErrNotExist) {
		return map[string]any{"configured": false}
	}
	if e != nil {
		return map[string]any{"configured": false, "error": "saved mixed configuration unavailable"}
	}
	seed, _ := hex.DecodeString(s.Seed)
	key := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
	id, _ := connectionroute.StablePeerID(key)
	out := map[string]any{"configured": true, "backends": s.Selection.Backends, "identity": id, "publicKey": hex.EncodeToString(key), "bindings": s.Bindings, "connectionScope": "new-connections", "existingTCPMigration": false}
	c.addMixedRuntimeStatus(out)
	return out
}
func (c *Core) mixedCommand(ctx context.Context, name string, raw json.RawMessage) (any, error) {
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	if name == "mixed.status" {
		var in struct{}
		if e := decodePayload(raw, &in); e != nil {
			return nil, e
		}
		return c.mixedStatus(), nil
	}
	if name == "mixed.unbind" {
		return c.unbindMixedPeer(raw)
	}
	if name == "mixed.bind" {
		return c.bindMixedPeers(ctx, raw)
	}
	return nil, errors.New("unknown mixed identity command")
}
