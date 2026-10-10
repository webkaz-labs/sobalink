//go:build resource_group_catalog_native && resource_management_native && resource_inspection_native && directlan_activation_native

package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/directlan"
	"github.com/webkaz-labs/sobalink/internal/endpointmeta"
	"github.com/webkaz-labs/sobalink/internal/resource"
	"github.com/webkaz-labs/sobalink/internal/resourceacceptance"
	"github.com/webkaz-labs/sobalink/internal/resourcecatalog"
	"github.com/webkaz-labs/sobalink/internal/resourcegrant"
	"github.com/webkaz-labs/sobalink/internal/resourcegroup"
	"github.com/webkaz-labs/sobalink/internal/testfixture"
	"github.com/webkaz-labs/sobalink/internal/webui"
)

// SOURCE ONLY until an exact independent tagged-source and execution gate.
// The sole setup reopen is an owned in-process C.Close/Open before consent;
// it does not establish group recovery or separate-OS-process acceptance.
const resourceGroupCatalogNativeSelector = "^TestResourceGroupCatalogNativeFixedTwoTargetOneShot$"

type resourceGroupCatalogNative struct {
	t            *testing.T
	ctx          context.Context
	cancel       context.CancelFunc
	setup        context.Context
	stopSetup    context.CancelFunc
	setupUntil   time.Time
	expires      int64
	root         string
	cores        [3]*Core
	owners       [3]*resourceInspectionNativeOwner
	retired      *resourceInspectionNativeOwner
	identities   [3]directlan.Identity
	endpoints    [3]netip.AddrPort
	reservations [3]*testfixture.PortReservation
	released     [3]bool
	once         sync.Once
	trace        bool
}

func newResourceGroupCatalogNative(t *testing.T) *resourceGroupCatalogNative {
	t.Helper()
	// No temp state, reservation, goroutine or Core precedes this entire guard.
	if os.Getenv("SOBALINK_RUN_RESOURCE_GROUP_CATALOG_NATIVE") != "reviewed-three-core-loopback-v1" {
		t.Skip("requires separately reviewed three-Core group/catalog execution")
	}
	if os.Getenv("SOBALINK_RUN_ACTIVATION_NATIVE") != "1" || !directLANNetstackReady {
		t.Fatal("explicit native activation prerequisite is missing")
	}
	run := flag.Lookup("test.run")
	if run == nil || run.Value.String() != resourceGroupCatalogNativeSelector {
		t.Fatal("exact single native group/catalog selector is required")
	}
	deadline, ok := t.Deadline()
	if !ok || time.Until(deadline) > 4*time.Minute || time.Until(deadline) < 200*time.Second {
		t.Fatal("native group/catalog requires the reviewed four-minute test budget")
	}
	for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy", "TS_PROXY"} {
		if os.Getenv(name) != "" {
			t.Fatal("native group/catalog requires an isolated proxy-free environment")
		}
	}
	return newResourceGroupCatalogNativeAfterGuard(t)
}

// Called only after the separate exact N1 or N4 native guard has passed.
func newResourceGroupCatalogNativeAfterGuard(t *testing.T) *resourceGroupCatalogNative {
	t.Helper()
	entry := time.Now()
	ctx, cancel := context.WithDeadline(context.Background(), entry.Add(180*time.Second))
	setupUntil := entry.Add(40 * time.Second)
	setup, stopSetup := context.WithDeadline(ctx, setupUntil)
	f := &resourceGroupCatalogNative{t: t, ctx: ctx, cancel: cancel, setup: setup, stopSetup: stopSetup, setupUntil: setupUntil, expires: entry.Add(240 * time.Second).Unix()}
	t.Cleanup(f.close)
	var err error
	f.root, err = os.MkdirTemp("", "sobalink-group-catalog-native-")
	if err != nil {
		t.Fatal("synthetic fixture directory creation failed")
	}
	f.identities = [3]directlan.Identity{{Seed: strings.Repeat("61", 32)}, {Seed: strings.Repeat("62", 32)}, {Seed: strings.Repeat("63", 32)}}
	sort.Slice(f.identities[:], func(i, j int) bool { return f.identities[i].PublicKey() < f.identities[j].PublicKey() })
	for i := range f.cores {
		reservation, err := testfixture.ReserveLoopbackTCPUDP(netip.MustParseAddr("127.0.0.1"), 54543, 54544, 54545)
		if err != nil {
			t.Fatal("initial fixed endpoint reservation failed")
		}
		f.reservations[i], f.endpoints[i] = reservation, reservation.Endpoint()
		if f.endpoints[i].Addr() != netip.MustParseAddr("127.0.0.1") {
			t.Fatal("fixture left exact numeric loopback")
		}
	}
	for i, role := range []string{"controller", "target-a", "target-b"} {
		dir := filepath.Join(f.root, role)
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal("synthetic role directory creation failed")
		}
		f.open(i, dir)
		f.initialLegacySelection(i, role)
	}
	f.pair(1)
	firstControllerPair, firstTargetPair := f.pairRecord(0, 1), f.pairRecord(1, 0)
	f.reopenControllerOffline()
	f.pair(2)
	if !reflect.DeepEqual(firstControllerPair, f.pairRecord(0, 1)) || !reflect.DeepEqual(firstTargetPair, f.pairRecord(1, 0)) {
		t.Fatal("second real bilateral upgrade changed the original first pair")
	}
	if f.setup.Err() != nil {
		t.Fatal("three-Core bootstrap exceeded its unchanged setup deadline")
	}
	for i := range f.cores {
		f.assertOrdinary(i)
		f.assertFirstUse(i)
	}
	f.assertPair(1)
	f.assertPair(2)
	f.stopSetup()
	var aliases [3]resourceacceptance.Owner
	for i, c := range f.cores {
		backend := c.nodeCopy().(*directLANBackend)
		aliases[i] = resourceacceptance.Owner{Core: c, Node: backend.Node}
	}
	if !resourceacceptance.Start(aliases) {
		t.Fatal("bounded native observation registration failed")
	}
	f.trace = true // The already-registered cleanup clears every pointer alias.
	return f
}

func (f *resourceGroupCatalogNative) open(i int, dir string) {
	f.t.Helper()
	if f.setup.Err() != nil {
		f.t.Fatal("setup deadline ended before Core construction")
	}
	lock, err := config.AcquireLock(dir)
	if err != nil {
		f.t.Fatal("synthetic lifecycle ownership unavailable")
	}
	owner := &resourceInspectionNativeOwner{lock: lock, done: make(chan struct{})}
	f.owners[i], f.cores[i] = owner, nil
	c, err := Open(f.ctx, Options{Directory: dir, Version: "synthetic-group-catalog", LifecycleLock: lock, EnableResourceInspection: true, SkipNetworkStart: true,
		NodeFactory: func(string, string) (NetworkBackend, error) {
			return nil, errors.New("non-direct synthetic backend is forbidden")
		}})
	owner.core, f.cores[i] = c, c
	if err != nil || c == nil {
		f.t.Fatal("normal owned offline Core construction failed")
	}
}

// Initial raw peers contain no managed pair context or granted application
// authority. The real bilateral upgrade is the only context producer below.
func (f *resourceGroupCatalogNative) initialLegacySelection(i int, role string) {
	f.t.Helper()
	c := f.cores[i]
	indices := []int{0}
	if i == 0 {
		indices = []int{1, 2}
	}
	peers, records := []directlan.Peer{}, []endpointmeta.PeerRecord{}
	for _, j := range indices {
		peer := directlan.Peer{Key: f.identities[j].PublicKey(), TunnelKey: f.identities[j].TunnelKey(), Endpoint: f.endpoints[j], Name: "synthetic-peer"}
		peers = append(peers, peer)
		records = append(records, endpointmeta.PeerRecord{Peer: directLANPeerWire(peer), Revision: "1"})
	}
	scope := endpointmeta.Scope{Family: "ipv4", Prefixes: []string{"127.0.0.0/8"}}
	model := endpointmeta.Snapshot{Version: 4, Revision: "1", LocalPeer: endpointmeta.PeerWire{Key: f.identities[i].PublicKey(), TunnelKey: f.identities[i].TunnelKey(), Endpoint: f.endpoints[i].String()}, LocalScope: scope, PreviousLocalEndpoint: f.endpoints[i].String(), ObservedAt: time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano), Peers: records}
	state := directLANState{Version: 4, Identity: f.identities[i], Selection: DirectLANSelection{Listen: f.endpoints[i].String(), Prefixes: scope.Prefixes}, Peers: peers, Metadata: &model}
	if validateDirectLANState(state) != nil {
		f.t.Fatal("synthetic initial legacy selection is invalid")
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		f.t.Fatal("synthetic initial state encoding failed")
	}
	c.op.Lock()
	defer c.op.Unlock()
	err = f.owners[i].lock.WithOwnership(c.dir, func() error {
		return config.AtomicWritePrivate(filepath.Join(c.dir, "direct-lan.json"), append(data, '\n'))
	})
	if err != nil {
		f.t.Fatal("owned initial state publication failed")
	}
	limits := selectedLANLimits(c.capacityPolicy())
	store, err := readDirectLANStore(filepath.Join(c.dir, "direct-lan.json"), limits.bytes, limits.peers)
	if err != nil || store == nil || store.limits.Load() == nil || *store.limits.Load() != *limits {
		f.t.Fatal("normal policy-aligned initial state load failed")
	}
	p := c.profileCopy()
	p.Settings.Network, p.Settings.Hostname = "direct-lan", "synthetic-"+role
	if i == 0 {
		p.Services = []ServiceSpec{{ID: "synthetic-service", Name: "Synthetic service", Direction: "forward", Network: "tcp", Ports: "8080", Lifetime: "until-stopped", LoopbackHost: "127.0.0.1", PeerID: f.identities[1].PublicKey()}}
	}
	if err := f.owners[i].lock.WithOwnership(c.dir, func() error { return c.saveProfile(p) }); err != nil {
		f.t.Fatal("owned synthetic profile publication failed")
	}
	c.mu.Lock()
	offline := c.node == nil && c.contextControl == nil && c.contextUpgrade == nil && c.attemptedNetwork == ""
	if offline {
		c.directLAN, c.profile = store, p
	}
	c.mu.Unlock()
	if !offline {
		f.t.Fatal("initial inert fixture configuration was no longer offline")
	}
}

func (f *resourceGroupCatalogNative) call(ctx context.Context, i int, name string, input any) (any, error) {
	f.t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		f.t.Fatal("synthetic command encoding failed")
	}
	return f.cores[i].Command(ctx, webui.Command{RequestID: "synthetic-group-catalog", Name: name, Payload: raw})
}

func (f *resourceGroupCatalogNative) local(i int, name string, input any) any {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(f.ctx, 8*time.Second)
	defer cancel()
	value, err := f.call(ctx, i, name, input)
	if err != nil {
		f.t.Fatalf("synthetic local command failed: code=%s", networkErrorCode(err))
	}
	return value
}

func (f *resourceGroupCatalogNative) pair(target int) {
	f.t.Helper()
	roles := [2]int{0, target}
	var inputs [2]UpgradeIntent
	for side, role := range roles {
		input := UpgradeIntent{PeerID: f.identities[roles[1-side]].PublicKey(), Deadline: f.setupUntil.UTC().Format(time.RFC3339Nano)}
		value, err := f.call(f.setup, role, "direct-lan.upgrade.review", input)
		review, ok := value.(UpgradeReview)
		if err != nil || !ok || review.RestartRequired || review.ResumePreparation || review.PeerID != input.PeerID || review.Deadline != input.Deadline {
			f.t.Fatal("fresh exact bilateral upgrade review unavailable")
		}
		input.ExpectedRevision = review.Revision
		inputs[side] = input
	}
	var jobs [2]*contextUpgradeJob
	for side, role := range roles {
		if !f.released[role] {
			if err := f.reservations[role].Close(); err != nil {
				f.t.Fatal("initial fixed endpoint release failed")
			}
			f.released[role] = true
		}
		value, err := f.call(f.setup, role, "direct-lan.upgrade.run", inputs[side])
		progress, ok := value.(UpgradeProgress)
		if err != nil || !ok || progress.RestartRequired {
			f.t.Fatal("single exact bilateral upgrade run was rejected")
		}
		c := f.cores[role]
		c.mu.RLock()
		jobs[side] = c.contextUpgrade
		c.mu.RUnlock()
		if jobs[side] == nil {
			f.t.Fatal("production upgrade did not own a job")
		}
	}
	for side, job := range jobs {
		select {
		case <-job.done:
		case <-f.setup.Done():
			f.t.Fatal("bilateral upgrade did not join inside unchanged setup budget")
		}
		c := f.cores[roles[side]]
		c.mu.RLock()
		phase := job.view.State
		c.mu.RUnlock()
		if phase != "network-started" {
			f.t.Fatal("bilateral upgrade did not reach ordinary managed ownership")
		}
		f.assertOrdinary(roles[side])
	}
	f.assertPair(target)
}

func (f *resourceGroupCatalogNative) assertFirstUse(i int) {
	f.t.Helper()
	c := f.cores[i]
	c.op.Lock()
	valid := c.resourceLock == f.owners[i].lock && c.resourceGrants != nil && !c.resourceGrants.frozen && c.resourceGrants.firstUse && c.resourceGroups != nil && c.resourceGroups.store != nil && c.resourceGroups.store.firstUse && c.resourceGroups.prepared == nil && c.resourceGroups.active == nil && len(c.resourceGroups.store.state.Runs) == 0
	c.op.Unlock()
	if !valid {
		f.t.Fatal("setup created consent, prepared group or unowned resource state")
	}
	for _, path := range []string{resourceGrantStatePath(c.dir), resourceGroupStatePath(c.dir)} {
		if _, err := os.Lstat(filepath.Dir(path)); !errors.Is(err, os.ErrNotExist) {
			f.t.Fatal("setup created a grant or group sidecar")
		}
	}
}

func (f *resourceGroupCatalogNative) assertOrdinary(i int) {
	f.t.Helper()
	c := f.cores[i]
	c.op.Lock()
	defer c.op.Unlock()
	backend, ok := c.nodeCopy().(*directLANBackend)
	valid := ok && backend != nil && backend.Node != nil && backend.Node.Endpoint() == f.endpoints[i] && backend.ctx.Err() == nil && c.ctx.Err() == nil
	if valid {
		owner := backend.currentCompletion()
		valid = owner != nil && owner.activationCurrent() && owner.coreCurrent("")
	}
	c.mu.RLock()
	valid = valid && c.contextControl == nil && c.resourceLock == f.owners[i].lock
	c.mu.RUnlock()
	if !valid {
		f.t.Fatal("current ordinary managed ownership was not certified")
	}
}

func (f *resourceGroupCatalogNative) pairRecord(i, peer int) endpointmeta.PeerRecord {
	f.t.Helper()
	state := f.cores[i].directLAN.copy()
	want := 1
	if i == 0 {
		want = 2
	}
	if state.Identity != f.identities[i] || state.Selection.Listen != f.endpoints[i].String() || state.Metadata == nil || len(state.Peers) != want || len(state.Metadata.Peers) != want {
		f.t.Fatal("synthetic star identity, endpoint or peer count changed")
	}
	for _, record := range state.Metadata.Peers {
		if record.Peer.Key == f.identities[peer].PublicKey() {
			if record.Peer.Endpoint != f.endpoints[peer].String() || record.PairContext == nil || !record.ContextConfirmed || record.UpgradePending != nil || record.EndpointState == nil {
				f.t.Fatal("actual bilateral pair lacks confirmed initial state")
			}
			initial, err := endpointmeta.InitialState(*record.PairContext, state.Identity.PublicKey())
			if err != nil || !reflect.DeepEqual(initial, *record.EndpointState) {
				f.t.Fatal("bilateral pair was not its actual initial endpoint state")
			}
			return record
		}
	}
	f.t.Fatal("exact synthetic star peer is missing")
	return endpointmeta.PeerRecord{}
}

func (f *resourceGroupCatalogNative) assertPair(target int) {
	f.t.Helper()
	a, b := f.pairRecord(0, target), f.pairRecord(target, 0)
	if !reflect.DeepEqual(a.PairContext, b.PairContext) {
		f.t.Fatal("real bilateral contexts do not match")
	}
}

func (f *resourceGroupCatalogNative) reopenControllerOffline() {
	f.t.Helper()
	for i := range f.cores {
		f.assertFirstUse(i)
	}
	c, old := f.cores[0], f.owners[0]
	profile, state := c.profileCopy(), c.directLAN.copy()
	resourceID, nonce := c.resourceIdentity, c.resourceNonce
	path := filepath.Join(c.dir, "direct-lan.json")
	before, err := os.ReadFile(path)
	if err != nil {
		f.t.Fatal("first-pair saved bytes unavailable")
	}
	old.startClose()
	wait, cancel := context.WithTimeout(f.setup, 10*time.Second)
	defer cancel()
	select {
	case <-old.done:
		if old.err != nil {
			f.t.Fatal("setup controller close or lifecycle release failed")
		}
	case <-wait.Done():
		f.t.Fatal("setup controller close did not join; no reopen attempted")
	}
	f.retired = old
	f.open(0, c.dir)
	fresh := f.cores[0]
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) || fresh.resourceIdentity != resourceID || fresh.resourceNonce == nonce || fresh.nodeCopy() != nil || !reflect.DeepEqual(profile, fresh.profileCopy()) || !reflect.DeepEqual(state, fresh.directLAN.copy()) {
		f.t.Fatal("normal setup reopen changed first-pair bytes or persistent identity")
	}
	f.assertFirstUse(0)
	f.assertPair(1)
}

func (f *resourceGroupCatalogNative) close() {
	f.once.Do(func() {
		f.stopSetup()
		f.cancel()
		if f.trace {
			resourceacceptance.Stop()
			f.trace = false
		}
		wait, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		joined := true
		for _, owner := range f.owners {
			if owner != nil {
				owner.startClose()
			}
		}
		all := [4]*resourceInspectionNativeOwner{f.owners[0], f.owners[1], f.owners[2], f.retired}
		for _, owner := range all {
			if owner == nil {
				continue
			}
			select {
			case <-owner.done:
				if owner.err != nil {
					joined = false
					f.t.Error("native owned Core/lock cleanup failed")
				}
			case <-wait.Done():
				joined = false
				f.t.Error("native cleanup did not join; lifecycle lock remains owned until Close returns")
			}
		}
		for _, reservation := range f.reservations {
			if reservation != nil && reservation.Close() != nil {
				joined = false
				f.t.Error("native reservation cleanup failed")
			}
		}
		if !joined || f.t.Failed() {
			f.t.Log("retained synthetic group/catalog fixture after failure or unjoined cleanup")
			return
		}
		if f.root != "" && os.RemoveAll(f.root) != nil {
			f.t.Error("joined synthetic fixture removal failed")
		}
	})
}

func (f *resourceGroupCatalogNative) descriptor(i int) resource.Descriptor {
	f.t.Helper()
	value := f.local(i, "resource.list", struct{}{})
	catalog, ok := value.(resource.Catalog)
	if !ok || catalog.SchemaVersion != resource.SchemaVersion || len(catalog.Resources) != 1 {
		f.t.Fatal("exact local settings resource unavailable")
	}
	return catalog.Resources[0]
}

func (f *resourceGroupCatalogNative) confirm(i int) resourcegrant.Record {
	f.t.Helper()
	input := resourcegrant.ManagementGrantInputs{Scope: resourcegrant.Management, Inputs: resourcegrant.GrantInputs{Target: f.descriptor(i).Target, PeerKey: f.identities[0].PublicKey(), ExpiresAt: f.expires,
		Actions: []string{resourcegrant.Inspect, resourcegrant.PreviewAction, resourcegrant.ApplyAction, resourcegrant.StatusAction}, Fields: []string{resourcegrant.FilesField, resourcegrant.PerPeerField}}}
	value := f.local(i, "resource.grant.management.preview", input)
	review, ok := value.(resourcegrant.ManagementGrantReview)
	if !ok || review.Grant.Validate() != nil || !review.InitializesState || review.Grant.Record.Target != input.Inputs.Target || review.Grant.Record.Relationship.Backend != resourcegrant.Backend || review.Grant.Record.Relationship.TargetKey != f.identities[i].PublicKey() || review.Grant.Record.Relationship.PeerKey != input.Inputs.PeerKey || review.Grant.Record.ExpiresAt != f.expires || !reflect.DeepEqual(review.Grant.Record.Actions, input.Inputs.Actions) || !reflect.DeepEqual(review.Grant.Record.Fields, input.Inputs.Fields) {
		f.t.Fatal("grant preview changed the explicit finite per-peer scope")
	}
	value = f.local(i, "resource.grant.management.confirm", resourcegrant.ManagementGrantConfirmation{Review: review, Confirm: true})
	view, ok := value.(resourcegrant.LocalGrantView)
	if !ok || view.InitializesState || len(view.Records) != 0 || len(view.ManagementRecords) != 1 || !reflect.DeepEqual(view.ManagementRecords[0], review.Grant) {
		f.t.Fatal("grant confirmation did not retain exactly its reviewed record")
	}
	ctx, cancel := context.WithTimeout(f.ctx, 8*time.Second)
	defer cancel()
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	for {
		// One shared local readiness deadline, never a remote warm-up.
		value, err := f.call(ctx, i, "resource.grant.inspect", map[string]any{"target": input.Inputs.Target})
		view, ok = value.(resourcegrant.LocalGrantView)
		if err != nil || !ok || view.TimeUncertain || len(view.ManagementRecords) != 1 || !reflect.DeepEqual(view.ManagementRecords[0], review.Grant) {
			f.t.Fatal("listener reconciliation changed the original grant")
		}
		if view.ListenerReady && view.Activation == "listening" {
			break
		}
		select {
		case <-ctx.Done():
			f.t.Fatal("management listener did not become ready")
		case <-tick.C:
		}
	}
	f.assertGrant(i, review.Grant.Record)
	return review.Grant.Record
}

func (f *resourceGroupCatalogNative) assertGrant(i int, want resourcegrant.Record) {
	f.t.Helper()
	data, err := os.ReadFile(resourceGrantStatePath(f.cores[i].dir))
	if err != nil {
		f.t.Fatal("saved management grant unavailable")
	}
	state, err := resourcegrant.Decode(data)
	if err != nil || len(state.Records) != 0 || len(state.ManagementRecords) != 1 || !reflect.DeepEqual(state.ManagementRecords[0].Record, want) || want.ExpiresAt != f.expires {
		f.t.Fatal("saved management grant changed identity, scope or original expiry")
	}
}

type resourceGroupCatalogFile struct {
	present bool
	data    []byte
}

func (f *resourceGroupCatalogNative) files(i int) [3]resourceGroupCatalogFile {
	f.t.Helper()
	var result [3]resourceGroupCatalogFile
	dir := f.cores[i].dir
	for index, path := range []string{filepath.Join(dir, capacityPolicyFile), resourceStatePath(dir), resourceGroupStatePath(dir)} {
		data, err := os.ReadFile(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			f.t.Fatal("synthetic settings/evidence read failed")
		}
		result[index] = resourceGroupCatalogFile{present: err == nil, data: data}
	}
	return result
}

func (f *resourceGroupCatalogNative) assertFiles(i int, want [3]resourceGroupCatalogFile) {
	f.t.Helper()
	if !reflect.DeepEqual(f.files(i), want) {
		f.t.Fatal("read, rejected apply or historical repeat changed settings/evidence")
	}
}

func (f *resourceGroupCatalogNative) group(name string, input any) any {
	f.t.Helper()
	// The production group handler retains its count-times-15-second budget.
	ctx, cancel := context.WithTimeout(f.ctx, 30*time.Second)
	defer cancel()
	value, err := f.call(ctx, 0, name, input)
	if err != nil {
		f.t.Fatalf("single group request failed: code=%s", networkErrorCode(err))
	}
	return value
}

func (f *resourceGroupCatalogNative) denyApply(input resourcegroup.ApplyInput) {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(f.ctx, 8*time.Second)
	defer cancel()
	value, err := f.call(ctx, 0, resourcegroup.LocalApplyCommand, input)
	if err == nil || value != nil {
		f.t.Fatal("inexact or changed local apply was accepted")
	}
}

func resourceGroupCatalogReplyJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil || len(data) > resourcegroup.MaxLocalResponseBytes {
		t.Fatal("bounded group reply encoding failed")
	}
	for _, forbidden := range []string{"\"origins\"", "\"relationship\"", "\"pairBinding\"", "\"managedGeneration\"", "\"issuanceNonce\"", "\"controllerResourceId\"", "synthetic-private"} {
		if bytes.Contains(data, []byte(forbidden)) {
			t.Fatal("group reply exposed private authority metadata")
		}
	}
	return data
}

func (f *resourceGroupCatalogNative) assertApplied(i int, requested resource.Settings, before capacity.Policy, row resourcegroup.MemberEvidence) {
	f.t.Helper()
	if row.Execution != resourcegroup.ExecutionSelected || row.Dispatch != resourcegroup.DispatchObserved || row.Request == nil || row.Request.Apply == nil || row.Target == nil || row.Target.EvidenceDurable == nil || row.Target.OperationID != row.Request.Apply.OperationID || row.LocalDurability != resourcegroup.LocalDurable || row.Status.State != resourcegroup.StatusNotQueried || row.AdmissionStop != resourcegroup.StopNone {
		f.t.Fatal("group row lost original operation or independent durability")
	}
	resourceManagementNativeApplied(f.t, row.Target.Outcome, *row.Target.EvidenceDurable)
	want := before.Clone()
	want.Resources[resourcegrant.FilesField], want.Resources[resourcegrant.PerPeerField] = requested.TransferConcurrentFiles, requested.TransferConcurrentPerPeer
	view := f.descriptor(i)
	if !reflect.DeepEqual(view.Requested, requested) || !capacityJSONEqual(f.cores[i].capacityPolicy(), want) {
		f.t.Fatal("real provider changed a different settings projection")
	}
	files := f.files(i)
	var saved capacity.Policy
	if !files[0].present || json.Unmarshal(files[0].data, &saved) != nil || !capacityJSONEqual(saved, want) {
		f.t.Fatal("durable settings differ from the real provider projection")
	}
	journal, err := readResourceEnvelope(resourceStatePath(f.cores[i].dir))
	if err != nil || journal.scoped == nil || *journal.HighWater != 1 || len(journal.scoped.Records) != 1 {
		f.t.Fatal("target did not retain exactly one scoped provider operation")
	}
	record := journal.scoped.Records[0].Remote
	if record == nil || record.Phase != "result" || !reflect.DeepEqual(record.Request, *row.Request.Apply) || record.Outcome != row.Target.Outcome {
		f.t.Fatal("target journal does not match its exact original request/result")
	}
	f.assertOrdinary(i)
}

func (f *resourceGroupCatalogNative) assertCatalog() {
	f.t.Helper()
	before := f.files(0)
	trace := resourceacceptance.Snapshot()
	view := f.descriptor(0)
	input := resourceCatalogRequest{SchemaVersion: 1, Sources: []resourceCatalogSource{{Kind: resourcecatalog.LocalSettings, Target: view.Target}, {Kind: resourcecatalog.LocalService}, {Kind: resourcecatalog.TransferActivity}}}
	value := f.local(0, "resource.catalog.snapshot", input)
	response, ok := value.(resourceCatalogResponse)
	raw, err := json.Marshal(response)
	decoded, decodeErr := DecodeResourceCatalogResponse(raw)
	if !ok || err != nil || decodeErr != nil || !reflect.DeepEqual(decoded, response) || !response.Snapshot.Complete || len(response.Snapshot.Sources) != 3 {
		f.t.Fatal("real local catalog did not return its complete typed envelope")
	}
	seen := map[string]bool{}
	for _, source := range response.Snapshot.Sources {
		if seen[source.Selection.Kind] || source.State != "current" || !source.Complete || source.Total == nil || *source.Total != int64(len(source.Rows)) {
			f.t.Fatal("catalog lost an explicitly declared local source")
		}
		seen[source.Selection.Kind] = true
		switch source.Selection.Kind {
		case resourcecatalog.LocalSettings:
			if len(source.Rows) != 1 || source.Rows[0].LocalSettings == nil || !reflect.DeepEqual(*source.Rows[0].LocalSettings, view) {
				f.t.Fatal("catalog local settings descriptor differs")
			}
		case resourcecatalog.LocalService:
			if len(source.Rows) != 1 || source.Rows[0].Identity.ID != "synthetic-service" || source.Rows[0].LocalService == nil || source.Rows[0].LocalService.State != "saved" || source.Rows[0].LocalService.Application != "unverified" || source.Rows[0].LocalService.Lifetime != "until-stopped" {
				f.t.Fatal("catalog did not preserve the inactive saved service")
			}
		case resourcecatalog.TransferActivity:
			if len(source.Rows) != 0 || source.Selection.ProcessID != f.cores[0].resourceCatalogProcessID() {
				f.t.Fatal("catalog real empty transfer manager lost its current process identity")
			}
		default:
			f.t.Fatal("catalog silently expanded its exact local source selection")
		}
	}
	f.assertFiles(0, before)
	if resourceacceptance.Snapshot() != trace {
		f.t.Fatal("local catalog issued a management exchange or provider operation")
	}
	f.cores[0].mu.RLock()
	active, outgoing, trust := len(f.cores[0].active), len(f.cores[0].outgoing), len(f.cores[0].profile.Peers)
	f.cores[0].mu.RUnlock()
	if active != 0 || outgoing != 0 || trust != 0 {
		f.t.Fatal("catalog created an implicit service, transfer or trust action")
	}
}

func (f *resourceGroupCatalogNative) assertEvents(run resourcegroup.RunView) resourceacceptance.View {
	f.t.Helper()
	view := resourceacceptance.Snapshot()
	if view.InvalidOrOverflow || view.Count != 17 {
		f.t.Fatal("native observation was incomplete, overflowing or contained extra actions")
	}
	var preview [3]int
	accepted := uint16(0)
	var ordered [2][5]uint16 // intent, invocation, frame attempt, frame written, provider
	for index, event := range view.Events[:view.Count] {
		if event.Sequence != uint16(index+1) {
			f.t.Fatal("native event ordering is not monotonic")
		}
		if event.Kind == resourceacceptance.GroupAccepted {
			if event.Role != resourceacceptance.Controller || event.RunID != run.RunID || accepted != 0 {
				f.t.Fatal("controller acceptance event mismatched the retained run")
			}
			accepted = event.Sequence
			continue
		}
		if event.Action == resourceacceptance.Preview {
			if event.Role != resourceacceptance.Controller || event.OperationID != "" {
				f.t.Fatal("preview event escaped its actual controller")
			}
			switch event.Kind {
			case resourceacceptance.ManagementClientInvoked:
				preview[0]++
			case resourceacceptance.ManagementFrameAttempted:
				preview[1]++
			case resourceacceptance.ManagementFrameWritten:
				preview[2]++
			default:
				f.t.Fatal("preview produced a mutation event")
			}
			continue
		}
		member := -1
		for i, row := range run.Evidence.Members {
			if event.OperationID == row.Request.Apply.OperationID {
				member = i
			}
		}
		if member < 0 || event.Action != resourceacceptance.Apply {
			f.t.Fatal("native event does not match an original target operation")
		}
		slot := -1
		switch event.Kind {
		case resourceacceptance.GroupIntent:
			slot = 0
			if event.RunID != run.RunID {
				f.t.Fatal("dispatch intent lost its accepted run identity")
			}
		case resourceacceptance.ManagementClientInvoked:
			slot = 1
		case resourceacceptance.ManagementFrameAttempted:
			slot = 2
		case resourceacceptance.ManagementFrameWritten:
			slot = 3
		case resourceacceptance.ProviderAdmitted:
			slot = 4
		}
		wantRole := resourceacceptance.Controller
		if slot == 4 {
			wantRole = resourceacceptance.Role(member + 2)
		}
		if slot < 0 || event.Role != wantRole || ordered[member][slot] != 0 {
			f.t.Fatal("extra, cross-peer or substituted native operation event")
		}
		ordered[member][slot] = event.Sequence
	}
	if preview != [3]int{2, 2, 2} || accepted == 0 {
		f.t.Fatal("fixed two-target preview or acceptance count differed")
	}
	for _, sequence := range ordered {
		if !(accepted < sequence[0] && sequence[0] < sequence[1] && sequence[1] < sequence[2] && sequence[2] < sequence[3] && sequence[2] < sequence[4]) {
			f.t.Fatal("durable controller acceptance/intent did not precede actual dispatch")
		}
	}
	// Sender write completion and target admission are intentionally unordered.
	if ordered[0][3] >= ordered[1][0] || ordered[0][4] >= ordered[1][0] {
		f.t.Fatal("group did not preserve serial canonical target admission")
	}
	return view
}

func TestResourceGroupCatalogNativeFixedTwoTargetOneShot(t *testing.T) {
	f := newResourceGroupCatalogNative(t)
	grants := [2]resourcegrant.Record{f.confirm(1), f.confirm(2)}
	f.assertCatalog()
	before := [3][3]resourceGroupCatalogFile{f.files(0), f.files(1), f.files(2)}
	policies := [2]capacity.Policy{f.cores[1].capacityPolicy(), f.cores[2].capacityPolicy()}
	wanted := [2]resource.Settings{resourceManagementNativeSettings(3, 1), resourceManagementNativeSettings(4, 2)}
	template, err := resourcegroup.NewTemplate(wanted[0])
	if err != nil {
		t.Fatal("synthetic fixed template invalid")
	}
	selection := resourcegroup.Selection{SchemaVersion: 1, Template: template, Members: []resourcegroup.Member{}}
	for i, grant := range grants {
		member := resourcegroup.Member{PeerKey: f.identities[i+1].PublicKey(), Selector: managementSelector(grant)}
		if i == 1 {
			member.Override = &resourcegroup.Override{TransferConcurrentFiles: &wanted[i].TransferConcurrentFiles, TransferConcurrentPerPeer: &wanted[i].TransferConcurrentPerPeer}
		}
		selection.Members = append(selection.Members, member)
	}
	value := f.group(resourcegroup.LocalPreviewCommand, resourcegroup.PreviewInput{SchemaVersion: 1, Selection: selection})
	prepared, ok := value.(resourcegroup.PreparedView)
	decoded, err := resourcegroup.DecodePreparedView(resourceGroupCatalogReplyJSON(t, value))
	if !ok || err != nil || !reflect.DeepEqual(decoded, prepared) || prepared.AdmissionState != resourcegroup.AdmissionPrepared || prepared.InitializesLocalEvidence == nil || !*prepared.InitializesLocalEvidence || len(prepared.Review.Rows) != 2 || len(prepared.Review.ExecutionPeers) != 2 {
		t.Fatal("fixed two-target prepared reply was not exact and complete")
	}
	for i, row := range prepared.Review.Rows {
		if row.State != resourcegroup.ReviewReady || row.PeerKey != selection.Members[i].PeerKey || prepared.Review.ExecutionPeers[i] != row.PeerKey || row.Reply == nil || row.Reply.Preview == nil || row.Reply.ManagementSelector != selection.Members[i].Selector || !reflect.DeepEqual(row.Reply.Preview.Requested, wanted[i]) {
			t.Fatal("ready row did not preserve its exact target, grant and override")
		}
	}
	for i := range f.cores {
		f.assertFiles(i, before[i])
	}
	apply := resourcegroup.ApplyInput{SchemaVersion: 1, ReviewID: prepared.ReviewID, ReviewRevision: prepared.Review.Revision, ExecutionPeers: append([]string{}, prepared.Review.ExecutionPeers...), Confirm: true}
	traceBeforeRejects := resourceacceptance.Snapshot()
	bad := [3]resourcegroup.ApplyInput{apply, apply, apply}
	bad[0].Confirm = false
	revision := []byte(apply.ReviewRevision)
	if revision[0] == '0' {
		revision[0] = '1'
	} else {
		revision[0] = '0'
	}
	bad[1].ReviewRevision = string(revision)
	bad[2].ExecutionPeers = append([]string{}, apply.ExecutionPeers[:1]...)
	for _, input := range bad {
		f.denyApply(input)
	}
	if resourceacceptance.Snapshot() != traceBeforeRejects {
		t.Fatal("rejected apply contacted a target or provider")
	}
	for i := range f.cores {
		f.assertFiles(i, before[i])
	}
	value = f.group(resourcegroup.LocalApplyCommand, apply)
	run, ok := value.(resourcegroup.RunView)
	parsed, err := resourcegroup.DecodeRunView(resourceGroupCatalogReplyJSON(t, value))
	if !ok || err != nil || !reflect.DeepEqual(run, parsed) || run.RunID != prepared.ReviewID || !reflect.DeepEqual(run.Review, prepared.Review) || run.Activity != resourcegroup.ActivityIdle || run.LocalDurability != resourcegroup.LocalDurable || !run.Summary.AllApplied || run.Summary.Applied != 2 || run.Summary.Executable != 2 || len(run.Evidence.Members) != 2 {
		t.Fatal("real two-target run did not retain its full exact successful evidence")
	}
	for i, row := range run.Evidence.Members {
		f.assertApplied(i+1, wanted[i], policies[i], row)
		f.assertGrant(i+1, grants[i])
	}
	controller := f.files(0)
	if !reflect.DeepEqual(controller[0], before[0][0]) || !reflect.DeepEqual(controller[1], before[0][1]) || !controller[2].present {
		t.Fatal("controller settings changed or its group history was not saved")
	}
	saved, err := decodeResourceGroupEnvelope(controller[2].data)
	if err != nil || len(saved.Runs) != 1 || *saved.HighWater != 1 || saved.Runs[0].RunID != run.RunID || saved.Runs[0].Admission != resourceGroupAdmissionFinished || !reflect.DeepEqual(saved.Runs[0].Review, run.Review) || !reflect.DeepEqual(saved.Runs[0].Evidence, run.Evidence) {
		t.Fatal("durable controller history differs from the real returned run")
	}
	trace := f.assertEvents(run)
	after := [3][3]resourceGroupCatalogFile{f.files(0), f.files(1), f.files(2)}
	repeated := f.group(resourcegroup.LocalApplyCommand, apply)
	status := f.local(0, resourcegroup.LocalStatusCommand, resourcegroup.StatusInput{SchemaVersion: 1, RunID: run.RunID})
	if !reflect.DeepEqual(repeated, run) || !reflect.DeepEqual(status, run) || resourceacceptance.Snapshot() != trace {
		t.Fatal("exact repeat/local status changed history or performed another operation")
	}
	changed := apply
	changed.ExecutionPeers = append([]string{}, apply.ExecutionPeers[:1]...)
	f.denyApply(changed)
	for i := range f.cores {
		f.assertFiles(i, after[i])
	}
	f.assertCatalog()
	if resourceacceptance.Snapshot() != trace {
		t.Fatal("historical or local-only operation generated an extra native event")
	}
	t.Log("three-Core fixed-two-target provider path and local catalog baseline observed; process/browser/restart acceptance not covered")
}
