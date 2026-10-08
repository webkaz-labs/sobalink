package core

import (
	"reflect"
	"strconv"
	"time"

	"github.com/webkaz-labs/sobalink/internal/directlan"
	"github.com/webkaz-labs/sobalink/internal/endpointmeta"
)

// prepareLegacyPeerAdditionLocked accepts the transport's complete ACTIVE peer
// snapshot, not a replacement for the retained store. The only admitted delta
// is one entirely new legacy identity. Terminal evidence and every preexisting
// record (including managed confirmation and replay history) remain unchanged.
// Caller holds Core.op and store.mu; this performs no publication or Node calls.
func (s *directLANStore) prepareLegacyPeerAdditionLocked(peers []directlan.Peer, now time.Time) (directLANState, error) {
	projection, err := s.managedCurrentEndpointProjectionLocked(now)
	if err != nil {
		return directLANState{}, err
	}
	if !directLANActiveMetadataSchema(s.state) || len(peers) != len(projection.Peers)+1 {
		return directLANState{}, endpointmeta.ErrReview
	}
	active := make(map[string]directlan.Peer, len(projection.Peers))
	for _, peer := range projection.Peers {
		active[peer.Key] = peer
	}
	retained := make(map[string]bool, len(s.state.Peers))
	for _, peer := range s.state.Peers {
		retained[peer.Key] = true
	}
	seen := make(map[string]bool, len(peers))
	var addition directlan.Peer
	added := false
	for _, peer := range peers {
		if seen[peer.Key] {
			return directLANState{}, directlan.ErrUntrusted
		}
		seen[peer.Key] = true
		if old, exists := active[peer.Key]; exists {
			if old != peer {
				return directLANState{}, directlan.ErrUntrusted
			}
			continue
		}
		if added || retained[peer.Key] {
			return directLANState{}, directlan.ErrUntrusted
		}
		addition, added = peer, true
	}
	if !added {
		return directLANState{}, directlan.ErrUntrusted
	}
	for key := range active {
		if !seen[key] {
			return directLANState{}, directlan.ErrUntrusted
		}
	}
	next := cloneDirectLANState(s.state)
	revision, err := strconv.ParseUint(next.Metadata.Revision, 10, 64)
	if err != nil || revision == ^uint64(0) {
		return directLANState{}, directlan.ErrCapacity
	}
	next.Metadata.Revision = strconv.FormatUint(revision+1, 10)
	next.Metadata.ObservedAt = now.UTC().Format(time.RFC3339Nano)
	next.Peers = append(next.Peers, addition)
	next.Metadata.Peers = append(next.Metadata.Peers, endpointmeta.PeerRecord{Peer: directLANPeerWire(addition), Revision: next.Metadata.Revision})
	if err := validateDirectLANState(next); err != nil {
		return directLANState{}, err
	}
	after, err := projectManagedCurrentEndpoint(next, now)
	if err != nil || !reflect.DeepEqual(after.PairContexts, projection.PairContexts) || !reflect.DeepEqual(after.DeniedPeerKeys, projection.DeniedPeerKeys) || !reflect.DeepEqual(after.InactiveEndpointPeerKeys(), projection.InactiveEndpointPeerKeys()) {
		return directLANState{}, endpointmeta.ErrReview
	}
	return next, nil
}

// A receipt can only follow the existing sole whole-file publisher. Neither a
// caller snapshot nor successful structural validation constitutes durability.
func (s *directLANStore) saveLegacyPeerAdditionLocked(process string, peers []directlan.Peer, now time.Time, live *contextSaveLiveness) error {
	if live == nil || live.ctx == nil || live.ordinary == nil || live.ordinary.store != s || live.ordinary.process != process || !s.contextPublicationCurrentLocked(process) {
		return endpointmeta.ErrReview
	}
	if err := live.err(); err != nil {
		return err
	}
	next, err := s.prepareLegacyPeerAdditionLocked(peers, now)
	if err != nil {
		return err
	}
	return s.writeContextPublicationLocked(process, next, live)
}

// Node invokes Persist under Node.mu. Never wait for Core.op, revisit Node, or
// acquire backend.mu here: normal stop/removal takes Core.op before Node.mu.
// A rejected callback leaves this owner stopped; pairing itself fails closed.
func (o *managedCompletionOwner) persistLegacyAddition(peers []directlan.Peer) (err error) {
	if o == nil || o.core == nil || o.store == nil {
		return directlan.ErrUnavailable
	}
	defer func() {
		if err != nil {
			o.invalidate()
		}
	}()
	if !o.core.op.TryLock() {
		return directlan.ErrUnavailable
	}
	defer o.core.op.Unlock()
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.authorityCurrent() || !o.coreCurrent("") {
		return directlan.ErrUnavailable
	}
	for _, peer := range peers {
		if !o.coreCurrent(peer.Key) {
			return directlan.ErrUntrusted
		}
	}
	s := o.store
	s.mu.Lock()
	defer s.mu.Unlock()
	if !o.epoch.Valid() || s.contextEpoch != o.epoch || o.authority.Load() != o.epoch ||
		s.contextPublication != o.receipt || s.reviewRevision != o.revision || s.limits.Load() != o.limitsSource ||
		*s.currentCapacity() != o.limits || contextConfigurationDigest(s.state) != o.configuration {
		return endpointmeta.ErrReview
	}
	live := &contextSaveLiveness{ctx: o.core.ctx, core: o.core.ctx, ordinary: o}
	if err := s.saveLegacyPeerAdditionLocked(o.process, peers, time.Now(), live); err != nil {
		return err
	}
	// Recheck full projection after durable publication, before releasing the
	// transport's commit lock. No added peer is installed if this step fails.
	if _, err := o.projectionLocked(time.Now()); err != nil {
		return err
	}
	if err := live.err(); err != nil {
		return err
	}
	if !o.coreCurrent("") || !s.contextPublicationCurrentLocked(o.process) ||
		s.limits.Load() != o.limitsSource || *s.currentCapacity() != o.limits {
		return endpointmeta.ErrReview
	}
	o.receipt, o.revision = s.contextPublication, s.reviewRevision
	o.configuration = contextConfigurationDigest(s.state)
	o.epoch = directlan.NewContextEpoch()
	s.contextEpoch = o.epoch
	o.authority.Store(o.epoch)
	// Stop can signal without Core.op. Reobserve after publication of the fresh
	// signal so concurrent invalidation cannot leave a new live authority.
	if err := live.err(); err != nil {
		o.epoch.Invalidate()
		return err
	}
	return nil
}
