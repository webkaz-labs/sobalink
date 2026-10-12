package transfer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path"
)

// ReceiveDurableFile never reconstructs an offer. The exact persisted lease,
// authenticated relationship and accepted destination are mandatory before any
// payload is consumed. Replay of a durable saved fact never touches user output.
func (m *Manager) ReceiveDurableFile(ctx context.Context, peer Peer, lease DurableLease, fileID string, reader io.Reader) (result DurableStatus, resultErr error) {
	if ctx == nil || reader == nil {
		return result, ErrState
	}
	m.mu.Lock()
	r, err := m.durableLeaseLocked(peer, lease, false)
	if err != nil {
		m.mu.Unlock()
		return result, err
	}
	d := m.durable
	if err = m.reconcileDurableLocked(r); err != nil {
		m.mu.Unlock()
		return result, err
	}
	index := -1
	for i := range r.Files {
		if r.Files[i].Entry.ID == fileID {
			index = i
			break
		}
	}
	if index < 0 || r.Files[index].Entry.Kind != File {
		m.mu.Unlock()
		return result, ErrNotFound
	}
	f := &r.Files[index]
	if f.Phase == "saved" {
		result = d.status(r)
		m.mu.Unlock()
		return result, nil
	}
	if m.recoveryCode != "" {
		m.mu.Unlock()
		return result, ErrReceiveRecovery
	}
	if err = d.verifyAdmission(); err != nil {
		m.mu.Unlock()
		return result, err
	}
	if r.State == Pending || terminal(r.State) || r.Preparing || !r.Started {
		m.mu.Unlock()
		return result, ErrState
	}
	if !d.now().Before(r.Lease.ExpiresAt) {
		m.mu.Unlock()
		return result, ErrDurableExpired
	}
	resumePublication := f.Phase == "publish_pending"
	p := m.peers[peer.ID]
	if r.active[fileID] != nil || m.active >= m.limits.MaxConcurrentFiles || p.active >= m.limits.MaxConcurrentPerPeer {
		m.mu.Unlock()
		return result, ErrBusy
	}
	parent, root, err := openDurableRoot(r)
	if err != nil {
		err = d.fail(err)
		m.mu.Unlock()
		return result, err
	}
	streamCtx, cancel := context.WithDeadline(ctx, r.Lease.ExpiresAt)
	if r.active == nil {
		r.active = map[string]context.CancelFunc{}
	}
	r.active[fileID] = cancel
	m.active++
	p.active++
	m.mu.Unlock()
	var file *os.File
	joined := make(chan struct{})
	stopClose := func() bool { return true }
	if closer, ok := reader.(io.Closer); ok {
		stopClose = context.AfterFunc(streamCtx, func() { _ = closer.Close(); close(joined) })
	}
	defer func() {
		if file != nil {
			_ = file.Close()
		}
		if !stopClose() {
			<-joined
		}
		cancel()
		// Root/file owners close before accounting can release their anchors.
		_ = root.Close()
		_ = parent.Close()
		m.mu.Lock()
		delete(r.active, fileID)
		m.active--
		p.active--
		if resultErr == nil && !m.closed {
			if e := m.cleanupDurableLocked(r); e != nil && d.blocked != nil {
				resultErr = e
				result = DurableStatus{}
			}
		}
		m.mu.Unlock()
	}()
	if !resumePublication {
		m.mu.Lock()
		if err = receiveCancellation(ctx, streamCtx); err == nil {
			_, err = m.checkPeerLocked(peer)
			if err == nil {
				err = m.checkDurableRecordRelationshipLocked(r)
			}
		}
		if err == nil {
			err = verifyDurableRoot(parent, root, r)
		}
		if err == nil {
			err = d.withOwner(func() error {
				if err == nil && f.Anchor == "" {
					f.Anchor, err = randomName("part-")
				}
				if err == nil {
					f.Phase = "receiving"
					err = d.save()
				}
				if err != nil {
					return err
				}
				name := durableStage + "/" + f.Anchor
				if f.Identity == "" {
					file, err = root.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
					if err == nil {
						f.Identity, err = accountingIdentity(file)
					}
					if err == nil {
						err = file.Sync()
					}
					if err == nil {
						stage, e := root.OpenRoot(durableStage)
						err = e
						if e == nil {
							err = retirementSyncDirectory(stage)
							_ = stage.Close()
						}
					}
					if err == nil {
						err = d.save()
					}
				} else {
					var info os.FileInfo
					info, err = root.Lstat(name)
					if err == nil && !info.Mode().IsRegular() {
						err = ErrDurableRecovery
					}
					if err == nil {
						file, err = durableOpenWritable(root, name)
					}
					if err == nil {
						id, e := accountingIdentity(file)
						err = e
						if id != f.Identity {
							err = ErrDurableRecovery
						}
					}
					if err == nil {
						err = file.Truncate(0)
					}
				}
				return err
			})
		}

		m.mu.Unlock()
		if err != nil {
			return result, err
		}
		hash := sha256.New()
		buffer := make([]byte, streamBufferBytes)
		var received int64
		empty := 0
		for {
			if err = receiveCancellation(ctx, streamCtx); err != nil {
				return result, err
			}
			want := min(int64(len(buffer)), f.Entry.Size-received+1)
			n, readErr := reader.Read(buffer[:want])
			if n < 0 || int64(n) > want {
				return result, ErrIntegrity
			}
			if err = receiveCancellation(ctx, streamCtx); err != nil {
				return result, err
			}
			if int64(n) > f.Entry.Size-received {
				return result, ErrIntegrity
			}
			if n > 0 {
				empty = 0
				written, e := m.diskSpace.Write(streamCtx, file, buffer[:n], m.diskReserve())
				if e != nil {
					return result, e
				}
				if written != n {
					return result, io.ErrShortWrite
				}
				_, _ = hash.Write(buffer[:n])
				received += int64(n)
			} else {
				empty++
				if empty >= 100 && readErr == nil {
					return result, io.ErrNoProgress
				}
			}
			if readErr == io.EOF {
				break
			}
			if readErr != nil {
				return result, readErr
			}
		}
		if received != f.Entry.Size || hex.EncodeToString(hash.Sum(nil)) != f.Entry.SHA256 {
			return result, ErrIntegrity
		}
		if err = file.Sync(); err != nil {
			return result, err
		}
		if err = file.Close(); err != nil {
			file = nil
			return result, err
		}
		file = nil
		m.mu.Lock()
		if err = receiveCancellation(ctx, streamCtx); err == nil {
			_, err = m.checkPeerLocked(peer)
			if err == nil {
				err = m.checkDurableRecordRelationshipLocked(r)
			}
		}
		if err == nil {
			f.Phase = "publish_pending"
			err = d.save()
		}
		m.mu.Unlock()
		if err != nil {
			return result, err
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err = receiveCancellation(ctx, streamCtx); err != nil {
		return result, err
	}
	if _, err = m.checkPeerLocked(peer); err != nil {
		return result, err
	}
	if err = m.checkDurableRecordRelationshipLocked(r); err != nil {
		return result, err
	}
	if terminal(r.State) {
		return result, ErrCancelled
	}
	if !d.now().Before(r.Lease.ExpiresAt) {
		return result, ErrDurableExpired
	}
	if err = d.verify(); err != nil {
		return result, err
	}
	if m.recoveryCode != "" {
		return result, ErrReceiveRecovery
	}
	err = d.withOwner(func() error {
		if err = verifyDurableRoot(parent, root, r); err != nil {
			return d.fail(err)
		}
		release, admitErr := durableCommitAdmission(ctx)
		if admitErr != nil {
			return admitErr
		}
		defer release()
		// The mutex is the publication admission boundary shared with pause, revoke,
		// cancel and close. Once Link wins, preserve/reconcile the actual save fact.
		if err = verifyDurableAnchor(root, *f); err != nil {
			return d.fail(err)
		}
		if d.fault != nil {
			if err = d.fault("before_link"); err != nil {
				return d.fail(err)
			}
		}
		if err = root.Link(durableStage+"/"+f.Anchor, f.Entry.Path); err != nil {
			return d.fail(err)
		}
		if d.fault != nil {
			if err = d.fault("after_link"); err != nil {
				return d.fail(err)
			}
		}
		if err = durableSavedLink(root, *f); err != nil {
			return d.fail(err)
		}
		if err = syncDurableFinal(root, f.Entry.Path); err != nil {
			return d.fail(err)
		}
		if d.fault != nil {
			if err = d.fault("after_final_sync"); err != nil {
				return d.fail(err)
			}
		}
		f.Phase = "saved"
		f.SavedAt = d.now().UTC()
		durableComplete(r)
		if err = d.save(); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return result, err
	}

	return d.status(r), nil
}

func verifyDurableAnchor(root *os.Root, f durableFile) error {
	file, err := durableOpenWritable(root, durableStage+"/"+f.Anchor)
	if err != nil {
		return err
	}
	defer file.Close()
	id, err := accountingIdentity(file)
	if err != nil || id != f.Identity {
		return ErrDurableRecovery
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != f.Entry.Size {
		return ErrDurableRecovery
	}
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(file, f.Entry.Size+1))
	if err != nil || n != f.Entry.Size || hex.EncodeToString(hash.Sum(nil)) != f.Entry.SHA256 {
		return ErrDurableRecovery
	}
	return file.Sync()
}
func durableSavedLink(root *os.Root, f durableFile) error {
	anchor, err := root.Lstat(durableStage + "/" + f.Anchor)
	if err != nil {
		return err
	}
	final, err := root.Lstat(f.Entry.Path)
	if err != nil {
		return err
	}
	if !anchor.Mode().IsRegular() || !final.Mode().IsRegular() || !os.SameFile(anchor, final) {
		return ErrDurableRecovery
	}
	return nil
}
func syncDurableFinal(root *os.Root, name string) error {
	parent, err := root.OpenRoot(path.Dir(name))
	if err != nil {
		return err
	}
	defer parent.Close()
	return retirementSyncDirectory(parent)
}

// Recovery never publishes a missing final name. It only records an existing
// same-inode effect, even after expiry. Missing output needs a fresh authorized
// file request; a conflicting or missing anchor can never stand in as proof.
func (m *Manager) reconcileDurableLocked(r *durableRecord) error {
	d := m.durable
	if err := d.verify(); err != nil {
		return err
	}
	if r.Preparing {
		return ErrDurableRecovery
	}
	need := false
	for _, f := range r.Files {
		if f.Phase == "publish_pending" {
			need = true
		}
	}
	if !need {
		return nil
	}
	parent, root, err := openDurableRoot(r)
	if err != nil {
		return d.fail(err)
	}
	defer parent.Close()
	defer root.Close()
	changed := false
	for i := range r.Files {
		f := &r.Files[i]
		if f.Phase != "publish_pending" {
			continue
		}
		if err = verifyDurableAnchor(root, *f); err != nil {
			return d.fail(err)
		}
		_, err = root.Lstat(f.Entry.Path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return d.fail(err)
		}
		if err = durableSavedLink(root, *f); err != nil {
			return d.fail(err)
		}
		if err = syncDurableFinal(root, f.Entry.Path); err != nil {
			return d.fail(err)
		}
		f.Phase = "saved"
		f.SavedAt = d.now().UTC()
		changed = true
	}
	if changed {
		durableComplete(r)
		if err = d.save(); err != nil {
			return err
		}
	}
	return nil
}

type durableCommitKey struct{}

// WithDurableCommitAdmission supplies a fresh transport-origin admission at the
// filesystem publication boundary. The callback must not acquire Manager/Core
// locks; the returned lease is held through publication and ledger completion.
func WithDurableCommitAdmission(ctx context.Context, admit func() (func(), error)) context.Context {
	return context.WithValue(ctx, durableCommitKey{}, admit)
}
func durableCommitAdmission(ctx context.Context) (func(), error) {
	if fn, ok := ctx.Value(durableCommitKey{}).(func() (func(), error)); ok {
		return fn()
	}
	return func() {}, ctx.Err()
}
