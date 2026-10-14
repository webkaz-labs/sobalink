package core

import (
	"errors"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/lanlink"
)

// JSON clients must be able to echo each generation without losing precision.
const setupMaxGeneration uint64 = 1<<53 - 1

// A durable basis is keyed only by the complete cryptographic key. It does not
// grant application trust. Revocation and explicit re-enrollment each advance
// the key generation; re-enrollment separately retires every old LAN writer.
type setupTrustRecord struct {
	PeerKey      string `json:"peer_key"`
	Verification string `json:"verification"`
	Revoked      bool   `json:"revoked,omitempty"`
	Generation   uint64 `json:"generation,omitempty"`
}

type SetupRevokedRecord struct {
	PeerKey      string `json:"peerKey"`
	Generation   uint64 `json:"generation"`
	Verification string `json:"verification"`
}
type SetupRevokedView struct {
	Kind    string               `json:"kind"`
	Version int                  `json:"version"`
	Records []SetupRevokedRecord `json:"records"`
}
type SetupReenrollInput struct {
	PeerKey            string `json:"peerKey"`
	ExpectedGeneration uint64 `json:"expectedGeneration"`
	Consent            bool   `json:"consent"`
}
type SetupReenrollView struct {
	Kind         string `json:"kind"`
	Version      int    `json:"version"`
	PeerKey      string `json:"peerKey"`
	Generation   uint64 `json:"generation"`
	Verification string `json:"verification"`
	Paired       bool   `json:"paired"`
	Trusted      bool   `json:"trusted"`
}

func setupVerification(p lanlink.SetupParticipant) string {
	if p.Protocol == 2 {
		return p.Verification
	}
	return "full-sha256"
}
func setupRecordGeneration(r setupTrustRecord) uint64 {
	// Earlier tombstones have no generation field. They still represent one
	// revocation and must never look like an untouched key to a new client.
	if r.Generation == 0 && r.Revoked {
		return 1
	}
	return r.Generation
}
func setupGenerations(state lanState) map[string]uint64 {
	out := make(map[string]uint64, len(state.SetupTrust))
	for _, r := range state.SetupTrust {
		out[r.PeerKey] = setupRecordGeneration(r)
	}
	return out
}
func validateSetupTrust(s lanState) error {
	if s.SetupPersistenceEpoch > setupMaxGeneration {
		return errors.New("invalid saved setup persistence epoch")
	}
	seen := map[string]bool{}
	for _, p := range s.SetupTrust {
		if !setupHex(p.PeerKey) || seen[p.PeerKey] || p.Generation > setupMaxGeneration || (p.Verification != "tofu" && p.Verification != "full-sha256" && p.Verification != "unknown") {
			return errors.New("invalid saved setup trust basis")
		}
		seen[p.PeerKey] = true
	}
	return nil
}
func rememberSetupTrust(state *lanState, key, basis string) error {
	for i, p := range state.SetupTrust {
		if p.PeerKey != key {
			continue
		}
		if p.Revoked {
			return setupError("setup_peer_revoked", "this key was revoked; explicitly re-enroll it offline before starting a fresh setup")
		}
		if p.Verification == "full-sha256" && basis != "full-sha256" {
			return setupError("setup_verification_downgrade", "this key was previously fully verified; start a fresh full-fingerprint session")
		}
		if basis == "full-sha256" || p.Verification == "unknown" {
			state.SetupTrust[i].Verification = basis
		}
		return nil
	}
	state.SetupTrust = append(state.SetupTrust, setupTrustRecord{PeerKey: key, Verification: basis})
	return nil
}
func revokeSetupTrust(state *lanState, key string) error {
	for i, r := range state.SetupTrust {
		if r.PeerKey != key {
			continue
		}
		if r.Revoked {
			return nil
		}
		if r.Generation >= setupMaxGeneration {
			return setupError("setup_generation_exhausted", "this key's setup generation is exhausted")
		}
		state.SetupTrust[i].Revoked = true
		state.SetupTrust[i].Generation++
		return nil
	}
	state.SetupTrust = append(state.SetupTrust, setupTrustRecord{PeerKey: key, Verification: "unknown", Revoked: true, Generation: 1})
	return nil
}
func markRemovedSetupTrust(before lanState, after *lanState) error {
	current := map[string]bool{}
	for _, p := range after.Trust.Peers {
		current[p.Key] = true
	}
	for _, p := range before.Trust.Peers {
		if !current[p.Key] {
			if err := revokeSetupTrust(after, p.Key); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *Core) recordSetupRevocation(key string) error {
	store := c.lanStoreCopy()
	if store == nil {
		return nil
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	next := cloneLANState(store.state)
	for _, r := range next.SetupTrust {
		if r.PeerKey == key && r.Revoked {
			return nil
		}
	}
	if err := revokeSetupTrust(&next, key); err != nil {
		return err
	}
	return store.saveLocked(next)
}

func (c *Core) revokedSetupKeys() SetupRevokedView {
	out := SetupRevokedView{Kind: "sobalink-setup-revoked", Version: 1, Records: []SetupRevokedRecord{}}
	if store := c.lanStoreCopy(); store != nil {
		for _, r := range store.copy().SetupTrust {
			if r.Revoked {
				out.Records = append(out.Records, SetupRevokedRecord{r.PeerKey, setupRecordGeneration(r), r.Verification})
			}
		}
	}
	return out
}

// Caller holds c.op. No existing network or setup owner is allowed to survive
// this explicit boundary. This resets no application permissions or pair data.
func (c *Core) reenrollSetupKey(in SetupReenrollInput) (SetupReenrollView, error) {
	var empty SetupReenrollView
	if !in.Consent || !setupHex(in.PeerKey) || in.ExpectedGeneration == 0 || in.ExpectedGeneration > setupMaxGeneration {
		return empty, setupError("setup_reenroll_consent_required", "review the exact revoked key and current generation, then explicitly consent to re-enrollment")
	}
	if c.nodeCopy() != nil || c.setupProcess().State != "ready" {
		return empty, setupError("setup_reenroll_offline_required", "stop the network and reopen the profile offline before re-enrollment")
	}
	if old := c.setupCopy(); old != nil {
		if old.ctx.Err() == nil {
			return empty, setupError("setup_busy", "cancel the current setup and wait for cleanup before re-enrollment")
		}
		select {
		case <-old.done:
		default:
			return empty, setupError("setup_busy", "wait for previous setup cleanup before re-enrollment")
		}
	}
	store := c.lanStoreCopy()
	if store == nil {
		return empty, setupError("setup_generation_changed", "the revoked key or generation changed; refresh the revoked-key list")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.routeRecovery || store.startUncertain {
		return empty, config.ErrAtomicRecovery
	}
	next := cloneLANState(store.state)
	for _, p := range next.Trust.Peers {
		if p.Key == in.PeerKey {
			return empty, setupError("setup_peer_still_paired", "complete revocation of the exact saved pair before re-enrollment")
		}
	}
	for _, r := range next.Remotes {
		if r.Peer.Key == in.PeerKey {
			return empty, setupError("setup_peer_still_paired", "complete revocation of the exact saved pair before re-enrollment")
		}
	}
	for i, r := range next.SetupTrust {
		if r.PeerKey != in.PeerKey || !r.Revoked || setupRecordGeneration(r) != in.ExpectedGeneration {
			continue
		}
		if in.ExpectedGeneration >= setupMaxGeneration || next.SetupPersistenceEpoch >= setupMaxGeneration {
			return empty, setupError("setup_generation_exhausted", "the saved setup generation is exhausted")
		}
		next.SetupTrust[i].Revoked = false
		next.SetupTrust[i].Generation = in.ExpectedGeneration + 1
		next.SetupPersistenceEpoch++
		if err := store.saveLocked(next); err != nil {
			return empty, err
		}
		return SetupReenrollView{Kind: "sobalink-setup-reenroll", Version: 1, PeerKey: r.PeerKey, Generation: next.SetupTrust[i].Generation, Verification: r.Verification}, nil
	}
	return empty, setupError("setup_generation_changed", "the revoked key or generation changed; refresh the revoked-key list")
}

func rejectRevokedSetupPeers(records []setupTrustRecord, peers []lanlink.Peer) error {
	revoked := map[string]bool{}
	for _, r := range records {
		if r.Revoked {
			revoked[r.PeerKey] = true
		}
	}
	for _, p := range peers {
		if revoked[p.Key] {
			return setupError("setup_peer_revoked", "a revoked key cannot be saved by a pending pairing operation")
		}
	}
	return nil
}
func (s *setupSession) verifySavedBasis(key string) error {
	if s.core == nil {
		return nil
	}
	store := s.core.lanStoreCopy()
	if store == nil {
		return nil
	}
	generation := uint64(0)
	for _, r := range store.copy().SetupTrust {
		if r.PeerKey != key {
			continue
		}
		if r.Revoked {
			return setupError("setup_peer_revoked", "this key was revoked; it cannot be registered by connection setup")
		}
		generation = setupRecordGeneration(r)
		if r.Verification == "full-sha256" && setupVerification(s.local) != "full-sha256" {
			return setupError("setup_verification_downgrade", "this key was previously fully verified; use a fresh full-fingerprint session")
		}
	}
	if s.setupGenerations[key] != generation {
		return setupError("setup_generation_changed", "this setup predates a revocation or re-enrollment; start a fresh session")
	}
	return nil
}
