//go:build product_activation_native && web_activation_native && managed_restart_native && linux

package main

import (
	"errors"
	"path/filepath"
	"testing"
)

// Fixed, best-effort observations of existing fixture-owned calls. These do not
// grant authority, drive readiness or add any acceptance/cleanup predicate.
// Each record has one writer; the owner joins its monitor before final flush.
type productNativeObservation struct {
	Schema      int    `json:"schema"`
	Stage       string `json:"stage"`
	Failed      bool   `json:"failed"`
	OwnerStatus string `json:"ownerStatus"`
	PeerStatus  string `json:"peerStatus"`
}
type productNativeDiagnostic struct {
	path string
	view productNativeObservation
}

var productNativeStages = []string{"not-started", "owner-start", "peer-open", "peer-review", "peer-apply", "foreground", "identity", "management-announced", "cli-launch", "review-read", "owner-status", "peer-status", "review-binding", "network-ready", "persisted-context", "pair-binding", "proof-written", "cli-terminal", "cli-review", "cli-review-binding", "cli-apply", "cli-complete"}

func newProductNativeDiagnostic(dir, role string) *productNativeDiagnostic {
	d := &productNativeDiagnostic{path: filepath.Join(dir, "product-"+role+"-diagnostic.json"), view: productNativeObservation{Schema: 1, Stage: "not-started", OwnerStatus: "unobserved", PeerStatus: "unobserved"}}
	d.flush()
	return d
}
func (d *productNativeDiagnostic) flush() { _ = productPrivateJSON(d.path, d.view) }
func (d *productNativeDiagnostic) stage(next string) {
	current, desired := -1, -1
	for i, stage := range productNativeStages {
		if stage == d.view.Stage {
			current = i
		}
		if stage == next {
			desired = i
		}
	}
	if desired > current {
		d.view.Stage = next
		d.flush()
	}
}
func (d *productNativeDiagnostic) finish(err error) { d.view.Failed = err != nil; d.flush() }
func productObservedStatus(status string) string {
	switch status {
	case "idle", "restart-required", "preparing", "exchanging", "local-confirmed", "connected", "network-started", "failed", "cancelled":
		return status
	default:
		return "other"
	}
}
func (d *productNativeDiagnostic) status(peer bool, status string) {
	value := &d.view.OwnerStatus
	if peer {
		value = &d.view.PeerStatus
	}
	observed := productObservedStatus(status)
	if *value != observed {
		*value = observed
		d.flush()
	}
}

func TestProductCleanupDiagnosticPreservesFirstFailure(t *testing.T) {
	s := &activationSupervisor{failure: "none"}
	s.failLocked("extra-observe")
	s.failLocked("extra-close")
	if !s.failed || s.failure != "extra-observe" {
		t.Fatal("sticky cleanup failure changed")
	}
	r := &productSupervisorResources{}
	if r.noteFailure("master-close", nil) != nil || r.FailureStage() != "none" {
		t.Fatal("nil result invented failure")
	}
	first := errors.New("synthetic first failure")
	if r.noteFailure("capture-error", first) != first {
		t.Fatal("underlying error changed")
	}
	r.noteFailure("master-close", errors.New("synthetic later failure"))
	if r.FailureStage() != "capture-error" {
		t.Fatal("first resource failure changed")
	}
}
