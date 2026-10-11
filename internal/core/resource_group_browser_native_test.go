//go:build resource_group_browser_native && resource_browser_native && resource_group_catalog_native && resource_management_native && resource_inspection_native && directlan_activation_native && linux

package core

// One real browser retrieves the actual N4-prepared two-target review, confirms,
// applies once and reads local status. Draft/preview authoring is not covered.
import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"sort"
	"syscall"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/resource"
	"github.com/webkaz-labs/sobalink/internal/resourceacceptance"
	"github.com/webkaz-labs/sobalink/internal/resourcegrant"
	"github.com/webkaz-labs/sobalink/internal/resourcegroup"
	"github.com/webkaz-labs/sobalink/web"
	"golang.org/x/sys/unix"
)

const resourceGroupBrowserSelector = "^TestResourceGroupBrowserNativeFixedTwoTargetApply$"

// This in-flight structural observation takes only the established snapshot
// locks, never waits on Core.op or invokes a command while the browser runs.
// activationCurrent requires Core.op; the unchanged N4 ownership assertion
// checks it before launch and after join. Provider/grant/journal checks follow join.
func resourceGroupBrowserManaged(f *resourceGroupCatalogNative) error {
	if f.ctx.Err() != nil {
		return errors.New("three-Core work budget ended")
	}
	for i, c := range f.cores {
		if c == nil || c.ctx.Err() != nil {
			return errors.New("three-Core owner ended")
		}
		backend, ok := c.nodeCopy().(*directLANBackend)
		if !ok || backend == nil || backend.Node == nil || backend.Node.Endpoint() != f.endpoints[i] || backend.ctx.Err() != nil {
			return errors.New("three-Core managed endpoint changed")
		}
		owner := backend.currentCompletion()
		if owner == nil || !owner.coreCurrent("") {
			return errors.New("three-Core managed ownership changed")
		}
		c.mu.RLock()
		valid := !c.closing && c.contextControl == nil && c.profile.Settings.Network == "direct-lan" && len(c.active) == 0 && len(c.proxies) == 0 && len(c.outgoing) == 0 && len(c.profile.Peers) == 0
		c.mu.RUnlock()
		if !valid {
			return errors.New("three-Core structural oracle failed")
		}
	}
	return nil
}

// Keep B1's scope unchanged. This exact sibling preserves its ownership,
// cancellation, inventory and deadlines, changing only the fixed child entry
// and the managed-three-Core structural oracle.
func resourceGroupBrowserRunScope(f *resourceGroupCatalogNative, root, webDir, node, chromium string, entered time.Time, cancellation <-chan os.Signal) productScopeProof {
	proof := productScopeProof{Schema: 1, PlaywrightExit: -1}
	identity, err := productScopeStat(os.Getpid())
	if err != nil {
		proof.Errors++
		return proof
	}
	if _, err = productScopeChildren(identity); err != nil {
		proof.Errors++
		return proof
	}
	selfFD, err := unix.PidfdOpen(identity.pid, 0)
	if err != nil {
		proof.Errors++
		return proof
	}
	err = unix.PidfdSendSignal(selfFD, 0, nil, 0)
	_ = unix.Close(selfFD)
	if err != nil {
		proof.Errors++
		return proof
	}
	log, err := os.OpenFile(filepath.Join(root, "browser-private.log"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		proof.Errors++
		return proof
	}
	defer log.Close()
	command := exec.Command(node, filepath.Join(webDir, "node_modules", "@playwright", "test", "cli.js"), "test", "--config", filepath.Join(webDir, "playwright.resource-group-native.config.mjs"), "--grep", "(?:^| )resource-native-group-two-target-apply-en$", "--workers", "1", "--retries", "0")
	command.Dir = webDir
	command.Env = []string{
		"PATH=/usr/bin:/bin", "HOME=" + filepath.Join(root, "home"), "TMPDIR=" + filepath.Join(root, "tmp"), "TMP=" + filepath.Join(root, "tmp"), "TEMP=" + filepath.Join(root, "tmp"),
		"XDG_CONFIG_HOME=" + filepath.Join(root, "config"), "XDG_CACHE_HOME=" + filepath.Join(root, "cache"), "XDG_DATA_HOME=" + filepath.Join(root, "data"), "XDG_RUNTIME_DIR=" + filepath.Join(root, "runtime"),
		"LANG=C.UTF-8", "LC_ALL=C.UTF-8", "PLAYWRIGHT_NO_COPY_PROMPT=1", "PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1",
		"SOBALINK_RUN_RESOURCE_GROUP_BROWSER_NATIVE=reviewed-three-core-group-apply-v1", "SOBA_RESOURCE_BROWSER_PRIVATE_ROOT=" + root, "SOBA_RESOURCE_BROWSER_CHROMIUM=" + chromium,
	}
	command.Stdout, command.Stderr = log, log
	if cancelled, cancelErr := productScopeCancelled(root, cancellation); cancelled || cancelErr != nil {
		proof.Forced = true
		if cancelErr != nil {
			proof.Errors++
		}
		proof.Complete = true
		proof.DescendantsReaped = true
		return proof
	}
	if time.Since(entered) >= 15*time.Second {
		proof.DeadlineExceeded = true
		return proof
	}
	if err := command.Start(); err != nil {
		proof.Errors++
		proof.Complete = true
		proof.DescendantsReaped = true
		return proof
	}
	primary := command.Process.Pid
	_ = command.Process.Release()
	known := make(map[int]*productScopeChild)
	defer func() {
		for _, child := range known {
			_ = unix.Close(child.fd)
		}
	}()
	primaryExited, reportedUnjoined := false, false
	forcePhase := 0
	var forcedAt time.Time
	for {
		if cancelled, cancelErr := productScopeCancelled(root, cancellation); cancelled || cancelErr != nil {
			proof.Forced = true
			if cancelErr != nil {
				proof.Errors++
			}
		}
		if err := productScopeDiscover(identity, known, &proof); err != nil {
			proof.Errors++
			proof.Forced = true
		}
		// Node parent + one worker + at most 24 browser descendants/owners. The
		// unchanged inherited helper's 512 guard remains only an emergency ceiling.
		if len(known) > 27 || proof.Observed > 64 {
			proof.Errors++
			proof.Forced = true
		}
		if err := resourceGroupBrowserManaged(f); err != nil {
			proof.Errors++
			proof.Forced = true
		}
		if _, err := resourceBrowserInventory(root, 256<<20, false); err != nil {
			proof.Errors++
			proof.Forced = true
		}
		if stat, err := log.Stat(); err != nil || stat.Size() > 1<<20 {
			proof.Errors++
			proof.Forced = true
		}
		noChildren := false
		for {
			var status unix.WaitStatus
			pid, waitErr := unix.Wait4(-1, &status, unix.WNOHANG, nil)
			if errors.Is(waitErr, unix.EINTR) {
				continue
			}
			if errors.Is(waitErr, unix.ECHILD) {
				noChildren = true
				break
			}
			if waitErr != nil {
				proof.Errors++
				proof.Forced = true
				break
			}
			if pid == 0 {
				break
			}
			proof.Reaped++
			if pid == primary && !primaryExited {
				primaryExited = true
				if status.Exited() {
					proof.PlaywrightExit = status.ExitStatus()
				} else {
					proof.PlaywrightExit = 128 + int(status.Signal())
				}
			}
		}
		productScopePrune(known)
		elapsed := time.Since(entered)
		if elapsed >= 90*time.Second {
			proof.DeadlineExceeded = true
		}
		if noChildren && primaryExited {
			proof.Complete = true
			proof.DescendantsReaped = true
			return proof
		}
		if (proof.Forced || proof.DeadlineExceeded) && forcePhase == 0 {
			proof.Forced = true
			forcePhase = 1
			forcedAt = time.Now()
			if productScopeSignal(known, unix.SIGTERM) != nil {
				proof.Errors++
			}
		}
		if elapsed >= 100*time.Second || (!forcedAt.IsZero() && time.Since(forcedAt) >= 10*time.Second) || proof.Errors > 0 {
			proof.Forced = true
			forcePhase = 2
			if productScopeSignal(known, unix.SIGSTOP) != nil {
				proof.Errors++
			}
			if productScopeSignal(known, unix.SIGKILL) != nil {
				proof.Errors++
			}
		} else if forcePhase == 1 {
			if productScopeSignal(known, unix.SIGTERM) != nil {
				proof.Errors++
			}
		}
		if (elapsed >= 110*time.Second || (!forcedAt.IsZero() && time.Since(forcedAt) >= 20*time.Second)) && !reportedUnjoined {
			reportedUnjoined = true
			proof.Forced = true
			_ = resourceBrowserWrite(filepath.Join(root, "unjoined-failure.json"), proof)
			fmt.Fprintln(os.Stderr, "FAIL: browser descendants unjoined; private evidence retained; hosted timeout is containment only")
		}
		// We keep owning children until joined or the finite hosted Go/job boundary
		// terminates the FAILED test. That boundary cannot generate Complete=true.
		pause := 250 * time.Millisecond
		if forcePhase != 0 {
			pause = 25 * time.Millisecond
		}
		time.Sleep(pause)
	}
}

type resourceGroupBrowserHTTP struct {
	Schema           int  `json:"schema"`
	Requests         int  `json:"requests"`
	StateRequests    int  `json:"stateRequests"`
	StateReady       int  `json:"stateReady"`
	LoginRequests    int  `json:"loginRequests"`
	CurrentRequests  int  `json:"currentRequests"`
	CurrentResponses int  `json:"currentResponses"`
	ApplyRequests    int  `json:"applyRequests"`
	ApplyResponses   int  `json:"applyResponses"`
	StatusRequests   int  `json:"statusRequests"`
	StatusResponses  int  `json:"statusResponses"`
	Blocked          int  `json:"blocked"`
	Errors           int  `json:"errors"`
	Captures         int  `json:"captures"`
	Completed        bool `json:"completed"`
	RequestsJoined   bool `json:"requestsJoined"`
}

type resourceGroupBrowserResult struct {
	Schema                int    `json:"schema"`
	Accepted              bool   `json:"accepted"`
	BrowserJoined         bool   `json:"browserJoined"`
	CoreClosed            bool   `json:"coreClosed"`
	LockClosed            bool   `json:"lockClosed"`
	ProductVerified       bool   `json:"productVerified"`
	NativeEventsVerified  bool   `json:"nativeEventsVerified"`
	HTTPOnly              bool   `json:"httpCountsOnly"`
	DraftAuthoringCovered bool   `json:"draftAuthoringCovered"`
	PixelReviewPerformed  bool   `json:"pixelReviewPerformed"`
	EventCount            int    `json:"eventCount"`
	PreviewTargets        int    `json:"previewTargets"`
	AppliedTargets        int    `json:"appliedTargets"`
	ProviderAdmissions    int    `json:"providerAdmissions"`
	FailureStage          string `json:"failureStage"`
}

func resourceGroupBrowserReplies(root string, prepared resourcegroup.PreparedView) (resourcegroup.RunView, string) {
	var empty resourcegroup.RunView
	var summary resourceBrowserSummary
	if resourceBrowserRead(filepath.Join(root, "browser-summary.json"), 4096, &summary) != nil || summary.Schema != 3 || summary.Expected != 1 || summary.Observed != 1 || summary.Passed != 1 || summary.Errors != 0 || !summary.SelectionValid || summary.Unexpected || !summary.Accepted || summary.Diagnostic != (resourceBrowserDiagnostic{Status: "none", Unit: "none", Category: "none"}) {
		return empty, "browser_summary"
	}
	var counts resourceGroupBrowserHTTP
	if resourceBrowserRead(filepath.Join(root, "http-observations.json"), 4096, &counts) != nil || counts.Schema != 1 || counts.Requests < 1 || counts.Requests > 256 || counts.StateRequests < 1 || counts.StateRequests > 96 || counts.StateReady < 1 || counts.StateReady > counts.StateRequests || counts.LoginRequests != 1 || counts.CurrentRequests != 1 || counts.CurrentResponses != 1 || counts.ApplyRequests != 1 || counts.ApplyResponses != 1 || counts.StatusRequests != 1 || counts.StatusResponses != 1 || counts.Blocked != 0 || counts.Errors != 0 || counts.Captures != 0 || !counts.Completed || !counts.RequestsJoined {
		return empty, "http_observations"
	}
	var raw json.RawMessage
	if resourceBrowserRead(filepath.Join(root, "current-result.json"), resourcegroup.MaxLocalResponseBytes, &raw) != nil {
		return empty, "current_review"
	}
	current, err := resourcegroup.DecodeCurrentReviewView(raw)
	if err != nil || current.State != resourcegroup.CurrentReviewCurrent || current.Prepared == nil || !reflect.DeepEqual(*current.Prepared, prepared) {
		return empty, "current_review"
	}
	if resourceBrowserRead(filepath.Join(root, "apply-request.json"), 4096, &raw) != nil {
		return empty, "apply_request"
	}
	input, err := resourcegroup.DecodeApplyInput(raw)
	want := resourcegroup.ApplyInput{SchemaVersion: 1, ReviewID: prepared.ReviewID, ReviewRevision: prepared.Review.Revision, ExecutionPeers: prepared.Review.ExecutionPeers, Confirm: true}
	if err != nil || !reflect.DeepEqual(input, want) {
		return empty, "apply_request"
	}
	if resourceBrowserRead(filepath.Join(root, "apply-result.json"), resourcegroup.MaxLocalResponseBytes, &raw) != nil {
		return empty, "group_response"
	}
	run, err := resourcegroup.DecodeRunView(raw)
	if err != nil || run.RunID != prepared.ReviewID || !reflect.DeepEqual(run.Review, prepared.Review) || run.Activity != resourcegroup.ActivityIdle || run.LocalDurability != resourcegroup.LocalDurable || !run.Summary.AllApplied || run.Summary.Applied != 2 || run.Summary.Executable != 2 || len(run.Evidence.Members) != 2 {
		return empty, "group_response"
	}
	if resourceBrowserRead(filepath.Join(root, "status-result.json"), resourcegroup.MaxLocalResponseBytes, &raw) != nil {
		return empty, "group_response"
	}
	status, err := resourcegroup.DecodeRunView(raw)
	if err != nil || !reflect.DeepEqual(status, run) {
		return empty, "group_response"
	}
	return run, "none"
}

func TestResourceGroupBrowserNativeFixedTwoTargetApply(t *testing.T) {
	// Exact independent guard before fixture state, signals, subreaper or Core.
	if os.Getenv("SOBALINK_RUN_RESOURCE_GROUP_BROWSER_NATIVE") != "reviewed-three-core-group-apply-v1" {
		t.Skip("requires exact reviewed three-Core browser group invocation")
	}
	runFlag, parallel, timeout, count := flag.Lookup("test.run"), flag.Lookup("test.parallel"), flag.Lookup("test.timeout"), flag.Lookup("test.count")
	if runFlag == nil || runFlag.Value.String() != resourceGroupBrowserSelector || parallel == nil || parallel.Value.String() != "1" || count == nil || count.Value.String() != "1" || timeout == nil || timeout.Value.String() != "4m0s" {
		t.Fatal("exact single browser group selector and four-minute bounds required")
	}
	deadline, ok := t.Deadline()
	if !ok || time.Until(deadline) > 4*time.Minute || time.Until(deadline) < 200*time.Second || !directLANNetstackReady || os.Getenv("SOBALINK_RUN_ACTIVATION_NATIVE") != "1" || os.Getenv("SOBA_RESOURCE_BROWSER_SCOPE") != "hosted-job-v1" {
		t.Fatal("reviewed native/browser ownership prerequisites missing")
	}
	for _, name := range []string{"SOBALINK_RUN_RESOURCE_BROWSER_NATIVE", "SOBALINK_RUN_RESOURCE_INSPECTION_NATIVE", "SOBALINK_RUN_RESOURCE_MANAGEMENT_NATIVE", "SOBALINK_RUN_RESOURCE_GROUP_CATALOG_NATIVE", "SOBALINK_RUN_RESOURCE_GROUP_PREWRITE_NATIVE", "SOBALINK_RUN_RESOURCE_GROUP_PROVIDER_PREWRITE_NATIVE", "SOBALINK_RUN_RESOURCE_GROUP_RESTART_NATIVE", "SOBALINK_RUN_RESOURCE_PROCESS_NATIVE", "SOBALINK_RUN_WEB_ACTIVATION_NATIVE", "SOBALINK_RUN_PRODUCT_ACTIVATION_NATIVE", "SOBALINK_RUN_MANAGED_RESTART_NATIVE", "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy", "TS_PROXY", "DEBUG", "PWDEBUG", "NODE_OPTIONS", "NODE_PATH"} {
		if os.Getenv(name) != "" {
			t.Fatal("browser group invocation contains unrelated opt-in or runtime override")
		}
	}
	root, webDir := os.Getenv("SOBA_RESOURCE_BROWSER_PRIVATE_ROOT"), os.Getenv("SOBA_RESOURCE_BROWSER_WEB_DIR")
	if resourceBrowserPrivateDir(root) != nil || !filepath.IsAbs(webDir) || filepath.Clean(webDir) != webDir {
		t.Fatal("exact private browser root and source required")
	}
	resolved, err := filepath.EvalSymlinks(webDir)
	if err != nil || resolved != webDir {
		t.Fatal("browser source directory alias forbidden")
	}
	node, chromium := os.Getenv("SOBA_RESOURCE_BROWSER_NODE"), os.Getenv("SOBA_RESOURCE_BROWSER_CHROMIUM")
	nodeHash, chromiumHash := os.Getenv("SOBA_RESOURCE_BROWSER_NODE_SHA256"), os.Getenv("SOBA_RESOURCE_BROWSER_CHROMIUM_SHA256")
	if !filepath.IsAbs(node) || !filepath.IsAbs(chromium) || !resource.ValidDigest(nodeHash) || !resource.ValidDigest(chromiumHash) || resourceBrowserVerifyBinary(node, nodeHash) != nil || resourceBrowserVerifyBinary(chromium, chromiumHash) != nil {
		t.Fatal("frozen browser executables do not match")
	}
	for _, name := range []string{"session.json", "current-result.json", "apply-request.json", "apply-result.json", "status-result.json", "http-observations.json", "browser-summary.json", "scope-proof.json", "native-result.json", "unjoined-failure.json", "browser-private.log", "resource-browser-cancel"} {
		if _, err := os.Lstat(filepath.Join(root, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("browser group root is not a fresh invocation")
		}
	}

	f := newResourceGroupCatalogNativeAfterGuard(t)
	workUntil, _ := f.ctx.Deadline()
	verifyUntil := workUntil.Add(-15 * time.Second)
	receipt := resourceGroupBrowserResult{Schema: 1, FailureStage: "product_invariants"}
	var proof productScopeProof
	// Register after the N4 cleanup: this joins it first, then writes only a
	// closed receipt. Its once-owned cleanup never releases a live Core lock.
	t.Cleanup(func() {
		// Retain the already inventoried private product root on both outcomes.
		// Later deadline/receipt failures must not lose it. Only inherited root
		// deletion is suppressed; all owners and reservations still join below.
		productRoot := f.root
		f.root = ""
		f.close()
		f.root = productRoot
		joined := true
		for _, owner := range [4]*resourceInspectionNativeOwner{f.owners[0], f.owners[1], f.owners[2], f.retired} {
			if owner == nil {
				joined = false
				continue
			}
			select {
			case <-owner.done:
				if owner.err != nil {
					joined = false
				}
			default:
				joined = false
			}
		}
		receipt.BrowserJoined = proof.Complete && proof.DescendantsReaped
		receipt.CoreClosed, receipt.LockClosed = joined, joined
		if !joined {
			receipt.FailureStage = "core_close"
		}
		if !time.Now().Before(workUntil) {
			receipt.FailureStage = "native_deadline"
		}
		receipt.Accepted = !t.Failed() && receipt.FailureStage == "none" && receipt.BrowserJoined && joined && receipt.ProductVerified && receipt.NativeEventsVerified
		if !receipt.Accepted && !t.Failed() {
			t.Error("real group browser acceptance incomplete")
		}
		if resourceBrowserWrite(filepath.Join(root, "native-result.json"), receipt) != nil {
			t.Error("private group browser result receipt failed")
		}
		if receipt.Accepted && !t.Failed() {
			t.Log("PASS: real prepared two-target browser apply and local status; provider evidence and owned cleanup joined; draft authoring not covered")
		}
	})
	grants := [2]resourcegrant.Record{f.confirm(1), f.confirm(2)}
	before := [3][3]resourceGroupCatalogFile{f.files(0), f.files(1), f.files(2)}
	policies := [2]capacity.Policy{f.cores[1].capacityPolicy(), f.cores[2].capacityPolicy()}
	wanted := [2]resource.Settings{resourceManagementNativeSettings(3, 1), resourceManagementNativeSettings(4, 2)}
	template, err := resourcegroup.NewTemplate(wanted[0])
	if err != nil {
		t.Fatal("fixed synthetic browser template invalid")
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
		t.Fatal("real fixed two-target browser review unavailable")
	}
	for i, row := range prepared.Review.Rows {
		if row.State != resourcegroup.ReviewReady || row.PeerKey != selection.Members[i].PeerKey || prepared.Review.ExecutionPeers[i] != row.PeerKey || row.Reply == nil || row.Reply.Preview == nil || row.Reply.ManagementSelector != selection.Members[i].Selector || !reflect.DeepEqual(row.Reply.Preview.Requested, wanted[i]) {
			t.Fatal("real browser review changed exact target or settings")
		}
	}
	for i := range f.cores {
		f.assertFiles(i, before[i])
		f.assertOrdinary(i)
	}
	if time.Until(workUntil) < 125*time.Second {
		receipt.FailureStage = "native_deadline"
		t.Fatal("insufficient original work budget for browser and cleanup")
	}
	entered := time.Now()
	cancellation := make(chan os.Signal, 2)
	signal.Notify(cancellation, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(cancellation)
	if unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0) != nil {
		receipt.FailureStage = "browser_ownership"
		t.Fatal("Linux browser descendant ownership unavailable")
	}
	defer unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 0, 0, 0, 0)
	for _, name := range []string{"home", "tmp", "config", "cache", "data", "runtime"} {
		if os.Mkdir(filepath.Join(root, name), 0700) != nil {
			t.Fatal("private browser directories unavailable")
		}
	}
	assets, err := web.Assets()
	if err != nil {
		t.Fatal("production embedded browser assets unavailable")
	}
	paths := []string{}
	err = fs.WalkDir(assets, ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() && path != "index.html" {
			paths = append(paths, "/"+path)
		}
		return nil
	})
	if err != nil || len(paths) < 1 || len(paths) > 32 {
		t.Fatal("bounded production asset inventory unavailable")
	}
	sort.Strings(paths)
	url, code, err := f.cores[0].StartWeb(assets)
	if err != nil {
		t.Fatal("production controller Web startup failed")
	}
	session := struct {
		Schema    int      `json:"schema"`
		URL       string   `json:"url"`
		Code      string   `json:"code"`
		ProcessID string   `json:"processID"`
		Assets    []string `json:"assets"`
	}{1, url, code, f.cores[0].resourceCatalogProcessID(), paths}
	if resourceBrowserWrite(filepath.Join(root, "session.json"), session) != nil {
		t.Fatal("private browser group session handoff failed")
	}
	code, session.Code = "", ""
	receipt.FailureStage = "browser_ownership"
	proof = resourceGroupBrowserRunScope(f, root, webDir, node, chromium, entered, cancellation)
	if resourceBrowserWrite(filepath.Join(root, "scope-proof.json"), proof) != nil {
		t.Fatal("browser group ownership receipt failed")
	}
	if !proof.Complete || !proof.DescendantsReaped || proof.Forced || proof.DeadlineExceeded || proof.Errors != 0 {
		t.Fatal("browser group ownership did not complete successfully")
	}
	if !time.Now().Before(verifyUntil) {
		receipt.FailureStage = "native_deadline"
		t.Fatal("original native verification budget ended")
	}
	// No fresh local command series is started on a renewed deadline. The N4
	// assertion helpers below share only the original remaining verification
	// window, reserving its final fifteen seconds for joined native cleanup.
	verify, stopVerify := context.WithDeadline(f.ctx, verifyUntil)
	defer stopVerify()
	f.ctx = verify
	checkTime := func() {
		if verify.Err() != nil || !time.Now().Before(verifyUntil) {
			receipt.FailureStage = "native_deadline"
			t.Fatal("original native verification budget ended")
		}
	}
	observed, stage := resourceGroupBrowserReplies(root, prepared)
	receipt.FailureStage = stage
	if stage != "none" {
		t.Fatal("private real group browser request/reply verification failed")
	}
	// A joined failing case reports its bounded summary first. An inconsistent
	// nonzero child exit cannot pass even if all private result files look valid.
	if proof.PlaywrightExit != 0 {
		receipt.FailureStage = "browser_ownership"
		t.Fatal("browser group exit disagrees with its successful receipts")
	}
	checkTime()
	receipt.FailureStage = "product_invariants"
	for i, row := range observed.Evidence.Members {
		checkTime()
		f.assertApplied(i+1, wanted[i], policies[i], row)
		f.assertGrant(i+1, grants[i])
		checkTime()
	}
	controller := f.files(0)
	if !reflect.DeepEqual(controller[0], before[0][0]) || !reflect.DeepEqual(controller[1], before[0][1]) || !controller[2].present {
		t.Fatal("browser group changed controller settings or lost history")
	}
	saved, err := decodeResourceGroupEnvelope(controller[2].data)
	if err != nil || len(saved.Runs) != 1 || *saved.HighWater != 1 || saved.Runs[0].RunID != observed.RunID || saved.Runs[0].Admission != resourceGroupAdmissionFinished || !reflect.DeepEqual(saved.Runs[0].Review, observed.Review) || !reflect.DeepEqual(saved.Runs[0].Evidence, observed.Evidence) {
		t.Fatal("browser group durable history differs from its actual reply")
	}
	for i := range f.cores {
		f.assertOrdinary(i)
	}
	if resourceGroupBrowserManaged(f) != nil {
		t.Fatal("browser group final managed ownership changed")
	}
	receipt.ProductVerified = true
	checkTime()
	receipt.FailureStage = "native_events"
	trace := f.assertEvents(observed)
	receipt.EventCount = int(trace.Count)
	for _, event := range trace.Events[:trace.Count] {
		if event.Kind == resourceacceptance.ManagementClientInvoked && event.Action == resourceacceptance.Preview {
			receipt.PreviewTargets++
		}
		if event.Kind == resourceacceptance.ProviderAdmitted && event.Action == resourceacceptance.Apply {
			receipt.ProviderAdmissions++
		}
	}
	receipt.AppliedTargets = int(observed.Summary.Applied)
	receipt.NativeEventsVerified = true
	checkTime()
	receipt.FailureStage = "none"
}
