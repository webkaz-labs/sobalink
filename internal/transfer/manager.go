package transfer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

type peerState struct {
	peer    Peer
	paused  bool
	revoked bool
	active  int
}

type batchState struct {
	value    Batch
	manifest Manifest
	root     *os.Root
	stage    string
	active   int
	reserved bool
	cancel   context.CancelFunc
	ctx      context.Context
}

// Manager has no listeners or transport credentials. Calls may run concurrently.
// Keep one Manager per local trust scope, and close it when that scope ends.
type Manager struct {
	mu       sync.Mutex
	limits   Limits
	store    PolicyStore
	peers    map[string]*peerState
	policies map[string]ReceivePolicy
	batches  map[string]*batchState
	reserved int64
	metadata int64
	active   int
	closed   bool
}

func NewManager(options Options) (*Manager, error) {
	limits, err := limitsOrDefault(options.Limits)
	if err != nil {
		return nil, err
	}
	m := &Manager{limits: limits, store: options.PolicyStore, peers: map[string]*peerState{}, policies: map[string]ReceivePolicy{}, batches: map[string]*batchState{}}
	if m.store != nil {
		policies, err := m.store.LoadPolicies()
		if err != nil {
			return nil, fmt.Errorf("load receive policies: %w", err)
		}
		if len(policies) > limits.MaxPeers {
			return nil, ErrLimit
		}
		for _, p := range policies {
			if !validPeer(p.Peer) || !validDestination(p.Destination) {
				return nil, fmt.Errorf("%w: invalid stored receive policy", ErrUnsafePath)
			}
			if _, exists := m.policies[p.Peer.ID]; exists {
				return nil, fmt.Errorf("%w: duplicate stored receive policy", ErrConflict)
			}
			m.policies[p.Peer.ID] = p
		}
	}
	return m, nil
}

// BindPeer is a trust-boundary operation for the authenticated caller. Trust
// renewal must supply a greater generation; revoked generations stay revoked.
func (m *Manager) BindPeer(peer Peer) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.validatePeerBindingLocked(peer); err != nil {
		return err
	}
	previous, found := m.peers[peer.ID]
	if found && peer == previous.peer {
		return nil
	}
	if found {
		m.cancelPeerLocked(peer.ID)
	}
	active := 0
	if previous != nil {
		active = previous.active
	}
	m.peers[peer.ID] = &peerState{peer: peer, active: active}
	if p, ok := m.policies[peer.ID]; ok && p.Peer != peer {
		delete(m.policies, peer.ID)
		return m.savePoliciesLocked(m.policies)
	}
	return nil
}

// ValidatePeerBinding checks the same admission preconditions as BindPeer
// without publishing receiver state. A caller doing durable approval first must
// serialize its binding mutations and Close across validation and publication.
func (m *Manager) ValidatePeerBinding(peer Peer) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.validatePeerBindingLocked(peer)
}

// ResetBindings changes the local trust scope before a transport starts. It
// preserves the PolicyStore unchanged and refuses to discard any batch state.
// The full replacement is validated before either bindings or policies change.
func (m *Manager) ResetBindings(peers []Peer, policies []ReceivePolicy) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ErrClosed
	}
	if m.active != 0 || len(m.batches) != 0 {
		return ErrState
	}
	if len(peers) > m.limits.MaxPeers || len(policies) > m.limits.MaxPeers {
		return ErrLimit
	}
	nextPeers := make(map[string]*peerState, len(peers))
	for _, peer := range peers {
		if !validPeer(peer) {
			return ErrUnknownPeer
		}
		if nextPeers[peer.ID] != nil {
			return ErrConflict
		}
		nextPeers[peer.ID] = &peerState{peer: peer}
	}
	nextPolicies := make(map[string]ReceivePolicy, len(policies))
	for _, policy := range policies {
		peer := nextPeers[policy.Peer.ID]
		if !validPeer(policy.Peer) || !validDestination(policy.Destination) || peer == nil || peer.peer != policy.Peer {
			return ErrPeerChanged
		}
		if _, exists := nextPolicies[policy.Peer.ID]; exists {
			return ErrConflict
		}
		nextPolicies[policy.Peer.ID] = policy
	}
	m.peers, m.policies = nextPeers, nextPolicies
	return nil
}

func (m *Manager) validatePeerBindingLocked(peer Peer) error {
	if m.closed {
		return ErrClosed
	}
	if !validPeer(peer) {
		return ErrUnknownPeer
	}
	previous, found := m.peers[peer.ID]
	if found {
		if peer.Generation < previous.peer.Generation || peer.Generation == previous.peer.Generation && previous.revoked {
			return ErrPeerChanged
		}
		if peer == previous.peer {
			return nil
		}
	} else if len(m.peers) >= m.limits.MaxPeers {
		return ErrLimit
	}
	if p, ok := m.policies[peer.ID]; ok && peer.Generation < p.Peer.Generation {
		return ErrPeerChanged
	}
	return nil
}

func (m *Manager) checkPeerLocked(peer Peer) (*peerState, error) {
	p, err := m.checkPeerIdentityLocked(peer)
	if err != nil {
		return nil, err
	}
	if p.paused {
		return nil, ErrPeerPaused
	}
	return p, nil
}

func (m *Manager) checkPeerIdentityLocked(peer Peer) (*peerState, error) {
	if m.closed {
		return nil, ErrClosed
	}
	p, ok := m.peers[peer.ID]
	if !ok {
		return nil, ErrUnknownPeer
	}
	if p.revoked || p.peer != peer {
		return nil, ErrPeerChanged
	}
	return p, nil
}

// Offer reserves bounded metadata and storage budget, but writes no file until
// acceptance. Reusing an ID with exactly the same peer and manifest is idempotent;
// reusing it with other content or identity fails closed.
func (m *Manager) Offer(peer Peer, manifest Manifest) (Batch, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.checkPeerLocked(peer); err != nil {
		return Batch{}, err
	}
	// Serialize validation as well as reservation: concurrent offers cannot
	// multiply temporary normalization/tree-validation allocations unboundedly.
	total, err := validateManifest(manifest, m.limits)
	if err != nil {
		return Batch{}, err
	}
	if existing, ok := m.batches[manifest.ID]; ok {
		if existing.value.Peer != peer || !sameManifest(existing.manifest, manifest) {
			return Batch{}, ErrConflict
		}
		return snapshot(existing), nil
	}
	metadata := metadataSize(manifest)
	if len(m.batches) >= m.limits.MaxBatches || total > m.limits.MaxReservedBytes-m.reserved || metadata > m.limits.MaxMetadataBytes-m.metadata {
		return Batch{}, ErrLimit
	}
	pending, perPeer := 0, 0
	for _, b := range m.batches {
		if b.value.State == Pending {
			pending++
		}
		if b.reserved && b.value.Peer.ID == peer.ID {
			perPeer++
		}
	}
	if pending >= m.limits.MaxPendingBatches || perPeer >= m.limits.MaxPendingPerPeer {
		return Batch{}, ErrLimit
	}
	manifest.Entries = append([]Entry(nil), manifest.Entries...)
	ctx, cancel := context.WithCancel(context.Background())
	b := &batchState{manifest: manifest, reserved: true, ctx: ctx, cancel: cancel,
		value: Batch{ID: manifest.ID, Peer: peer, State: Pending, TotalBytes: total, CreatedAt: time.Now().UTC()}}
	for _, e := range manifest.Entries {
		b.value.Files = append(b.value.Files, FileStatus{Entry: e, State: FilePending})
	}
	m.batches[manifest.ID] = b
	m.reserved += total
	m.metadata += metadata
	if p, ok := m.policies[peer.ID]; ok && p.Peer == peer && p.AutoAccept {
		if err := m.acceptLocked(b, p.Destination); err != nil {
			return snapshot(b), err
		}
	}
	return snapshot(b), nil
}

func sameManifest(a, b Manifest) bool {
	if a.ID != b.ID || len(a.Entries) != len(b.Entries) {
		return false
	}
	for i := range a.Entries {
		if a.Entries[i] != b.Entries[i] {
			return false
		}
	}
	return true
}

func snapshot(b *batchState) Batch {
	value := b.value
	value.Files = append([]FileStatus(nil), value.Files...)
	return value
}

func (m *Manager) Get(id string) (Batch, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.batches[id]
	if !ok {
		return Batch{}, ErrNotFound
	}
	return snapshot(b), nil
}

func (m *Manager) List() []Batch {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Batch, 0, len(m.batches))
	for _, b := range m.batches {
		out = append(out, snapshot(b))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out
}

func validDestination(destination string) bool {
	return destination != "" && len(destination) <= 4096 && filepath.IsAbs(destination) && filepath.Clean(destination) == destination
}

func (m *Manager) Accept(id, destination string) (Batch, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.batches[id]
	if !ok {
		return Batch{}, ErrNotFound
	}
	if _, err := m.checkPeerLocked(b.value.Peer); err != nil {
		return snapshot(b), err
	}
	if b.value.State != Pending {
		if b.value.Destination != "" && filepath.Dir(b.value.Destination) == destination && b.value.State != Cancelled && b.value.State != Rejected {
			return snapshot(b), nil
		}
		return snapshot(b), ErrState
	}
	err := m.acceptLocked(b, destination)
	return snapshot(b), err
}

func (m *Manager) acceptLocked(b *batchState, destination string) error {
	root, actual, stage, err := prepareDestination(destination, b.manifest.Entries)
	if err != nil {
		return err
	}
	b.root, b.stage, b.value.Destination = root, stage, actual
	b.value.State = Accepted
	for i := range b.value.Files {
		if b.value.Files[i].Kind == Directory {
			b.value.Files[i].State = FileSaved
			b.value.Files[i].StoredName = b.value.Files[i].Path
		}
	}
	m.updateStateLocked(b)
	return nil
}

func (m *Manager) Reject(id string) (Batch, error) { return m.stop(id, Rejected) }
func (m *Manager) Cancel(id string) (Batch, error) { return m.stop(id, Cancelled) }

func (m *Manager) stop(id string, state BatchState) (Batch, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.batches[id]
	if !ok {
		return Batch{}, ErrNotFound
	}
	if b.value.State == state {
		return snapshot(b), nil
	}
	if terminal(b.value.State) || state == Rejected && b.value.State != Pending {
		return snapshot(b), ErrState
	}
	m.stopLocked(b, state)
	return snapshot(b), nil
}

func terminal(state BatchState) bool {
	return state == Completed || state == Cancelled || state == Rejected
}

func (m *Manager) stopLocked(b *batchState, state BatchState) {
	b.value.State = state
	b.cancel()
	for i := range b.value.Files {
		if b.value.Files[i].State != FileSaved {
			b.value.Files[i].State = FileCancelled
			b.value.Files[i].Error = ErrorCode(ErrCancelled)
		}
	}
	m.releaseLocked(b)
	m.closeRootLocked(b)
}

func (m *Manager) releaseLocked(b *batchState) {
	// A cancelled stream may still be blocked in a caller-owned Reader. Keep its
	// storage reservation until temporary files have actually been removed.
	if b.reserved && b.active == 0 {
		m.reserved -= b.value.TotalBytes
		b.reserved = false
	}
}

func (m *Manager) closeRootLocked(b *batchState) {
	if b.root != nil && b.active == 0 && terminal(b.value.State) {
		_ = b.root.Remove(b.stage) // Only an empty staging directory is removed.
		_ = b.root.Close()
		b.root = nil
	}
}

// PausePeer prevents new offers and streams. Existing streams are cancelled;
// failed files can be explicitly retried after unpausing. Approval is retained.
func (m *Manager) PausePeer(id string, paused bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ErrClosed
	}
	p, ok := m.peers[id]
	if !ok {
		return ErrUnknownPeer
	}
	if p.revoked {
		return ErrPeerChanged
	}
	m.pausePeerLocked(id, p, paused)
	return nil
}

func (m *Manager) pausePeerLocked(id string, p *peerState, paused bool) {
	p.paused = paused
	if paused {
		for _, b := range m.batches {
			if b.value.Peer.ID == id && !terminal(b.value.State) {
				b.cancel()
			}
		}
	} else {
		for _, b := range m.batches {
			if b.value.Peer.ID == id && !terminal(b.value.State) && b.active == 0 {
				b.ctx, b.cancel = context.WithCancel(context.Background())
			}
		}
	}
}

func (m *Manager) RevokePeer(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ErrClosed
	}
	p, ok := m.peers[id]
	if !ok {
		return ErrUnknownPeer
	}
	p.revoked = true
	m.cancelPeerLocked(id)
	delete(m.policies, id)
	return m.savePoliciesLocked(m.policies)
}

func (m *Manager) cancelPeerLocked(id string) {
	for _, b := range m.batches {
		if b.value.Peer.ID == id && !terminal(b.value.State) {
			m.stopLocked(b, Cancelled)
		}
	}
}

// RetryFile restarts a failed item from byte zero. Successful items retain their
// acknowledgement, and another batch acceptance is not required.
func (m *Manager) RetryFile(id, fileID string) (Batch, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.batches[id]
	if !ok {
		return Batch{}, ErrNotFound
	}
	if _, err := m.checkPeerLocked(b.value.Peer); err != nil {
		return snapshot(b), err
	}
	index := fileIndex(b, fileID)
	if index < 0 {
		return snapshot(b), ErrNotFound
	}
	f := &b.value.Files[index]
	if terminal(b.value.State) || b.value.State == Pending || f.State != FileFailed {
		return snapshot(b), ErrState
	}
	if b.ctx.Err() != nil {
		if b.active != 0 {
			return snapshot(b), ErrBusy
		}
		b.ctx, b.cancel = context.WithCancel(context.Background())
	}
	b.value.CompletedBytes -= f.CompletedBytes
	f.CompletedBytes, f.Error, f.State = 0, "", FilePending
	m.updateStateLocked(b)
	return snapshot(b), nil
}

func fileIndex(b *batchState, id string) int {
	for i := range b.value.Files {
		if b.value.Files[i].ID == id {
			return i
		}
	}
	return -1
}

func (m *Manager) updateStateLocked(b *batchState) {
	if terminal(b.value.State) || b.value.State == Pending {
		return
	}
	saved, failed, receiving := 0, false, false
	for _, f := range b.value.Files {
		saved += boolInt(f.State == FileSaved)
		failed = failed || f.State == FileFailed
		receiving = receiving || f.State == FileReceiving
	}
	switch {
	case saved == len(b.value.Files):
		b.value.State = Completed
		m.releaseLocked(b)
		m.closeRootLocked(b)
	case receiving:
		b.value.State = Receiving
	case failed:
		b.value.State = Partial
	default:
		b.value.State = Accepted
	}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// SetReceivePolicy must be called only for an explicit user-approved designated
// directory and the exact verified peer generation. It does not approve already
// pending batches. A policy is effective only after its durable save succeeds.
func (m *Manager) SetReceivePolicy(policy ReceivePolicy) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	next, err := m.prepareReceivePolicyLocked(policy)
	if err != nil {
		return err
	}
	if err := m.savePoliciesLocked(next); err != nil {
		return err
	}
	m.policies = next
	return nil
}

func (m *Manager) prepareReceivePolicyLocked(policy ReceivePolicy) (map[string]ReceivePolicy, error) {
	// A paused peer may have its local policy edited without unpausing inbound
	// transfers or changing the trust generation.
	if _, err := m.checkPeerIdentityLocked(policy.Peer); err != nil {
		return nil, err
	}
	if !validDestination(policy.Destination) {
		return nil, ErrUnsafePath
	}
	root, err := openDestination(policy.Destination)
	if err != nil {
		return nil, err
	}
	_ = root.Close()
	copy := make(map[string]ReceivePolicy, len(m.policies)+1)
	for id, p := range m.policies {
		copy[id] = p
	}
	copy[policy.Peer.ID] = policy
	if len(copy) > m.limits.MaxPeers {
		return nil, ErrLimit
	}
	return copy, nil
}

// UpdateReceiveSettings atomically publishes a reviewed policy and pause state
// after one persistence callback succeeds. A nil policy preserves the current
// runtime policy without reopening its directory. AutoAccept=false removes it;
// removal remains fail-closed in memory if persistence fails. The callback runs
// under m.mu and must persist the combined settings without reentering Manager.
func (m *Manager) UpdateReceiveSettings(peer Peer, policy *ReceivePolicy, paused bool, persist func([]ReceivePolicy) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, err := m.checkPeerIdentityLocked(peer)
	if err != nil {
		return err
	}
	if persist == nil {
		return ErrState
	}
	next := make(map[string]ReceivePolicy, len(m.policies)+1)
	for id, value := range m.policies {
		next[id] = value
	}
	if policy != nil {
		if policy.Peer != peer {
			return ErrPeerChanged
		}
		if policy.AutoAccept {
			next, err = m.prepareReceivePolicyLocked(*policy)
			if err != nil {
				return err
			}
		} else {
			delete(next, peer.ID)
		}
	}
	if err := persist(policyList(next)); err != nil {
		if policy != nil && !policy.AutoAccept {
			delete(m.policies, peer.ID)
		}
		return err
	}
	m.policies = next
	m.pausePeerLocked(peer.ID, p, paused)
	return nil
}

func (m *Manager) RemoveReceivePolicy(peerID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ErrClosed
	}
	// Removal fails closed in memory even when persistent storage is unavailable.
	delete(m.policies, peerID)
	return m.savePoliciesLocked(m.policies)
}

func (m *Manager) ReceivePolicies() []ReceivePolicy {
	m.mu.Lock()
	defer m.mu.Unlock()
	return policyList(m.policies)
}

func policyList(policies map[string]ReceivePolicy) []ReceivePolicy {
	list := make([]ReceivePolicy, 0, len(policies))
	for _, policy := range policies {
		list = append(list, policy)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Peer.ID < list[j].Peer.ID })
	return list
}

func (m *Manager) savePoliciesLocked(policies map[string]ReceivePolicy) error {
	if m.store == nil {
		return nil
	}
	if err := m.store.SavePolicies(policyList(policies)); err != nil {
		return fmt.Errorf("save receive policies: %w", err)
	}
	return nil
}

// Forget releases completed/cancelled/rejected history and its idempotency key.
// Call only after the sender no longer needs ACK retries. Until then the bounded
// history deliberately rejects new offers rather than silently evicting keys.
func (m *Manager) Forget(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.batches[id]
	if !ok {
		return ErrNotFound
	}
	if !terminal(b.value.State) || b.active != 0 {
		return ErrState
	}
	m.metadata -= metadataSize(b.manifest)
	delete(m.batches, id)
	return nil
}

func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil
	}
	m.closed = true
	for _, b := range m.batches {
		if !terminal(b.value.State) {
			m.stopLocked(b, Cancelled)
		}
	}
	return nil
}

func transferError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return fmt.Errorf("%w: %w", ErrCancelled, ctx.Err())
	}
	if errors.Is(err, io.EOF) {
		return ErrIntegrity
	}
	return err
}
