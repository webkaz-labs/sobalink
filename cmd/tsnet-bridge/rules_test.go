package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/webkaz-labs/tsnet-bridge/internal/app"
	"github.com/webkaz-labs/tsnet-bridge/internal/config"
	"github.com/webkaz-labs/tsnet-bridge/internal/policy"
)

func testRule(name string) config.Rule {
	return config.Rule{Name: name, Purpose: "web", Direction: "forward", Network: "tcp", ListenPort: 8080, TargetHost: "server.example.ts.net", TargetPort: 80, PeerID: "node-server"}
}
func saveRules(t *testing.T, rules ...config.Rule) string {
	t.Helper()
	d := t.TempDir()
	c, e := config.NewRules()
	if e != nil {
		t.Fatal(e)
	}
	c.Rules = rules
	if e = config.Save(d, c); e != nil {
		t.Fatal(e)
	}
	return d
}
func installRequest(t *testing.T, fn func(context.Context, string, string, any) error) {
	t.Helper()
	old := request
	request = fn
	t.Cleanup(func() { request = old })
}
func assign(v, value any) error {
	b, e := json.Marshal(value)
	if e != nil {
		return e
	}
	if v == nil {
		return nil
	}
	return json.Unmarshal(b, v)
}

type fakeRules struct {
	mu                   sync.Mutex
	t                    *testing.T
	dir                  string
	commands             []app.RuleCommand
	states               map[string]app.RuleStatus
	peers                []policy.Peer
	failStart, failRenew bool
}

func newFake(t *testing.T, dir string) *fakeRules {
	f := &fakeRules{t: t, dir: dir, states: map[string]app.RuleStatus{}, peers: []policy.Peer{{ID: "node-server", DNSName: "server.example.ts.net.", IPs: []netip.Addr{netip.MustParseAddr("100.64.0.2")}}}}
	c, e := config.Load(dir)
	if e != nil {
		t.Fatal(e)
	}
	for _, r := range c.Rules {
		f.states[r.Name] = app.RuleStatus{Name: r.Name, Direction: r.Direction, Network: r.Network, State: "stopped", Target: config.Address(r.TargetHost, r.TargetPort), Application: "unverified"}
	}
	installRequest(t, f.call)
	return f
}
func (f *fakeRules) status() app.Status {
	s := app.Status{Mode: "rules", State: "idle", Backend: "Running"}
	for _, r := range f.states {
		s.Rules = append(s.Rules, r)
	}
	return s
}
func (f *fakeRules) call(ctx context.Context, _ string, command string, v any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e := ctx.Err(); e != nil {
		return e
	}
	if command == "peers" {
		return assign(v, f.peers)
	}
	if command == "status" {
		return assign(v, f.status())
	}
	if !strings.HasPrefix(command, "rules:") {
		return errors.New("unexpected command")
	}
	var q app.RuleCommand
	if e := json.Unmarshal([]byte(strings.TrimPrefix(command, "rules:")), &q); e != nil {
		return e
	}
	f.commands = append(f.commands, q)
	switch q.Action {
	case "save":
		c, e := config.Load(f.dir)
		if e != nil {
			return e
		}
		found := false
		for i, r := range c.Rules {
			if r.Name == q.Rule.Name {
				if !q.Replace {
					return errors.New("replace flag required")
				}
				c.Rules[i] = *q.Rule
				found = true
			}
		}
		if !found {
			c.Rules = append(c.Rules, *q.Rule)
		}
		if e = config.Save(f.dir, c); e != nil {
			return e
		}
		f.states[q.Rule.Name] = app.RuleStatus{Name: q.Rule.Name, Direction: q.Rule.Direction, Network: q.Rule.Network, State: "stopped", Application: "unverified"}
	case "start":
		snapshot, e := config.Load(f.dir)
		if e != nil {
			return e
		}
		reviewed, e := selectedRules(snapshot, q.Names, q.Group)
		if e != nil {
			return e
		}
		if q.ExpectedRules != config.RulesDigest(reviewed) {
			return errors.New("reviewed scope changed")
		}
		names := q.Names
		if q.Group != "" {
			c, _ := config.Load(f.dir)
			for _, g := range c.Groups {
				if g.Name == q.Group {
					names = g.Rules
				}
			}
		}
		for _, n := range names {
			r := f.states[n]
			r.State = "ready"
			r.Owner = q.Owner
			if f.failStart {
				r.State = "blocked"
				r.ReasonCode = "port-in-use"
				r.Reason = "selected port unavailable"
			}
			f.states[n] = r
		}
	case "stop":
		for _, n := range q.Names {
			r := f.states[n]
			if r.Owner == q.Owner {
				r.State = "stopped"
				f.states[n] = r
			}
		}
	case "renew":
		if f.failRenew {
			return errors.New("lease unavailable")
		}
	}
	return assign(v, f.status())
}
func (f *fakeRules) actions() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var s []string
	for _, q := range f.commands {
		s = append(s, q.Action)
	}
	return s
}

func TestInitIsOfflineAndGeneric(t *testing.T) {
	d := t.TempDir()
	installRequest(t, func(context.Context, string, string, any) error { t.Fatal("init touched IPC"); return nil })
	var out bytes.Buffer
	if e := run(context.Background(), []string{"--state-dir", d, "init"}, strings.NewReader(""), &out); e != nil {
		t.Fatal(e)
	}
	c, e := config.Load(d)
	if e != nil || c.Version != 2 || len(c.Rules) != 0 || c.PublicKey != "" {
		t.Fatal(c, e)
	}
	if _, e = os.Stat(filepath.Join(d, "credentials.json")); !os.IsNotExist(e) {
		t.Fatal("created credentials")
	}
	if e = initRules(d, nil, &out); e == nil {
		t.Fatal("overwrote profile")
	}
}
func TestPeerPickerPinsExactCurrentIdentity(t *testing.T) {
	peers := []policy.Peer{{ID: "node-a", DNSName: "same.one.ts.net", IPs: []netip.Addr{netip.MustParseAddr("100.64.0.1")}}, {ID: "node-b", DNSName: "same.two.ts.net"}}
	if _, e := selectPeers(peers, "same", false); e == nil {
		t.Fatal("ambiguous hostname accepted")
	}
	for _, selection := range []string{"1", "node-a", "same.one.ts.net", "100.64.0.1"} {
		picked, e := selectPeers(peers, selection, false)
		if e != nil || picked[0].ID != "node-a" {
			t.Fatal(selection, picked, e)
		}
	}
	if _, e := selectPeers(peers, "node-a,node-b", false); e == nil {
		t.Fatal("multiple forwards accepted")
	}
	picked, e := selectPeers(peers, "node-a,node-b", true)
	if e != nil || len(picked) != 2 {
		t.Fatal(picked, e)
	}
}
func TestConnectSaveOnlyDoesNotStart(t *testing.T) {
	d := saveRules(t)
	fake := newFake(t, d)
	old := checkPort
	checkPort = func(context.Context, string, int) error { return nil }
	defer func() { checkPort = old }()
	var out bytes.Buffer
	args := []string{"--peer", "server", "--purpose", "web", "--name", "site", "--port", "80", "--listen-port", "8080", "--save-only", "--confirm"}
	if e := configureRule(context.Background(), d, "forward", args, strings.NewReader(""), &out); e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(fake.actions(), []string{"save"}) {
		t.Fatal(fake.actions())
	}
	c, _ := config.Load(d)
	if c.Rules[0].Enabled || c.Rules[0].PeerID != "node-server" || c.Rules[0].TargetPort != 80 {
		t.Fatal(c.Rules)
	}
	if !strings.Contains(out.String(), "Local app endpoint: 127.0.0.1:8080") {
		t.Fatal(out.String())
	}
}
func TestSamePortAndAlternativeRequireExplicitChoice(t *testing.T) {
	d := saveRules(t)
	fake := newFake(t, d)
	old := checkPort
	checkPort = func(context.Context, string, int) error { return nil }
	defer func() { checkPort = old }()
	var out bytes.Buffer
	base := []string{"--peer", "server", "--purpose", "ssh", "--name", "shell", "--save-only", "--confirm"}
	if e := configureRule(context.Background(), d, "forward", base, strings.NewReader(""), &out); e == nil {
		t.Fatal("silently changed privileged port")
	}
	if len(fake.actions()) != 0 {
		t.Fatal(fake.actions())
	}
	if !strings.Contains(out.String(), "local port 22") {
		t.Fatal(out.String())
	}
	out.Reset()
	if e := configureRule(context.Background(), d, "forward", base, strings.NewReader("2222\n"), &out); e != nil {
		t.Fatal(e)
	}
	c, _ := config.Load(d)
	if c.Rules[0].ListenPort != 2222 || c.Rules[0].TargetPort != 22 {
		t.Fatal(c.Rules)
	}
	// A nonprivileged purpose first proposes the actual identical local port.
	d2 := saveRules(t)
	newFake(t, d2)
	out.Reset()
	if e := configureRule(context.Background(), d2, "forward", []string{"--peer", "server", "--purpose", "web", "--name", "site", "--port", "8765", "--save-only", "--confirm"}, strings.NewReader(""), &out); e != nil {
		t.Fatal(e)
	}
	c, _ = config.Load(d2)
	if c.Rules[0].ListenPort != 8765 {
		t.Fatal(c.Rules)
	}
}
func TestConflictNeverSilentlyChangesPort(t *testing.T) {
	d := saveRules(t)
	fake := newFake(t, d)
	old := checkPort
	checkPort = func(context.Context, string, int) error { return errors.New("in use") }
	defer func() { checkPort = old }()
	var out bytes.Buffer
	if e := configureRule(context.Background(), d, "forward", []string{"--peer", "server", "--purpose", "web", "--name", "site", "--save-only", "--confirm"}, strings.NewReader(""), &out); e == nil {
		t.Fatal("silently replaced conflicting port")
	}
	if len(fake.actions()) != 0 {
		t.Fatal(fake.actions())
	}
}
func TestSharePreviewConfirmationAndLifetime(t *testing.T) {
	d := saveRules(t)
	fake := newFake(t, d)
	var out bytes.Buffer
	args := []string{"--peer", "server", "--purpose", "web", "--name", "share-site", "--port", "8080", "--ttl", "1h"}
	if e := configureRule(context.Background(), d, "share", args, strings.NewReader("n\n"), &out); e == nil {
		t.Fatal("unconfirmed share started")
	}
	if len(fake.actions()) != 0 {
		t.Fatal(fake.actions())
	}
	if !strings.Contains(out.String(), "node-server") || !strings.Contains(out.String(), "1h0m0s") || !strings.Contains(out.String(), "Localhost-only trust") {
		t.Fatal(out.String())
	}
	if e := configureRule(context.Background(), d, "share", append(args, "--confirm"), strings.NewReader(""), &out); e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(fake.actions(), []string{"save", "start"}) {
		t.Fatal(fake.actions())
	}
	last := fake.commands[1]
	if last.TTLSeconds != 3600 {
		t.Fatal(last)
	}
}
func TestSavedConnectIsOneStepAndStartFailureNonzero(t *testing.T) {
	d := saveRules(t, testRule("web"))
	fake := newFake(t, d)
	var out bytes.Buffer
	if e := configureRule(context.Background(), d, "forward", []string{"web"}, strings.NewReader(""), &out); e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(fake.actions(), []string{"start"}) {
		t.Fatal(fake.actions())
	}
	fake.failStart = true
	out.Reset()
	if e := namedAction(context.Background(), d, "start", []string{"web"}, strings.NewReader(""), &out); e == nil {
		t.Fatal("failed start returned success")
	}
	if !strings.Contains(out.String(), "port-in-use") {
		t.Fatal(out.String())
	}
}
func TestWaitReadySelectedOwnerFailureCancellationTimeout(t *testing.T) {
	d := saveRules(t, testRule("web"))
	fake := newFake(t, d)
	fake.states["web"] = app.RuleStatus{Name: "web", State: "ready", Owner: "mine"}
	fake.states["other"] = app.RuleStatus{Name: "other", State: "failed", Owner: "elsewhere"}
	if _, e := waitReady(context.Background(), d, []string{"web"}, "mine", time.Millisecond); e != nil {
		t.Fatal(e)
	}
	if _, e := waitReady(context.Background(), d, []string{"web"}, "other", time.Millisecond); e == nil {
		t.Fatal("wrong owner accepted")
	}
	fake.states["web"] = app.RuleStatus{Name: "web", State: "blocked", ReasonCode: "peer-unavailable"}
	if _, e := waitReady(context.Background(), d, []string{"web"}, "", time.Millisecond); e == nil {
		t.Fatal("partial failure accepted")
	}
	synctest.Test(t, func(t *testing.T) {
		fake.states["web"] = app.RuleStatus{Name: "web", State: "starting"}
		ctx, c := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer c()
		if _, e := waitReady(ctx, d, []string{"web"}, "", 10*time.Millisecond); !errors.Is(e, context.DeadlineExceeded) {
			t.Fatal(e)
		}
	})
	ctx, c := context.WithCancel(context.Background())
	c()
	if _, e := waitReady(ctx, d, []string{"web"}, "", time.Millisecond); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
}
func TestTaskDirectArgumentsAndOwnedCleanup(t *testing.T) {
	d := saveRules(t, testRule("web"), testRule("other"))
	fake := newFake(t, d)
	fake.states["other"] = app.RuleStatus{Name: "other", State: "ready", Owner: "someone-else"}
	old := runTaskProcess
	defer func() { runTaskProcess = old }()
	var got []string
	runTaskProcess = func(_ context.Context, argv []string, _ io.Reader, _ io.Writer) error {
		got = append([]string(nil), argv...)
		return errors.New("command failed")
	}
	var out bytes.Buffer
	e := taskCommand(context.Background(), d, []string{"--rules", "web", "--", "program", "argument;not-a-shell", "$(untouched)"}, strings.NewReader(""), &out)
	if e == nil || e.Error() != "command failed" {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(got, []string{"program", "argument;not-a-shell", "$(untouched)"}) {
		t.Fatal(got)
	}
	if !reflect.DeepEqual(fake.actions(), []string{"start", "stop"}) {
		t.Fatal(fake.actions())
	}
	a, b := fake.commands[0], fake.commands[1]
	if a.Owner == "" || a.Owner != b.Owner || a.LeaseSeconds != 30 || !reflect.DeepEqual(b.Names, []string{"web"}) {
		t.Fatal(a, b)
	}
	if fake.states["other"].State != "ready" {
		t.Fatal("stopped another task")
	}
}
func TestTaskRenewsAndCancelsOnLostLease(t *testing.T) {
	d := saveRules(t, testRule("web"))
	fake := newFake(t, d)
	fake.failRenew = true
	old := runTaskProcess
	defer func() { runTaskProcess = old }()
	runTaskProcess = func(ctx context.Context, _ []string, _ io.Reader, _ io.Writer) error { <-ctx.Done(); return ctx.Err() }
	synctest.Test(t, func(t *testing.T) {
		var out bytes.Buffer
		e := taskCommand(context.Background(), d, []string{"--rules", "web", "--", "ignored"}, strings.NewReader(""), &out)
		if e == nil || !strings.Contains(e.Error(), "lease renewal failed") {
			t.Fatal(e)
		}
		if !reflect.DeepEqual(fake.actions(), []string{"start", "renew", "stop"}) {
			t.Fatal(fake.actions())
		}
	})
}
func TestTaskFailedStartNeverRunsCommandAndCleansOwned(t *testing.T) {
	d := saveRules(t, testRule("web"))
	fake := newFake(t, d)
	fake.failStart = true
	old := runTaskProcess
	defer func() { runTaskProcess = old }()
	runTaskProcess = func(context.Context, []string, io.Reader, io.Writer) error {
		t.Fatal("executed after failure")
		return nil
	}
	var out bytes.Buffer
	if e := taskCommand(context.Background(), d, []string{"--rules", "web", "--", "ignored"}, strings.NewReader(""), &out); e == nil {
		t.Fatal("accepted failed start")
	}
	if !reflect.DeepEqual(fake.actions(), []string{"start", "stop"}) {
		t.Fatal(fake.actions())
	}
}
func TestTaskExecutorDoesNotInvokeShell(t *testing.T) {
	if strings.Contains(strings.Join(os.Args, " "), "task-helper-argument") {
		return
	}
	exe, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	var out bytes.Buffer
	args := []string{exe, "-test.run=^TestTaskHelperProcess$", "--", "task-helper-argument;literal"}
	if e = runTaskProcess(context.Background(), args, strings.NewReader(""), &out); e != nil {
		t.Fatal(e, out.String())
	}
	if !strings.Contains(out.String(), "direct argument retained") {
		t.Fatal(out.String())
	}
}
func TestTaskHelperProcess(t *testing.T) {
	if len(os.Args) == 0 || os.Args[len(os.Args)-1] != "task-helper-argument;literal" {
		return
	}
	fmt.Fprintln(os.Stdout, "direct argument retained")
}

type mutateOnRead struct {
	done   bool
	mutate func()
	text   *strings.Reader
}

func (r *mutateOnRead) Read(b []byte) (int, error) {
	if !r.done {
		r.done = true
		r.mutate()
	}
	return r.text.Read(b)
}
func TestShareConfirmationCannotRaceRuleReplacement(t *testing.T) {
	rule := testRule("api")
	rule.Direction = "share"
	rule.TargetHost = "127.0.0.1"
	rule.PeerID = ""
	rule.AllowedPeers = []config.PeerRef{{ID: "node-server", Host: "server.example.ts.net"}}
	dir := saveRules(t, rule)
	f := newFake(t, dir)
	var out bytes.Buffer
	in := &mutateOnRead{text: strings.NewReader("yes\n"), mutate: func() {
		c, e := config.Load(dir)
		if e != nil {
			t.Fatal(e)
		}
		c.Rules[0].TargetPort = 9999
		if e = config.Save(dir, c); e != nil {
			t.Fatal(e)
		}
	}}
	if e := namedAction(t.Context(), dir, "start", []string{"--ttl", "1m", "api"}, in, &out); e == nil {
		t.Fatal("changed share started")
	}
	if f.states["api"].State == "ready" {
		t.Fatal("unreviewed local target opened")
	}
}
func TestTaskConfirmationCannotRaceRuleReplacement(t *testing.T) {
	rule := testRule("api")
	rule.Direction = "share"
	rule.TargetHost = "127.0.0.1"
	rule.PeerID = ""
	rule.AllowedPeers = []config.PeerRef{{ID: "node-server", Host: "server.example.ts.net"}}
	dir := saveRules(t, rule)
	f := newFake(t, dir)
	var out bytes.Buffer
	in := &mutateOnRead{text: strings.NewReader("yes\n"), mutate: func() {
		c, _ := config.Load(dir)
		c.Rules[0].AllowedPeers = []config.PeerRef{{ID: "another", Host: "another.example.ts.net"}}
		if e := config.Save(dir, c); e != nil {
			t.Fatal(e)
		}
	}}
	if e := taskCommand(t.Context(), dir, []string{"--rules", "api", "--ttl", "1m", "--", "must-not-run"}, in, &out); e == nil {
		t.Fatal("changed share task started")
	}
	if f.states["api"].State == "ready" {
		t.Fatal("unreviewed recipient allowed")
	}
}
