//go:build resource_process_native

package control

import (
	"strings"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/resourceacceptance"
)

func TestResourceProcessCommandClassification(t *testing.T) {
	cases := []struct {
		raw  string
		kind resourceacceptance.ProcessCommandKind
	}{
		{"control.limits", resourceacceptance.ProcessControlLimits},
		{"status", resourceacceptance.ProcessLocalStatus},
		{"stop", resourceacceptance.ProcessLocalStop},
		{"lifecycle.upgrade.identity", resourceacceptance.ProcessUpgradeIdentity},
	}
	names := []string{"direct-lan.upgrade.review", "direct-lan.upgrade.run", "direct-lan.upgrade.status",
		"resource.list", "resource.inspect", "resource.preview", "resource.apply", "resource.operation.status",
		"resource.grant.management.preview", "resource.grant.management.confirm", "resource.grant.inspect",
		"resource.group.preview", "resource.group.review.current", "resource.group.apply", "resource.group.status"}
	for i, name := range names {
		cases = append(cases, struct {
			raw  string
			kind resourceacceptance.ProcessCommandKind
		}{
			`{"name":"` + name + `","requestId":"fixed","payload":{"ignored":true}}`, resourceacceptance.ProcessCommandKind(int(resourceacceptance.ProcessUpgradeReview) + i)})
	}
	for _, test := range cases {
		if got := resourceProcessCommandKind(test.raw); got != test.kind {
			t.Fatal("closed command route misclassified")
		}
	}
}

func TestResourceProcessCommandJSONSemantics(t *testing.T) {
	for _, raw := range []string{
		`{"name":"resource.list"}`, `{"NAME":"resource.list"}`, `{"NaMe":"resource.list"}`,
		`{"name":"resource.\u006cist"}`, `{"name":"resource.apply","name":"resource.list"}`,
		`{"name":"resource.apply","Name":"resource.list"}`, `{"name":"resource.list","name":null}`,
		`{"payload":{"name":"ui"},"name":"resource.list"}`, `{"name":"resource.list","requestId":null}`,
		`{"name":"resource.list","requestId":17}`,
	} {
		// Transport dispatch does not certify the request ID or payload. Core's
		// own decoder/context/business checks still choose the product result.
		if got := resourceProcessCommandKind(raw); got != resourceacceptance.ProcessResourceList {
			t.Fatal("name-only JSON semantics diverged")
		}
	}
	for _, raw := range []string{"", "ui", " status", `"status"`, "{}", "null", "[]", "[{}]",
		`{"name":null}`, `{"name":1}`, `{"name":false}`, `{"name":{}}`, `{"name":[]}`,
		`{"name":"resource.list"} {}`, `{"name":"resource.list"} trailing`, `{"name":"resource.list",}`,
		`{"name":"resource.list","payload":{"broken":}}`, `{"name":"resource.list","name":1}`,
		`{"name":"control.limits"}`, `{"name":"status"}`, `{"name":"stop"}`, `{"name":"lifecycle.upgrade.identity"}`,
		`{"name":"resource.grant.management.inspect"}`, `{"name":"resource.group.cancel"}`,
		`{"name":"resource.group.status.refresh"}`, `{"name":"peer.reconnect"}`} {
		if got := resourceProcessCommandKind(raw); got != resourceacceptance.ProcessCommandUnknown {
			t.Fatal("unexpected transport name accepted")
		}
	}
	base := `{"name":"resource.list"}`
	bounded := base + strings.Repeat(" ", resourceProcessCommandLimit-len(base))
	if resourceProcessCommandKind(bounded) != resourceacceptance.ProcessResourceList {
		t.Fatal("exact classifier byte bound rejected")
	}
	if resourceProcessCommandKind(bounded+" ") != resourceacceptance.ProcessCommandUnknown {
		t.Fatal("oversized classifier input accepted")
	}
}

func TestResourceProcessObserverHelpersStayInactive(t *testing.T) {
	if resourceacceptance.ProcessIPCDirectoryMatches("/synthetic/state/a") {
		t.Fatal("unexpected global observer")
	}
	observeResourceProcessDialAttempt("/synthetic/state/a")
	observeResourceProcessDialCompleted("/synthetic/state/a")
	observeResourceProcessDispatch("/synthetic/state/a", `{"name":"resource.list"}`)
	observeResourceProcessDispatch("/synthetic/state/a", strings.Repeat("?", resourceProcessCommandLimit+1))
	if resourceacceptance.ProcessIPCDirectoryMatches("/synthetic/state/a") {
		t.Fatal("control hook installed an observer")
	}
}
