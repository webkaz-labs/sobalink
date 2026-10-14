package transfer

import (
	"context"
	"errors"
	"math"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func (m *Manager) DurableAvailable() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.durable == nil || m.closed {
		return false
	}
	err := m.durable.verifyAdmission()
	return err == nil || errors.Is(err, ErrBusy)
}

// EnableDurableReceiver is local initialization under the caller's separately
// persisted initializing/ready profile guard. A remote offer must never call this method.
func (m *Manager) EnableDurableReceiver(options DurableOptions) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ErrClosed
	}
	if m.durable != nil {
		if m.durable.path != options.Path || m.durable.state.Epoch != options.Epoch {
			return ErrDurableRecovery
		}
		if options.Initialize && len(m.durable.state.Records) != 0 {
			return ErrDurableRecovery
		}
		return m.durable.verify()
	}
	m.durable = openDurable(options)
	if err := m.durable.verify(); err != nil {
		return err
	}
	_ = m.inventoryDurableLocked()
	return m.durable.verify()
}

func (m *Manager) OfferDurable(peer Peer, recipientID string, offer DurableOffer) (DurableStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.checkPeerLocked(peer); err != nil {
		return DurableStatus{}, err
	}
	d := m.durable
	if err := d.verify(); err != nil {
		return DurableStatus{}, err
	}
	if err := m.checkDurableRelationshipLocked(peer, m.durableEpochs[peer.ID].epoch); err != nil {
		return DurableStatus{}, err
	}
	if offer.RecipientID != recipientID || recipientID == "" {
		return DurableStatus{}, ErrDurablePermission
	}
	digest, err := DurableDigest(offer)
	if err != nil || digest != offer.Digest {
		return DurableStatus{}, ErrDurableConflict
	}
	now := d.now().UTC()
	if offer.SentAt.IsZero() || absDuration(now.Sub(offer.SentAt)) > DurableClockSkew {
		return DurableStatus{}, ErrDurableClock
	}
	if r := d.record(offer.DeliveryID); r != nil {
		if m.checkDurableRecordRelationshipLocked(r) != nil || r.Peer != peer || r.Offer.Digest != digest {
			return DurableStatus{}, ErrDurableConflict
		}
		if now.After(r.Lease.ReceiptUntil) {
			return DurableStatus{}, ErrDurableUnknownLease
		}
		if err := m.reconcileDurableLocked(r); err != nil {
			return DurableStatus{}, err
		}
		return d.status(r), nil
	}
	if err := d.verifyAdmission(); err != nil {
		return DurableStatus{}, err
	}
	if m.recoveryCode != "" {
		return DurableStatus{}, ErrReceiveRecovery
	}
	if m.batches[offer.DeliveryID] != nil {
		return DurableStatus{}, ErrDurableConflict
	}
	if offer.CreatedAt.IsZero() || offer.CreatedAt.After(now.Add(DurableClockSkew)) || offer.ExpiresAt.Sub(offer.CreatedAt) < time.Minute || offer.ExpiresAt.Sub(offer.CreatedAt) > DurableMaxLifetime || !now.Before(offer.ExpiresAt) {
		return DurableStatus{}, ErrDurableExpired
	}
	if err = ValidateManifest(offer.Manifest, m.limits); err != nil {
		return DurableStatus{}, err
	}
	// This reserved namespace is receiver-owned and cannot be offered as output.
	for _, entry := range offer.Manifest.Entries {
		for _, part := range strings.Split(entry.Path, "/") {
			if canonical(part) == canonical(durableStage) {
				return DurableStatus{}, ErrInvalidManifest
			}
		}
	}
	reserve := durableMetadataReservation(offer)
	var metadata, activeMetadata, bytes int64
	var perPeer, pending, peerPending int
	for _, r := range d.state.Records {
		metadata += r.ReservedMetadata
		bytes += durableBytes(r)
		if r.Peer.ID == peer.ID {
			perPeer++
		}
		if d.recordActive(r) {
			activeMetadata += r.ReservedMetadata
			pending++
			if r.Peer.ID == peer.ID {
				peerPending++
			}
		}
	}
	for _, b := range m.batches {
		if !terminal(b.value.State) {
			pending++
			if b.value.Peer.ID == peer.ID {
				peerPending++
			}
		}
	}
	var total int64
	for _, e := range offer.Manifest.Entries {
		total += e.Size
	}
	if len(d.state.Records) >= durableLedgerRecords || perPeer >= 128 || reserve > durableLedgerBytes-metadata {
		return DurableStatus{}, ErrDurableEvidenceCapacity
	}
	if m.durableCountLocked()+len(m.batches) >= m.limits.MaxBatches || reserve > m.limits.MaxMetadataBytes-m.metadata-activeMetadata || pending >= m.limits.MaxPendingBatches || peerPending >= m.limits.MaxPendingPerPeer || total > durableCapacityLeft(m.limits.MaxReservedBytes, m.reserved, bytes) {
		return DurableStatus{}, ErrDurableCapacity
	}
	token, err := randomName("")
	if err != nil {
		return DurableStatus{}, err
	}
	offer.Manifest.Entries = append([]Entry(nil), offer.Manifest.Entries...)
	offer.SentAt = time.Time{}
	r := &durableRecord{Offer: offer, Peer: peer, RevocationEpoch: m.durableEpochs[peer.ID].epoch, Network: m.durableEpochs[peer.ID].network, State: Pending, ReservedMetadata: reserve, Files: []durableFile{}, Directories: []durableDirectory{}, Lease: DurableLease{Version: DurableVersion, DeliveryID: offer.DeliveryID, Digest: digest, ReceiverEpoch: d.state.Epoch, Token: token, RecipientID: recipientID, ExpiresAt: offer.ExpiresAt, ReceiptUntil: offer.ExpiresAt.Add(DurableReceiptGrace)}}
	for _, e := range offer.Manifest.Entries {
		r.Files = append(r.Files, durableFile{Entry: e, Phase: "pending"})
	}
	d.state.Records = append(d.state.Records, r)
	if err = d.save(); err != nil {
		return DurableStatus{}, err
	}
	if p, ok := m.policies[peer.ID]; ok && p.Peer == peer && p.AutoAccept {
		if err = m.acceptDurableLocked(r, p.Destination); err != nil {
			return DurableStatus{}, err
		}
	}
	return d.status(r), nil
}
func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}
func durableBytes(r *durableRecord) int64 {
	var n int64
	for _, f := range r.Files {
		if !terminal(r.State) || r.Preparing || len(r.active) > 0 || f.Phase == "publish_pending" || f.Anchor != "" {
			n += f.Entry.Size
		}
	}
	return n
}
func durableTotal(r *durableRecord) int64 {
	var n int64
	for _, f := range r.Files {
		n += f.Entry.Size
	}
	return n
}
func durableMetadataReservation(o DurableOffer) int64 {
	// Include worst-case escaped destination/root/identity, per-entry anchors and
	// all intermediate directory identities before accepting a reservation.
	n := int64(64<<10) + 6*ManifestMetadataBytes(o.Manifest)
	for _, e := range o.Manifest.Entries {
		n += 4096 + int64(strings.Count(e.Path, "/")+1)*(6*int64(len(e.Path))+1024)
	}
	return n
}
func (d *durableReceiver) status(r *durableRecord) DurableStatus {
	out := DurableStatus{Lease: r.Lease, State: r.State, ReceiverNow: d.now().UTC(), ReasonCode: r.Reason}
	receipt := &DurableReceipt{Lease: r.Lease, State: r.State, Files: []FileAck{}}
	for _, f := range r.Files {
		if f.Phase == "saved" {
			receipt.Files = append(receipt.Files, FileAck{BatchID: r.Offer.DeliveryID, FileID: f.Entry.ID, StoredName: f.Entry.Path, Size: f.Entry.Size, SHA256: f.Entry.SHA256})
			if f.SavedAt.After(receipt.SavedAt) {
				receipt.SavedAt = f.SavedAt
			}
		}
	}
	if len(receipt.Files) > 0 {
		out.Receipt = receipt
	}
	return out
}
func durableBatch(r *durableRecord) Batch {
	b := Batch{ID: r.Offer.DeliveryID, Peer: r.Peer, State: r.State, CreatedAt: r.Offer.CreatedAt, TotalBytes: durableTotal(r)}
	if r.Destination != "" {
		b.Destination = filepath.Join(r.Destination, r.Root)
	}
	for _, f := range r.Files {
		v := FileStatus{Entry: f.Entry, State: FilePending}
		if f.Phase == "saved" {
			v.State = FileSaved
			v.StoredName = f.Entry.Path
			v.CompletedBytes = f.Entry.Size
			b.CompletedBytes += f.Entry.Size
		} else if r.active[f.Entry.ID] != nil {
			v.State = FileReceiving
		} else if r.Reason != "" {
			v.Error = r.Reason
		}
		b.Files = append(b.Files, v)
	}
	return b
}
func (m *Manager) StatusDurable(peer Peer, lease DurableLease) (DurableStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.durableLeaseLocked(peer, lease, false)
	if err != nil {
		return DurableStatus{}, err
	}
	if err = m.reconcileDurableLocked(r); err != nil {
		return DurableStatus{}, err
	}
	return m.durable.status(r), nil
}
func (m *Manager) durableLeaseLocked(peer Peer, lease DurableLease, write bool) (*durableRecord, error) {
	if _, err := m.checkPeerLocked(peer); err != nil {
		return nil, err
	}
	d := m.durable
	if err := d.verify(); err != nil {
		return nil, err
	}
	r := d.record(lease.DeliveryID)
	if r == nil || r.Peer != peer || !sameDurableLease(r.Lease, lease) || m.checkDurableRecordRelationshipLocked(r) != nil {
		return nil, ErrDurableUnknownLease
	}
	now := d.now()
	if now.After(r.Lease.ReceiptUntil) {
		return nil, ErrDurableUnknownLease
	}
	if write && !now.Before(r.Lease.ExpiresAt) {
		return nil, ErrDurableExpired
	}
	return r, nil
}

func (m *Manager) acceptDurableLocked(r *durableRecord, destination string) error {
	d := m.durable
	if err := d.verifyAdmission(); err != nil {
		return err
	}
	if _, err := m.checkPeerLocked(r.Peer); err != nil {
		return err
	}
	if err := m.checkDurableRecordRelationshipLocked(r); err != nil {
		return err
	}
	if r.State != Pending {
		if r.Destination == destination && r.State != Rejected && r.State != Cancelled {
			return nil
		}
		return ErrState
	}
	if !d.now().Before(r.Lease.ExpiresAt) {
		return ErrDurableExpired
	}
	if r.Preparing {
		return ErrDurableRecovery
	}
	parent, err := openDestination(destination)
	if err != nil {
		return err
	}
	defer parent.Close()
	if err = checkRootSpace(context.Background(), parent, m.diskSpace, m.limits.DiskReserveBytes); err != nil {
		return err
	}
	if err = retirementSyncDirectory(parent); err != nil {
		return errors.Join(ErrDurableStorage, err)
	}
	identity, err := durableIdentity(parent, ".", true)
	if err != nil {
		return err
	}

	r.Destination, r.DestinationIdentity, r.State = destination, identity, Accepted
	return d.save()
}

// StartDurable requires possession of the already persisted lease. Accepting an
// offer records consent only; no root, directory or file is created beforehand.
func (m *Manager) StartDurable(ctx context.Context, peer Peer, lease DurableLease) (DurableStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ctx == nil || ctx.Err() != nil {
		return DurableStatus{}, ErrCancelled
	}
	r, err := m.durableLeaseLocked(peer, lease, false)
	if err != nil {
		return DurableStatus{}, err
	}
	if err = m.reconcileDurableLocked(r); err != nil {
		return DurableStatus{}, err
	}
	if r.Started {
		return m.durable.status(r), nil
	}
	if r.State != Accepted {
		return DurableStatus{}, ErrState
	}
	if err = m.durable.verifyAdmission(); err != nil {
		return DurableStatus{}, err
	}
	if m.recoveryCode != "" {
		return DurableStatus{}, ErrReceiveRecovery
	}
	if !m.durable.now().Before(r.Lease.ExpiresAt) {
		return DurableStatus{}, ErrDurableExpired
	}
	release, err := durableCommitAdmission(ctx)
	if err != nil {
		return DurableStatus{}, err
	}
	defer release()
	if err = m.durable.withOwner(func() error { return m.startDurableLocked(ctx, r) }); err != nil {
		return DurableStatus{}, err
	}
	return m.durable.status(r), nil
}
func (m *Manager) startDurableLocked(ctx context.Context, r *durableRecord) error {
	d := m.durable
	destination := r.Destination
	parent, err := openDestination(destination)
	if err != nil {
		return err
	}
	defer parent.Close()
	identity, err := durableIdentity(parent, ".", true)
	if err != nil || identity != r.DestinationIdentity {
		return ErrDurableRecovery
	}
	if err = checkRootSpace(ctx, parent, m.diskSpace, m.limits.DiskReserveBytes); err != nil {
		return err
	}
	name, err := randomName("sobalink-durable-")
	if err != nil {
		return err
	}
	r.Root, r.Preparing, r.Started = name, true, true
	if err = d.save(); err != nil {
		return err
	}
	if err = parent.Mkdir(name, 0700); err != nil {
		return d.fail(err)
	}
	if err = protectDirectory(filepath.Join(destination, name)); err != nil {
		return d.fail(err)
	}
	root, err := parent.OpenRoot(name)
	if err != nil {
		return d.fail(err)
	}
	defer root.Close()
	r.RootIdentity, err = durableIdentity(parent, name, true)
	if err != nil {
		return d.fail(err)
	}
	if err = retirementSyncDirectory(parent); err != nil {
		return d.fail(err)
	}
	if err = d.save(); err != nil {
		return err
	}
	if err = root.Mkdir(durableStage, 0700); err != nil {
		return d.fail(err)
	}
	r.StageIdentity, err = durableIdentity(root, durableStage, true)
	if err != nil {
		return d.fail(err)
	}
	wanted := map[string]bool{}
	for _, f := range r.Files {
		dir := path.Dir(f.Entry.Path)
		if f.Entry.Kind == Directory {
			dir = f.Entry.Path
		}
		for dir != "." {
			wanted[dir] = true
			dir = path.Dir(dir)
		}
	}
	dirs := make([]string, 0, len(wanted))
	for dir := range wanted {
		dirs = append(dirs, dir)
	}
	sort.Slice(dirs, func(i, j int) bool {
		a, b := strings.Count(dirs[i], "/"), strings.Count(dirs[j], "/")
		if a != b {
			return a < b
		}
		return dirs[i] < dirs[j]
	})
	for _, dir := range dirs {
		if err = root.Mkdir(dir, 0700); err != nil {
			return d.fail(err)
		}
		id, e := durableIdentity(root, dir, true)
		if e != nil {
			return d.fail(e)
		}
		r.Directories = append(r.Directories, durableDirectory{dir, id})
	}
	// All directories belong to this exclusively created root. A crash before
	// these identities commit leaves Preparing set and blocks rather than adopts.
	if err = d.save(); err != nil {
		return err
	}
	if err = syncDurableDirectories(root, r); err != nil {
		return d.fail(err)
	}
	if err = verifyDurableRoot(parent, root, r); err != nil {
		return d.fail(err)
	}
	r.Preparing = false
	r.State = Accepted
	for i := range r.Files {
		if r.Files[i].Entry.Kind == Directory {
			r.Files[i].Phase = "saved"
			r.Files[i].SavedAt = d.now().UTC()
		}
	}
	durableComplete(r)
	return d.save()
}
func durableComplete(r *durableRecord) {
	if r.State == Cancelled || r.State == Rejected {
		return
	}
	for _, f := range r.Files {
		if f.Phase != "saved" {
			return
		}
	}
	r.State = Completed
}
func syncDurableDirectories(root *os.Root, r *durableRecord) error {
	for i := len(r.Directories) - 1; i >= 0; i-- {
		dir, err := root.OpenRoot(r.Directories[i].Path)
		if err != nil {
			return err
		}
		err = retirementSyncDirectory(dir)
		_ = dir.Close()
		if err != nil {
			return err
		}
	}
	stage, err := root.OpenRoot(durableStage)
	if err != nil {
		return err
	}
	err = retirementSyncDirectory(stage)
	_ = stage.Close()
	if err != nil {
		return err
	}
	return retirementSyncDirectory(root)
}
func verifyDurableRoot(parent, root *os.Root, r *durableRecord) error {
	if err := verifyPreparationParent(r.Destination, r.DestinationIdentity); err != nil {
		return err
	}
	for _, item := range []struct {
		root     *os.Root
		name, id string
	}{{parent, ".", r.DestinationIdentity}, {parent, r.Root, r.RootIdentity}, {root, ".", r.RootIdentity}, {root, durableStage, r.StageIdentity}} {
		id, err := durableIdentity(item.root, item.name, true)
		if err != nil || id != item.id {
			return ErrDurableRecovery
		}
	}
	for _, dir := range r.Directories {
		id, err := durableIdentity(root, dir.Path, true)
		if err != nil || id != dir.Identity {
			return ErrDurableRecovery
		}
	}
	return nil
}
func openDurableRoot(r *durableRecord) (*os.Root, *os.Root, error) {
	if r.Preparing || r.RootIdentity == "" || r.StageIdentity == "" {
		return nil, nil, ErrDurableRecovery
	}
	parent, err := openDestination(r.Destination)
	if err != nil {
		return nil, nil, err
	}
	root, err := parent.OpenRoot(r.Root)
	if err != nil {
		_ = parent.Close()
		return nil, nil, err
	}
	if err = verifyDurableRoot(parent, root, r); err != nil {
		_ = root.Close()
		_ = parent.Close()
		return nil, nil, err
	}
	return parent, root, nil
}
func (m *Manager) stopDurableLocked(r *durableRecord, state BatchState) (Batch, error) {
	if err := m.durable.verify(); err != nil {
		return durableBatch(r), err
	}
	if r.State == state {
		return durableBatch(r), nil
	}
	if terminal(r.State) || state == Rejected && r.State != Pending {
		return durableBatch(r), ErrState
	}
	if len(r.active) == 0 {
		if err := m.reconcileDurableLocked(r); err != nil {
			return m.durable.project(r), err
		}
		for i := range r.Files {
			if r.Files[i].Phase == "publish_pending" {
				r.Files[i].Phase = "receiving"
			}
		}
	}
	// Publication is serialized under m.mu. Preserve every saved fact and anchor.
	if r.State == Completed {
		return m.durable.project(r), nil
	}
	r.State = state
	err := m.durable.save()
	for _, cancel := range r.active {
		cancel()
	}
	return durableBatch(r), err
}
func (m *Manager) interruptDurablePeerLocked(id string) {
	if m.durable != nil {
		for _, r := range m.durable.state.Records {
			if id == "" || r.Peer.ID == id {
				for _, cancel := range r.active {
					cancel()
				}
			}
		}
	}
}
func (m *Manager) durableReservedLocked() int64 {
	var n int64
	if m.durable != nil {
		for _, r := range m.durable.state.Records {
			value := durableBytes(r)
			if value > math.MaxInt64-n {
				return math.MaxInt64
			}
			n += value
		}
		if m.durable.blocked != nil {
			// Failed cleanup/terminal publication can tentatively clear anchors
			// or remove rows. Retain the last certified physical reservation.
			var confirmed int64
			for _, r := range m.durable.confirmed.Records {
				value := durableBytes(r)
				if value > math.MaxInt64-confirmed {
					return math.MaxInt64
				}
				confirmed += value
			}
			n = max(n, confirmed)
		}
	}
	return n
}

type durableRelationship struct {
	peer    Peer
	epoch   string
	network string
}

// BindDurableRelationship carries the persistent relationship fence from the
// authenticated profile. Numeric trust generations alone may be reused after
// revocation and restart; this separate durable binding cannot be inferred.
func (m *Manager) BindDurableRelationship(peer Peer, network, epoch string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.checkPeerIdentityLocked(peer); err != nil {
		return err
	}
	if len(epoch) > 128 || network == "" || len(network) > 32 {
		return ErrDurablePermission
	}
	if m.durableEpochs == nil {
		m.durableEpochs = map[string]durableRelationship{}
	}
	if previous, ok := m.durableEpochs[peer.ID]; ok && previous != (durableRelationship{peer, epoch, network}) {
		m.interruptDurablePeerLocked(peer.ID)
	}
	m.durableEpochs[peer.ID] = durableRelationship{peer, epoch, network}
	return nil
}
func (m *Manager) checkDurableRelationshipLocked(peer Peer, epoch string) error {
	binding, ok := m.durableEpochs[peer.ID]
	if !ok || binding.peer != peer || binding.epoch != epoch {
		return ErrDurablePermission
	}
	return nil
}
func (m *Manager) checkDurableRecordRelationshipLocked(r *durableRecord) error {
	if err := m.checkDurableRelationshipLocked(r.Peer, r.RevocationEpoch); err != nil {
		return err
	}
	if m.durableEpochs[r.Peer.ID].network != r.Network {
		return ErrDurablePermission
	}
	return nil
}

// Terminal evidence is retained separately from live admission. Physical
// owners and unresolved publication still consume live slots, even after a
// cancellation fence. Any uncertain snapshot conservatively charges all rows.
func (d *durableReceiver) recordActive(r *durableRecord) bool {
	if d.blocked != nil || !terminal(r.State) || r.Preparing || len(r.active) > 0 {
		return true
	}
	for _, f := range r.Files {
		if f.Phase == "publish_pending" {
			return true
		}
	}
	return false
}

func (m *Manager) durableAdmissionLocked(peer string) (count, perPeer int, metadata int64) {
	if m.durable == nil {
		return
	}
	d := m.durable
	seen := map[string]bool{}
	for _, r := range d.state.Records {
		seen[r.Offer.DeliveryID] = true
		if d.recordActive(r) {
			count++
			metadata += r.ReservedMetadata
			if r.Peer.ID == peer {
				perPeer++
			}
		}
	}
	if d.blocked != nil {
		// Failed retirement may have removed tentative rows. Charge retained
		// certified evidence too, including its stricter per-peer bound.
		for _, r := range d.confirmed.Records {
			if !seen[r.Offer.DeliveryID] {
				count++
				metadata += r.ReservedMetadata
				if r.Peer.ID == peer {
					perPeer++
				}
			}
		}
	}
	return
}
func (m *Manager) durableCountLocked() int {
	count, _, _ := m.durableAdmissionLocked("")
	return count
}
func (m *Manager) durableMetadataLocked() int64 {
	_, _, metadata := m.durableAdmissionLocked("")
	return metadata
}
func sameDurableLease(a, b DurableLease) bool {
	a.ExpiresAt, a.ReceiptUntil = a.ExpiresAt.UTC(), a.ReceiptUntil.UTC()
	b.ExpiresAt, b.ReceiptUntil = b.ExpiresAt.UTC(), b.ReceiptUntil.UTC()
	return a == b
}

// CheckDurableRelationship is read-only; an incoming request cannot revert a
// newer binding by replaying a stale snapshot of private trust.
func (m *Manager) CheckDurableRelationship(peer Peer, network, epoch string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.checkPeerLocked(peer); err != nil {
		return err
	}
	if err := m.checkDurableRelationshipLocked(peer, epoch); err != nil {
		return err
	}
	if m.durableEpochs[peer.ID].network != network {
		return ErrDurablePermission
	}
	return nil
}
func durableCapacityLeft(limit int64, used ...int64) int64 {
	for _, n := range used {
		if n < 0 || n > limit {
			return -1
		}
		limit -= n
	}
	return limit
}
