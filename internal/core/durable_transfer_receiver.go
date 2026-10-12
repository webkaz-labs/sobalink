package core

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/connectionroute"
	"github.com/webkaz-labs/sobalink/internal/transfer"
)

func (c *Core) initializeDurableReceiver(initialize bool) error {
	p := c.profileCopy()
	if p.DurableFiles == nil || c.resourceLock == nil {
		return transfer.ErrDurableStorage
	}
	namespace := filepath.Join(c.dir, "durable-v1")
	owner := c.resourceLock
	var bound func() error
	err := owner.WithOwnershipInfo(c.dir, func(directory, lock os.FileInfo) error {
		if initialize {
			if err := config.SecureChildDirectoryBound(c.dir, "durable-v1", directory); err != nil {
				return err
			}
		}
		profile, err := os.OpenRoot(c.dir)
		if err != nil {
			return err
		}
		defer profile.Close()
		opened, err := profile.Stat(".")
		if err != nil || !os.SameFile(directory, opened) {
			return transfer.ErrDurableRecovery
		}
		root, err := profile.OpenRoot("durable-v1")
		if err != nil {
			return err
		}
		defer root.Close()
		info, err := root.Stat(".")
		if err != nil {
			return err
		}
		bound = func() error {
			actual, err := os.Lstat(c.dir)
			if err != nil || !actual.IsDir() || !os.SameFile(directory, actual) {
				return transfer.ErrDurableRecovery
			}
			current, err := os.Lstat(filepath.Join(c.dir, "process.lock"))
			if err != nil || !current.Mode().IsRegular() || !os.SameFile(lock, current) {
				return transfer.ErrDurableRecovery
			}
			child, err := os.Lstat(namespace)
			if err != nil || !child.IsDir() || !os.SameFile(info, child) {
				return transfer.ErrDurableRecovery
			}
			return nil
		}
		if err = bound(); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	owned := func(operation func() error) error {
		err := owner.TryWithOwnershipInfo(c.dir, func(_, _ os.FileInfo) error {
			if err := bound(); err != nil {
				return err
			}
			err := operation()
			if check := bound(); check != nil {
				return errors.Join(err, config.ErrAtomicCommitted, check)
			}
			return err
		})
		if errors.Is(err, config.ErrLifecycleBusy) {
			return transfer.ErrBusy
		}
		return err
	}
	return c.transfers.EnableDurableReceiver(transfer.DurableOptions{Path: filepath.Join(namespace, "receiver.json"), Epoch: p.DurableFiles.Epoch, Initialize: initialize, OwnerCheck: func() error { return owned(func() error { return nil }) }, OwnerOperation: owned})
}

func (c *Core) serveDurableTransfer(w http.ResponseWriter, r *http.Request, peer transfer.Peer) bool {
	if !strings.HasPrefix(r.URL.Path, "/v1/durable-transfers/") {
		return false
	}
	failed := func(err error) {
		code := transfer.DurableErrorCode(err)
		status := http.StatusConflict
		if errors.Is(err, transfer.ErrDurableCapacity) {
			status = http.StatusInsufficientStorage
		}
		reply(w, status, map[string]string{"code": code, "error": code})
	}
	if r.Method != http.MethodPost {
		peerFailure(w, http.StatusMethodNotAllowed)
		return true
	}
	if !c.durableProfileReady() {
		failed(transfer.ErrDurableRecovery)
		return true
	}
	current, ok := c.trust(peer.ID)
	if !ok || current.Generation != peer.Generation || current.Paused {
		failed(transfer.ErrDurablePermission)
		return true
	}
	// No remote data can establish a relationship epoch. Only the current private
	// profile supplies this binding; every later publication rechecks it.
	if err := c.transfers.CheckDurableRelationship(peer, current.Network, current.RevocationEpoch); err != nil {
		failed(err)
		return true
	}
	var status transfer.DurableStatus
	var err error
	switch r.URL.Path {
	case transfer.DurableOfferPath:
		var offer transfer.DurableOffer
		if err = readJSON(r, c.receiveManifestJSONBytes()+4096, &offer); err != nil {
			peerFailure(w, 400)
			return true
		}
		var recipient string
		recipient, err = c.durableRecipientID(r, offer.RecipientID)
		if err == nil {
			var release func()
			release, err = incomingPeerMessageCommit(r.Context())
			if err == nil {
				defer release()
				status, err = c.transfers.OfferDurable(peer, recipient, offer)
			}
		}

	case transfer.DurableStartPath:
		var request transfer.DurableRequest
		if err = readJSON(r, 4096, &request); err != nil {
			peerFailure(w, 400)
			return true
		}
		if _, err = c.durableRecipientID(r, request.Lease.RecipientID); err == nil {
			ctx := transfer.WithDurableCommitAdmission(r.Context(), func() (func(), error) { return incomingPeerMessageCommit(r.Context()) })
			status, err = c.transfers.StartDurable(ctx, peer, request.Lease)
		}
	case transfer.DurableStatusPath:
		var request transfer.DurableRequest
		if err = readJSON(r, 4096, &request); err != nil {
			peerFailure(w, 400)
			return true
		}
		if _, err = c.durableRecipientID(r, request.Lease.RecipientID); err == nil {
			status, err = c.transfers.StatusDurable(peer, request.Lease)
		}
	case transfer.DurableFilePath:
		encoded := r.Header.Get(transfer.DurableLeaseHeader)
		if len(encoded) > 4096 || encoded == "" {
			peerFailure(w, 400)
			return true
		}
		var data []byte
		data, err = base64.RawURLEncoding.DecodeString(encoded)
		var lease transfer.DurableLease
		if err == nil {
			decoder := json.NewDecoder(bytes.NewReader(data))
			decoder.DisallowUnknownFields()
			err = decoder.Decode(&lease)
		}
		if err != nil {
			peerFailure(w, 400)
			return true
		}
		fileID := r.URL.Query().Get("fileID")
		if !config.ValidPeerID(fileID) {
			peerFailure(w, 400)
			return true
		}
		if _, err = c.durableRecipientID(r, lease.RecipientID); err != nil {
			failed(err)
			return true
		}
		var batch transfer.Batch
		batch, err = c.transfers.Get(lease.DeliveryID)
		if err != nil || batch.Peer != peer {
			failed(transfer.ErrDurableUnknownLease)
			return true
		}
		size := int64(-1)
		for _, file := range batch.Files {
			if file.ID == fileID {
				size = file.Size
			}
		}
		if size < 0 {
			peerFailure(w, 404)
			return true
		}
		r.Body = http.MaxBytesReader(w, r.Body, size+1)
		ctx, finish := c.operationContext(r.Context(), "fileTransferSeconds")
		defer finish()
		ctx = transfer.WithDurableCommitAdmission(ctx, func() (func(), error) { return incomingPeerMessageCommit(r.Context()) })
		deadline, _ := ctx.Deadline()
		controller := http.NewResponseController(w)
		_ = controller.SetReadDeadline(deadline)
		_ = controller.SetWriteDeadline(deadline)
		status, err = c.transfers.ReceiveDurableFile(ctx, peer, lease, fileID, r.Body)
	default:
		peerFailure(w, 404)
		return true
	}
	if err != nil {
		failed(err)
	} else {
		reply(w, 200, status)
	}
	return true
}

// Resolve the requested receiver identity from the fresh authenticated local
// transport (or the reviewed mixed identity), never from a display name.
func (c *Core) durableRecipientID(r *http.Request, want string) (string, error) {
	node := c.nodeCopy()
	if node == nil {
		return "", transfer.ErrDurablePermission
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	if mixed, ok := node.(*mixedBackend); ok {
		remote, err := netip.ParseAddrPort(r.RemoteAddr)
		if err != nil {
			return "", transfer.ErrDurablePermission
		}
		mixed.mu.Lock()
		source, found := mixed.sources[remote]
		mixed.mu.Unlock()
		if !found {
			return "", transfer.ErrDurablePermission
		}
		actual := mixed.nodes[source.backend]
		if actual == nil {
			return "", transfer.ErrDurablePermission
		}
		state, err := actual.State(ctx)
		if err != nil || !state.Snapshot.Running {
			return "", transfer.ErrDurablePermission
		}
		self := state.SelfID
		if self == "" {
			if key, ok := actual.(interface{ PublicKey() string }); ok {
				self = key.PublicKey()
			}
		}
		if self != "" && want == mixedID(source.backend, self) {
			return want, nil
		}
		saved, err := c.readMixedState()
		if err != nil {
			return "", transfer.ErrDurablePermission
		}
		seed, err := hex.DecodeString(saved.Seed)
		if err != nil || len(seed) != ed25519.SeedSize {
			return "", transfer.ErrDurablePermission
		}
		own, err := connectionroute.StablePeerID(ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey))
		if err == nil && want == own && node == c.nodeCopy() {
			return want, nil
		}
		return "", transfer.ErrDurablePermission
	}
	state, err := node.State(ctx)
	if err != nil || !state.Snapshot.Running {
		return "", transfer.ErrDurablePermission
	}
	own := state.SelfID
	if own == "" {
		if key, ok := node.(interface{ PublicKey() string }); ok {
			own = key.PublicKey()
		}
	}
	if own == "" || own != want || node != c.nodeCopy() {
		return "", transfer.ErrDurablePermission
	}
	return own, nil
}
