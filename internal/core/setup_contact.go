package core

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/webkaz-labs/sobalink/internal/lanpolicy"
)

const setupContactPrefix = "sobalink-setup-contact-v1:"
const setupVerificationPrefix = "sobalink-setup-verify-v1:"
const setupLifetime = 120 * time.Second
const maxSetupContact = 1536

func setupError(code, message string) error { return &localCommandError{code, message} }
func setupHex(s string) bool {
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == 32 && hex.EncodeToString(b) == s && s != strings.Repeat("0", 64)
}
func setupAlias(s string) bool {
	if len(s) < 1 || len(s) > 80 || !utf8.ValidString(s) || strings.TrimSpace(s) != s {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == '\u2028' || r == '\u2029' {
			return false
		}
	}
	return true
}
func setupEndpoint(raw string) (netip.AddrPort, error) {
	ap, e := netip.ParseAddrPort(raw)
	if e != nil || ap.String() != raw || ap.Port() < 1024 || ap.Port() >= DiscoveryPort && ap.Port() <= 54545 || ap.Addr().Is4In6() || ap.Addr().Zone() != "" || !(ap.Addr().IsPrivate() || ap.Addr().IsLoopback()) || ap.Addr().IsUnspecified() || ap.Addr().IsMulticast() {
		return netip.AddrPort{}, setupError("setup_contact_invalid", "invalid setup contact; request current contact information from the other device")
	}
	return ap, nil
}
func validateSetupContact(c SetupContact, now time.Time) error {
	if c.Kind != "sobalink-setup-contact" || c.Version != 1 || !setupHex(c.SessionID) || !setupAlias(c.Alias) || !now.Before(c.ExpiresAt) || c.ExpiresAt.After(now.Add(setupLifetime)) {
		return setupError("setup_contact_invalid", "invalid or expired setup contact; request current contact information from the other device")
	}
	_, e := setupEndpoint(c.Bootstrap)
	return e
}

// EncodeSetupContact exports only short-lived public fields, never a capability.
func EncodeSetupContact(c SetupContact) (string, error) {
	if c.Kind != "sobalink-setup-contact" || c.Version != 1 || !setupHex(c.SessionID) || !setupAlias(c.Alias) || c.ExpiresAt.IsZero() {
		return "", errors.New("invalid setup contact")
	}
	if _, e := setupEndpoint(c.Bootstrap); e != nil {
		return "", e
	}
	c.ExpiresAt = c.ExpiresAt.UTC()
	b, e := json.Marshal(c)
	if e != nil {
		return "", e
	}
	out := setupContactPrefix + base64.RawURLEncoding.EncodeToString(b)
	if len(out) > maxSetupContact {
		return "", errors.New("setup contact is too large")
	}
	return out, nil
}

// InspectSetupContact parses an untrusted hint. It grants no authority and dials nothing.
func InspectSetupContact(text string, now time.Time) (SetupContact, error) {
	var c SetupContact
	if len(text) > maxSetupContact {
		return c, setupError("setup_contact_invalid", "setup contact exceeds its size limit")
	}
	text = strings.Trim(text, " \t\r\n")
	if !strings.HasPrefix(text, setupContactPrefix) {
		return c, setupError("setup_contact_invalid", "use current setup contact text from the other device")
	}
	b, e := base64.RawURLEncoding.Strict().DecodeString(strings.TrimPrefix(text, setupContactPrefix))
	if e != nil || strictLANJSON(b, &c) != nil {
		return SetupContact{}, setupError("setup_contact_invalid", "invalid setup contact")
	}
	if e = validateSetupContact(c, now); e != nil {
		return SetupContact{}, e
	}
	canonical, e := EncodeSetupContact(c)
	if e != nil || canonical != text {
		return SetupContact{}, setupError("setup_contact_invalid", "setup contact is not canonical")
	}
	return c, nil
}
func setupScopeID(s SetupScope) string {
	b, _ := json.Marshal(struct {
		Index                          int
		Address, Prefix, Name, Binding string
	}{s.InterfaceIndex, s.LocalAddress, s.Prefix, s.Interface, s.linkBinding})
	d := sha256.Sum256(b)
	return hex.EncodeToString(d[:])
}
func localSetupScopes() ([]SetupScope, error) {
	return readSetupScopes(net.Interfaces, func(i net.Interface) ([]net.Addr, error) { return i.Addrs() }, false)
}
func readSetupScopes(interfaces func() ([]net.Interface, error), addresses func(net.Interface) ([]net.Addr, error), loopback bool) ([]SetupScope, error) {
	list, e := interfaces()
	if e != nil {
		return nil, setupError("setup_scope_unavailable", "local network choices are unavailable; refresh and try again")
	}
	out := []SetupScope{}
	seen := map[string]bool{}
	for _, i := range list {
		if i.Index <= 0 || i.Flags&net.FlagUp == 0 || !loopback && i.Flags&net.FlagLoopback != 0 {
			continue
		}
		as, e := addresses(i)
		if e != nil {
			return nil, setupError("setup_scope_changed", "local addresses changed; refresh the network choices")
		}
		for _, a := range as {
			p, e := netip.ParsePrefix(a.String())
			if e != nil {
				continue
			}
			ip := p.Addr()
			if ip.Is4In6() || ip.Zone() != "" || !(ip.IsPrivate() || loopback && ip.IsLoopback()) {
				continue
			}
			if _, err := (lanpolicy.Config{Mode: lanpolicy.AllowedLANDestinations, Prefixes: []string{p.Masked().String()}}).Canonical(); err != nil {
				continue
			}
			s := SetupScope{InterfaceIndex: i.Index, Interface: i.Name, LocalAddress: ip.String(), Prefix: p.Masked().String(), linkBinding: i.HardwareAddr.String()}
			s.ScopeID = setupScopeID(s)
			if seen[s.ScopeID] {
				continue
			}
			seen[s.ScopeID] = true
			out = append(out, s)
			if len(out) > maxLANAddressChoices {
				return nil, setupError("setup_scope_unavailable", "too many local addresses; use advanced network setup")
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ScopeID < out[j].ScopeID })
	return out, nil
}
func (c *Core) setupScopes() ([]SetupScope, error) {
	if c.setupScopeRead != nil {
		return c.setupScopeRead()
	}
	return localSetupScopes()
}
func (c *Core) setupScope(id string) (SetupScope, error) {
	all, e := c.setupScopes()
	if e != nil {
		return SetupScope{}, e
	}
	for _, s := range all {
		if s.ScopeID == id && setupScopeID(s) == id {
			return s, nil
		}
	}
	return SetupScope{}, setupError("setup_scope_changed", "selected network changed; refresh and approve its current address and scope")
}
