package config

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	MaxRules        = 64
	MaxGroups       = 32
	MaxAllowedPeers = 32
)

type Rule struct {
	Name         string    `json:"name"`
	Purpose      string    `json:"purpose"`
	Direction    string    `json:"direction"`
	Network      string    `json:"network"`
	ListenPort   int       `json:"listen_port"`
	TargetHost   string    `json:"target_host"`
	TargetPort   int       `json:"target_port"`
	PeerID       string    `json:"peer_id,omitempty"`
	AllowedPeers []PeerRef `json:"allowed_peers,omitempty"`
	Enabled      bool      `json:"enabled"`
}

type PeerRef struct {
	ID   string `json:"id"`
	Host string `json:"host"`
}

type Group struct {
	Name  string   `json:"name"`
	Rules []string `json:"rules"`
}

var namePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)
var peerIDPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9:_-]{0,127}$`)

func ValidName(s string) bool   { return namePattern.MatchString(s) }
func ValidPeerID(s string) bool { return peerIDPattern.MatchString(s) }

// NewRules creates an idle profile without joining a tailnet or opening listeners.
func NewRules() (Config, error) {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return Config{}, err
	}
	return Config{Version: 2, Hostname: "tsnet-bridge-" + hex.EncodeToString(b), Mode: "rules"}, nil
}

func (r Rule) Validate() error {
	if !ValidName(r.Name) {
		return errors.New("rule name must be 1..64 letters, digits, hyphens or underscores, beginning with a letter or digit")
	}
	switch r.Purpose {
	case "web", "ssh", "db", "ai", "custom", "rustdesk":
	default:
		return errors.New("purpose must be web, ssh, db, ai, custom or rustdesk")
	}
	if r.Network != "tcp" && r.Network != "udp" {
		return errors.New("network must be tcp or udp")
	}
	if r.TargetPort < 1 || r.TargetPort > 65535 {
		return errors.New("target port must be in 1..65535")
	}
	switch r.Direction {
	case "forward":
		if r.ListenPort < 1024 || r.ListenPort > 65535 {
			return errors.New("forward listen port must be in 1024..65535")
		}
		if !ValidHost(r.TargetHost) {
			return errors.New("forward target must be a tailnet IP or peer DNS name")
		}
		if !ValidPeerID(r.PeerID) {
			return errors.New("forward requires a pinned peer ID")
		}
		if len(r.AllowedPeers) != 0 {
			return errors.New("forward cannot have allowed peers")
		}
	case "share":
		if r.ListenPort < 1 || r.ListenPort > 65535 {
			return errors.New("share listen port must be in 1..65535")
		}
		if r.TargetHost != "127.0.0.1" && r.TargetHost != "::1" {
			return errors.New("share target must be exactly numeric loopback 127.0.0.1 or ::1")
		}
		if r.PeerID != "" {
			return errors.New("share cannot have a destination peer ID")
		}
		if len(r.AllowedPeers) < 1 || len(r.AllowedPeers) > MaxAllowedPeers {
			return fmt.Errorf("share requires 1..%d explicitly allowed peers", MaxAllowedPeers)
		}
		seen := map[string]bool{}
		for _, p := range r.AllowedPeers {
			if !ValidPeerID(p.ID) || !ValidHost(p.Host) {
				return errors.New("allowed peer requires a pinned ID and tailnet host")
			}
			if seen[p.ID] {
				return errors.New("duplicate allowed peer ID")
			}
			seen[p.ID] = true
		}
	default:
		return errors.New("direction must be forward or share")
	}
	return nil
}

func (c Config) validateRules() error {
	if !label.MatchString(c.Hostname) || len(c.Hostname) > 63 {
		return errors.New("invalid node hostname")
	}
	if c.Mode != "rules" {
		return errors.New("version 2 mode must be rules")
	}
	if len(c.Rules) > MaxRules || len(c.Groups) > MaxGroups {
		return fmt.Errorf("profile supports at most %d rules and %d groups", MaxRules, MaxGroups)
	}
	names := map[string]bool{}
	for _, r := range c.Rules {
		if err := r.Validate(); err != nil {
			return fmt.Errorf("rule %q: %w", r.Name, err)
		}
		if names[r.Name] {
			return fmt.Errorf("duplicate rule name %q", r.Name)
		}
		names[r.Name] = true
	}
	groups := map[string]bool{}
	for _, g := range c.Groups {
		if !ValidName(g.Name) {
			return errors.New("invalid group name")
		}
		if groups[g.Name] {
			return fmt.Errorf("duplicate group name %q", g.Name)
		}
		groups[g.Name] = true
		if len(g.Rules) == 0 || len(g.Rules) > MaxRules {
			return fmt.Errorf("group %q requires 1..%d rules", g.Name, MaxRules)
		}
		seen := map[string]bool{}
		for _, n := range g.Rules {
			if !names[n] {
				return fmt.Errorf("group %q refers to unknown rule %q", g.Name, n)
			}
			if seen[n] {
				return fmt.Errorf("group %q repeats rule %q", g.Name, n)
			}
			seen[n] = true
		}
	}
	// Migration retains the dedicated RustDesk profile metadata and constraints.
	// It never converts the RustDesk ports to generic automatic suggestions.
	if c.PublicKey != "" || c.IDHost != "" || c.RelayHost != "" || c.IDPort != 0 || c.RelayPort != 0 || c.LocalIDPort != 0 || c.LocalRelayPort != 0 || c.SOCKSPort != 0 {
		old := c
		old.Version = 1
		old.Mode = "forward"
		old.Rules = nil
		old.Groups = nil
		if err := old.Validate(); err != nil {
			return fmt.Errorf("legacy RustDesk metadata: %w", err)
		}
		expected := legacyRules(old, "placeholder", "placeholder")
		for _, want := range expected {
			found := false
			for _, r := range c.Rules {
				if r.Name == want.Name {
					found = true
					if r.Purpose != "rustdesk" || r.Direction != "forward" || r.Network != want.Network || r.ListenPort != want.ListenPort || r.TargetHost != want.TargetHost || r.TargetPort != want.TargetPort {
						return errors.New("migrated RustDesk fixed rules must retain their original ports and hosts")
					}
				}
			}
			if !found {
				return errors.New("migrated RustDesk fixed rules cannot be removed")
			}
		}
	}
	return nil
}

// Disabled returns an independent copy appropriate for persistence/import/export.
// Enabled is never a restart instruction; runtime starts are always explicit.
func (c Config) Disabled() Config {
	c.Rules = append([]Rule(nil), c.Rules...)
	for i := range c.Rules {
		c.Rules[i].Enabled = false
		c.Rules[i].AllowedPeers = append([]PeerRef(nil), c.Rules[i].AllowedPeers...)
	}
	c.Groups = append([]Group(nil), c.Groups...)
	for i := range c.Groups {
		c.Groups[i].Rules = append([]string(nil), c.Groups[i].Rules...)
	}
	return c
}

func legacyRules(c Config, idPeer, relayPeer string) []Rule {
	return []Rule{
		{Name: "rustdesk-nat", Purpose: "rustdesk", Direction: "forward", Network: "tcp", ListenPort: c.LocalIDPort - 1, TargetHost: c.IDHost, TargetPort: c.IDPort - 1, PeerID: idPeer},
		{Name: "rustdesk-id", Purpose: "rustdesk", Direction: "forward", Network: "tcp", ListenPort: c.LocalIDPort, TargetHost: c.IDHost, TargetPort: c.IDPort, PeerID: idPeer},
		{Name: "rustdesk-heartbeat", Purpose: "rustdesk", Direction: "forward", Network: "udp", ListenPort: c.LocalIDPort, TargetHost: c.IDHost, TargetPort: c.IDPort, PeerID: idPeer},
		{Name: "rustdesk-relay", Purpose: "rustdesk", Direction: "forward", Network: "tcp", ListenPort: c.LocalRelayPort, TargetHost: c.RelayHost, TargetPort: c.RelayPort, PeerID: relayPeer},
	}
}

func MigrateV1(c Config, idPeer, relayPeer string) (Config, error) {
	if c.Version != 1 {
		return Config{}, errors.New("migration requires a version 1 profile")
	}
	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	if c.Mode != "forward" {
		return Config{}, errors.New("SOCKS profiles cannot be losslessly migrated; keep the version 1 profile or init a separate version 2 profile")
	}
	if relayPeer == "" && strings.EqualFold(strings.TrimSuffix(c.IDHost, "."), strings.TrimSuffix(c.RelayHost, ".")) {
		relayPeer = idPeer
	}
	c.Rules = legacyRules(c, idPeer, relayPeer)
	c.Groups = []Group{{Name: "rustdesk", Rules: []string{"rustdesk-nat", "rustdesk-id", "rustdesk-heartbeat", "rustdesk-relay"}}}
	c.Version = 2
	c.Mode = "rules"
	return c, c.Validate()
}

// SaveMigration retains the exact original file before atomically replacing it.
// The caller must hold the profile lock and have obtained explicit confirmation.
func SaveMigration(dir string, next Config) (string, error) {
	if next.Version != 2 {
		return "", errors.New("migration destination must be version 2")
	}
	if err := next.Validate(); err != nil {
		return "", err
	}
	old, err := Load(dir)
	if err != nil {
		return "", err
	}
	if old.Version != 1 {
		return "", errors.New("source profile is no longer version 1")
	}
	p := filepath.Join(dir, "profile.json")
	data, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	backup := filepath.Join(dir, "profile.v1.backup.json")
	f, err := os.OpenFile(backup, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", fmt.Errorf("create original backup: %w", err)
	}
	if err = Protect(backup, false); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return backup, err
	}
	return backup, Save(dir, next)
}

// RulesDigest binds a start request to the exact reviewed rule definitions.
// Ordering and saved Enabled flags do not change the runtime permission scope.
func RulesDigest(rules []Rule) string {
	copy := Config{Rules: rules}.Disabled().Rules
	sort.Slice(copy, func(i, j int) bool { return copy[i].Name < copy[j].Name })
	for i := range copy {
		sort.Slice(copy[i].AllowedPeers, func(a, b int) bool { return copy[i].AllowedPeers[a].ID < copy[i].AllowedPeers[b].ID })
	}
	data, _ := json.Marshal(copy)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
