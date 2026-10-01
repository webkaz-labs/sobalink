package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/webkaz-labs/tsnet-bridge/internal/app"
	"github.com/webkaz-labs/tsnet-bridge/internal/config"
	"github.com/webkaz-labs/tsnet-bridge/internal/control"
	"github.com/webkaz-labs/tsnet-bridge/internal/policy"
)

// request is injectable so CLI lifecycle tests never require a real node or login.
var request = control.Call
var checkPort = probePort
var runTaskProcess = func(ctx context.Context, argv []string, in io.Reader, out io.Writer) error {
	command := exec.CommandContext(ctx, argv[0], argv[1:]...)
	command.Stdin = in
	command.Stdout = out
	command.Stderr = out
	command.WaitDelay = 2 * time.Second
	return command.Run()
}

func flags(name string, out io.Writer) *flag.FlagSet {
	f := flag.NewFlagSet(name, flag.ContinueOnError)
	f.SetOutput(out)
	return f
}

type prompts struct {
	scanner *bufio.Scanner
	out     io.Writer
}

func newPrompts(in io.Reader, out io.Writer) *prompts { return &prompts{bufio.NewScanner(in), out} }
func (p *prompts) ask(message string) (string, error) {
	fmt.Fprint(p.out, message)
	if !p.scanner.Scan() {
		if err := p.scanner.Err(); err != nil {
			return "", err
		}
		return "", errors.New("canceled; no changes made")
	}
	return strings.TrimSpace(p.scanner.Text()), nil
}
func (p *prompts) confirm(message string, yes bool) error {
	if yes {
		return nil
	}
	answer, err := p.ask(message + " [y/N]: ")
	if err != nil {
		return err
	}
	if !strings.EqualFold(answer, "y") && !strings.EqualFold(answer, "yes") {
		return errors.New("canceled; no changes made")
	}
	return nil
}

func initRules(dir string, args []string, out io.Writer) error {
	f := flags("init", out)
	hostname := f.String("hostname", "", "node hostname (optional)")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return errors.New("usage: init [--hostname NAME]")
	}
	c, err := config.NewRules()
	if err != nil {
		return err
	}
	if *hostname != "" {
		c.Hostname = *hostname
	}
	if err = c.Validate(); err != nil {
		return err
	}
	lock, err := config.AcquireLock(dir)
	if err != nil {
		return err
	}
	defer lock.Close()
	if _, err = os.Lstat(filepath.Join(dir, "profile.json")); err == nil {
		return errors.New("profile already exists; use a different --state-dir or preview migrate")
	} else if !os.IsNotExist(err) {
		return err
	}
	if err = config.Save(dir, c); err != nil {
		return err
	}
	fmt.Fprintln(out, "Idle profile saved. No networking, sign-in, forwarding or sharing has started.\nNext: tsnet-bridge login, then tsnet-bridge connect or tsnet-bridge share.")
	return nil
}

func currentPeers(ctx context.Context, dir string) ([]policy.Peer, error) {
	var peers []policy.Peer
	if err := call(ctx, dir, "peers", &peers); err != nil {
		return nil, fmt.Errorf("current peer list unavailable; start the node and sign in explicitly with login: %w", err)
	}
	active := make([]policy.Peer, 0, len(peers))
	for _, p := range peers {
		if config.ValidPeerID(p.ID) && !p.Expired {
			if _, err := peerHost(p); err == nil {
				active = append(active, p)
			}
		}
	}
	sort.Slice(active, func(i, j int) bool {
		if active[i].DNSName == active[j].DNSName {
			return active[i].ID < active[j].ID
		}
		return active[i].DNSName < active[j].DNSName
	})
	return active, nil
}
func peerHost(p policy.Peer) (string, error) {
	if p.DNSName != "" && config.ValidHost(p.DNSName) {
		return strings.TrimSuffix(p.DNSName, "."), nil
	}
	for _, ip := range p.IPs {
		if config.TailnetIP(ip) {
			return ip.String(), nil
		}
	}
	return "", errors.New("peer has no permitted address")
}
func printPeers(ctx context.Context, dir string, args []string, out io.Writer) error {
	f := flags("peers", out)
	j := f.Bool("json", false, "structured output")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return errors.New("usage: peers [--json]")
	}
	peers, err := currentPeers(ctx, dir)
	if err != nil {
		return err
	}
	if *j {
		return json.NewEncoder(out).Encode(peers)
	}
	for i, p := range peers {
		h, _ := peerHost(p)
		fmt.Fprintf(out, "%d. %s  peer=%s\n", i+1, h, p.ID)
	}
	if len(peers) == 0 {
		fmt.Fprintln(out, "No current eligible peers. Check sign-in, device approval and tailnet permissions.")
	}
	return nil
}
func selectPeers(peers []policy.Peer, selection string, multiple bool) ([]config.PeerRef, error) {
	var selected []config.PeerRef
	seen := map[string]bool{}
	for _, token := range strings.Split(selection, ",") {
		token = strings.TrimSpace(token)
		if token == "" {
			return nil, errors.New("choose an explicit peer")
		}
		var found []policy.Peer
		if n, err := strconv.Atoi(token); err == nil && n >= 1 && n <= len(peers) {
			found = append(found, peers[n-1])
		} else {
			for _, p := range peers {
				h, _ := peerHost(p)
				match := p.ID == token || policy.Normalize(h) == policy.Normalize(token) || (!strings.Contains(token, ".") && policy.Normalize(strings.Split(h, ".")[0]) == policy.Normalize(token))
				if ip, err := netip.ParseAddr(token); err == nil {
					for _, a := range p.IPs {
						if a == ip {
							match = true
						}
					}
				}
				if match {
					found = append(found, p)
				}
			}
		}
		if len(found) != 1 {
			return nil, fmt.Errorf("peer %q is missing or ambiguous; choose its unique displayed peer ID", token)
		}
		p := found[0]
		if seen[p.ID] {
			continue
		}
		seen[p.ID] = true
		h, _ := peerHost(p)
		selected = append(selected, config.PeerRef{ID: p.ID, Host: h})
	}
	if len(selected) > config.MaxAllowedPeers || (!multiple && len(selected) != 1) {
		return nil, errors.New("choose one peer for connect, or at most 32 peers for share")
	}
	return selected, nil
}

type purposeTemplate struct {
	name string
	port int
	note string
}

var templates = []purposeTemplate{
	{"web", 8080, "HTTP candidate; choose the application's actual port. HTTPS/origin may need additional app configuration."},
	{"ssh", 22, "SSH/SFTP; host-key verification remains required."},
	{"db", 5432, "PostgreSQL candidate; confirm your database and authentication."},
	{"ai", 11434, "AI API candidate; confirm the service port and require application authentication."},
	{"custom", 0, "Specify the service's port explicitly."},
}

func templateFor(name string) (purposeTemplate, error) {
	for _, t := range templates {
		if name == t.name {
			return t, nil
		}
	}
	return purposeTemplate{}, errors.New("purpose must be web, ssh, db, ai or custom")
}

func configureRule(ctx context.Context, dir, direction string, args []string, in io.Reader, out io.Writer) error {
	f := flags(direction, out)
	peer := f.String("peer", "", "current peer name, ID, number; comma-separated for share")
	purpose := f.String("purpose", "", "web, ssh, db, ai or custom")
	name := f.String("name", "", "saved rule name")
	port := f.Int("port", 0, "actual service port")
	listen := f.Int("listen-port", 0, "explicit listen port")
	network := f.String("network", "tcp", "advanced: tcp or udp")
	loopback := f.String("loopback", "127.0.0.1", "share target: 127.0.0.1 or ::1")
	ttl := f.Duration("ttl", 0, "lifetime (share: required, 1s..24h)")
	saveOnly := f.Bool("save-only", false, "save disabled without starting")
	yes := f.Bool("confirm", false, "confirm the displayed configuration and start/save")
	replace := f.Bool("replace", false, "replace an existing saved rule after preview (must be stopped)")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() == 1 {
		if *peer != "" || *purpose != "" || *name != "" || *port != 0 || *listen != 0 || *network != "tcp" || *loopback != "127.0.0.1" || *saveOnly || *replace {
			return errors.New("saved-rule start accepts only --ttl and --confirm; omit the name to configure a new rule")
		}
		c, e := config.Load(dir)
		if e != nil {
			return e
		}
		rules, e := selectedRules(c, []string{f.Arg(0)}, "")
		if e != nil {
			return e
		}
		if rules[0].Direction != direction {
			return errors.New("saved rule has a different direction; use start NAME or the matching connect/share command")
		}
		forwarded := []string{"--ttl", ttl.String()}
		if *yes {
			forwarded = append(forwarded, "--confirm")
		}
		forwarded = append(forwarded, f.Arg(0))
		return namedAction(ctx, dir, "start", forwarded, in, out)
	}
	if f.NArg() != 0 {
		return errors.New("unexpected arguments; use one saved rule name, named flags or the interactive prompts")
	}
	c, err := config.Load(dir)
	if err != nil {
		return fmt.Errorf("run init first: %w", err)
	}
	if c.Version != 2 {
		return errors.New("this command requires a version 2 profile; preview migrate or init a separate --state-dir")
	}
	peers, err := currentPeers(ctx, dir)
	if err != nil {
		return err
	}
	if len(peers) == 0 {
		return errors.New("no eligible current peers; check tailnet sign-in and permissions")
	}
	p := newPrompts(in, out)
	if *peer == "" {
		for i, v := range peers {
			h, _ := peerHost(v)
			fmt.Fprintf(out, "%d. %s  peer=%s\n", i+1, h, v.ID)
		}
		*peer, err = p.ask("Choose peer (number or ID; comma-separated for share): ")
		if err != nil {
			return err
		}
	}
	selected, err := selectPeers(peers, *peer, direction == "share")
	if err != nil {
		return err
	}
	if *purpose == "" {
		fmt.Fprintln(out, "Purpose: web / ssh / db / ai / custom")
		*purpose, err = p.ask("Purpose: ")
		if err != nil {
			return err
		}
	}
	tmpl, err := templateFor(*purpose)
	if err != nil {
		return err
	}
	fmt.Fprintln(out, tmpl.note)
	if *port == 0 {
		if tmpl.port == 0 {
			value, e := p.ask("Service port: ")
			if e != nil {
				return e
			}
			*port, e = strconv.Atoi(value)
			if e != nil {
				return errors.New("service port must be a number")
			}
		} else {
			*port = tmpl.port
		}
	}
	if *name == "" {
		suggested := *purpose + "-" + strings.Split(selected[0].Host, ".")[0]
		if !config.ValidName(suggested) {
			suggested = *purpose + "-service"
		}
		value, e := p.ask("Rule name [" + suggested + "]: ")
		if e != nil {
			return e
		}
		if value == "" {
			value = suggested
		}
		*name = value
	}
	old := false
	for _, r := range c.Rules {
		if r.Name == *name {
			old = true
		}
	}
	if old && !*replace {
		return fmt.Errorf("rule %q already exists; use --replace to preview a deliberate replacement", *name)
	}
	r := config.Rule{Name: *name, Purpose: *purpose, Direction: direction, Network: *network, TargetPort: *port, ListenPort: *listen}
	if direction == "share" {
		r.TargetHost = *loopback
		r.AllowedPeers = selected
	} else {
		r.TargetHost = selected[0].Host
		r.PeerID = selected[0].ID
	}
	if r.ListenPort == 0 {
		r.ListenPort = r.TargetPort
	}
	if direction == "forward" {
		reason := ""
		if r.ListenPort < 1024 {
			reason = "the same local port requires elevated privileges"
		} else if r.ListenPort <= 65535 {
			if err = checkPort(ctx, r.Network, r.ListenPort); err != nil {
				reason = "the same local port is unavailable"
			}
		}
		if reason != "" && *listen == 0 {
			alternative := suggestPort(r.TargetPort)
			fmt.Fprintf(out, "Cannot use local port %d: %s. Candidate alternative: %d.\n", r.ListenPort, reason, alternative)
			// --confirm never approves an unseen changed port. Explicit --listen-port does.
			answer, e := p.ask(fmt.Sprintf("Choose local port explicitly [%d], or cancel: ", alternative))
			if e != nil {
				return e
			}
			if answer == "" {
				answer = strconv.Itoa(alternative)
			}
			r.ListenPort, e = strconv.Atoi(answer)
			if e != nil {
				return errors.New("invalid local port")
			}
		} else if reason != "" {
			return fmt.Errorf("explicit local port %d unavailable; choose another --listen-port deliberately", r.ListenPort)
		}
	}
	if err = r.Validate(); err != nil {
		return err
	}
	if *ttl < 0 || *ttl > 24*time.Hour || (*ttl > 0 && *ttl < time.Second) {
		return errors.New("ttl must be 1s..24h, with whole seconds")
	}
	if *ttl%time.Second != 0 {
		return errors.New("ttl must use whole seconds")
	}
	if direction == "share" && !*saveOnly && *ttl == 0 {
		value, e := p.ask("Share lifetime (1s..24h, e.g. 1h): ")
		if e != nil {
			return e
		}
		*ttl, e = time.ParseDuration(value)
		if e != nil || *ttl < time.Second || *ttl > 24*time.Hour || *ttl%time.Second != 0 {
			return errors.New("share ttl must be whole seconds in 1s..24h")
		}
	}
	printRulePreview(out, r, *ttl)
	if direction == "share" {
		fmt.Fprintln(out, "Remote peers will reach this local service. Localhost-only trust is insufficient: enable app authentication. Stopping the bridge does not revoke data already transferred or cancel remote jobs.")
	}
	action := "Save disabled and start this rule?"
	if *saveOnly {
		action = "Save this disabled rule without connecting?"
	}
	if err = p.confirm(action, *yes); err != nil {
		return err
	}
	if err = saveRule(ctx, dir, r, *replace); err != nil {
		return err
	}
	fmt.Fprintln(out, "Saved disabled:", r.Name)
	if *saveOnly {
		return nil
	}
	s, err := ruleRequest(ctx, dir, app.RuleCommand{Action: "start", ExpectedRules: config.RulesDigest([]config.Rule{r}), Names: []string{r.Name}, TTLSeconds: int64(*ttl / time.Second)})
	if err != nil {
		return err
	}
	if err = printStatus(out, s, false); err != nil {
		return err
	}
	return checkStarted(s, []string{r.Name}, "")
}
func suggestPort(port int) int {
	switch port {
	case 22:
		return 2222
	case 80:
		return 8080
	case 443:
		return 8443
	}
	if port > 0 && port <= 55535 {
		return port + 10000
	}
	return 18080
}
func probePort(ctx context.Context, network string, port int) error {
	lc := net.ListenConfig{}
	if network == "tcp" {
		l, e := lc.Listen(ctx, "tcp4", config.Loopback(port))
		if e == nil {
			e = l.Close()
		}
		return e
	}
	if network == "udp" {
		l, e := lc.ListenPacket(ctx, "udp4", config.Loopback(port))
		if e == nil {
			e = l.Close()
		}
		return e
	}
	return errors.New("network must be tcp or udp")
}
func printRulePreview(out io.Writer, r config.Rule, ttl time.Duration) {
	fmt.Fprintf(out, "Name: %s\nPurpose: %s\nDirection: %s\nNetwork: %s\n", r.Name, r.Purpose, r.Direction, r.Network)
	if r.Direction == "forward" {
		fmt.Fprintf(out, "Local app endpoint: %s\nRemote service: %s\nPinned peer: %s\n", config.Loopback(r.ListenPort), config.Address(r.TargetHost, r.TargetPort), r.PeerID)
	} else {
		fmt.Fprintf(out, "Tailnet listen port: %d\nLocal service: %s\n", r.ListenPort, config.Address(r.TargetHost, r.TargetPort))
		for _, p := range r.AllowedPeers {
			fmt.Fprintf(out, "Allowed peer: %s (%s)\n", p.Host, p.ID)
		}
	}
	if ttl > 0 {
		fmt.Fprintln(out, "Lifetime:", ttl)
	}
	fmt.Fprintln(out, "Application behavior: unverified. TLS and SSH identity checks remain enabled.")
}
func ruleRequest(ctx context.Context, dir string, cmd app.RuleCommand) (app.Status, error) {
	var s app.Status
	b, err := json.Marshal(cmd)
	if err != nil {
		return s, err
	}
	err = call(ctx, dir, "rules:"+string(b), &s)
	return s, err
}
func saveRule(ctx context.Context, dir string, r config.Rule, replace bool) error {
	// A live service owns the process lock and serializes saves with runtime starts.
	if running(ctx, dir) {
		_, err := ruleRequest(ctx, dir, app.RuleCommand{Action: "save", Rule: &r, Replace: replace})
		return err
	}
	lock, err := config.AcquireLock(dir)
	if err != nil {
		return err
	}
	defer lock.Close()
	c, err := config.Load(dir)
	if err != nil {
		return err
	}
	if c.Version != 2 {
		return errors.New("rule changes require a version 2 profile")
	}
	found := false
	for i := range c.Rules {
		if c.Rules[i].Name == r.Name {
			if !replace {
				return errors.New("rule appeared during save; review it before replacing")
			}
			c.Rules[i] = r
			found = true
		}
	}
	if !found {
		c.Rules = append(c.Rules, r)
	}
	return config.Save(dir, c)
}

func ruleSelection(f *flag.FlagSet) (*string, *string, *string, *time.Duration, *bool) {
	return f.String("rules", "", "comma-separated rule names"), f.String("group", "", "saved group"), f.String("owner", "", "restrict to a task owner"), f.Duration("ttl", 0, "rule lifetime; mandatory for shares (1s..24h)"), f.Bool("json", false, "structured output")
}
func namesFrom(list string, args []string) ([]string, error) {
	var names []string
	if list != "" {
		names = append(names, strings.Split(list, ",")...)
	}
	names = append(names, args...)
	seen := map[string]bool{}
	for _, n := range names {
		if !config.ValidName(n) {
			return nil, fmt.Errorf("invalid rule name %q", n)
		}
		if seen[n] {
			return nil, fmt.Errorf("repeated rule %q", n)
		}
		seen[n] = true
	}
	return names, nil
}
func namedAction(ctx context.Context, dir, action string, args []string, in io.Reader, out io.Writer) error {
	f := flags(action, out)
	list, group, owner, ttl, j := ruleSelection(f)
	yes := f.Bool("confirm", false, "confirm displayed share scope")
	if err := f.Parse(args); err != nil {
		return err
	}
	names, err := namesFrom(*list, f.Args())
	if err != nil {
		return err
	}
	if *group != "" && len(names) > 0 {
		return errors.New("choose either --group or rule names")
	}
	if action == "stop-shares" && (len(names) > 0 || *group != "" || *owner != "" || *ttl != 0) {
		return errors.New("stop-shares stops every share; use stop NAME for selected manually owned rules")
	}
	if action != "start" && *ttl != 0 {
		return errors.New("ttl applies only to start")
	}
	if action != "stop-shares" && *group == "" && len(names) == 0 {
		return errors.New("specify rule names or --group")
	}
	if *owner != "" && !config.ValidName(*owner) {
		return errors.New("invalid owner")
	}
	if *ttl < 0 || *ttl > 24*time.Hour || *ttl%time.Second != 0 {
		return errors.New("ttl must be whole seconds in 1s..24h")
	}
	var expected string
	if action == "start" {
		snapshot, e := config.Load(dir)
		if e != nil {
			return e
		}
		reviewed, e := selectedRules(snapshot, names, *group)
		if e != nil {
			return e
		}
		expected = config.RulesDigest(reviewed)
		if *j && !*yes {
			for _, r := range reviewed {
				if r.Direction == "share" {
					return errors.New("--json share start requires --confirm after reviewing the scope")
				}
			}
		}
		preview := out
		if *j {
			preview = io.Discard
		}
		if err = confirmRulesStart(reviewed, *ttl, newPrompts(in, preview), *yes); err != nil {
			return err
		}
	}
	s, err := ruleRequest(ctx, dir, app.RuleCommand{Action: action, ExpectedRules: expected, Names: names, Group: *group, Owner: *owner, TTLSeconds: int64(*ttl / time.Second)})
	if err != nil {
		return err
	}
	if err = printStatus(out, s, *j); err != nil {
		return err
	}
	if action == "start" {
		c, e := config.Load(dir)
		if e != nil {
			return e
		}
		rules, e := selectedRules(c, names, *group)
		if e != nil {
			return e
		}
		names = nil
		for _, r := range rules {
			names = append(names, r.Name)
		}
		return checkStarted(s, names, *owner)
	}
	return nil
}
func checkStarted(s app.Status, names []string, owner string) error {
	for _, n := range names {
		found := false
		for _, r := range s.Rules {
			if r.Name != n {
				continue
			}
			found = true
			if r.State != "ready" {
				return fmt.Errorf("rule %s did not start (%s): %s; inspect status or doctor and retry explicitly", n, r.ReasonCode, r.Reason)
			}
			if owner != "" && r.Owner != owner {
				return fmt.Errorf("rule %s is not owned by this task", n)
			}
		}
		if !found {
			return fmt.Errorf("start response omitted requested rule %s", n)
		}
	}
	return nil
}
func selectedRules(c config.Config, names []string, group string) ([]config.Rule, error) {
	if c.Version != 2 {
		return nil, errors.New("requires a version 2 profile")
	}
	if group != "" {
		found := false
		for _, g := range c.Groups {
			if g.Name == group {
				names = append([]string(nil), g.Rules...)
				found = true
			}
		}
		if !found {
			return nil, fmt.Errorf("unknown group %q", group)
		}
	}
	var rules []config.Rule
	for _, n := range names {
		found := false
		for _, r := range c.Rules {
			if r.Name == n {
				rules = append(rules, r)
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("unknown rule %q", n)
		}
	}
	return rules, nil
}
func confirmRulesStart(rules []config.Rule, ttl time.Duration, p *prompts, yes bool) error {
	shares := false
	for _, r := range rules {
		if r.Direction == "share" {
			shares = true
			printRulePreview(p.out, r, ttl)
		}
	}
	if !shares {
		return nil
	}
	if ttl < time.Second || ttl > 24*time.Hour {
		return errors.New("share start requires --ttl in 1s..24h")
	}
	fmt.Fprintln(p.out, "Only these explicitly selected peers can reach these loopback services. App authentication is still required. TTL does not cancel remote jobs.")
	return p.confirm("Start these shares for the displayed lifetime?", yes)
}

func listRules(ctx context.Context, dir string, args []string, out io.Writer, sharesOnly bool) error {
	f := flags("rules", out)
	j := f.Bool("json", false, "structured output")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return errors.New("list takes only --json")
	}
	c, err := config.Load(dir)
	if err != nil {
		return err
	}
	if c.Version != 2 {
		return errors.New("named rules require a version 2 profile")
	}
	if !sharesOnly {
		if *j {
			return json.NewEncoder(out).Encode(c.Disabled())
		}
		for _, r := range c.Rules {
			fmt.Fprintf(out, "%s  %s/%s  %s  %d -> %s (saved disabled)\n", r.Name, r.Direction, r.Network, r.Purpose, r.ListenPort, config.Address(r.TargetHost, r.TargetPort))
		}
		return nil
	}
	var s app.Status
	if err = call(ctx, dir, "status", &s); err != nil {
		return err
	}
	filtered := s.Rules[:0]
	for _, r := range s.Rules {
		if r.Direction == "share" {
			filtered = append(filtered, r)
		}
	}
	s.Rules = filtered
	return printStatus(out, s, *j)
}

func groupCommand(ctx context.Context, dir string, args []string, in io.Reader, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: group list | save [--confirm] NAME RULE... | start/stop [flags] NAME")
	}
	action := args[0]
	args = args[1:]
	if action == "list" {
		c, err := config.Load(dir)
		if err != nil {
			return err
		}
		if len(args) != 0 {
			return errors.New("group list takes no arguments")
		}
		for _, g := range c.Groups {
			fmt.Fprintf(out, "%s: %s\n", g.Name, strings.Join(g.Rules, ", "))
		}
		return nil
	}
	if action == "start" || action == "stop" {
		// Parse the group name independently so flags may precede it.
		f := flags("group "+action, out)
		ttl := f.Duration("ttl", 0, "share lifetime (1s..24h)")
		owner := f.String("owner", "", "task owner")
		yes := f.Bool("confirm", false, "confirm displayed share scope")
		j := f.Bool("json", false, "structured output")
		if err := f.Parse(args); err != nil {
			return err
		}
		if f.NArg() != 1 {
			return errors.New("specify exactly one group name")
		}
		forwarded := []string{"--group", f.Arg(0), "--ttl", ttl.String(), "--owner", *owner}
		if *yes {
			forwarded = append(forwarded, "--confirm")
		}
		if *j {
			forwarded = append(forwarded, "--json")
		}
		return namedAction(ctx, dir, action, forwarded, in, out)
	}
	if action != "save" {
		return errors.New("group action must be list, save, start or stop")
	}
	f := flags("group save", out)
	replace := f.Bool("replace", false, "replace a previously saved group after preview")
	yes := f.Bool("confirm", false, "confirm the displayed saved group")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() < 2 {
		return errors.New("usage: group save [--confirm] NAME RULE...")
	}
	g := config.Group{Name: f.Arg(0), Rules: f.Args()[1:]}
	if !config.ValidName(g.Name) {
		return errors.New("invalid group name")
	}
	c, err := config.Load(dir)
	if err != nil {
		return err
	}
	for _, old := range c.Groups {
		if old.Name == g.Name && !*replace {
			return errors.New("group exists; use --replace to preview replacing it")
		}
	}
	if _, err = selectedRules(c, g.Rules, ""); err != nil {
		return err
	}
	fmt.Fprintf(out, "Group %s: %s\nSaving a group does not start any rule.\n", g.Name, strings.Join(g.Rules, ", "))
	if err = newPrompts(in, out).confirm("Save this group?", *yes); err != nil {
		return err
	}
	if running(ctx, dir) {
		_, err = ruleRequest(ctx, dir, app.RuleCommand{Action: "group-save", GroupConfig: &g, Replace: *replace})
		return err
	}
	lock, err := config.AcquireLock(dir)
	if err != nil {
		return err
	}
	defer lock.Close()
	c, err = config.Load(dir)
	if err != nil {
		return err
	}
	found := false
	for i := range c.Groups {
		if c.Groups[i].Name == g.Name {
			if !*replace {
				return errors.New("group appeared after preview; review it before replacing")
			}
			c.Groups[i] = g
			found = true
		}
	}
	if !found {
		c.Groups = append(c.Groups, g)
	}
	return config.Save(dir, c)
}

func waitReady(ctx context.Context, dir string, names []string, owner string, poll time.Duration) (app.Status, error) {
	for {
		var s app.Status
		if err := call(ctx, dir, "status", &s); err != nil {
			return s, err
		}
		ready := true
		for _, name := range names {
			found := false
			for _, r := range s.Rules {
				if r.Name != name {
					continue
				}
				found = true
				if owner != "" && r.Owner != owner {
					return s, fmt.Errorf("rule %s is not owned by this task", name)
				}
				switch r.State {
				case "ready":
				case "failed", "blocked", "stopped", "expired":
					return s, fmt.Errorf("rule %s is %s (%s): %s", name, r.State, r.ReasonCode, r.Reason)
				default:
					ready = false
				}
			}
			if !found {
				return s, fmt.Errorf("requested rule %s is missing", name)
			}
		}
		if ready {
			return s, nil
		}
		if err := pause(ctx, poll); err != nil {
			return s, err
		}
	}
}
func waitCommand(ctx context.Context, dir string, args []string, out io.Writer) error {
	f := flags("wait-ready", out)
	list := f.String("rules", "", "comma-separated rule names")
	group := f.String("group", "", "saved group")
	owner := f.String("owner", "", "expected task owner")
	timeout := f.Duration("timeout", 30*time.Second, "maximum readiness wait")
	j := f.Bool("json", false, "structured output")
	if err := f.Parse(args); err != nil {
		return err
	}
	names, err := namesFrom(*list, f.Args())
	if err != nil {
		return err
	}
	if *group != "" && len(names) > 0 {
		return errors.New("choose --group or rule names")
	}
	if *group == "" && len(names) == 0 {
		return errors.New("choose named rules or a group")
	}
	if *timeout <= 0 || *timeout > 24*time.Hour {
		return errors.New("timeout must be positive and at most 24h")
	}
	if *owner != "" && !config.ValidName(*owner) {
		return errors.New("invalid owner")
	}
	c, err := config.Load(dir)
	if err != nil {
		return err
	}
	rules, err := selectedRules(c, names, *group)
	if err != nil {
		return err
	}
	names = names[:0]
	for _, r := range rules {
		names = append(names, r.Name)
	}
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	s, err := waitReady(ctx, dir, names, *owner, 200*time.Millisecond)
	if printErr := printStatus(out, s, *j); printErr != nil {
		return printErr
	}
	if err == nil && !*j {
		fmt.Fprintln(out, "Transport is ready; application behavior and remote job completion are unverified.")
	}
	return err
}

func taskCommand(ctx context.Context, dir string, args []string, in io.Reader, out io.Writer) error {
	f := flags("task", out)
	list := f.String("rules", "", "comma-separated existing rule names")
	group := f.String("group", "", "saved group")
	ttl := f.Duration("ttl", 0, "connection lifetime, mandatory for shares")
	timeout := f.Duration("timeout", 30*time.Second, "readiness timeout")
	yes := f.Bool("confirm", false, "confirm displayed share scope")
	if err := f.Parse(args); err != nil {
		return err
	}
	argv := f.Args()
	if len(argv) == 0 {
		return errors.New("usage: task --rules NAME[,NAME] [--ttl 1h] -- COMMAND ARG...")
	}
	names, err := namesFrom(*list, nil)
	if err != nil {
		return err
	}
	if *group != "" && len(names) > 0 {
		return errors.New("choose --rules or --group")
	}
	if *group == "" && len(names) == 0 {
		return errors.New("task requires --rules or --group")
	}
	if *timeout <= 0 || *timeout > 24*time.Hour {
		return errors.New("timeout must be positive and at most 24h")
	}
	if *ttl < 0 || *ttl > 24*time.Hour || *ttl%time.Second != 0 {
		return errors.New("ttl must use whole seconds in 1s..24h")
	}
	c, err := config.Load(dir)
	if err != nil {
		return err
	}
	rules, err := selectedRules(c, names, *group)
	if err != nil {
		return err
	}
	names = nil
	for _, r := range rules {
		names = append(names, r.Name)
	}
	if err = confirmRulesStart(rules, *ttl, newPrompts(in, out), *yes); err != nil {
		return err
	}
	random := make([]byte, 16)
	if _, err = rand.Read(random); err != nil {
		return err
	}
	owner := "task-" + hex.EncodeToString(random)
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	// Owned cleanup is attempted even if a partially completed start reports error.
	defer func() {
		cleanup, c := context.WithTimeout(context.Background(), 8*time.Second)
		defer c()
		_, e := ruleRequest(cleanup, dir, app.RuleCommand{Action: "stop", Names: names, Owner: owner})
		if e != nil {
			fmt.Fprintln(out, "Owned cleanup could not be confirmed; successfully leased rules expire 30s after their last renewal:", e)
		}
	}()
	started, err := ruleRequest(ctx, dir, app.RuleCommand{Action: "start", ExpectedRules: config.RulesDigest(rules), Names: names, Owner: owner, TTLSeconds: int64(*ttl / time.Second), LeaseSeconds: 30})
	if err != nil {
		return err
	}
	if err = checkStarted(started, names, owner); err != nil {
		_ = printStatus(out, started, false)
		return err
	}
	renewalDone := make(chan struct{})
	go func() {
		defer close(renewalDone)
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_, e := ruleRequest(ctx, dir, app.RuleCommand{Action: "renew", Names: names, Owner: owner, LeaseSeconds: 30})
				if e != nil {
					cancel(fmt.Errorf("connection lease renewal failed: %w", e))
					return
				}
			}
		}
	}()
	defer func() { cancel(nil); <-renewalDone }()
	waiting, stopWait := context.WithTimeout(ctx, *timeout)
	_, err = waitReady(waiting, dir, names, owner, 200*time.Millisecond)
	stopWait()
	if err != nil {
		return err
	}
	fmt.Fprintln(out, "Transport ready. Running the command directly; remote jobs require their own cancellation API.")
	err = runTaskProcess(ctx, argv, in, out)
	if cause := context.Cause(ctx); cause != nil {
		return cause
	}
	return err
}
