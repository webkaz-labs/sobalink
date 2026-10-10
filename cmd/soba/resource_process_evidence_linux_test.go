//go:build resource_process_native && directlan_activation_native && linux && (amd64 || arm64)

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/netip"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/core"
	"github.com/webkaz-labs/sobalink/internal/endpointmeta"
	"github.com/webkaz-labs/sobalink/internal/operationjournal"
	"github.com/webkaz-labs/sobalink/internal/resource"
	pm "github.com/webkaz-labs/sobalink/internal/resourceacceptance/processmodel"
	"github.com/webkaz-labs/sobalink/internal/resourcegrant"
	"github.com/webkaz-labs/sobalink/internal/resourcegroup"
)

// Ordinary status contains legitimate nullable presentation fields; resource
// authority DTOs are additionally validated by their concrete public codecs.
// This bounded parser rejects duplicate/case-alias/unknown typed fields for
// objects, arrays and scalar projections without inventing a no-null status API.
func (f *p1Fixture) decode(raw []byte, v any, max int) {
	f.require(len(raw) > 0 && len(raw) <= max, "response_bound")
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	f.require(p1JSONValue(d, 0, reflect.TypeOf(v)), "strict_response_shape")
	_, err := d.Token()
	f.require(err == io.EOF, "strict_response_trailing")
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	f.require(d.Decode(v) == nil, "strict_response_decode")
}
func p1JSONValue(d *json.Decoder, depth int, shape reflect.Type) bool {
	if depth > 16 {
		return false
	}
	nullable := shape == nil
	for shape != nil && shape.Kind() == reflect.Pointer {
		nullable = true
		shape = shape.Elem()
	}
	if shape != nil && (shape.Kind() == reflect.Slice || shape.Kind() == reflect.Map || shape.Kind() == reflect.Interface) {
		nullable = true
	}
	token, err := d.Token()
	if err != nil {
		return false
	}
	if token == nil {
		return nullable
	}
	delimiter, structured := token.(json.Delim)
	if !structured {
		return true
	}
	switch delimiter {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			keyToken, err := d.Token()
			key, ok := keyToken.(string)
			if err != nil || !ok || seen[key] {
				return false
			}
			seen[key] = true
			var child reflect.Type
			if shape != nil && shape.Kind() == reflect.Struct {
				child = p1JSONField(shape, key)
				if child == nil {
					return false
				}
			} else if shape != nil && shape.Kind() == reflect.Map {
				child = shape.Elem()
			}
			if !p1JSONValue(d, depth+1, child) {
				return false
			}
		}
		end, err := d.Token()
		return err == nil && end == json.Delim('}')
	case '[':
		var element reflect.Type
		if shape != nil && (shape.Kind() == reflect.Slice || shape.Kind() == reflect.Array) {
			element = shape.Elem()
		}
		for d.More() {
			if !p1JSONValue(d, depth+1, element) {
				return false
			}
		}
		end, err := d.Token()
		return err == nil && end == json.Delim(']')
	default:
		return false
	}
}
func p1JSONField(shape reflect.Type, key string) reflect.Type {
	for shape.Kind() == reflect.Pointer {
		shape = shape.Elem()
	}
	if shape.Kind() != reflect.Struct {
		return nil
	}
	for i := 0; i < shape.NumField(); i++ {
		field := shape.Field(i)
		if field.Anonymous {
			if nested := p1JSONField(field.Type, key); nested != nil {
				return nested
			}
		}
		tag := strings.Split(field.Tag.Get("json"), ",")[0]
		if tag == key && tag != "" && tag != "-" {
			return field.Type
		}
	}
	return nil
}
func (f *p1Fixture) assertDescriptor(d resource.Descriptor) {
	f.require(d.Target.Validate() == nil && d.Type == resource.Type && d.Authority == "local" && d.Provider == "local" && resource.ValidDigest(d.Revision) && d.Requested.Validate() == nil && reflect.DeepEqual(d.Operations, []string{"list", "inspect", "preview", "apply", "operation.status"}), "resource_descriptor")
}
func (f *p1Fixture) assertCLI(n int, role pm.OwnerRole, raw, stderr []byte, exit int) {
	if n == 29 || n == 32 || n == 33 {
		want := "resource_group_review_changed"
		if n == 29 {
			want = "resource_group_review_unavailable"
		}
		var failure struct {
			Code  string `json:"code"`
			Error string `json:"error"`
		}
		f.decode(stderr, &failure, 4096)
		f.require(exit == 1 && len(raw) == 0 && failure.Code == want && failure.Error == resourceCollectionErrorTexts[want].en, "negative_typed_error")
		return
	}
	f.require(exit == 0 && len(stderr) == 0, "cli_product_failure")
	switch n {
	case 1, 2, 3, 8, 26:
		var view resource.Catalog
		f.decode(raw, &view, 8192)
		f.require(view.SchemaVersion == 1 && len(view.Resources) == 1, "resource_catalog")
		f.assertDescriptor(view.Resources[0])
		i := p1RoleIndex(role)
		if f.ids[i] == "" {
			f.ids[i] = view.Resources[0].ResourceID
		} else {
			f.require(f.ids[i] == view.Resources[0].ResourceID, "resource_identity_changed")
		}
	case 4, 5, 6, 9, 22, 27:
		f.assertStatus(role, raw, n == 22 || n == 27)
	case 7, 25, 38, 39, 40:
		var reply struct {
			State string `json:"state"`
		}
		f.decode(raw, &reply, 1024)
		f.require(reply.State == "stopping", "normal_stop_dispatch")
	case 10, 13:
		var review resourcegrant.ManagementGrantReview
		f.decode(raw, &review, 8192)
		i := p1RoleIndex(role) - 1
		r := review.Grant.Record
		f.require(review.Grant.Validate() == nil && review.InitializesState && review.UpgradesFormat && resource.ValidDigest(review.BaseRevision) && resource.ValidDigest(review.ReviewRevision) && r.Target.ResourceID == f.ids[i+1] && r.ExpiresAt == f.epoch+360 && r.Relationship.Backend == resourcegrant.Backend && r.Relationship.TargetKey == f.identities[i+1].PublicKey() && r.Relationship.PeerKey == f.identities[0].PublicKey() && r.State == resourcegrant.Active && r.Revision == 1 && r.IssuedAt >= f.epoch && r.IssuedAt < r.ExpiresAt, "grant_scope")
		f.require(reflect.DeepEqual(r.Actions, []string{resourcegrant.Inspect, resourcegrant.PreviewAction, resourcegrant.ApplyAction, resourcegrant.StatusAction}) && reflect.DeepEqual(r.Fields, []string{resourcegrant.FilesField, resourcegrant.PerPeerField}), "grant_actions")
		f.grants[i] = review.Grant
		name := "grant-a.json"
		if i == 1 {
			name = "grant-b.json"
		}
		f.writeNew(filepath.Join(f.root, "inputs", name), raw, true)
	case 11, 12, 14, 15:
		var view resourcegrant.LocalGrantView
		f.decode(raw, &view, 8192)
		f.assertGrant(p1RoleIndex(role)-1, view)
	case 16, 21:
		view, err := resourcegroup.DecodePreparedView(raw)
		f.require(err == nil, "prepared_decode")
		selection, wanted := f.selection, f.wanted
		if n == 21 {
			selection = f.unusedSelection
			wanted = [2]resource.Settings{{TransferConcurrentFiles: capacity.Limited(5), TransferConcurrentPerPeer: capacity.Limited(2)}, {TransferConcurrentFiles: capacity.Limited(6), TransferConcurrentPerPeer: capacity.Limited(3)}}
		}
		resolved, err := resourcegroup.ResolveSelection(selection)
		f.require(err == nil && reflect.DeepEqual(view.Review.Selection, resolved) && view.AdmissionState == resourcegroup.AdmissionPrepared && view.InitializesLocalEvidence != nil && *view.InitializesLocalEvidence == (n == 16) && len(view.Review.Rows) == 2 && len(view.Review.ExecutionPeers) == 2, "prepared_complete")
		for i, row := range view.Review.Rows {
			f.require(row.State == resourcegroup.ReviewReady && row.PeerKey == selection.Members[i].PeerKey && view.Review.ExecutionPeers[i] == row.PeerKey && row.Reply != nil && row.Reply.Preview != nil && row.Reply.ManagementSelector == selection.Members[i].Selector && reflect.DeepEqual(row.Reply.Preview.Requested, wanted[i]), "prepared_target")
		}
		apply := resourcegroup.ApplyInput{SchemaVersion: 1, ReviewID: view.ReviewID, ReviewRevision: view.Review.Revision, ExecutionPeers: append([]string{}, view.Review.ExecutionPeers...), Confirm: true}
		f.require(apply.Validate() == nil, "prepared_apply")
		if n == 16 {
			f.prepared = view
			f.apply = apply
		} else {
			f.require(view.ReviewID != f.run.RunID, "unused_review_identity")
			f.unused = view
			f.unusedApply = apply
		}
		f.assertPublicGroup(raw)
	case 17, 18, 19:
		f.assertDryRun(n, raw)
	case 20, 24, 30, 31, 34, 35:
		view, err := resourcegroup.DecodeRunView(raw)
		f.require(err == nil, "run_decode")
		f.assertPublicGroup(raw)
		if n == 20 {
			f.assertCompletedRun(view)
			f.run = view
		} else {
			f.require(reflect.DeepEqual(view, f.run), "history_changed")
		}
	case 23, 28:
		view, err := resourcegroup.DecodeCurrentReviewView(raw)
		f.require(err == nil, "current_review_decode")
		if n == 23 {
			f.require(view.State == resourcegroup.CurrentReviewCurrent && view.Prepared != nil && reflect.DeepEqual(*view.Prepared, f.unused), "unused_current_review")
		} else {
			f.require(view.State == resourcegroup.CurrentReviewNone && view.Prepared == nil, "unused_review_restored")
		}
	case 36, 37:
		f.assertHuman(raw, n == 37)
	default:
		f.require(false, "cli_assertion_missing")
	}
}
func (f *p1Fixture) assertDryRun(n int, raw []byte) {
	var view struct {
		Applied    bool            `json:"applied"`
		Command    string          `json:"command"`
		Payload    json.RawMessage `json:"payload"`
		Validation string          `json:"validation"`
	}
	f.decode(raw, &view, 64<<10)
	f.require(!view.Applied && view.Validation == "local-input-only", "dry_run_envelope")
	switch n {
	case 17:
		var in resourcegroup.ApplyInput
		f.decode(view.Payload, &in, 8192)
		f.require(view.Command == resourcegroup.LocalApplyCommand && reflect.DeepEqual(in, f.apply), "dry_run_apply")
	case 18:
		var in resourcegroup.StatusInput
		f.decode(view.Payload, &in, 1024)
		f.require(view.Command == resourcegroup.LocalStatusCommand && in == (resourcegroup.StatusInput{SchemaVersion: 1, RunID: f.apply.ReviewID}), "dry_run_status")
	case 19:
		var in resourcegroup.PreviewInput
		f.decode(view.Payload, &in, resourcegroup.MaxSelectionBytes)
		f.require(view.Command == resourcegroup.LocalPreviewCommand && in.SchemaVersion == 1 && in.ReplaceReviewID == "" && reflect.DeepEqual(in.Selection, f.selection), "dry_run_preview")
	}
}
func (f *p1Fixture) assertStatus(role pm.OwnerRole, raw []byte, ready bool) {
	var fields map[string]json.RawMessage
	f.decode(raw, &fields, 64<<10)
	allowed := []string{"version", "processId", "self", "peers", "messages", "transfers", "receiveRecovery", "services", "shares", "proxies", "startup", "savedProxies", "availableServices", "reservedPorts", "settings", "servicePresets", "limits", "lan", "directLAN", "mixed", "resourceCatalogProcessId"}
	f.require(len(fields) == len(allowed), "status_fields")
	for _, name := range allowed {
		f.require(len(fields[name]) > 0, "status_field_missing")
	}
	var pid int
	var token string
	f.decode(fields["processId"], &pid, 64)
	f.decode(fields["resourceCatalogProcessId"], &token, 128)
	f.require(pid == f.owner(role).cmd.Process.Pid && resource.ValidDigest(token), "status_process")
	old := f.catalogs[int(role)-1]
	if old != "" {
		f.require(old == token, "catalog_changed_inside_owner")
	}
	f.catalogs[int(role)-1] = token
	if role == pm.RoleC1 {
		f.require(token != f.catalogs[0], "setup_catalog_reused")
	}
	if role == pm.RoleC2 {
		f.require(token != f.catalogs[int(pm.RoleC1)-1] && token != f.catalogs[0], "measured_catalog_reused")
	}
	var settings core.Settings
	f.decode(fields["settings"], &settings, 2048)
	f.require(settings.Network == "direct-lan" && settings.ReceiveDirectory == "" && settings.Locale == "en" && settings.Theme == "system", "status_settings")
	var lan map[string]json.RawMessage
	f.decode(fields["directLAN"], &lan, 8192)
	var configured, listener, recovery bool
	var key, endpoint string
	f.decode(lan["configured"], &configured, 16)
	f.decode(lan["listenerReady"], &listener, 16)
	f.decode(lan["recoveryRequired"], &recovery, 16)
	f.decode(lan["publicKey"], &key, 128)
	f.decode(lan["endpoint"], &endpoint, 128)
	i := p1RoleIndex(role)
	f.require(configured && listener == ready && !recovery && key == f.identities[i].PublicKey() && endpoint == f.endpoints[i].String(), "status_network")
	for _, name := range []string{"messages", "transfers", "shares", "proxies", "availableServices"} {
		var values []json.RawMessage
		f.decode(fields[name], &values, 16384)
		f.require(values != nil && len(values) == 0, "status_unrequested_activity")
	}
	var recoveryView struct {
		State         string   `json:"state"`
		Code          string   `json:"code"`
		ReservedBytes *int64   `json:"reservedBytes"`
		Applied       bool     `json:"applied"`
		Review        []string `json:"review"`
	}
	f.decode(fields["receiveRecovery"], &recoveryView, 4096)
	f.require(recoveryView.State == "blocked" && recoveryView.Code == "legacy_review_required" && recoveryView.ReservedBytes == nil && !recoveryView.Applied && reflect.DeepEqual(recoveryView.Review, []string{"previous_default_destinations", "previous_peer_destinations", "previous_manual_destinations", "unfinished_staging", "previously_saved_output", "untracked_partials_resolved"}), "unmodified_legacy_receive_state")
	var peers []map[string]json.RawMessage
	f.decode(fields["peers"], &peers, 8192)
	expectedPeers := 1
	if i == 0 {
		expectedPeers = 2
	}
	f.require(len(peers) == expectedPeers, "status_peers")
	for _, peer := range peers {
		var trusted bool
		f.decode(peer["trusted"], &trusted, 16)
		f.require(!trusted, "implicit_trust")
	}
	var services []map[string]json.RawMessage
	f.decode(fields["services"], &services, 8192)
	if i == 0 {
		f.require(len(services) == 1, "saved_service_count")
		for name, want := range map[string]string{"id": "synthetic-service", "status": "saved", "application": "unverified", "lifetime": "until-stopped", "ports": "8080", "direction": "forward"} {
			var value string
			f.decode(services[0][name], &value, 1024)
			f.require(value == want, "saved_service_semantics")
		}
	} else {
		f.require(len(services) == 0, "unexpected_service")
	}
}
func (f *p1Fixture) assertGrant(i int, v resourcegrant.LocalGrantView) {
	f.require(v.Target == f.grants[i].Record.Target && !v.InitializesState && !v.TimeUncertain && v.ListenerReady && v.Activation == "listening" && len(v.Records) == 0 && len(v.ManagementRecords) == 1 && reflect.DeepEqual(v.ManagementRecords[0], f.grants[i]) && f.grants[i].Record.ExpiresAt == f.epoch+360, "grant_stability")
	saved, err := resourcegrant.Decode(f.readPrivate([]pm.OwnerRole{pm.RoleA0, pm.RoleB0}[i], "resource-grants/state.json", resourcegrant.MaxBytes, false))
	f.require(err == nil && saved.Version == resourcegrant.ManagementVersion && len(saved.Records) == 0 && len(saved.ManagementRecords) == 1 && reflect.DeepEqual(saved.ManagementRecords[0], f.grants[i]), "durable_grant")
}
func (f *p1Fixture) assertPublicGroup(raw []byte) {
	for _, forbidden := range []string{"\"origins\"", "\"relationship\"", "\"pairBinding\"", "\"managedGeneration\"", "\"issuanceNonce\"", "\"controllerResourceId\""} {
		f.require(!bytes.Contains(raw, []byte(forbidden)), "public_group_redaction")
	}
}
func (f *p1Fixture) assertCompletedRun(run resourcegroup.RunView) {
	f.require(run.RunID == f.prepared.ReviewID && reflect.DeepEqual(run.Review, f.prepared.Review) && run.Activity == resourcegroup.ActivityIdle && run.LocalDurability == resourcegroup.LocalDurable && run.Summary.AllApplied && run.Summary.AdmissionFinished && !run.Summary.ReconciliationRequired && run.Summary.Applied == 2 && run.Summary.Executable == 2 && len(run.Evidence.Members) == 2, "completed_group")
	for i, row := range run.Evidence.Members {
		f.require(row.PeerKey == f.identities[i+1].PublicKey() && row.Execution == resourcegroup.ExecutionSelected && row.Dispatch == resourcegroup.DispatchObserved && row.Request != nil && row.Request.Apply != nil && row.Target != nil && row.Target.EvidenceDurable != nil && *row.Target.EvidenceDurable && row.Target.OperationID == row.Request.Apply.OperationID && row.LocalDurability == resourcegroup.LocalDurable && row.Status.State == resourcegroup.StatusNotQueried && row.AdmissionStop == resourcegroup.StopNone, "completed_member")
		f.require(row.Target.Outcome == (resource.Outcome{Status: "applied", Configuration: "durable", Accounting: "succeeded", Transfer: "succeeded"}) && reflect.DeepEqual(row.Request.Apply.Settings, f.wanted[i]) && row.Request.ManagementSelector == f.selection.Members[i].Selector, "completed_original_request")
	}
}
func (f *p1Fixture) readLAN(role pm.OwnerRole) p1LANFile {
	var saved p1LANFile
	f.decode(f.readPrivate(role, "direct-lan.json", 64<<10, false), &saved, 64<<10)
	f.validateLAN(saved, p1RoleIndex(role), true)
	return saved
}
func (f *p1Fixture) assertPairs(count int) {
	controller := f.readLAN(pm.RoleC0)
	for i := 1; i <= count; i++ {
		target := f.readLAN([]pm.OwnerRole{pm.RoleC0, pm.RoleA0, pm.RoleB0}[i])
		a, b := controller.Peers[i-1], target.Peers[0]
		f.require(a.Peer.Key == f.identities[i].PublicKey() && b.Peer.Key == f.identities[0].PublicKey() && a.ContextConfirmed && b.ContextConfirmed && a.PairContext != nil && b.PairContext != nil && reflect.DeepEqual(a.PairContext, b.PairContext) && a.EndpointState != nil && b.EndpointState != nil, "bilateral_pair")
		binding, err := a.PairContext.Binding()
		f.require(err == nil && binding == a.EndpointState.PairBinding && binding == b.EndpointState.PairBinding, "pair_binding")
		for _, state := range []*endpointmeta.EndpointState{a.EndpointState, b.EndpointState} {
			f.require(state.ReceiveStatus == "initial" && state.IssuedVersion == 0 && state.ReceivedVersion == 0 && state.IssuedHighwater == "0" && state.ReceivedHighwater == "0" && state.AuthorityRevision == "0" && state.Approval == nil && state.Follow == nil && state.IssuedProof == nil && state.ReceivedProof == nil, "pair_initial_endpoint")
		}
		if count == 2 && i == 1 && f.authority[0].Version != 0 {
			f.require(reflect.DeepEqual(a, f.authority[0].Peers[0]) && reflect.DeepEqual(b, f.authority[1].Peers[0]), "first_pair_changed")
		}
	}
	if count == 1 {
		f.authority[0] = controller
		f.authority[1] = f.readLAN(pm.RoleA0)
	}
}
func (f *p1Fixture) assertInitialFiles() {
	for i, role := range []pm.OwnerRole{pm.RoleC0, pm.RoleA0, pm.RoleB0} {
		var p core.Profile
		f.decode(f.readPrivate(role, "sobalink.json", 16<<10, false), &p, 16<<10)
		f.require(len(p.Peers) == 0 && len(p.Groups) == 0 && p.Settings.Network == "direct-lan", "initial_profile")
		saved := f.readLAN(role)
		f.validateLAN(saved, i, false)
		for _, name := range []string{"resource-grants/state.json", "resource-groups/state.json", "capacity.json"} {
			f.require(f.readPrivate(role, name, 1<<20, true) == nil, "initial_authority_present")
		}
		decoded, err := operationjournal.Decode(f.readPrivate(role, "resource-state/state.json", operationjournal.MaxBytes, false))
		f.require(err == nil && decoded.Legacy != nil && decoded.Legacy.ResourceID == f.ids[i] && len(decoded.Legacy.Records) == 0 && decoded.Legacy.HighWater != nil && *decoded.Legacy.HighWater == 0, "initial_journal")
	}
	f.policies = [2]capacity.Policy{capacity.Defaults(), capacity.Defaults()}
}
func (f *p1Fixture) captureAuthority() {
	for i, role := range []pm.OwnerRole{pm.RoleC1, pm.RoleA0, pm.RoleB0} {
		f.authority[i] = f.readLAN(role)
		f.decode(f.readPrivate(role, "sobalink.json", 16<<10, false), &f.profiles[i], 16<<10)
	}
	f.assertAuthority()
}
func (f *p1Fixture) assertAuthority() {
	if f.run.RunID == "" {
		f.require(false, "authority_without_run")
	}
	f.require(time.Now().Unix() < f.epoch+360, "original_grant_expired")
	for i, role := range []pm.OwnerRole{pm.RoleC2, pm.RoleA0, pm.RoleB0} {
		saved := f.readLAN(role)
		before := f.authority[i]
		saved.Revision = before.Revision
		saved.ObservedAt = before.ObservedAt
		for j := range saved.Peers {
			saved.Peers[j].Revision = before.Peers[j].Revision
		}
		f.require(reflect.DeepEqual(saved, before), "pair_authority_changed")
		var p core.Profile
		f.decode(f.readPrivate(role, "sobalink.json", 16<<10, false), &p, 16<<10)
		f.require(reflect.DeepEqual(p, f.profiles[i]) && len(p.Peers) == 0, "profile_changed")
		decoded, err := operationjournal.Decode(f.readPrivate(role, "resource-state/state.json", operationjournal.MaxBytes, false))
		f.require(err == nil, "final_journal_decode")
		id := ""
		if decoded.Legacy != nil {
			id = decoded.Legacy.ResourceID
		} else {
			id = decoded.Journal.ResourceID
		}
		f.require(id == f.ids[i], "persisted_identity_changed")
		if i > 0 {
			state, err := resourcegrant.Decode(f.readPrivate(role, "resource-grants/state.json", resourcegrant.MaxBytes, false))
			f.require(err == nil && len(state.ManagementRecords) == 1 && len(state.Records) == 0 && reflect.DeepEqual(state.ManagementRecords[0], f.grants[i-1]), "original_grant_changed")
		}
	}
	f.assertGroupJournal()
}
func (f *p1Fixture) assertTargetJournals() {
	for i, role := range []pm.OwnerRole{pm.RoleA0, pm.RoleB0} {
		row := f.run.Evidence.Members[i]
		decoded, err := operationjournal.Decode(f.readPrivate(role, "resource-state/state.json", operationjournal.MaxBytes, false))
		f.require(err == nil && decoded.Journal != nil && decoded.Journal.HighWater != nil && *decoded.Journal.HighWater == 1 && len(decoded.Journal.Records) == 1, "target_journal_count")
		tagged := decoded.Journal.Records[0]
		record := tagged.Remote
		f.require(tagged.Kind == operationjournal.RemoteKind && record != nil && tagged.Local == nil && record.Phase == "result" && record.Sequence == 1 && reflect.DeepEqual(record.Request, *row.Request.Apply) && record.Outcome == row.Target.Outcome && record.AuthorizingRevision == f.grants[i].Record.Revision && record.Scope.GrantID == f.grants[i].Record.ID && record.Scope.Relationship == f.grants[i].Record.Relationship && record.Scope.Target == f.grants[i].Record.Target, "target_original_journal")
		var policy capacity.Policy
		f.decode(f.readPrivate(role, "capacity.json", 64<<10, false), &policy, 64<<10)
		f.require(policy.Validate() == nil, "target_policy")
		want := f.policies[i].Clone()
		want.Resources[resourcegrant.FilesField] = f.wanted[i].TransferConcurrentFiles
		want.Resources[resourcegrant.PerPeerField] = f.wanted[i].TransferConcurrentPerPeer
		f.require(reflect.DeepEqual(policy, want), "unrelated_policy_changed")
	}
}

type p1GroupOrigin struct {
	PeerKey      string                      `json:"peerKey"`
	State        string                      `json:"state"`
	Relationship *resourcegrant.Relationship `json:"relationship,omitempty"`
}
type p1GroupRecord struct {
	RunID            string                     `json:"runId"`
	AcceptedSequence uint64                     `json:"acceptedSequence"`
	AcceptedAt       int64                      `json:"acceptedAt"`
	Review           resourcegroup.ReviewBody   `json:"review"`
	Origins          []p1GroupOrigin            `json:"origins"`
	InputHash        string                     `json:"inputHash"`
	Evidence         resourcegroup.EvidenceBody `json:"evidence"`
	Admission        string                     `json:"admission"`
	UpdateSequence   uint64                     `json:"updateSequence"`
}
type p1GroupEnvelope struct {
	SchemaVersion        int             `json:"schemaVersion"`
	ControllerResourceID string          `json:"controllerResourceId"`
	HighWater            *uint64         `json:"highWater"`
	Runs                 []p1GroupRecord `json:"runs"`
}

func (f *p1Fixture) assertGroupJournal() {
	var envelope p1GroupEnvelope
	f.decode(f.readPrivate(pm.RoleC2, "resource-groups/state.json", 1<<20, false), &envelope, 1<<20)
	f.require(envelope.SchemaVersion == 1 && envelope.ControllerResourceID == f.ids[0] && envelope.HighWater != nil && *envelope.HighWater == 1 && len(envelope.Runs) == 1, "controller_journal_count")
	row := envelope.Runs[0]
	raw, err := json.Marshal(f.apply)
	f.require(err == nil, "accepted_input_encoding")
	hash := sha256.Sum256(append([]byte("sobalink.resourcegroup.accepted.v1\x00"), raw...))
	f.require(row.RunID == f.run.RunID && row.AcceptedSequence == 1 && row.AcceptedAt == f.run.AcceptedAt && row.Admission == "finished" && row.UpdateSequence > 0 && row.InputHash == hex.EncodeToString(hash[:]) && reflect.DeepEqual(row.Review, f.run.Review) && reflect.DeepEqual(row.Evidence, f.run.Evidence) && len(row.Origins) == 2, "controller_original_journal")
	for i, origin := range row.Origins {
		want := resourcegrant.Relationship{Backend: resourcegrant.Backend, TargetKey: f.identities[0].PublicKey(), PeerKey: f.identities[i+1].PublicKey(), PairBinding: f.grants[i].Record.Relationship.PairBinding}
		f.require(origin.PeerKey == want.PeerKey && origin.State == "captured" && origin.Relationship != nil && *origin.Relationship == want, "controller_original_origin")
	}
}
func (f *p1Fixture) assertConstructors(c *p1Child, events []pm.Event, network bool) {
	counts := p1LifecycleCounts(events)
	for _, kind := range []pm.Kind{pm.OwnerLockBound, pm.CoreBound, pm.WebOpened, pm.IPCReady} {
		f.require(counts[kind] == 1, "owner_binding_count")
	}
	f.require(counts[pm.LegacyConstructorAttempted] == 0 && counts[pm.ManagedStartupConstructorAttempted] == 0, "constructor_fallback")
	want := uint64(0)
	if network {
		want = 1
	}
	f.require(counts[pm.ManagedConstructorAttempted] == want && counts[pm.OrdinaryNodeBound] == want, "ordinary_constructor_count")
	control := want
	if c.role == pm.RoleC2 {
		control = 0
	}
	for _, kind := range []pm.Kind{pm.ControlConstructorAttempted, pm.ControlNodeBound, pm.ControlNodeJoined} {
		f.require(counts[kind] == control, "control_constructor_count")
	}
	if control == 1 {
		var positions [5]uint64
		for _, e := range events {
			for i, k := range []pm.Kind{pm.ControlConstructorAttempted, pm.ControlNodeBound, pm.ControlNodeJoined, pm.ManagedConstructorAttempted, pm.OrdinaryNodeBound} {
				if e.Observation.Kind == k {
					positions[i] = e.Sequence
				}
			}
		}
		for i := 1; i < len(positions); i++ {
			f.require(positions[i-1] < positions[i], "control_join_before_ordinary")
		}
	}
	var lock, core, web, ipc uint64
	for _, e := range events {
		switch e.Observation.Kind {
		case pm.OwnerLockBound:
			lock = e.Sequence
		case pm.CoreBound:
			core = e.Sequence
		case pm.WebOpened:
			web = e.Sequence
		case pm.IPCReady:
			ipc = e.Sequence
		}
	}
	f.require(lock < core && core < web && web < ipc, "entry_readiness_order")
}
func (f *p1Fixture) assertManagementTotals() {
	runID, ok := pm.ParseRunID(f.run.RunID)
	f.require(ok, "run_observation_id")
	var preview [3]uint64
	var ordered [2][4]uint64
	var providers [2]uint64
	var accepted uint64
	for _, c := range f.owners {
		if c == nil {
			continue
		}
		for _, e := range c.prefix() {
			v := e.Observation
			if v.Kind < pm.GroupAccepted || v.Kind > pm.ProviderAdmitted {
				continue
			}
			if v.Kind == pm.GroupAccepted {
				f.require(c.role == pm.RoleC1 && v.RunID == runID && accepted == 0, "group_acceptance")
				accepted = e.Sequence
				continue
			}
			if v.Action == pm.Preview {
				f.require(c.role == pm.RoleC1, "preview_owner")
				index := -1
				switch v.Kind {
				case pm.ManagementClientInvoked:
					index = 0
				case pm.ManagementFrameAttempted:
					index = 1
				case pm.ManagementFrameWritten:
					index = 2
				}
				f.require(index >= 0, "preview_event_kind")
				preview[index]++
				continue
			}
			member := -1
			for i, row := range f.run.Evidence.Members {
				operation, ok := pm.ParseOperationID(row.Request.Apply.OperationID)
				f.require(ok, "operation_observation_id")
				if operation == v.OperationID {
					member = i
				}
			}
			f.require(member >= 0 && v.Action == pm.Apply, "operation_observation")
			if v.Kind == pm.ProviderAdmitted {
				want := pm.RoleA0
				if member == 1 {
					want = pm.RoleB0
				}
				f.require(c.role == want && providers[member] == 0, "provider_owner")
				providers[member] = e.Sequence
				continue
			}
			f.require(c.role == pm.RoleC1, "management_controller_owner")
			index := -1
			switch v.Kind {
			case pm.GroupIntent:
				index = 0
				f.require(v.RunID == runID, "intent_run")
			case pm.ManagementClientInvoked:
				index = 1
			case pm.ManagementFrameAttempted:
				index = 2
			case pm.ManagementFrameWritten:
				index = 3
			}
			f.require(index >= 0 && ordered[member][index] == 0, "duplicate_management_event")
			ordered[member][index] = e.Sequence
		}
	}
	f.require(preview == [3]uint64{4, 4, 4} && accepted > 0 && providers[0] > 0 && providers[1] > 0, "management_total")
	for _, row := range ordered {
		f.require(accepted < row[0] && row[0] < row[1] && row[1] < row[2] && row[2] < row[3], "controller_dispatch_order")
	}
	f.require(ordered[0][3] < ordered[1][0], "serial_controller_dispatch")
	// Target provider sequences belong to other processes and are never compared
	// with controller sequences; exact original operation/journal values bind them.
}
func (f *p1Fixture) assertHuman(raw []byte, ja bool) {
	text := string(raw)
	f.require(len(raw) > 0 && len(raw) <= 64<<10 && strings.Contains(text, cliCommandExample(f.roleDir(pm.RoleC2), "resource", "group", "status", "--run-id", f.run.RunID)), "human_profile_guidance")
	for _, phrase := range []string{"Historical run evidence; this does not describe current settings or transfer completion.", "Records never become runnable jobs after restart."} {
		if !ja {
			f.require(strings.Contains(text, phrase), "human_english")
		}
	}
	if ja {
		for _, phrase := range []string{"過去の実行記録です。現在の設定やファイル転送の完了を示すものではありません。", "再起動後に記録から自動実行することはありません。", "永続化済み"} {
			f.require(strings.Contains(text, phrase), "human_japanese")
		}
	}
	for _, grant := range f.grants {
		for _, secret := range []string{grant.Record.Relationship.PairBinding, grant.Record.ID} {
			f.require(!strings.Contains(text, secret), "human_private_material")
		}
	}
	for _, id := range f.identities {
		f.require(!strings.Contains(text, id.Seed), "human_seed")
	}
	f.assertPublicGroup(raw)
}
func (f *p1Fixture) assertFinalEvidence() {
	f.require(f.cliCount == 40 && f.pairCount == 2 && f.checkpointCount == 26 && f.maintenanceAfter >= f.maintenanceBefore+2, "final_schedule")
	for _, v := range f.verified {
		f.require(v, "missing_verification")
	}
	var origins [5]string
	for i, c := range f.owners {
		f.require(c != nil && c.joined && !c.failed.Load(), "final_owner_join")
		f.assertConstructors(c, c.prefix(), true)
		f.assertLocalLedger(c)
		f.require(c.count(pm.OwnersClosed) == 1 && c.count(pm.EntryReturned) == 1, "owner_final_events")
		if c.role == pm.RoleC2 || c.role == pm.RoleA0 || c.role == pm.RoleB0 {
			f.assertNoReplay(c)
		}
		raw := f.readOutput(c, false, 1<<20)
		lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
		f.require(len(lines) == 3 && strings.HasPrefix(lines[0], "Local UI: http://"), "owner_web_output")
		origin := strings.TrimPrefix(lines[0], "Local UI: ")
		endpoint, err := netip.ParseAddrPort(strings.TrimPrefix(origin, "http://"))
		f.require(err == nil && endpoint.Addr() == netip.MustParseAddr("127.0.0.1") && endpoint.Port() != 0 && c.count(pm.WebOpened) == 1, "owner_web_origin")
		origins[i] = origin
		f.require(len(f.readOutput(c, true, 1<<20)) == 0, "owner_stderr")
	}
	for _, controller := range []int{0, 3, 4} {
		f.require(origins[controller] != origins[1] && origins[controller] != origins[2] && origins[1] != origins[2], "concurrent_web_origins")
	}
	for _, c := range f.clis {
		f.require(c != nil && c.joined && c.cliValidated && !c.failed.Load(), "final_cli_join")
	}
	f.require(time.Now().Before(f.cleanup), "cleanup_original_cutoff")
}
func (f *p1Fixture) finalReceipt() []byte {
	type receipt struct {
		Scenario                string     `json:"scenario"`
		Source                  string     `json:"source"`
		Tree                    string     `json:"tree"`
		SourceManifest          string     `json:"sourceManifest"`
		Binary                  string     `json:"binary"`
		Assets                  string     `json:"assets"`
		Dependencies            string     `json:"dependencies"`
		Toolchain               string     `json:"toolchain"`
		Target                  string     `json:"target"`
		Selector                string     `json:"selector"`
		Outcome                 string     `json:"outcome"`
		Stage                   string     `json:"stage"`
		Owners                  int        `json:"owners"`
		CLIs                    int        `json:"clis"`
		Checkpoints             int        `json:"checkpoints"`
		OriginalExpiryPreserved bool       `json:"originalExpiryPreserved"`
		StableIDPreserved       bool       `json:"stableIdPreserved"`
		HistoryEqual            bool       `json:"historyEqual"`
		NoReplay                bool       `json:"noReplay"`
		Maintenance             uint64     `json:"maintenance"`
		Joined                  bool       `json:"joined"`
		EvidenceScope           string     `json:"evidenceScope"`
		ReceiveState            string     `json:"receiveState"`
		Unjoined                p1Unjoined `json:"unjoined"`
	}
	outcome := "failed"
	if f.success {
		outcome = "passed"
	}
	owners, clis := 0, 0
	joined := true
	for _, c := range f.children() {
		if c == nil {
			continue
		}
		if c.invocation.Mode == pm.Owner {
			owners++
		} else {
			clis++
		}
		joined = joined && c.joined
	}
	joined = joined && owners == 5 && clis == 40 && f.cliCount == 40
	r := receipt{Scenario: "source-built-controller-restart-v1", Source: f.manifest.SourceCommit, Tree: f.manifest.SourceTree, SourceManifest: f.manifest.SourceManifestSHA256, Binary: f.manifest.BinarySHA256, Assets: f.manifest.AssetSHA256, Dependencies: f.manifest.DependencyManifestSHA256, Toolchain: f.manifest.ToolchainSHA256, Target: f.manifest.Target, Selector: p1Selector, Outcome: outcome, Stage: f.stage, Owners: owners, CLIs: f.cliCount, Checkpoints: f.checkpointCount, OriginalExpiryPreserved: f.success, StableIDPreserved: f.success, HistoryEqual: f.success, NoReplay: f.success, Maintenance: p1MaintenanceDelta(f.maintenanceBefore, f.maintenanceAfter), Joined: joined, EvidenceScope: "instrumented source-built Linux entry only; no installed-binary or other-target acceptance", ReceiveState: "legacy_review_required; no transfer acceptance or repair", Unjoined: f.unjoined}
	raw, _ := json.Marshal(r)
	return raw
}

func p1MaintenanceDelta(before, after uint64) uint64 {
	if after < before {
		return 0
	}
	return after - before
}

func p1CLICommand(op pm.CLIOperation) pm.Command {
	switch op {
	case pm.CLILocalStatus:
		return pm.LocalStatus
	case pm.CLILocalStop:
		return pm.LocalStop
	case pm.CLIResourceList:
		return pm.ResourceList
	case pm.CLIManagementGrantPreview:
		return pm.ManagementGrantPreview
	case pm.CLIManagementGrantConfirm:
		return pm.ManagementGrantConfirm
	case pm.CLIGrantInspect:
		return pm.ManagementGrantInspect
	case pm.CLIGroupPreview:
		return pm.GroupPreview
	case pm.CLIGroupApply:
		return pm.GroupApply
	case pm.CLIGroupCurrent:
		return pm.GroupCurrent
	case pm.CLIGroupStatus:
		return pm.GroupStatus
	}
	return 0
}
func (f *p1Fixture) noteCommand(role pm.OwnerRole, command pm.Command, id string) {
	f.require(role >= pm.RoleC0 && role <= pm.RoleC2 && command >= pm.LocalStatus && command <= pm.GroupStatus, "command_ledger")
	i := int(role) - 1
	f.expectedDispatch[i][pm.ControlLimits]++
	f.expectedDispatch[i][command]++
	if command != pm.UpgradeIdentity {
		f.expectedLocal[i][command]++
	}
	if id != "" {
		f.require(len(f.expectedDigests[i]) < 168, "request_ledger_capacity")
		f.expectedDigests[i] = append(f.expectedDigests[i], sha256.Sum256([]byte(id)))
	}
}
func (f *p1Fixture) assertLocalLedger(c *p1Child) {
	var dispatched, local [20]uint16
	digests := map[[32]byte]bool{}
	for _, e := range c.prefix() {
		v := e.Observation
		switch v.Kind {
		case pm.IPCRequestDispatched:
			f.require(v.Command >= pm.ControlLimits && v.Command <= pm.GroupStatus, "dispatch_enum")
			dispatched[v.Command]++
		case pm.LocalCommand:
			f.require(v.Command >= pm.LocalStatus && v.Command <= pm.GroupStatus && v.Command != pm.UpgradeIdentity, "local_enum")
			local[v.Command]++
			if v.Command != pm.LocalStatus && v.Command != pm.LocalStop {
				f.require(v.RequestDigest != [32]byte{} && !digests[v.RequestDigest], "local_request_identity")
				digests[v.RequestDigest] = true
			} else {
				f.require(v.RequestDigest == [32]byte{}, "literal_request_digest")
			}
		case pm.IPCConnectAttempt, pm.IPCConnectCompleted:
			f.require(false, "owner_outbound_private_ipc")
		}
	}
	i := int(c.role) - 1
	f.require(dispatched == f.expectedDispatch[i] && local == f.expectedLocal[i], "negotiation_dispatch_ledger")
	for _, digest := range f.expectedDigests[i] {
		f.require(digests[digest], "driver_request_missing")
	}
}
