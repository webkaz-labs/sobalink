//go:build resource_management_native && resource_inspection_native && directlan_activation_native

package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/operationjournal"
	"github.com/webkaz-labs/sobalink/internal/resource"
	"github.com/webkaz-labs/sobalink/internal/resourcegrant"
	"github.com/webkaz-labs/sobalink/internal/webui"
)

// These three bounded scenarios require a separate source/execution review.
// Their existence is not execution evidence or full security/device acceptance.
// The inspection tag supplies the retained-cleanup fixture, not permission to
// run management. No inspection opt-in is read or written by this entry.
const resourceManagementNativeSelector = "^TestResourceManagementNative(PreviewApplyReplayAndMigration|RevokeReplacementAndProtocolSeparation|RestartPreservesOriginalExpiryAndHistory)$"
const resourceManagementNativeLifetime = 2 * time.Minute // Exceeds the fixture's unchanged 90-second aggregate context; never renewed.

type resourceManagementNativePair struct{ *resourceInspectionNativePair }

func newResourceManagementNativePair(t *testing.T) *resourceManagementNativePair {
	t.Helper()
	// Every guard precedes temporary directories, endpoint reservation and Core.
	if os.Getenv("SOBALINK_RUN_RESOURCE_MANAGEMENT_NATIVE") != "reviewed-production-loopback-v2" {
		t.Skip("requires separately reviewed native management execution")
	}
	if os.Getenv("SOBALINK_RUN_ACTIVATION_NATIVE") != "1" {
		t.Fatal("explicit native activation prerequisite is missing")
	}
	run := flag.Lookup("test.run")
	if run == nil || run.Value.String() != resourceManagementNativeSelector && run.Value.String() != "^"+t.Name()+"$" {
		t.Fatal("exact native management selector is required")
	}
	if deadline, ok := t.Deadline(); !ok || time.Until(deadline) > 8*time.Minute {
		t.Fatal("a nonzero test-run timeout of at most eight minutes is required")
	}
	for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy", "TS_PROXY"} {
		if os.Getenv(name) != "" {
			t.Fatal("native fixture requires an isolated environment without proxy overrides")
		}
	}
	f := &resourceManagementNativePair{newResourceNativeOwnedPairWithPolicy(t, true)}
	f.assertCurrentManagedOwner(0, managementOwnerBaseline)
	f.assertCurrentManagedOwner(1, managementOwnerBaseline)
	return f
}

func (f *resourceManagementNativePair) commandOnce(ctx context.Context, i int, name string, input any) (any, error) {
	f.t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		f.t.Fatal("synthetic management command encoding failed")
	}
	return f.cores[i].Command(ctx, webui.Command{RequestID: "synthetic-resource-management", Name: name, Payload: raw})
}

func resourceManagementNativeInput(record resourcegrant.Record, action string) resourcegrant.RemoteManagementInput {
	return resourcegrant.RemoteManagementInput{PeerKey: record.Relationship.TargetKey,
		Request: resourcegrant.ManagementRequest{ManagementSelector: managementSelector(record), Action: action},
		Confirm: action == resourcegrant.ApplyAction}
}

func resourceManagementNativeCommand(action string) string {
	if action == resourcegrant.StatusAction {
		return "resource.remote.management.operation.status"
	}
	return "resource.remote.management." + action
}

func (f *resourceManagementNativePair) remoteOnce(input resourcegrant.RemoteManagementInput) (any, error) {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(f.ctx, 20*time.Second)
	defer cancel()
	// The real client still owns its fixed 15-second budget and one request.
	return f.commandOnce(ctx, 0, resourceManagementNativeCommand(input.Request.Action), input)
}

func (f *resourceManagementNativePair) checkedReply(value any, err error, input resourcegrant.RemoteManagementInput) resourcegrant.ManagementReply {
	f.t.Helper()
	if err != nil {
		f.t.Fatalf("management request failed: code=%s", networkErrorCode(err))
	}
	reply, ok := value.(resourcegrant.ManagementReply)
	if !ok || reply.Validate() != nil || reply.ManagementSelector != input.Request.ManagementSelector || reply.Action != input.Request.Action {
		f.t.Fatal("management reply did not retain its exact selector and action")
	}
	// Check the complete serialized field allowlist, including nested arms.
	data, err := json.Marshal(reply)
	if err != nil {
		f.t.Fatal("management reply encoding failed")
	}
	fields := resourceManagementNativeFields(f.t, data, "protocolVersion", "target", "grantId", "grantRevision", "action", resourceManagementNativeReplyArm(reply))
	resourceManagementNativeFields(f.t, fields["target"], "schemaVersion", "resourceId")
	switch {
	case reply.Inspection != nil:
		projection := resourceManagementNativeFields(f.t, fields["inspection"], "requested", "effective")
		resourceManagementNativeProjection(f.t, projection)
	case reply.Preview != nil:
		projection := resourceManagementNativeFields(f.t, fields["preview"], "operationId", "baseRevision", "reviewRevision", "requested", "effective")
		resourceManagementNativeProjection(f.t, projection)
		if !resource.ValidDigest(reply.Preview.OperationID) || strings.Contains(reply.Preview.OperationID, ":") {
			f.t.Fatal("remote operation identifier was not opaque")
		}
	case reply.Operation != nil:
		operation := resourceManagementNativeFields(f.t, fields["operation"], "operationId", "outcome", "evidenceDurable")
		resourceManagementNativeFields(f.t, operation["outcome"], "status", "configuration", "accounting", "transfer")
	case reply.Unavailable != nil:
		resourceManagementNativeFields(f.t, fields["unavailable"], "operationId")
	}
	f.cores[0].requestMu.Lock()
	cached := len(f.cores[0].requests)
	f.cores[0].requestMu.Unlock()
	if cached != 0 {
		f.t.Fatal("management response was retained in local request history")
	}
	return reply
}

func resourceManagementNativeReplyArm(reply resourcegrant.ManagementReply) string {
	switch {
	case reply.Inspection != nil:
		return "inspection"
	case reply.Preview != nil:
		return "preview"
	case reply.Operation != nil:
		return "operation"
	default:
		return "unavailable"
	}
}

func resourceManagementNativeFields(t *testing.T, data []byte, names ...string) map[string]json.RawMessage {
	t.Helper()
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil || len(fields) != len(names) {
		t.Fatal("remote reply exposed an unexpected field shape")
	}
	for _, name := range names {
		if fields[name] == nil {
			t.Fatal("remote reply omitted an allowlisted field")
		}
	}
	return fields
}

func resourceManagementNativeProjection(t *testing.T, fields map[string]json.RawMessage) {
	t.Helper()
	resourceManagementNativeFields(t, fields["requested"], resourcegrant.FilesField, resourcegrant.PerPeerField)
	resourceManagementNativeFields(t, fields["effective"], resourcegrant.FilesField, resourcegrant.PerPeerField)
}

func (f *resourceManagementNativePair) mustRemote(input resourcegrant.RemoteManagementInput) resourcegrant.ManagementReply {
	f.t.Helper()
	value, err := f.remoteOnce(input)
	return f.checkedReply(value, err, input)
}

func (f *resourceManagementNativePair) confirmManagement() resourcegrant.Record {
	f.t.Helper()
	before := f.view(1)
	input := resourcegrant.ManagementGrantInputs{Scope: resourcegrant.Management, Inputs: resourcegrant.GrantInputs{
		Target: f.descriptor(1).Target, PeerKey: f.cores[0].directLAN.copy().Identity.PublicKey(),
		ExpiresAt: time.Now().Add(resourceManagementNativeLifetime).Unix(),
		Actions:   []string{resourcegrant.Inspect, resourcegrant.PreviewAction, resourcegrant.ApplyAction, resourcegrant.StatusAction},
		Fields:    []string{resourcegrant.FilesField, resourcegrant.PerPeerField}}}
	value := f.mustLocal(1, "resource.grant.management.preview", input)
	review, ok := value.(resourcegrant.ManagementGrantReview)
	if !ok || review.Grant.Validate() != nil || review.InitializesState != before.InitializesState || review.Grant.Record.Target != input.Inputs.Target || review.Grant.Record.Relationship.PeerKey != input.Inputs.PeerKey || review.Grant.Record.ExpiresAt != input.Inputs.ExpiresAt || !reflect.DeepEqual(review.Grant.Record.Actions, input.Inputs.Actions) || !reflect.DeepEqual(review.Grant.Record.Fields, input.Inputs.Fields) {
		f.t.Fatal("management grant preview changed the explicit finite reviewed scope")
	}
	value = f.mustLocal(1, "resource.grant.management.confirm", resourcegrant.ManagementGrantConfirmation{Review: review, Confirm: true})
	view, ok := value.(resourcegrant.LocalGrantView)
	if !ok || view.InitializesState || len(view.ManagementRecords) != len(before.ManagementRecords)+1 || !reflect.DeepEqual(view.Records, before.Records) || !reflect.DeepEqual(view.ManagementRecords[:len(before.ManagementRecords)], before.ManagementRecords) || !reflect.DeepEqual(view.ManagementRecords[len(before.ManagementRecords)], review.Grant) {
		f.t.Fatal("management confirmation changed retained grants or reviewed scope")
	}
	f.assertManagementSaved(review.Grant.Record)
	f.awaitManagement(review.Grant.Record)
	return review.Grant.Record
}

func (f *resourceManagementNativePair) assertManagementSaved(want resourcegrant.Record) {
	f.t.Helper()
	data, err := os.ReadFile(resourceGrantStatePath(f.cores[1].dir))
	if err != nil {
		f.t.Fatal("synthetic management grant could not be read")
	}
	state, err := resourcegrant.Decode(data)
	if err != nil || state.Version != resourcegrant.ManagementVersion {
		f.t.Fatal("management grant format is unavailable")
	}
	for _, record := range state.ManagementRecords {
		if record.Record.ID == want.ID {
			if !reflect.DeepEqual(record.Record, want) {
				f.t.Fatal("saved management scope or original expiry changed")
			}
			return
		}
	}
	f.t.Fatal("exact management grant was not retained")
}

func (f *resourceManagementNativePair) awaitManagement(want resourcegrant.Record) {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(f.ctx, 8*time.Second)
	defer cancel()
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	for {
		view := f.view(1)
		found := false
		for _, record := range view.ManagementRecords {
			if record.Record.ID == want.ID {
				found = reflect.DeepEqual(record.Record, want)
			}
		}
		if !found {
			f.t.Fatal("listener reconciliation changed the exact management grant")
		}
		if view.ListenerReady && view.Activation == "listening" {
			return
		}
		select {
		case <-ctx.Done():
			f.t.Fatal("management listener did not become ready")
		case <-tick.C:
		}
	}
}

type resourceManagementNativeBytes struct {
	settings, journal []byte
	settingsPresent   bool
}

func (f *resourceManagementNativePair) savedBytes() resourceManagementNativeBytes {
	f.t.Helper()
	settings, err := os.ReadFile(filepath.Join(f.cores[1].dir, capacityPolicyFile))
	present := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		f.t.Fatal("synthetic settings could not be read")
	}
	journal, err := os.ReadFile(resourceStatePath(f.cores[1].dir))
	if err != nil {
		f.t.Fatal("synthetic journal could not be read")
	}
	return resourceManagementNativeBytes{settings, journal, present}
}

func (f *resourceManagementNativePair) unchanged(want resourceManagementNativeBytes) {
	f.t.Helper()
	got := f.savedBytes()
	if got.settingsPresent != want.settingsPresent || !bytes.Equal(got.settings, want.settings) || !bytes.Equal(got.journal, want.journal) {
		f.t.Fatal("read, denied request or historical replay changed settings or journal bytes")
	}
}

func (f *resourceManagementNativePair) journal() resourceEnvelope {
	f.t.Helper()
	state, err := readResourceEnvelope(resourceStatePath(f.cores[1].dir))
	if err != nil {
		f.t.Fatal("synthetic journal validation failed")
	}
	return state
}

func resourceManagementNativeSettings(files, peer int64) resource.Settings {
	return resource.Settings{TransferConcurrentFiles: capacity.Limited(files), TransferConcurrentPerPeer: capacity.Limited(peer)}
}

func (f *resourceManagementNativePair) assertSettings(settings resource.Settings, before capacity.Policy) {
	f.t.Helper()
	want := before.Clone()
	want.Resources[resourcegrant.FilesField] = settings.TransferConcurrentFiles
	want.Resources[resourcegrant.PerPeerField] = settings.TransferConcurrentPerPeer
	local := f.descriptor(1)
	if !reflect.DeepEqual(local.Requested, settings) || local.Effective.TransferConcurrentFiles != *settings.TransferConcurrentFiles.Value || local.Effective.TransferConcurrentPerPeer != *settings.TransferConcurrentPerPeer.Value || !capacityJSONEqual(f.cores[1].capacityPolicy(), want) {
		f.t.Fatal("provider did not change exactly the two reviewed settings")
	}
	var saved capacity.Policy
	if json.Unmarshal(f.savedBytes().settings, &saved) != nil || !capacityJSONEqual(saved, want) {
		f.t.Fatal("durable settings do not match the local provider projection")
	}
}

func resourceManagementNativeApplied(t *testing.T, outcome resource.Outcome, durable bool) {
	t.Helper()
	if !durable || outcome != (resource.Outcome{Status: "applied", Configuration: "durable", Accounting: "succeeded", Transfer: "succeeded"}) {
		t.Fatal("real provider and terminal evidence did not report all successful stages")
	}
}

type resourceManagementNativeOwnerStage uint8

const (
	managementOwnerBaseline resourceManagementNativeOwnerStage = iota
	managementOwnerBeforeLocalApply
	managementOwnerAfterLocalApply
	managementOwnerBeforeRemoteApply
	managementOwnerAfterRemoteApply
	managementOwnerAfterRestart
)

func (stage resourceManagementNativeOwnerStage) label() string {
	switch stage {
	case managementOwnerBaseline:
		return "baseline"
	case managementOwnerBeforeLocalApply:
		return "before_local_apply"
	case managementOwnerAfterLocalApply:
		return "after_local_apply"
	case managementOwnerBeforeRemoteApply:
		return "before_remote_apply"
	case managementOwnerAfterRemoteApply:
		return "after_remote_apply"
	case managementOwnerAfterRestart:
		return "after_restart"
	default:
		return "invalid_stage"
	}
}

// Closed booleans only. No raw error, address, identity, pointer value or digest
// may reach diagnostics. These are sequential observations, not atomic proof.
type resourceManagementNativeOwnerPredicates struct {
	backend, owner, store, current, endpoint               bool
	authorityBefore, core, receipt, receiptCurrent, epoch  bool
	revision, limitsLoaded, limitsOwner, limitsPointer     bool
	limitsValue, configuration, projection, authorityAfter bool
}

// Core.op is held. Call the composite predicate before taking owner/store
// diagnostic locks; never call it recursively while either is held. Match its
// Core.op -> owner.mu -> Core.mu (released) -> store.mu discipline explicitly.
func (f *resourceManagementNativePair) managedOwnerPredicates(i int) resourceManagementNativeOwnerPredicates {
	c := f.cores[i]
	var seen resourceManagementNativeOwnerPredicates
	backend, ok := c.nodeCopy().(*directLANBackend)
	seen.backend = ok && backend != nil && backend.Node != nil
	if !seen.backend {
		return seen
	}
	seen.endpoint = backend.Node.Endpoint() == f.endpoints[i]
	owner := backend.currentCompletion()
	seen.owner = owner != nil
	if !seen.owner {
		return seen
	}
	seen.current = owner.activationCurrent()
	owner.mu.RLock()
	defer owner.mu.RUnlock()
	seen.authorityBefore = owner.authorityCurrent()
	seen.core = owner.coreCurrent("") // Must precede store.mu: this takes Core.mu.
	store := owner.store
	seen.store = store != nil
	if !seen.store {
		return seen
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	seen.receipt = store.contextPublication == owner.receipt
	seen.receiptCurrent = store.contextPublicationCurrentLocked(owner.process)
	seen.epoch = store.contextEpoch == owner.epoch
	seen.revision = store.reviewRevision == owner.revision
	limits := store.limits.Load()
	seen.limitsLoaded = limits != nil
	seen.limitsOwner = owner.limitsSource != nil
	seen.limitsPointer = limits == owner.limitsSource
	seen.limitsValue = *store.currentCapacity() == owner.limits
	seen.configuration = contextConfigurationDigest(store.state) == owner.configuration
	_, err := owner.projectionLocked(time.Now())
	seen.projection = err == nil
	seen.authorityAfter = owner.authorityCurrent()
	return seen
}

func (f *resourceManagementNativePair) assertCurrentManagedOwner(i int, stage resourceManagementNativeOwnerStage) {
	f.t.Helper()
	c := f.cores[i]
	var seen resourceManagementNativeOwnerPredicates
	func() {
		c.op.Lock()
		defer c.op.Unlock()
		seen = f.managedOwnerPredicates(i)
	}()
	if !seen.current || !seen.endpoint {
		f.t.Logf("managed_owner stage=%s side=%d backend=%t owner=%t store=%t current=%t endpoint=%t authority_before=%t core=%t receipt=%t receipt_current=%t epoch=%t revision=%t limits_loaded=%t limits_owner=%t limits_pointer=%t limits_value=%t configuration=%t projection=%t authority_after=%t", stage.label(), i,
			seen.backend, seen.owner, seen.store, seen.current, seen.endpoint, seen.authorityBefore, seen.core, seen.receipt, seen.receiptCurrent, seen.epoch, seen.revision, seen.limitsLoaded, seen.limitsOwner, seen.limitsPointer, seen.limitsValue, seen.configuration, seen.projection, seen.authorityAfter)
		f.t.Fatal("ordinary managed owner is not current at the required stage")
	}
	f.t.Logf("managed_owner stage=%s side=%d current=true endpoint=true", stage.label(), i)
}

func (f *resourceManagementNativePair) applyLocal(settings resource.Settings) resource.Operation {
	f.t.Helper()
	f.assertCurrentManagedOwner(1, managementOwnerBeforeLocalApply)
	before := f.cores[1].capacityPolicy()
	value := f.mustLocal(1, "resource.preview", resource.PreviewRequest{Target: f.descriptor(1).Target, Settings: settings})
	preview, ok := value.(resource.Preview)
	if !ok {
		f.t.Fatal("local synthetic settings review unavailable")
	}
	value = f.mustLocal(1, "resource.apply", resource.ApplyRequest{Target: preview.Target, OperationID: preview.OperationID, BaseRevision: preview.BaseRevision, Revision: preview.Revision, Settings: preview.Requested})
	operation, ok := value.(resource.Operation)
	if !ok {
		f.t.Fatal("local synthetic settings operation unavailable")
	}
	resourceManagementNativeApplied(f.t, operation.Outcome, operation.EvidenceDurable)
	f.assertSettings(settings, before)
	f.assertCurrentManagedOwner(1, managementOwnerAfterLocalApply)
	return operation
}

func (f *resourceManagementNativePair) preview(record resourcegrant.Record, settings resource.Settings) resourcegrant.RemoteManagementInput {
	f.t.Helper()
	input := resourceManagementNativeInput(record, resourcegrant.PreviewAction)
	input.Request.Preview = &resourcegrant.ManagementPreviewRequest{Settings: settings}
	before := f.savedBytes()
	reply := f.mustRemote(input)
	if reply.Preview == nil || !reflect.DeepEqual(reply.Preview.Requested, settings) || reply.Preview.Effective.TransferConcurrentFiles != *settings.TransferConcurrentFiles.Value || reply.Preview.Effective.TransferConcurrentPerPeer != *settings.TransferConcurrentPerPeer.Value {
		f.t.Fatal("remote preview changed reviewed settings")
	}
	f.unchanged(before)
	apply := resourceManagementNativeInput(record, resourcegrant.ApplyAction)
	apply.Request.Apply = &resourcegrant.ManagementApplyRequest{OperationID: reply.Preview.OperationID, BaseRevision: reply.Preview.BaseRevision, ReviewRevision: reply.Preview.ReviewRevision, Settings: reply.Preview.Requested}
	return apply
}

func (f *resourceManagementNativePair) applyRemote(input resourcegrant.RemoteManagementInput) resourcegrant.ManagementOperation {
	f.t.Helper()
	f.assertCurrentManagedOwner(1, managementOwnerBeforeRemoteApply)
	before := f.cores[1].capacityPolicy()
	state := f.journal()
	// Each production ManageRemote invocation dials and closes its own TCP
	// connection. Successful use of the prior preview token on this separate
	// apply proves that the actual epoch-bound token remained valid; no fixture
	// gate, captured authority setter, session warming or retry is involved.
	reply := f.mustRemote(input)
	if reply.Operation == nil || reply.Operation.OperationID != input.Request.Apply.OperationID {
		f.t.Fatal("separate-connection apply did not retain the preview operation")
	}
	resourceManagementNativeApplied(f.t, reply.Operation.Outcome, *reply.Operation.EvidenceDurable)
	f.assertSettings(input.Request.Apply.Settings, before)
	f.assertCurrentManagedOwner(1, managementOwnerAfterRemoteApply)
	after := f.journal()
	if after.scoped == nil || *after.HighWater != *state.HighWater+1 || len(after.scoped.Records) == 0 {
		f.t.Fatal("successful apply did not consume exactly one scoped journal sequence")
	}
	retained := after.scoped.Records[len(after.scoped.Records)-1].Remote
	if retained == nil || retained.Phase != "result" || !resource.ValidDigest(retained.ManagedGeneration) || !reflect.DeepEqual(retained.Request, *input.Request.Apply) || retained.Outcome != reply.Operation.Outcome {
		f.t.Fatal("durable remote record differs from the actual provider result")
	}
	return *reply.Operation
}

func (f *resourceManagementNativePair) status(record resourcegrant.Record, id string) resourcegrant.ManagementReply {
	f.t.Helper()
	input := resourceManagementNativeInput(record, resourcegrant.StatusAction)
	input.Request.Status = &resourcegrant.ManagementStatusRequest{OperationID: id}
	return f.mustRemote(input)
}

func (f *resourceManagementNativePair) deny(input resourcegrant.RemoteManagementInput, code string) {
	f.t.Helper()
	before := f.savedBytes()
	value, err := f.remoteOnce(input)
	if value != nil || networkErrorCode(err) != code {
		f.t.Fatal("denied management request disclosed data or returned an unexpected code")
	}
	f.unchanged(before)
}

func (f *resourceManagementNativePair) revoke(record resourcegrant.Record) {
	f.t.Helper()
	value := f.mustLocal(1, "resource.grant.revoke", resourcegrant.GrantRevoke{Target: record.Target, GrantID: record.ID, GrantRevision: record.Revision, Confirm: true})
	view, ok := value.(resourcegrant.LocalGrantView)
	if !ok || view.ListenerReady {
		f.t.Fatal("management revocation left listener admission ready")
	}
	want := record
	want.State, want.Revision = resourcegrant.Revoked, record.Revision+1
	f.assertManagementSaved(want)
}

func TestResourceManagementNativePreviewApplyReplayAndMigration(t *testing.T) {
	f := newResourceManagementNativePair(t)
	record := f.confirmManagement()
	// Establish a genuine local v1 record before any management preview.
	anchor := f.applyLocal(resourceManagementNativeSettings(5, 2))
	legacy := f.journal()
	if legacy.scoped != nil || legacy.SchemaVersion != resource.SchemaVersion || *legacy.HighWater != 1 || len(legacy.Records) != 1 || legacy.Records[0].Request.OperationID != anchor.OperationID {
		t.Fatal("pre-existing local v1 evidence was not established")
	}
	before := f.savedBytes()
	inspection := f.mustRemote(resourceManagementNativeInput(record, resourcegrant.Inspect))
	local := f.descriptor(1)
	if inspection.Inspection == nil || !reflect.DeepEqual(inspection.Inspection.Requested, local.Requested) || inspection.Inspection.Effective != local.Effective {
		t.Fatal("management inspection did not match the local two-field projection")
	}
	f.unchanged(before)
	apply := f.preview(record, resourceManagementNativeSettings(3, 1))
	missing := f.status(record, apply.Request.Apply.OperationID)
	if missing.Unavailable == nil || missing.Unavailable.OperationID != apply.Request.Apply.OperationID {
		t.Fatal("unused preview unexpectedly exposed operation evidence")
	}
	f.unchanged(before)
	result := f.applyRemote(apply)
	migrated := f.journal()
	if migrated.scoped == nil || len(migrated.scoped.Records) != 2 || migrated.scoped.Records[0].Kind != operationjournal.LocalKind || migrated.scoped.Records[0].Local == nil || !reflect.DeepEqual(*migrated.scoped.Records[0].Local, legacy.Records[0]) {
		t.Fatal("first remote apply did not preserve the exact pre-existing local record")
	}
	// This separate authorized local operation makes a second provider invocation
	// distinguishable from idempotently writing the remote operation's old value.
	f.applyLocal(resourceManagementNativeSettings(7, 2))
	before = f.savedBytes()
	replayed := f.mustRemote(apply)
	status := f.status(record, result.OperationID)
	if !reflect.DeepEqual(replayed.Operation, &result) || !reflect.DeepEqual(status.Operation, &result) {
		t.Fatal("replay/status changed historical evidence")
	}
	f.unchanged(before)
	if f.descriptor(1).Effective.TransferConcurrentFiles != 7 || *f.journal().HighWater != 3 {
		t.Fatal("historical replay ran the provider again or advanced the sequence")
	}
	f.assertManagementSaved(record)
}

func TestResourceManagementNativeRevokeReplacementAndProtocolSeparation(t *testing.T) {
	f := newResourceManagementNativePair(t)
	record := f.confirmManagement()
	apply := f.preview(record, resourceManagementNativeSettings(3, 1))
	result := f.applyRemote(apply)
	unused := f.preview(record, resourceManagementNativeSettings(4, 2))
	wrong := resourceManagementNativeInput(record, resourcegrant.StatusAction)
	wrong.Request.GrantRevision++
	wrong.Request.Status = &resourcegrant.ManagementStatusRequest{OperationID: result.OperationID}
	f.deny(wrong, "resource_management_remote_unavailable")
	// The production v1 client sends only its hello until support succeeds.
	// A wrong but well-formed selector still receives explicit unsupported from
	// the v2-only listener, rather than a selector-dependent result.
	oldInput := resourceInspectionNativeInput(record)
	oldInput.Request.GrantRevision++
	before := f.savedBytes()
	value, err := f.local(0, "resource.remote.inspect", oldInput)
	if value != nil || networkErrorCode(err) != "resource_remote_unsupported" {
		t.Fatal("management listener did not reject v1 during compatibility negotiation")
	}
	f.unchanged(before)
	f.revoke(record)
	inspect := resourceManagementNativeInput(record, resourcegrant.Inspect)
	preview := resourceManagementNativeInput(record, resourcegrant.PreviewAction)
	preview.Request.Preview = &resourcegrant.ManagementPreviewRequest{Settings: unused.Request.Apply.Settings}
	status := resourceManagementNativeInput(record, resourcegrant.StatusAction)
	status.Request.Status = &resourcegrant.ManagementStatusRequest{OperationID: result.OperationID}
	for _, denied := range []resourcegrant.RemoteManagementInput{inspect, preview, unused, status} {
		f.deny(denied, "resource_management_remote_unavailable")
	}
	// An independently reviewed new grant has no access to predecessor evidence.
	replacement := f.confirmManagement()
	if replacement.ID == record.ID {
		t.Fatal("replacement reused a revoked grant identity")
	}
	before = f.savedBytes()
	missing := f.status(replacement, result.OperationID)
	if missing.Unavailable == nil || missing.Operation != nil || missing.Unavailable.OperationID != result.OperationID {
		t.Fatal("replacement grant disclosed predecessor history")
	}
	f.unchanged(before)
	oldApply := apply
	oldApply.Request.ManagementSelector = managementSelector(replacement)
	f.deny(oldApply, "resource_management_remote_unavailable")
	f.revoke(replacement)
	// Explicitly return to legacy inspect-only scope without granting v2 actions.
	in := resourcegrant.GrantInputs{Target: record.Target, PeerKey: record.Relationship.PeerKey, ExpiresAt: time.Now().Add(resourceManagementNativeLifetime).Unix(), Actions: []string{resourcegrant.Inspect}, Fields: []string{resourcegrant.FilesField, resourcegrant.PerPeerField}}
	grantReview, ok := f.mustLocal(1, "resource.grant.preview", in).(resourcegrant.GrantReview)
	if !ok || grantReview.Grant.Validate() != nil || grantReview.Grant.ExpiresAt != in.ExpiresAt {
		t.Fatal("separate inspection grant review unavailable")
	}
	f.mustLocal(1, "resource.grant.confirm", resourcegrant.GrantConfirmation{Review: grantReview, Confirm: true})
	f.awaitListening(1, grantReview.Grant)
	before = f.savedBytes()
	f.inspectOnce(0, 1, grantReview.Grant)
	f.unchanged(before)
	f.deny(resourceManagementNativeInput(grantReview.Grant, resourcegrant.Inspect), "resource_management_remote_unsupported")
}

func TestResourceManagementNativeRestartPreservesOriginalExpiryAndHistory(t *testing.T) {
	f := newResourceManagementNativePair(t)
	record := f.confirmManagement()
	apply := f.preview(record, resourceManagementNativeSettings(3, 1))
	result := f.applyRemote(apply)
	f.applyLocal(resourceManagementNativeSettings(7, 2))
	// Mint this unconsumed token after both retained operations, so rejection
	// cannot be explained by an already-consumed sequence slot.
	unused := f.preview(record, resourceManagementNativeSettings(8, 3))
	original := f.journal()
	retained := *original.scoped.Records[0].Remote
	nonce := f.cores[1].resourceNonce
	f.reopen(1) // Actual owned Core.Close/Open, never an OS-process restart.
	f.assertCurrentManagedOwner(1, managementOwnerAfterRestart)
	f.awaitManagement(record)
	f.assertManagementSaved(record)
	if time.Now().Unix() >= record.ExpiresAt || f.cores[1].resourceNonce == nonce {
		t.Fatal("restart reused the old issuance nonce or exceeded original expiry")
	}
	before := f.savedBytes()
	replayed := f.mustRemote(apply)
	status := f.status(record, result.OperationID)
	if !reflect.DeepEqual(replayed.Operation, &result) || !reflect.DeepEqual(status.Operation, &result) {
		t.Fatal("owned restart lost exact historical evidence")
	}
	f.unchanged(before)
	f.deny(unused, "resource_management_remote_unavailable")
	if f.descriptor(1).Effective.TransferConcurrentFiles != 7 || *f.journal().HighWater != *original.HighWater {
		t.Fatal("restart replay or stale preview reran the provider")
	}
	fresh := f.preview(record, unused.Request.Apply.Settings)
	if fresh.Request.Apply.OperationID == unused.Request.Apply.OperationID || fresh.Request.Apply.OperationID == apply.Request.Apply.OperationID {
		t.Fatal("restart did not issue a distinct current token")
	}
	f.applyRemote(fresh)
	after := f.journal()
	if after.scoped == nil || len(after.scoped.Records) != 3 || after.scoped.Records[0].Remote == nil || !reflect.DeepEqual(*after.scoped.Records[0].Remote, retained) || after.scoped.Records[2].Remote == nil || after.scoped.Records[2].Remote.IssuanceNonce != f.cores[1].resourceNonce || after.scoped.Records[2].Remote.ManagedGeneration == retained.ManagedGeneration {
		t.Fatal("new-boot apply rewrote old evidence or retained old issuance identity/epoch")
	}
	f.assertManagementSaved(record)
}
