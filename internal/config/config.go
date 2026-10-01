// Package config owns the versioned, deliberately small connection profile.
package config

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

type Config struct {
	Rules          []Rule  `json:"rules,omitempty"`
	Groups         []Group `json:"groups,omitempty"`
	Version        int     `json:"version"`
	Hostname       string  `json:"hostname"`
	Mode           string  `json:"mode"`
	IDHost         string  `json:"id_host,omitempty"`
	RelayHost      string  `json:"relay_host,omitempty"`
	PublicKey      string  `json:"public_key,omitempty"`
	IDPort         int     `json:"id_port,omitempty"`
	RelayPort      int     `json:"relay_port,omitempty"`
	LocalIDPort    int     `json:"local_id_port,omitempty"`
	LocalRelayPort int     `json:"local_relay_port,omitempty"`
	SOCKSPort      int     `json:"socks_port,omitempty"`
}
type Credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func New(host, key string) (Config, error) {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return Config{}, err
	}
	c := Config{Version: 1, Hostname: "tsnet-bridge-" + hex.EncodeToString(b), Mode: "forward", IDHost: host, RelayHost: host, PublicKey: key, IDPort: 21116, RelayPort: 21117, LocalIDPort: 32116, LocalRelayPort: 32117, SOCKSPort: 1080}
	return c, c.Validate()
}
func NewCredentials() (Credentials, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return Credentials{}, err
	}
	return Credentials{Username: "bridge-" + hex.EncodeToString(b[:4]), Password: base64.RawURLEncoding.EncodeToString(b[4:])}, nil
}

var label = regexp.MustCompile(`^[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?$`)

func ValidHost(host string) bool {
	if ip, err := netip.ParseAddr(host); err == nil {
		return TailnetIP(ip)
	}
	host = strings.TrimSuffix(host, ".")
	if len(host) == 0 || len(host) > 253 {
		return false
	}
	for _, l := range strings.Split(host, ".") {
		if !label.MatchString(l) {
			return false
		}
	}
	return true
}
func TailnetIP(ip netip.Addr) bool {
	return ip.Zone() == "" && ip != netip.MustParseAddr("100.100.100.100") && (netip.MustParsePrefix("100.64.0.0/10").Contains(ip) || netip.MustParsePrefix("fd7a:115c:a1e0::/48").Contains(ip))
}
func (c Config) Validate() error {
	if c.Version == 2 {
		return c.validateRules()
	}
	if c.Version != 1 {
		return errors.New("unsupported profile version")
	}
	if len(c.Rules) != 0 || len(c.Groups) != 0 {
		return errors.New("named rules and groups require version 2")
	}
	if !label.MatchString(c.Hostname) || len(c.Hostname) > 63 {
		return errors.New("invalid node hostname")
	}
	if c.Mode != "forward" && c.Mode != "socks" {
		return errors.New("mode must be forward or socks")
	}
	if !ValidHost(c.IDHost) || !ValidHost(c.RelayHost) {
		return errors.New("server host must be a tailnet IP or peer DNS name, without port or URL")
	}
	key, err := base64.StdEncoding.DecodeString(c.PublicKey)
	if err != nil || len(key) != 32 {
		return errors.New("RustDesk public key must be base64 encoding of 32 bytes")
	}
	for name, p := range map[string]int{"id_port": c.IDPort, "relay_port": c.RelayPort, "local_id_port": c.LocalIDPort, "local_relay_port": c.LocalRelayPort, "socks_port": c.SOCKSPort} {
		if p < 1024 || p > 65535 {
			return fmt.Errorf("%s must be in 1024..65535", name)
		}
	}
	if c.IDPort < 1025 || c.LocalIDPort < 1025 {
		return errors.New("ID port minus one must remain unprivileged")
	}
	if c.LocalRelayPort == c.LocalIDPort || c.LocalRelayPort == c.LocalIDPort-1 {
		return errors.New("local ID and relay ports overlap")
	}
	return nil
}
func Address(host string, port int) string { return net.JoinHostPort(host, strconv.Itoa(port)) }
func Loopback(port int) string             { return Address("127.0.0.1", port) }
func DefaultDir() (string, error) {
	d, e := os.UserConfigDir()
	return filepath.Join(d, "tsnet-bridge"), e
}
func Load(dir string) (Config, error) {
	var c Config
	e := ReadJSON(filepath.Join(dir, "profile.json"), &c)
	if e != nil {
		return c, e
	}
	return c, c.Validate()
}
func ReadJSON(path string, v any) error {
	info, e := os.Lstat(path)
	if e != nil {
		return e
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("JSON file must be a regular file, not a symlink")
	}
	if info.Size() > 64<<10 {
		return errors.New("JSON file exceeds 64 KiB limit")
	}
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, 64<<10))
	d.DisallowUnknownFields()
	if e = d.Decode(v); e != nil {
		return e
	}
	var extra any
	if e = d.Decode(&extra); e != io.EOF {
		return errors.New("unexpected data after JSON")
	}
	return nil
}
func Save(dir string, c Config) error {
	if e := c.Validate(); e != nil {
		return e
	}
	if c.Version == 2 {
		c = c.Disabled()
	}
	return WriteJSON(filepath.Join(dir, "profile.json"), c)
}
func WriteJSON(path string, v any) error {
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return e
	}
	if len(b)+1 > 64<<10 {
		return errors.New("JSON exceeds 64 KiB limit; reduce rules or peer scope before saving")
	}
	return AtomicWrite(path, append(b, '\n'))
}
func AtomicWrite(path string, b []byte) error {
	if e := SecureDir(filepath.Dir(path)); e != nil {
		return e
	}
	return atomicWriteFile(path, b)
}

// AtomicWritePrivate writes a private file while preserving permissions of an
// existing parent directory, for exports and per-user startup registrations.
func AtomicWritePrivate(path string, b []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("destination parent must be a real directory")
	}
	return atomicWriteFile(path, b)
}
func atomicWriteFile(path string, b []byte) error {
	if info, e := os.Lstat(path); e == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
		return errors.New("refusing non-regular destination")
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".write-*")
	if e != nil {
		return e
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if e = f.Chmod(0600); e == nil {
		e = Protect(tmp, false)
	}
	if e == nil {
		_, e = f.Write(b)
	}
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e != nil {
		return e
	}
	if ce != nil {
		return ce
	}
	return replace(tmp, path)
}
