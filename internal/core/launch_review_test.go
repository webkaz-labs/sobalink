package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLaunchReviewContainsEffectsAndRevisionsWithoutSecretsOrWrites(t *testing.T) {
	p := newCorePair(t)
	saved := saveProxyFixture(t, p.a, false, true)
	path := filepath.Join(p.a.dir, "saved-proxies.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	review, err := ReadLaunchReview(p.a.dir)
	if err != nil {
		t.Fatal(err)
	}
	if !review.ProxiesRestart || review.ServicesRestart {
		t.Fatal("incorrect prospective effects", review)
	}
	if _, ok := review.Startup["suppressed"]; ok {
		t.Fatal("runtime suppression leaked into prospective review")
	}
	public, _ := json.Marshal(review)
	for _, secret := range []string{"fixture-private-user", "fixture-private-password", "username", "password"} {
		if strings.Contains(string(public), secret) {
			t.Fatal("private credential leaked into OS-registration review")
		}
	}
	after, _ := os.ReadFile(path)
	afterInfo, _ := os.Stat(path)
	if string(before) != string(after) || !info.ModTime().Equal(afterInfo.ModTime()) || info.Mode() != afterInfo.Mode() {
		t.Fatal("review changed private state")
	}
	mustCommand(t, p.a, "proxy.saved.disable", map[string]string{"name": saved.Name, "expectedRevision": saved.Revision})
	next, err := ReadLaunchReview(p.a.dir)
	if err != nil {
		t.Fatal(err)
	}
	nextPublic, _ := json.Marshal(next)
	if next.ProxiesRestart || string(nextPublic) == string(public) {
		t.Fatal("changed approval was not reflected in prospective review")
	}
}

func TestLaunchReviewReportsOnlyExactEnabledOutboundApprovals(t *testing.T) {
	c, spec := startupFixture(t)
	saveStartupFixture(t, c, spec)
	review, err := ReadLaunchReview(c.dir)
	if err != nil || !review.ServicesRestart {
		t.Fatal(review, err)
	}
	p := c.profileCopy()
	p.Services[0].Ports = "444"
	if err := c.writeProfile(p); err != nil {
		t.Fatal(err)
	}
	next, err := ReadLaunchReview(c.dir)
	if err != nil || next.ServicesRestart {
		t.Fatal("edited scope remained approved", next, err)
	}
}
