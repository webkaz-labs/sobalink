//go:build resource_process_native && directlan_activation_native && linux && (amd64 || arm64)

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/core"
	"github.com/webkaz-labs/sobalink/internal/resource"
	pm "github.com/webkaz-labs/sobalink/internal/resourceacceptance/processmodel"
	"github.com/webkaz-labs/sobalink/internal/resourcegrant"
	"github.com/webkaz-labs/sobalink/internal/webui"
)

const p1Selector = "^TestResourceProcessControllerRestartKeepsHistoryNoReplay$"
const p1OptIn = "reviewed-three-process-restart-v1"
const p1Tags = "resource_process_native,directlan_activation_native,ts_omit_portmapper,ts_omit_captiveportal,ts_omit_useproxy"

type p1Failure struct{ stage string }

func (f *p1Fixture) require(ok bool, stage string) {
	if !ok {
		f.stage = stage
		panic(p1Failure{stage})
	}
}

func TestResourceProcessControllerRestartKeepsHistoryNoReplay(t *testing.T) {
	opt := os.Getenv("SOBALINK_RUN_RESOURCE_PROCESS_NATIVE")
	if opt == "" {
		t.Skip("unperformed: separate reviewed Linux P1 invocation required")
	}
	if opt != p1OptIn || os.Getenv("SOBALINK_RUN_ACTIVATION_NATIVE") != "1" {
		t.Fatal("invalid P1 opt-in")
	}
	if run := flag.Lookup("test.run"); run == nil || run.Value.String() != p1Selector {
		t.Fatal("exact P1 selector required")
	}
	for _, key := range []string{"SOBALINK_RUN_RESOURCE_GROUP_CATALOG_NATIVE", "SOBALINK_RUN_RESOURCE_GROUP_RESTART_NATIVE", "SOBALINK_RUN_RESOURCE_INSPECTION_NATIVE", "SOBALINK_RUN_RESOURCE_MANAGEMENT_NATIVE", "SOBALINK_RUN_WEB_ACTIVATION_NATIVE", "SOBALINK_RUN_MANAGED_RESTART_NATIVE", "SOBALINK_RUN_PRODUCT_ACTIVATION_NATIVE", "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy", "TS_PROXY"} {
		if os.Getenv(key) != "" {
			t.Fatal("unexpected P1 environment mode")
		}
	}
	testDeadline, ok := t.Deadline()
	now := time.Now()
	// Anchor once to the already-running six-minute test budget. Startup and
	// manifest/hash work spend this original interval; none can replenish it.
	epoch := testDeadline.Unix() - 360
	outer := now.Add(time.Unix(epoch+360, 0).Sub(now))
	if !ok || now.Before(time.Unix(epoch, 0)) || !now.Before(time.Unix(epoch+10, 0)) || time.Until(testDeadline) < 350*time.Second || time.Until(testDeadline) > 360*time.Second || testDeadline.Before(outer) {
		t.Fatal("P1 original outer deadline is not covered by reviewed test budget")
	}
	f := &p1Fixture{epoch: epoch, setup: now.Add(time.Unix(epoch+90, 0).Sub(now)), work: now.Add(time.Unix(epoch+300, 0).Sub(now)), cleanup: now.Add(time.Unix(epoch+330, 0).Sub(now)), outer: outer}
	defer func() {
		recovered := recover()
		if recovered != nil {
			if x, ok := recovered.(p1Failure); ok {
				f.stage = x.stage
			} else {
				f.stage = "unexpected_panic"
			}
		}
		if !f.success {
			f.closeFailure()
		}
		joined := f.closeFixture()
		if !joined {
			f.success = false
			f.stage = "observer_cleanup_unjoined"
		}
		if f.success && joined && !f.removeSuccessfulRoot() {
			f.success = false
			f.stage = "owned_root_removal_failed"
		}
		receipt := f.finalReceipt()
		if f.success && joined {
			t.Log(string(receipt))
		} else {
			t.Log(string(receipt))
			t.Error("P1 failed; protected evidence retained; stage=" + f.stage)
		}
	}()
	f.supervisorGroup = syscall.Getpgrp()
	f.require(f.supervisorGroup > 0, "supervisor_group")
	f.readManifest()
	f.require(f.manifest.Target == "linux-"+runtime.GOARCH, "manifest_target")
	f.seed()
	f.scenario()
	f.success = true
}

func (f *p1Fixture) scenario() {
	f.startOwner(pm.RoleC0)
	f.startOwner(pm.RoleA0)
	f.startOwner(pm.RoleB0)
	for n := 1; n <= 6; n++ {
		f.cli(n)
	}
	f.assertInitialFiles()
	f.assertFileInventory()
	f.checkpointGroup(0)
	f.pair(0)
	f.checkpointGroup(1)
	f.cli(7)
	f.startOwner(pm.RoleC1)
	f.cli(8)
	f.cli(9)
	f.pair(1)
	for n := 10; n <= 15; n++ {
		f.cli(n)
	}
	f.verification(5)
	f.verification(6)
	f.checkpointGroup(2)
	f.writeSelections()
	f.cli(16)
	f.checkpointGroup(3)
	f.dryFiles = f.protectedFiles()
	for n := 17; n <= 19; n++ {
		f.cli(n)
	}
	f.checkpointGroup(4)
	f.require(reflect.DeepEqual(f.dryFiles, f.protectedFiles()), "dry_run_state_changed")
	f.cli(20)
	f.assertTargetJournals()
	f.cli(21)
	f.cli(22)
	f.cli(23)
	f.cli(24)
	f.verification(7)
	f.verification(8)
	f.checkpointGroup(5)
	f.assertManagementTotals()
	f.captureAuthority()
	f.cli(25)
	f.startOwner(pm.RoleC2)
	for n := 26; n <= 33; n++ {
		f.cli(n)
	}
	for n := 9; n <= 12; n++ {
		f.verification(n)
	}
	f.checkpointGroup(6)
	f.assertAuthority()
	f.observeMaintenance()
	for n := 13; n <= 15; n++ {
		f.verification(n)
	}
	f.checkpointGroup(7)
	for n := 34; n <= 37; n++ {
		f.cli(n)
	}
	f.checkpointGroup(8)
	f.cli(38)
	f.cli(39)
	f.cli(40)
	f.assertAuthority()
	f.assertTargetJournals()
	f.assertManagementTotals()
	f.assertFinalEvidence()
	f.assertFileInventory()
}

func (f *p1Fixture) cli(n int) {
	f.require(n == f.cliCount+1 && n >= 1 && n <= 40 && f.driver == nil && f.activeCLI == nil, "cli_schedule")
	role, op, _, _, dry := f.cliArguments(n)
	budget := 8 * time.Second
	switch n {
	case 16, 20, 21, 29, 31, 32:
		budget = 35 * time.Second
	case 33:
		budget = 20 * time.Second
	}
	phase := f.work
	if n <= 9 {
		phase = f.setup
	}
	if n >= 38 {
		phase = f.cleanup
		f.require(time.Now().Add(time.Duration(f.liveOwners())*8*time.Second+6*time.Second).Before(phase), "cleanup_reservation")
	}
	until := f.admit(budget, phase)
	if !dry {
		f.noteCommand(role, p1CLICommand(op), "")
	}
	child := f.launch(pm.Invocation{Mode: pm.CLI, Ordinal: uint8(n), Operation: op}, role, until)
	f.clis[n-1], f.activeCLI = child, child
	f.cliCount = n
	f.joinChild(child, until)
	f.activeCLI = nil
	f.checkCLIStream(child, n)
	raw := f.readOutput(child, false, 64<<10)
	f.assertCLI(n, role, raw, f.readOutput(child, true, 64<<10), child.exitCode)
	if op == pm.CLILocalStop {
		f.joinChild(f.owner(role), until)
	}
	f.healthy()
}

func (f *p1Fixture) cliArguments(n int) (pm.OwnerRole, pm.CLIOperation, []string, string, bool) {
	role, locale := pm.RoleC1, "en"
	if n <= 7 {
		role = pm.RoleC0
	}
	if n >= 26 {
		role = pm.RoleC2
	}
	switch n {
	case 2, 5, 10, 11, 12, 39:
		role = pm.RoleA0
	case 3, 6, 13, 14, 15, 40:
		role = pm.RoleB0
	}
	if n == 35 || n == 37 {
		locale = "ja"
	}
	switch n {
	case 1, 2, 3, 8, 26:
		return role, pm.CLIResourceList, []string{"resource", "list", "--json"}, locale, false
	case 4, 5, 6, 9, 22, 27:
		return role, pm.CLILocalStatus, []string{"status", "--json"}, locale, false
	case 7, 25, 38, 39, 40:
		return role, pm.CLILocalStop, []string{"stop", "--json"}, locale, false
	case 10, 13:
		i := p1RoleIndex(role)
		return role, pm.CLIManagementGrantPreview, []string{"resource", "grant", "preview", "--management", "--id", f.ids[i], "--peer", f.identities[0].PublicKey(), "--expires-at", time.Unix(f.epoch+360, 0).UTC().Format(time.RFC3339), "--json"}, locale, false
	case 11, 14:
		name := "grant-a.json"
		if n == 14 {
			name = "grant-b.json"
		}
		return role, pm.CLIManagementGrantConfirm, []string{"resource", "grant", "confirm", "--management", "--review-file", filepath.Join(f.root, "inputs", name), "--confirm", "--json"}, locale, false
	case 12, 15:
		return role, pm.CLIGrantInspect, []string{"resource", "grant", "inspect", "--id", f.ids[p1RoleIndex(role)], "--json"}, locale, false
	case 16, 19, 21:
		name := "selection-applied.json"
		if n == 21 {
			name = "selection-unused.json"
		}
		op := pm.CLIGroupPreview
		if n == 19 {
			op = pm.CLIDryRunGroupPreview
		}
		return role, op, []string{"resource", "group", "preview", "--selection-file", filepath.Join(f.root, "inputs", name), "--json"}, locale, n == 19
	case 17, 20, 29, 31, 32, 33:
		in := f.apply
		if n == 29 {
			in = f.unusedApply
		}
		if n == 32 {
			sum := sha256.Sum256([]byte("p1-changed-revision-v1\x00" + in.ReviewRevision))
			in.ReviewRevision = hex.EncodeToString(sum[:])
			f.require(in.ReviewRevision != f.apply.ReviewRevision, "negative_revision")
		}
		peers := in.ExecutionPeers
		if n == 33 {
			peers = []string{f.identities[1].PublicKey()}
		}
		f.require(in.Validate() == nil, "apply_input")
		args := []string{"resource", "group", "apply", "--review-id", in.ReviewID, "--revision", in.ReviewRevision}
		for _, peer := range peers {
			args = append(args, "--execute-peer", peer)
		}
		args = append(args, "--confirm", "--json")
		op := pm.CLIGroupApply
		if n == 17 {
			op = pm.CLIDryRunGroupApply
		}
		return role, op, args, locale, n == 17
	case 23, 28:
		return role, pm.CLIGroupCurrent, []string{"resource", "group", "review", "current", "--json"}, locale, false
	case 18, 24, 30, 34, 35, 36, 37:
		args := []string{"resource", "group", "status", "--run-id", f.apply.ReviewID}
		if n != 36 && n != 37 {
			args = append(args, "--json")
		}
		op := pm.CLIGroupStatus
		if n == 18 {
			op = pm.CLIDryRunGroupStatus
		}
		return role, op, args, locale, n == 18
	}
	f.require(false, "unknown_cli_ordinal")
	return 0, 0, nil, "", false
}

// Both the limits negotiation and actual action use the unchanged local CLI
// transport function. No observer input carries any command or product data.
type p1DriverSelection struct {
	verification, pair, side, kind, poll int
	intent                               core.UpgradeIntent
}

func p1VerificationRole(n int) pm.OwnerRole {
	switch n {
	case 1:
		return pm.RoleC0
	case 3:
		return pm.RoleC1
	case 4, 6, 8, 10, 12, 15:
		return pm.RoleB0
	case 13:
		return pm.RoleC2
	default:
		return pm.RoleA0
	}
}
func (f *p1Fixture) driverDispatch(selection p1DriverSelection, until time.Time) json.RawMessage {
	f.require(f.driver == nil && f.activeCLI == nil, "driver_serialization")
	var role pm.OwnerRole
	var name, id string
	var payload any
	var observed pm.Command
	if n := selection.verification; n != 0 {
		f.require(n >= 1 && n <= 15 && !f.verified[n-1] && selection.pair == 0 && selection.side == 0 && selection.kind == 0 && selection.poll == 0 && selection.intent == (core.UpgradeIntent{}), "verification_union")
		role = p1VerificationRole(n)
		switch {
		case n <= 4:
			name = lifecycleIdentityCommand
			observed = pm.UpgradeIdentity
		case n >= 13:
			name = "status"
			observed = pm.LocalStatus
		case n == 9 || n == 10:
			name = "resource.grant.inspect"
			observed = pm.ManagementGrantInspect
			payload = struct {
				Target resource.Target `json:"target"`
			}{resource.Target{SchemaVersion: 1, ResourceID: f.ids[p1RoleIndex(role)]}}
		default:
			name = "resource.inspect"
			observed = pm.ResourceInspect
			payload = resource.Target{SchemaVersion: 1, ResourceID: f.ids[p1RoleIndex(role)]}
		}
		if payload != nil {
			id = "p1-verification-" + strconv.Itoa(n)
		}
	} else {
		pair, side, kind, poll := selection.pair, selection.side, selection.kind, selection.poll
		f.require(pair == f.pairCount && pair <= 1 && side >= 0 && side <= 1 && kind >= 0 && kind <= 2 && poll >= 0 && poll < 40, "upgrade_union")
		role = pm.RoleC0
		if pair == 1 {
			role = pm.RoleC1
		}
		other := pair + 1
		if side == 1 {
			other = 0
			role = pm.RoleA0
			if pair == 1 {
				role = pm.RoleB0
			}
		}
		name = "direct-lan.upgrade.review"
		observed = pm.UpgradeReview
		if kind == 1 {
			name = "direct-lan.upgrade.run"
			observed = pm.UpgradeRun
		}
		if kind == 2 {
			name = "direct-lan.upgrade.status"
			observed = pm.UpgradeStatus
		}
		if kind == 2 {
			f.require(selection.intent == (core.UpgradeIntent{}), "status_empty_intent")
		} else {
			f.require(poll == 0 && selection.intent.PeerID == f.identities[other].PublicKey() && selection.intent.Deadline == time.Unix(f.epoch+90, 0).UTC().Format(time.RFC3339) && (kind == 0 && selection.intent.ExpectedRevision == "" || kind == 1 && resource.ValidDigest(selection.intent.ExpectedRevision)), "upgrade_exact_intent")
		}
		payload = selection.intent
		id = "p1-upgrade-" + strconv.Itoa(pair) + "-" + strconv.Itoa(side) + "-" + strconv.Itoa(kind) + "-" + strconv.Itoa(poll)
	}
	request := name
	if payload != nil {
		raw, err := json.Marshal(payload)
		f.require(err == nil, "driver_payload")
		command, err := json.Marshal(webui.Command{RequestID: id, Name: name, Payload: raw})
		f.require(err == nil && len(command) <= 8192, "driver_command")
		request = string(command)
	}
	f.noteCommand(role, observed, id)
	ctx, cancel := context.WithDeadline(context.Background(), until)
	task := &p1Driver{cancel: cancel, done: make(chan struct{})}
	f.driver = task
	go func() { defer close(task.done); task.err = controlCall(ctx, f.roleDir(role), request, &task.raw) }()
	f.require(p1Wait(task.done, until), "driver_unjoined")
	cancel()
	f.driver = nil
	f.require(task.err == nil && len(task.raw) > 0 && len(task.raw) <= 64<<10, "driver_reply")
	f.healthy()
	return task.raw
}
func (f *p1Fixture) upgradeRequest(pair, side, kind, poll int, in core.UpgradeIntent) json.RawMessage {
	budget := 8 * time.Second
	if kind == 2 {
		budget = time.Second
	}
	return f.driverDispatch(p1DriverSelection{pair: pair, side: side, kind: kind, poll: poll, intent: in}, f.admit(budget, f.setup))
}

func (f *p1Fixture) pair(pair int) {
	f.require(pair == f.pairCount && pair < 2, "pair_schedule")
	left, right := pm.RoleC0, pm.RoleA0
	if pair == 1 {
		left, right = pm.RoleC1, pm.RoleB0
	}
	f.verification(1 + pair*2)
	f.verification(2 + pair*2)
	roles := [2]pm.OwnerRole{left, right}
	var inputs [2]core.UpgradeIntent
	for side := 0; side < 2; side++ {
		in := core.UpgradeIntent{PeerID: f.identities[p1RoleIndex(roles[1-side])].PublicKey(), Deadline: time.Unix(f.epoch+90, 0).UTC().Format(time.RFC3339)}
		raw := f.upgradeRequest(pair, side, 0, 0, in)
		var review core.UpgradeReview
		f.decode(raw, &review, 8192)
		f.require(!review.RestartRequired && !review.ResumePreparation && review.PeerID == in.PeerID && review.Deadline == in.Deadline && resource.ValidDigest(review.Revision) && review.LocalEndpoint == f.endpoints[p1RoleIndex(roles[side])].String() && review.PeerEndpoint == f.endpoints[p1RoleIndex(roles[1-side])].String(), "upgrade_review")
		in.ExpectedRevision = review.Revision
		inputs[side] = in
	}
	for side := 0; side < 2; side++ {
		i := p1RoleIndex(roles[side])
		if !f.released[i] {
			f.require(f.reservations[i].Close() == nil, "reservation_close")
			f.released[i] = true
		}
		var progress core.UpgradeProgress
		f.decode(f.upgradeRequest(pair, side, 1, 0, inputs[side]), &progress, 8192)
		f.assertUpgradeProgress(progress, inputs[side], false)
	}
	var done [2]bool
	var previous [2]time.Time
	for poll := 0; poll < 40 && (!done[0] || !done[1]); poll++ {
		for side := 0; side < 2; side++ {
			if done[side] {
				continue
			}
			spacing := previous[side].Add(250 * time.Millisecond)
			if time.Now().Before(spacing) {
				f.require(spacing.Before(f.setup), "upgrade_spacing_budget")
				timer := time.NewTimer(time.Until(spacing))
				<-timer.C
			}
			previous[side] = time.Now()
			var p core.UpgradeProgress
			f.decode(f.upgradeRequest(pair, side, 2, poll, core.UpgradeIntent{}), &p, 8192)
			f.assertUpgradeProgress(p, inputs[side], true)
			done[side] = p.State == "network-started"
		}
	}
	f.require(done[0] && done[1], "upgrade_not_terminal")
	f.pairCount++
	f.assertPairs(pair + 1)
}

func (f *p1Fixture) assertUpgradeProgress(p core.UpgradeProgress, in core.UpgradeIntent, poll bool) {
	f.require(!p.RestartRequired && p.ErrorCode == "" && p.Error == "" && p.PeerID == in.PeerID && p.Deadline == in.Deadline, "upgrade_progress_binding")
	switch p.State {
	case "preparing", "exchanging", "connecting", "confirming", "network-started":
	default:
		f.require(false, "upgrade_terminal_failure")
	}
}

func (f *p1Fixture) verification(n int) {
	f.require(n >= 1 && n <= 15 && !f.verified[n-1], "verification_schedule")
	role := p1VerificationRole(n)
	phase := f.work
	if n <= 4 {
		phase = f.setup
	}
	raw := f.driverDispatch(p1DriverSelection{verification: n}, f.admit(8*time.Second, phase))
	f.verified[n-1] = true
	if n <= 4 {
		var identity upgradeIdentity
		f.decode(raw, &identity, 8192)
		f.require(identity.ProcessID == f.owner(role).cmd.Process.Pid && identity.Offline && !identity.AttemptedNetwork && !identity.NetworkReady && identity.Executable == f.manifest.Binary && resource.ValidDigest(identity.Instance), "lifecycle_identity")
		return
	}
	if n >= 13 {
		f.assertStatus(role, raw, true)
		return
	}
	if n == 9 || n == 10 {
		var view resourcegrant.LocalGrantView
		f.decode(raw, &view, 8192)
		f.assertGrant(p1RoleIndex(role)-1, view)
		return
	}
	var descriptor resource.Descriptor
	f.decode(raw, &descriptor, 8192)
	i := p1RoleIndex(role) - 1
	f.assertDescriptor(descriptor)
	f.require(descriptor.Target.ResourceID == f.ids[i+1], "resource_inspect")
	if n == 5 || n == 6 {
		f.beforeDescriptors[i] = descriptor
	} else {
		f.require(reflect.DeepEqual(descriptor.Requested, f.wanted[i]), "resource_inspect_settings")
	}
}
