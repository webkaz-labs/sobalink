package core

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/transfer"
)

// Capacity choices have a separate small envelope, so profile growth never
// requires reading an unbounded file to discover its own resource budget.
const capacityPolicyFile = "capacity.json"

func readCapacityPolicy(dir string) (capacity.Policy, error) {
	p := capacity.Defaults()
	if err := readBoundedPrivateJSON(filepath.Join(dir, capacityPolicyFile), 64<<10, &p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return p, err
	}
	return p, p.Validate()
}

func (c *Core) capacityPolicy() capacity.Policy {
	c.mu.RLock()
	p := c.capacity.Clone()
	c.mu.RUnlock()
	if p.Version == 0 {
		return capacity.Defaults()
	}
	return p
}

func (c *Core) limit(group, key string) int64 { return c.capacityPolicy().Number(group, key) }

// ReadProfile reads a private profile using its separately selected finite
// storage budget. It is also used by local lifecycle review commands.
func ReadProfile(path string) (Profile, error) {
	p := Profile{}
	limits, err := readCapacityPolicy(filepath.Dir(path))
	if err != nil {
		return p, err
	}
	err = readBoundedPrivateJSON(path, limits.Number("resources", "profileBytes"), &p)
	return p, err
}

func readBoundedPrivateJSON(path string, limit int64, out any) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("private state must be a regular file, not a symlink")
	}
	if limit < 1 || info.Size() > limit {
		return &localCommandError{"profile_capacity", fmt.Sprintf("private state requires %d bytes; configured storage budget is %d bytes", info.Size(), limit)}
	}
	f, err := (transfer.Source{LocalPath: path}).Open()
	if err != nil {
		return err
	}
	defer f.Close()
	r := &io.LimitedReader{R: f, N: limit + 1}
	d := json.NewDecoder(r)
	d.DisallowUnknownFields()
	if err = d.Decode(out); err != nil {
		return err
	}
	if err = d.Decode(new(any)); err != io.EOF {
		return errors.New("unexpected data after private state")
	}
	if r.N <= 0 {
		return errors.New("private state exceeds configured storage budget")
	}
	return nil
}

func (c *Core) writeProfile(p Profile) error {
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	limit := c.limit("resources", "profileBytes")
	if int64(len(b))+1 > limit {
		return &localCommandError{"profile_capacity", fmt.Sprintf("profile needs %d bytes; raise the %d-byte profile storage budget before saving", len(b)+1, limit)}
	}
	return c.writeAtomic(filepath.Join(c.dir, "sobalink.json"), append(b, '\n'))
}

func capacityRevision(p capacity.Policy, profile Profile) string {
	b, _ := json.Marshal(struct {
		Policy  capacity.Policy
		Profile Profile
	}{p, profile})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func capacityReviewRevision(current capacity.Policy, profile Profile, proposed capacity.Policy) string {
	b, _ := json.Marshal(struct {
		Current  string
		Proposed capacity.Policy
	}{capacityRevision(current, profile), proposed})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// Add keys here only when the consuming code enforces that choice. Unknown
// future knobs cannot masquerade as effective settings.
var supportedCapacityLogical = map[string]bool{"savedServices": true, "trustedPeers": true, "sharePeers": true, "rangePolicies": true, "portIntervals": true,
	"messageBytes": true, "stagingSeconds": true,
	"batchEntries": true, "fileBytes": true, "batchBytes": true, "transferHistoryEntries": true, "receiveWaitSeconds": true, "fileTransferSeconds": true,
	"pathDepth": true, "pathBytes": true,
	"messageHistoryEntries": true, "messageHistoryBytes": true, "messageHistoryAgeSeconds": true}
var supportedCapacityResources = map[string]bool{"workerFrameBytes": true, "workerRequests": true, "workerHandles": true, "profileBytes": true, "lanStateBytes": true, "discoveryBytes": true, "materializedListeners": true, "pageBytes": true, "pageEntries": true,
	"messageTextBytes": true,
	"tcpConnections":   true, "tcpPerPolicy": true, "tcpPerPeer": true, "udpSessions": true, "udpPerPolicy": true, "udpQueuedBytes": true, "udpPolicyQueuedBytes": true, "udpQueuePackets": true}

func init() {
	for _, key := range []string{"diskReserveBytes", "transferSpoolBytes", "receiveReservedBytes", "transferManifestBytes", "transferMetadataBytes", "transferPending", "transferPendingPerPeer", "transferConcurrentFiles", "transferConcurrentPerPeer", "messageStorageBytes", "stagingInventoryEntries", "stagingInventoryDepth"} {
		supportedCapacityResources[key] = true
	}
}

func validateSupportedCapacity(p capacity.Policy) error {
	if err := p.Validate(); err != nil {
		return err
	}
	if _, err := selectedWorkerLimits(p); err != nil {
		return err
	}
	for _, g := range []struct {
		values    map[string]capacity.Choice
		supported map[string]bool
	}{{p.Logical, supportedCapacityLogical}, {p.Resources, supportedCapacityResources}} {
		for key, value := range g.values {
			if value.Mode != "default" && !g.supported[key] {
				return &localCommandError{"policy_unsupported", fmt.Sprintf("%s cannot be changed by this build", key)}
			}
		}
	}
	return nil
}

func validateCapacityBackend(_ string, p capacity.Policy) error {
	// Each backend now enforces the selected trust policy during admission.
	// Loading existing LAN pairs is bounded by its separate private-state budget.
	return p.Validate()
}

func (c *Core) capacityUsage() map[string]int64 {
	c.mu.RLock()
	usage := map[string]int64{"savedServices": int64(len(c.profile.Services)), "trustedPeers": int64(len(c.profile.Peers)), "activeServices": int64(len(c.active)), "materializedListeners": 0, "rangePolicies": 0, "portIntervals": 0}
	usage["groups"] = int64(len(c.profile.Groups))
	usage["groupMembers"] = 0
	for _, group := range c.profile.Groups {
		if int64(len(group.ServiceIDs)) > usage["groupMembers"] {
			usage["groupMembers"] = int64(len(group.ServiceIDs))
		}
	}
	usage["orphanSpoolBytes"], usage["orphanSpoolEntries"] = c.orphanSpoolBytes, c.orphanSpoolEntries
	usage["materializedListeners"] = int64(len(c.proxies) + c.suspendedProxyReservations())
	for _, a := range c.active {
		// Admission counts retained reservations, including a suspended grant
		// whose listener is being recovered. Match that same count in previews.
		if a.spec.Direction == "forward" || a.spec.Network == "udp" {
			usage["materializedListeners"] += int64(a.effective.Count())
		}
		if a.spec.Direction == "share" {
			usage["rangePolicies"]++
			usage["portIntervals"] += int64(a.effective.IntervalCount())
		}
	}
	if b, err := json.MarshalIndent(c.profile, "", "  "); err == nil {
		usage["profileBytes"] = int64(len(b)) + 1
	}
	resources, lan := c.resources, c.lan
	c.mu.RUnlock()
	if c.transfers != nil {
		if bytes := c.transfers.ReceiveRecovery().ReservedBytes; bytes != nil {
			usage["receiveReservedBytes"] = *bytes
		}
	}
	if private, err := c.privateSettingsUsage(); err == nil {
		for key, size := range private {
			usage[key] = size
			usage["profileBytes"] = max(usage["profileBytes"], size)
		}
	}
	usage["lanPairedPeers"], usage["lanStateBytes"] = 0, 0
	if lan != nil {
		lan.mu.Lock()
		usage["lanPairedPeers"] = int64(len(lan.state.Remotes))
		if b, err := json.MarshalIndent(lan.state, "", "  "); err == nil {
			usage["lanStateBytes"] = int64(len(b)) + 1
		}
		if info, err := os.Lstat(lan.path); err == nil {
			usage["lanStateBytes"] = max(usage["lanStateBytes"], info.Size())
		}
		lan.mu.Unlock()
	}
	usage["tcpConnections"], usage["udpSessions"], usage["udpQueuedBytes"] = 0, 0, 0
	if resources != nil {
		current := resources.Usage()
		usage["tcpConnections"] = current.TCPConnections
		usage["udpSessions"] = current.UDPSessions
		usage["udpQueuedBytes"] = current.UDPQueuedBytes
	}
	return usage
}

func (c *Core) capacityView() map[string]any {
	p := c.capacityPolicy()
	resolved, _ := p.Resolve()
	logical, resources := map[string]bool{}, map[string]bool{}
	for key, value := range supportedCapacityLogical {
		logical[key] = value
	}
	for key, value := range supportedCapacityResources {
		resources[key] = value
	}
	return map[string]any{"version": capacity.Version, "requested": p, "effective": resolved, "catalog": capacity.Catalog(), "adjustable": map[string]any{"logical": logical, "resources": resources}, "usage": c.capacityUsage(), "revision": capacityRevision(p, c.profileCopy())}
}

func (c *Core) capacityCommand(name string, raw json.RawMessage) (any, error) {
	if name == "policy.config" {
		var in struct{}
		if err := decodePayload(raw, &in); err != nil {
			return nil, err
		}
		return c.capacityView(), nil
	}
	var in struct {
		Policy           capacity.Policy `json:"policy"`
		ExpectedRevision string          `json:"expectedRevision"`
	}
	if err := decodePayload(raw, &in); err != nil {
		return nil, err
	}
	if err := validateSupportedCapacity(in.Policy); err != nil {
		return nil, err
	}
	current := c.capacityPolicy()
	profile := c.profileCopy()
	if err := validateCapacityBackend(profile.Settings.Network, in.Policy); err != nil {
		return nil, err
	}
	revision := capacityReviewRevision(current, profile, in.Policy)
	resolved, _ := in.Policy.Resolve()
	if err := c.validatePrivateSettingsCapacity(resolved.Number("resources", "profileBytes")); err != nil {
		return nil, err
	}
	nextTransferLimits := receiveTransferLimits(in.Policy)
	if err := transfer.ValidateLimits(nextTransferLimits); err != nil {
		return nil, err
	}
	view := map[string]any{"version": capacity.Version, "requested": in.Policy, "effective": resolved, "usage": c.capacityUsage(), "revision": revision, "destructive": false}
	if name == "policy.preview" {
		return view, nil
	}
	if in.ExpectedRevision == "" || in.ExpectedRevision != revision {
		return nil, &localCommandError{"policy_revision_conflict", "capacity policy or profile changed; preview the current choices before applying"}
	}
	// Lower choices govern future admission. They neither evict records nor
	// cancel active work. A profile budget must still hold already saved data.
	encoded, _ := json.MarshalIndent(profile, "", "  ")
	if int64(len(encoded))+1 > resolved.Number("resources", "profileBytes") {
		return nil, &localCommandError{"policy_in_use", "profile storage budget cannot be below the current saved profile size"}
	}
	// Incoming message append uses c.mu rather than the command lock. Hold it
	// across the storage check and durable policy publication, so an append
	// cannot become unreadable under a concurrently lowered storage budget.
	saveErr := func() error {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.closing || c.ctx != nil && c.ctx.Err() != nil {
			return errors.New("application is stopping")
		}
		if info, err := os.Lstat(filepath.Join(c.dir, "messages.json")); err == nil && info.Size() > resolved.Number("resources", "messageStorageBytes") {
			return &localCommandError{"policy_in_use", "message storage budget cannot be below saved history; review an explicit history cleanup first"}
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return c.applyLANCapacityLocked(in.Policy, func() error {
			err := config.WriteJSONWith(c.writeAtomic, filepath.Join(c.dir, capacityPolicyFile), in.Policy)
			if atomicPublished(err) {
				c.capacity = in.Policy.Clone()
			}
			return err
		})
	}()
	if !atomicPublished(saveErr) {
		return nil, saveErr
	}
	// Core command/Close serialization keeps the manager alive here. This
	// changes admission only; occupied reservations remain accounted for.
	if c.transfers != nil {
		if err := c.transfers.UpdateAccountingLimits(receiveAccountingLimits(in.Policy)); err != nil {
			return nil, err
		}
		if err := c.transfers.UpdateLimits(nextTransferLimits); err != nil {
			return nil, errors.Join(saveErr, err)
		}
	}
	return c.capacityView(), saveErr
}

func capacityJSONEqual(a, b capacity.Policy) bool {
	left, _ := json.Marshal(a)
	right, _ := json.Marshal(b)
	return bytes.Equal(left, right)
}
