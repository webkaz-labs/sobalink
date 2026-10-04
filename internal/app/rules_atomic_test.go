package app

import (
	"errors"
	"reflect"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/config"
)

func TestRuleSaveReconcilesCommittedConfigWithoutStarting(t *testing.T) {
	for _, action := range []string{"save-insert", "save-replace", "group-insert", "group-replace"} {
		t.Run(action, func(t *testing.T) {
			s, _ := ruleFixture(t)
			original := testRule(t, "original")
			initial, err := config.NewRules()
			if err != nil {
				t.Fatal(err)
			}
			initial.Rules = []config.Rule{original}
			if action == "group-replace" {
				initial.Groups = []config.Group{{Name: "reviewed", Rules: []string{"original"}}}
			}
			if err := config.Save(s.Dir, initial); err != nil {
				t.Fatal(err)
			}
			s.Config = initial
			s.rules = newRuleManager(s)

			q := RuleCommand{}
			switch action {
			case "save-insert":
				added := testRule(t, "added")
				q = RuleCommand{Action: "save", Rule: &added}
			case "save-replace":
				replacement := original
				replacement.TargetPort = 443
				q = RuleCommand{Action: "save", Rule: &replacement, Replace: true}
			case "group-insert":
				q = RuleCommand{Action: "group-save", GroupConfig: &config.Group{Name: "reviewed", Rules: []string{"original"}}}
			case "group-replace":
				q = RuleCommand{Action: "group-save", GroupConfig: &config.Group{Name: "reviewed", Rules: []string{"original"}}, Replace: true}
			}
			committedErr := errors.Join(config.ErrAtomicCommitted, errors.New("injected post-publication durability error"))
			s.rules.writeConfig = func(path string, data []byte) error {
				if err := config.AtomicWrite(path, data); err != nil {
					return err
				}
				return committedErr
			}

			_, err = s.rules.command(t.Context(), q)
			if err != committedErr || !errors.Is(err, config.ErrAtomicCommitted) {
				t.Fatalf("command error = %v, want original committed error %v", err, committedErr)
			}
			disk, err := config.Load(s.Dir)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(s.Config, disk) {
				t.Fatalf("runtime config differs from published config:\nruntime: %#v\ndisk: %#v", s.Config, disk)
			}
			if len(s.rules.entries) != len(disk.Rules) {
				t.Fatalf("runtime entries = %d, published rules = %d", len(s.rules.entries), len(disk.Rules))
			}
			for _, saved := range disk.Rules {
				entry := s.rules.entries[saved.Name]
				if entry == nil || !reflect.DeepEqual(entry.config, saved) {
					t.Fatalf("runtime entry %q does not match published rule: %#v", saved.Name, entry)
				}
				if saved.Enabled || entry.desired || entry.server != nil {
					t.Fatalf("published rule %q was enabled or activated", saved.Name)
				}
				if entry.status.State != "stopped" || entry.status.Target != config.Address(saved.TargetHost, saved.TargetPort) {
					t.Fatalf("runtime status for published rule %q is stale: %#v", saved.Name, entry.status)
				}
			}
		})
	}
}

func TestRuleSavePrecommitFailureLeavesRuntimeAndDiskUnchanged(t *testing.T) {
	for _, action := range []string{"save", "group-save"} {
		t.Run(action, func(t *testing.T) {
			s, _ := ruleFixture(t)
			original := testRule(t, "original")
			initial, err := config.NewRules()
			if err != nil {
				t.Fatal(err)
			}
			initial.Rules = []config.Rule{original}
			if err := config.Save(s.Dir, initial); err != nil {
				t.Fatal(err)
			}
			s.Config = initial
			s.rules = newRuleManager(s)
			beforeDisk, err := config.Load(s.Dir)
			if err != nil {
				t.Fatal(err)
			}
			beforeEntries := make(map[string]config.Rule, len(s.rules.entries))
			for name, entry := range s.rules.entries {
				beforeEntries[name] = entry.config
			}

			failure := errors.New("injected precommit failure")
			s.rules.writeConfig = func(string, []byte) error { return failure }
			q := RuleCommand{Action: action}
			if action == "save" {
				added := testRule(t, "added")
				q.Rule = &added
			} else {
				q.GroupConfig = &config.Group{Name: "reviewed", Rules: []string{"original"}}
			}
			_, err = s.rules.command(t.Context(), q)
			if !errors.Is(err, failure) {
				t.Fatalf("command error = %v, want injected failure", err)
			}
			afterDisk, err := config.Load(s.Dir)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(beforeDisk, afterDisk) || !reflect.DeepEqual(initial, s.Config) {
				t.Fatal("precommit failure changed persisted or in-memory config")
			}
			if len(beforeEntries) != len(s.rules.entries) {
				t.Fatal("precommit failure changed runtime entries")
			}
			for name, rule := range beforeEntries {
				entry := s.rules.entries[name]
				if entry == nil || !reflect.DeepEqual(rule, entry.config) || entry.desired || entry.server != nil {
					t.Fatalf("precommit failure changed runtime entry %q", name)
				}
			}
		})
	}
}
