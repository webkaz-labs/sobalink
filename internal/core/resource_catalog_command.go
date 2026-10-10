package core

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/resource"
	"github.com/webkaz-labs/sobalink/internal/resourcecatalog"
)

// The original open binding is retained through every capture and final
// disclosure. A newly opened replacement is never accepted as the old owner.
type resourceCatalogOwner struct {
	ctx               context.Context
	lock              *config.Lock
	nonce, resourceID string
	binding           *resourcePathBinding
}

func (c *Core) resourceCatalogOwnerLocked() (*resourceCatalogOwner, error) {
	if c.ctx == nil || c.ctx.Err() != nil || c.resourceLock == nil || !resource.ValidID(c.resourceNonce) || !resource.ValidID(c.resourceIdentity) {
		return nil, resourceCatalogUnavailable()
	}
	o := &resourceCatalogOwner{ctx: c.ctx, lock: c.resourceLock, nonce: c.resourceNonce, resourceID: c.resourceIdentity}
	err := o.lock.WithOwnershipInfo(c.dir, func(directory, lock os.FileInfo) error {
		b, err := openResourcePathBinding(c.dir, directory, lock)
		if err != nil {
			return err
		}
		if c.resourceDirectoryIdentity == nil || !os.SameFile(c.resourceDirectoryIdentity, b.journalInfo) {
			b.close()
			return errResourceBinding
		}
		o.binding = b
		return nil
	})
	if err != nil {
		if o.binding != nil {
			o.binding.close()
		}
		return nil, resourceCatalogUnavailable()
	}
	return o, nil
}

func (c *Core) resourceCatalogOwnerCurrentLocked(o *resourceCatalogOwner) error {
	if o == nil || o.binding == nil || c.ctx != o.ctx || o.ctx.Err() != nil || c.resourceLock != o.lock || c.resourceNonce != o.nonce || c.resourceIdentity != o.resourceID || c.resourceDirectoryIdentity == nil || !os.SameFile(c.resourceDirectoryIdentity, o.binding.journalInfo) {
		return resourceCatalogUnavailable()
	}
	c.mu.RLock()
	closing := c.closing
	c.mu.RUnlock()
	if closing {
		return resourceCatalogUnavailable()
	}
	err := o.lock.WithOwnershipInfo(c.dir, func(directory, lock os.FileInfo) error {
		if !os.SameFile(directory, o.binding.profileInfo) || !os.SameFile(lock, o.binding.lockInfo) {
			return errResourceBinding
		}
		return o.binding.check()
	})
	if err != nil {
		return resourceCatalogUnavailable()
	}
	return nil
}

func resourceCatalogProcessToken(nonce string) string {
	if !resource.ValidID(nonce) {
		return ""
	}
	sum := sha256.Sum256([]byte("sobalink/local-resource-catalog/process/v1\x00" + nonce))
	return hex.EncodeToString(sum[:])
}

// The State projection calls this without holding Core.op. Empty means the
// owning resource boot has not initialized, never a fallback PID/host token.
func (c *Core) resourceCatalogProcessID() string {
	c.op.Lock()
	defer c.op.Unlock()
	if c.ctx == nil || c.ctx.Err() != nil || c.resourceLock == nil || !resource.ValidID(c.resourceIdentity) {
		return ""
	}
	return resourceCatalogProcessToken(c.resourceNonce)
}

func resourceCatalogEpoch() (string, error) {
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", resourceCatalogUnavailable()
	}
	return hex.EncodeToString(nonce[:]), nil
}

func resourceCatalogSelections(request resourceCatalogRequest, epoch, process string) []resourcecatalog.Selection {
	out := make([]resourcecatalog.Selection, 0, len(request.Sources))
	for _, source := range request.Sources {
		s := resourcecatalog.Selection{SourceID: source.Kind, Kind: source.Kind, Epoch: epoch, Target: source.Target, PeerKey: source.PeerKey, GrantID: source.GrantID, GrantRevision: source.GrantRevision}
		if source.Kind == resourcecatalog.RemoteService {
			s.PeerKey = source.PeerID
		}
		if source.Kind == resourcecatalog.TransferActivity {
			s.ProcessID = process
		}
		out = append(out, s)
	}
	return out
}

func resourceCatalogFailure(s resourcecatalog.Selection, state string) resourceCatalogCapture {
	return resourceCatalogCapture{Selection: s, State: state, CheckedAt: time.Now().UnixMilli(), Rows: []resourcecatalog.Row{}}
}

func resourceCatalogComplete(s resourcecatalog.Selection, checked int64, rows []resourcecatalog.Row) resourceCatalogCapture {
	if rows == nil {
		rows = []resourcecatalog.Row{}
	}
	total := int64(len(rows))
	return resourceCatalogCapture{Selection: s, State: "current", CheckedAt: checked, Revision: resourceDigest(rows), Total: &total, Rows: rows}
}

// Command is authenticated-local only. Its caller branches before executeCommand
// acquires Core.op, and no result is inserted in request-ID mutation history.
func (c *Core) resourceCatalogCommand(ctx context.Context, raw json.RawMessage) (any, error) {
	request, err := decodeResourceCatalogRequest(raw)
	if err != nil {
		return nil, err
	}
	if ctx == nil || c.ctx == nil {
		return nil, resourceCatalogUnavailable()
	}
	done, err := c.beginWork()
	if err != nil {
		return nil, resourceCatalogUnavailable()
	}
	defer done()
	run, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(c.ctx, cancel)
	defer stop()
	defer cancel()
	c.op.Lock()
	servicePeer, settingsPeer := "", ""
	for _, selected := range request.Sources {
		if selected.Kind == resourcecatalog.RemoteService {
			servicePeer = selected.PeerID
		}
		if selected.Kind == resourcecatalog.RemoteSettingsV1 || selected.Kind == resourcecatalog.RemoteSettingsV2 {
			settingsPeer = selected.PeerKey
		}
	}
	if servicePeer != "" && settingsPeer != "" {
		c.mu.RLock()
		mode := c.profile.Settings.Network
		_, direct := c.node.(*directLANBackend)
		c.mu.RUnlock()
		if mode != "direct-lan" || !direct {
			c.op.Unlock()
			return nil, resourceCatalogInvalid()
		}
	}
	owner, err := c.resourceCatalogOwnerLocked()
	policy := c.capacityPolicy()
	c.op.Unlock()
	if err != nil {
		return nil, err
	}
	defer owner.binding.close()
	l, err := resourceCatalogPinnedLimits(policy)
	if err != nil {
		return nil, err
	}
	epoch, err := resourceCatalogEpoch()
	if err != nil {
		return nil, err
	}
	selections := resourceCatalogSelections(request, epoch, resourceCatalogProcessToken(owner.nonce))
	scope := resourceDigest(selections)
	budget, err := resourceCatalogBudgetFor(epoch, scope, selections, l)
	if err != nil {
		return nil, err
	}
	captures := make([]resourceCatalogCapture, 0, len(selections))
	var remoteOrigin *resourceManagementOrigin
	var discoveryOrigin *resourceCatalogDiscoveryOrigin
	for _, s := range selections {
		if run.Err() != nil {
			return nil, run.Err()
		}
		c.op.Lock()
		err = c.resourceCatalogOwnerCurrentLocked(owner)
		c.op.Unlock()
		if err != nil {
			return nil, err
		}
		var captured resourceCatalogCapture
		switch s.Kind {
		case resourcecatalog.LocalSettings:
			captured = c.resourceCatalogLocalSettings(run, s, l, budget)
		case resourcecatalog.LocalService:
			captured = c.resourceCatalogSavedServices(run, s, l, budget)
		case resourcecatalog.TransferActivity:
			captured = c.resourceCatalogTransfers(run, s, l, budget)
		case resourcecatalog.RemoteService:
			captured, discoveryOrigin = c.resourceCatalogDiscoveredServices(run, s, l, budget)
		case resourcecatalog.RemoteSettingsV1, resourcecatalog.RemoteSettingsV2:
			captured, remoteOrigin = c.resourceCatalogRemoteSettings(run, s, l, budget)
		}
		captures = append(captures, captured)
	}
	// Current backend checks are local state reads. Never execute them while
	// holding Core.op or invoke the public recursive command dispatcher.
	discoveryCurrent := discoveryOrigin != nil && c.resourceCatalogDiscoveryCurrent(run, discoveryOrigin)
	c.op.Lock()
	defer c.op.Unlock()
	if run.Err() != nil {
		return nil, run.Err()
	}
	if c.resourceCatalogOwnerCurrentLocked(owner) != nil {
		return nil, resourceCatalogUnavailable()
	}
	remoteCurrent := remoteOrigin != nil && c.resourceManagementOriginCurrentLocked(*remoteOrigin) == nil
	for i := range captures {
		s := captures[i].Selection
		if (s.Kind == resourcecatalog.RemoteSettingsV1 || s.Kind == resourcecatalog.RemoteSettingsV2) && remoteOrigin != nil && !remoteCurrent || s.Kind == resourcecatalog.RemoteService && discoveryOrigin != nil && !discoveryCurrent {
			captures[i] = resourceCatalogFailure(s, "unavailable")
		}
		if s.Kind == resourcecatalog.RemoteService && captures[i].State == "current" {
			now := time.Now()
			stale := !freshDiscoveryCheck(time.UnixMilli(captures[i].CheckedAt), now)
			for _, row := range captures[i].Rows {
				if row.RemoteService != nil && row.RemoteService.ExpiresAt != 0 && row.RemoteService.ExpiresAt <= now.UnixMilli() {
					stale = true
				}
			}
			if stale {
				captures[i].State = "stale"
			}
		}
	}
	if !remoteCurrent {
		remoteOrigin = nil
	}
	if !discoveryCurrent {
		discoveryOrigin = nil
	}
	// At most two remaining remote candidates can be invalidated. Rebuild only
	// after clearing an affected candidate, never recapture any provider or
	// discard a completed independent local source because a peer changed.
	for remaining := 2; remaining >= 0; remaining-- {
		response, err := resourceCatalogBuildResponse(epoch, scope, selections, captures, l)
		if err != nil {
			return nil, err
		}
		if run.Err() != nil {
			return nil, run.Err()
		}
		if c.resourceCatalogOwnerCurrentLocked(owner) != nil {
			return nil, resourceCatalogUnavailable()
		}
		remoteChanged := remoteOrigin != nil && c.resourceManagementOriginCurrentLocked(*remoteOrigin) != nil
		discoveryChanged := discoveryOrigin != nil && !c.resourceCatalogDiscoveryScalarsCurrent(discoveryOrigin)
		if !remoteChanged && !discoveryChanged {
			return response, nil
		}
		for i := range captures {
			s := captures[i].Selection
			if remoteChanged && (s.Kind == resourcecatalog.RemoteSettingsV1 || s.Kind == resourcecatalog.RemoteSettingsV2) || discoveryChanged && s.Kind == resourcecatalog.RemoteService {
				captures[i] = resourceCatalogFailure(s, "unavailable")
			}
		}
		if remoteChanged {
			remoteOrigin = nil
		}
		if discoveryChanged {
			discoveryOrigin = nil
		}
	}
	return nil, resourceCatalogUnavailable()
}

func resourceCatalogBuildResponse(epoch, scope string, selections []resourcecatalog.Selection, captures []resourceCatalogCapture, l resourcecatalog.Limits) (resourceCatalogResponse, error) {
	builder, err := resourcecatalog.NewBuilder(epoch, scope, selections, l)
	if err != nil {
		return resourceCatalogResponse{}, resourceCatalogCapacity()
	}
	for _, captured := range captures {
		if captured.State == "unconfirmed" {
			continue
		}
		if captured.State != "current" && captured.State != "stale" {
			if builder.Fail(captured.Selection, captured.State, captured.CheckedAt) != nil {
				_ = builder.Fail(captured.Selection, "invalid", time.Now().UnixMilli())
			}
			continue
		}
		_ = builder.AddPage(resourcecatalog.Page{Selection: captured.Selection, State: captured.State, CheckedAt: captured.CheckedAt, Revision: captured.Revision, Total: captured.Total, Rows: captured.Rows})
	}
	snapshot, err := builder.Finish()
	if err != nil {
		return resourceCatalogResponse{}, resourceCatalogCapacity()
	}
	response := resourceCatalogResponse{1, resourceCatalogViewOf(l), snapshot}
	// C1 checked exact snapshot size; the separately calculated full local
	// success framing is rechecked against the pinned allowance before return.
	encoded, err := json.Marshal(response)
	allowance, frameErr := resourceCatalogEnvelopeAllowance(l)
	snapshotBytes, snapshotErr := resourcecatalog.EncodeSnapshot(snapshot, l)
	if err != nil || frameErr != nil || snapshotErr != nil || int64(len(encoded)-len(snapshotBytes)) > allowance {
		return resourceCatalogResponse{}, resourceCatalogCapacity()
	}
	return response, nil
}
