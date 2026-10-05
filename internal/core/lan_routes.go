package core

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"slices"
	"time"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/lanlink"
)

func (s *lanStore) requireRouteRecovery() { s.mu.Lock(); s.routeRecovery = true; s.mu.Unlock() }

func (s *lanStore) routesNeedRecovery() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.routeRecovery
}

func validateLANRoutes(state lanState) error {
	if state.Version == 1 {
		if len(state.RouteCandidates) != 0 {
			return errors.New("route candidates require private state version 2")
		}
		for _, remote := range state.Remotes {
			if remote.Routes != nil {
				return errors.New("route approvals require private state version 2")
			}
		}
	}
	if len(state.RouteCandidates) >= lanlink.MaxRouteCandidates {
		return lanlink.ErrRouteUpdate
	}
	seen := map[string]bool{}
	if state.Selection != nil {
		seen[state.Selection.Address] = true
	}
	for _, candidate := range state.RouteCandidates {
		if candidate.Validate() != nil || seen[candidate.Relay.Address.String()] {
			return lanlink.ErrRouteUpdate
		}
		seen[candidate.Relay.Address.String()] = true
	}
	if len(state.RouteCandidates) != 0 && state.Selection == nil {
		return lanlink.ErrRouteUpdate
	}
	for _, remote := range state.Remotes {
		if remote.Routes != nil && remote.Routes.Version >= 2 && state.Version < 3 {
			return errors.New("persistent route permissions require private state version 3")
		}
		if err := lanlink.ValidateRouteState(state.Identity, remote); err != nil {
			return err
		}
	}
	return nil
}

// Existing setup grants only its original singleton. Additional exact routes
// require an explicit local edit, never an authenticated remote offer alone.
func ownLANCandidates(state lanState) ([]lanlink.RouteCandidate, error) {
	relay, err := storedLANRelay(state)
	if err != nil {
		return nil, err
	}
	scope := "external"
	if relay.Address.Addr().IsPrivate() || relay.Address.Addr().IsLoopback() {
		scope = "local"
	}
	out := []lanlink.RouteCandidate{{Relay: relay, Scope: scope}}
	return append(out, state.RouteCandidates...), nil
}

func (b *lanBackend) routeControl() *lanlink.Node { return b.routeNode }
func (c *Core) routeControl() (*lanlink.Node, func(), error) {
	if store := c.lanStoreCopy(); store != nil && store.routesNeedRecovery() {
		return nil, nil, config.ErrAtomicRecovery
	}
	if active := c.nodeCopy(); active != nil {
		if provider, ok := active.(interface{ routeControl() *lanlink.Node }); ok && provider.routeControl() != nil {
			return provider.routeControl(), func() {}, nil
		}
		return nil, nil, errors.New("start the selected LAN backend to manage its paired routes")
	}
	store := c.lanStoreCopy()
	if store == nil {
		return nil, nil, errors.New("configure LAN and pair a device first")
	}
	if store.routesNeedRecovery() {
		return nil, nil, config.ErrAtomicRecovery
	}
	state := store.copy()
	relay, err := storedLANRelay(state)
	if err != nil {
		return nil, nil, err
	}
	book := lanlink.NewBookWithPeerLimit(store.peerLimit)
	if err := book.Restore(state.Trust); err != nil {
		return nil, nil, err
	}
	node, err := lanlink.NewNode(lanlink.NodeConfig{Identity: state.Identity, DestinationPolicy: state.DestinationPolicy, Relay: relay, Trust: book, Remotes: state.Remotes, Persist: store.persist})
	if err != nil {
		return nil, nil, codedLANError(err)
	}
	return node, func() { _ = node.Close() }, nil
}

func (c *Core) editLANRoute(candidate lanlink.RouteCandidate, removeID string) error {
	if c.nodeCopy() != nil {
		return &lanCommandError{"network_restart_required", "stop soba and start with --offline before editing prepared relay candidates; saved pairs are kept"}
	}
	store := c.lanStoreCopy()
	if store == nil {
		return errors.New("configure the original LAN relay first")
	}
	next := store.copy()
	if next.Selection == nil {
		return errors.New("configure the original LAN relay first")
	}
	if removeID != "" {
		i := slices.IndexFunc(next.RouteCandidates, func(v lanlink.RouteCandidate) bool { return v.ID() == removeID })
		if i < 0 {
			return errors.New("additional relay candidate was not found")
		}
		next.RouteCandidates = slices.Delete(next.RouteCandidates, i, i+1)
	} else {
		if err := candidate.Validate(); err != nil {
			return err
		}
		for _, existing := range next.RouteCandidates {
			if existing == candidate {
				return nil
			}
		}
		next.RouteCandidates = append(next.RouteCandidates, candidate)
	}
	if next.Version < 2 {
		next.Version = 2
	}
	err := store.save(next)
	if err != nil && removeID != "" {
		store.requireRouteRecovery()
		return errors.Join(err, config.ErrAtomicRecovery)
	}
	return err
}

func (c *Core) lanRoutesCommand(ctx context.Context, name string, raw json.RawMessage) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if name == "lan.routes.add" {
		var input struct {
			Address           string `json:"address"`
			CertificateSHA256 string `json:"certificateSHA256"`
			Scope             string `json:"scope"`
		}
		if err := decodePayload(raw, &input); err != nil {
			return nil, err
		}
		ap, err := netip.ParseAddrPort(input.Address)
		if err != nil || ap.String() != input.Address {
			return nil, lanlink.ErrRouteUpdate
		}
		err = c.editLANRoute(lanlink.RouteCandidate{Relay: lanlink.TrustedRelay{Address: ap, CertificateSHA256: input.CertificateSHA256}, Scope: input.Scope}, "")
		return map[string]bool{"saved": err == nil, "restartRequired": err == nil}, routeCommandError(err)
	}
	if name == "lan.routes.remove" {
		var input struct {
			CandidateID string `json:"candidateId"`
		}
		if err := decodePayload(raw, &input); err != nil {
			return nil, err
		}
		if input.CandidateID == "" {
			return nil, lanlink.ErrRouteUpdate
		}
		err := c.editLANRoute(lanlink.RouteCandidate{}, input.CandidateID)
		return map[string]bool{"saved": err == nil, "restartRequired": err == nil}, routeCommandError(err)
	}
	var input struct {
		PeerID       string    `json:"peerId"`
		Update       string    `json:"update,omitempty"`
		Digest       string    `json:"digest,omitempty"`
		CandidateIDs []string  `json:"candidateIds,omitempty"`
		Expires      time.Time `json:"expires,omitempty"`
		TTLSeconds   int64     `json:"ttlSeconds,omitempty"`
		Lifetime     string    `json:"lifetime,omitempty"`
		Withdraw     bool      `json:"withdraw,omitempty"`
	}
	if err := decodePayload(raw, &input); err != nil {
		return nil, err
	}
	if name == "lan.routes.list" && input.PeerID == "" {
		store := c.lanStoreCopy()
		if store == nil {
			return map[string]any{"candidates": []lanlink.RouteCandidate{}}, nil
		}
		candidates, err := ownLANCandidates(store.copy())
		primary := ""
		if len(candidates) > 0 {
			primary = candidates[0].ID()
		}
		return map[string]any{"candidates": routeCandidateViews(candidates), "primaryCandidateId": primary, "editable": c.nodeCopy() == nil}, err
	}
	if !validLANPublicKey(input.PeerID) {
		return nil, errors.New("exact paired public identity required")
	}
	if len(input.Update) > maxLANInvitation {
		return nil, lanlink.ErrRouteUpdate
	}
	node, closeNode, err := c.routeControl()
	if err != nil {
		return nil, routeCommandError(err)
	}
	defer closeNode()
	switch name {
	case "lan.routes.list":
		return publicRouteSnapshot(node, input.PeerID)
	case "lan.routes.export":
		var expires time.Time
		switch input.Lifetime {
		case lanlink.RouteLifetimeFinite:
			duration, err := capacity.Duration(input.TTLSeconds)
			if err != nil || !input.Expires.IsZero() {
				return nil, lanlink.ErrRouteUpdate
			}
			expires = time.Now().UTC().Add(duration)
		case lanlink.RouteLifetimeUntilRevoked:
			if input.TTLSeconds != 0 || !input.Expires.IsZero() {
				return nil, lanlink.ErrRouteUpdate
			}
		default:
			return nil, lanlink.ErrRouteUpdate
		}
		candidates, err := ownLANCandidates(c.lanStoreCopy().copy())
		if err != nil {
			return nil, err
		}
		if input.Withdraw {
			candidates = nil
		}
		frame, err := node.ExportRouteUpdateWithLifetime(input.PeerID, candidates, input.Lifetime, expires)
		if err != nil {
			return nil, routeCommandError(err)
		}
		return map[string]any{"update": string(frame), "peerId": input.PeerID, "expires": publicRouteExpiry(expires), "lifetime": input.Lifetime, "withdrawalPendingDelivery": input.Withdraw}, nil
	case "lan.routes.inspect":
		review, err := node.InspectRouteUpdate(input.PeerID, []byte(input.Update))
		return publicRouteReview(review), routeCommandError(err)
	case "lan.routes.apply":
		if input.TTLSeconds != 0 {
			return nil, lanlink.ErrRouteUpdate
		}
		err = node.ApplyRouteUpdateWithLifetime(input.PeerID, []byte(input.Update), input.Digest, input.CandidateIDs, input.Lifetime, input.Expires)
	case "lan.routes.revoke":
		err = node.RevokeRoutes(input.PeerID, input.CandidateIDs)
		if err != nil && !errors.Is(err, lanlink.ErrRouteUpdate) && !errors.Is(err, lanlink.ErrUntrusted) && !errors.Is(err, lanlink.ErrRoutePermission) {
			err = errors.Join(err, config.ErrAtomicRecovery)
		}
	case "lan.routes.review":
		review, err := node.ReviewRoutes(input.PeerID)
		return publicRouteReview(review), routeCommandError(err)
	case "lan.routes.approve":
		if input.TTLSeconds != 0 {
			return nil, lanlink.ErrRouteUpdate
		}
		err = node.ApproveRoutesWithLifetime(input.PeerID, input.Digest, input.CandidateIDs, input.Lifetime, input.Expires)
	default:
		return nil, errors.New("unknown paired-route action")
	}
	// Keep failed reductions blocked after a disposable offline Node closes.
	if errors.Is(err, config.ErrAtomicRecovery) {
		if store := c.lanStoreCopy(); store != nil {
			store.requireRouteRecovery()
		}
	}
	if err != nil {
		return nil, routeCommandError(err)
	}
	return publicRouteSnapshot(node, input.PeerID)
}

func routeCandidateViews(candidates []lanlink.RouteCandidate) []map[string]any {
	out := make([]map[string]any, 0, len(candidates))
	for _, candidate := range candidates {
		out = append(out, map[string]any{"candidateId": candidate.ID(), "address": candidate.Relay.Address.String(), "certificateSHA256": candidate.Relay.CertificateSHA256, "scope": candidate.Scope})
	}
	return out
}
func publicRouteReview(review lanlink.RouteReview) map[string]any {
	return map[string]any{"digest": review.Digest, "issuer": review.Update.Issuer, "recipient": review.Update.Recipient, "sequence": review.Update.Sequence, "issued": review.Update.Issued, "lifetime": review.Update.EffectiveLifetime(), "expires": publicRouteExpiry(review.Update.Expires), "candidates": routeCandidateViews(review.Update.Candidates)}
}
func publicRouteSnapshot(node *lanlink.Node, peer string) (any, error) {
	s, err := node.RouteSnapshot(peer)
	if err != nil {
		return nil, routeCommandError(err)
	}
	approvals := make([]map[string]any, 0, len(s.Approvals))
	for _, a := range s.Approvals {
		if a.Lifetime == "" && !a.Expires.IsZero() {
			a.Lifetime = lanlink.RouteLifetimeFinite
		}
		approvals = append(approvals, map[string]any{"candidateId": a.CandidateID, "lifetime": a.Lifetime, "expires": publicRouteExpiry(a.Expires)})
	}
	permitted := make([]string, 0, len(s.Permitted))
	for _, candidate := range s.Permitted {
		permitted = append(permitted, candidate.ID())
	}
	observation, _ := node.RouteObservation(peer)
	return map[string]any{"observation": publicRouteObservation(observation), "legacy": s.Legacy, "issuedSequence": s.IssuedSequence, "receivedSequence": s.ReceivedSequence, "candidates": routeCandidateViews(s.Candidates), "approvals": approvals, "permittedIds": permitted, "lifetime": s.Lifetime, "expires": publicRouteExpiry(s.Expires), "nextExpiry": publicRouteExpiry(s.NextExpiry), "recoveryRequired": s.RecoveryRequired}, nil
}

func routeCommandError(err error) error {
	if errors.Is(err, config.ErrAtomicCommitted) || errors.Is(err, config.ErrAtomicRecovery) {
		return privateAtomicError(&lanCommandError{"lan_routes_recovery", "route changes are paused because private state durability is unconfirmed; stop soba; a failed save may leave old permissions on disk, so inspect and reconcile the saved state before restarting"}, err)
	}
	if errors.Is(err, lanlink.ErrRouteUpdate) {
		return &lanCommandError{"lan_routes_invalid", "route update is invalid, expired, stale or belongs to a different pairing; obtain and review a fresh update"}
	}
	if errors.Is(err, lanlink.ErrRoutePermission) {
		return &lanCommandError{"lan_routes_unavailable", "no current locally approved route; review the paired device's latest route update"}
	}
	return err
}

func publicRouteObservation(o lanlink.RouteObservation) map[string]any {
	out := map[string]any{"state": o.State, "path": o.Path}
	if o.CandidateID != "" {
		out["candidateId"] = o.CandidateID
	}
	if o.Scope != "" {
		out["scope"] = o.Scope
	}
	if !o.ObservedAt.IsZero() {
		out["observedAt"] = o.ObservedAt
	}
	if !o.Expires.IsZero() {
		out["expires"] = o.Expires
	}
	return out
}

func publicRouteExpiry(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value
}
