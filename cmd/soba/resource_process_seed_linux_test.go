//go:build resource_process_native && directlan_activation_native && linux && (amd64 || arm64)

package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/core"
	"github.com/webkaz-labs/sobalink/internal/directlan"
	"github.com/webkaz-labs/sobalink/internal/endpointmeta"
	"github.com/webkaz-labs/sobalink/internal/resource"
	pm "github.com/webkaz-labs/sobalink/internal/resourceacceptance/processmodel"
	"github.com/webkaz-labs/sobalink/internal/resourcegrant"
	"github.com/webkaz-labs/sobalink/internal/resourcegroup"
	"github.com/webkaz-labs/sobalink/internal/testfixture"
	"golang.org/x/sys/unix"
)

// This reservation is source-derived, not an operating-system disk quota.
// Three profiles overreserve all three group stores even though only C writes
// one run: 3*(2*1MiB group + 2*256KiB journal + 2*32KiB grants +512KiB main)
// =9.1875MiB. Directory/socket/atomic metadata adds2MiB, below12MiB.
// Supervisor output14.375MiB + inputs80KiB + receipts256KiB =14.703125MiB.
// Aggregate26.703125MiB leaves5.296875MiB; no default4MiB profile/2MiB LAN
// limit is incorrectly treated as the size of this tiny fixed scenario.
const p1ProductReserve = int64(12 << 20)
const p1RootLimit = int64(32 << 20)

type p1Manifest struct {
	SchemaVersion            int    `json:"schemaVersion"`
	Scenario                 int    `json:"scenario"`
	Binary                   string `json:"binary"`
	BinarySHA256             string `json:"binarySHA256"`
	SourceCommit             string `json:"sourceCommit"`
	SourceTree               string `json:"sourceTree"`
	SourceManifestSHA256     string `json:"sourceManifestSHA256"`
	AssetSHA256              string `json:"assetSHA256"`
	DependencyManifestSHA256 string `json:"dependencyManifestSHA256"`
	ToolchainSHA256          string `json:"toolchainSHA256"`
	Target                   string `json:"target"`
	Selector                 string `json:"selector"`
	Tags                     string `json:"tags"`
	CGOEnabled               bool   `json:"cgoEnabled"`
}

type p1Driver struct {
	cancel func()
	done   chan struct{}
	err    error
	raw    json.RawMessage
}
type p1FileSnapshot struct {
	Present bool
	Digest  [32]byte
	Size    int
}
type p1Fixture struct {
	manifest                             p1Manifest
	supervisorGroup                      int
	failureCleanup                       atomic.Bool
	unjoined                             p1Unjoined
	epoch                                int64
	setup, work, cleanup, outer          time.Time
	root                                 string
	rootInfo                             os.FileInfo
	rootHandle                           *p1File
	roleHandles                          [3]*p1File
	roleInfo                             [3]os.FileInfo
	fixtureID                            [16]byte
	owners                               [5]*p1Child
	clis                                 [40]*p1Child
	activeCLI                            *p1Child
	driver                               *p1Driver
	cliCount, pairCount, checkpointCount int
	verified                             [15]bool
	expectedDispatch, expectedLocal      [5][20]uint16
	expectedDigests                      [5][][32]byte
	reservations                         [3]*testfixture.PortReservation
	endpoints                            [3]netip.AddrPort
	released                             [3]bool
	identities                           [3]directlan.Identity
	ids                                  [3]string
	catalogs                             [5]string
	grants                               [2]resourcegrant.ManagementRecord
	prepared, unused                     resourcegroup.PreparedView
	apply, unusedApply                   resourcegroup.ApplyInput
	run                                  resourcegroup.RunView
	selection, unusedSelection           resourcegroup.Selection
	wanted                               [2]resource.Settings
	beforeDescriptors                    [2]resource.Descriptor
	policies                             [2]capacity.Policy
	dryFiles                             [3][6]p1FileSnapshot
	authority                            [3]p1LANFile
	profiles                             [3]core.Profile
	baseline                             [5]uint64
	maintenanceBefore, maintenanceAfter  uint64
	stage                                string
	success                              bool
	quotaMu                              sync.Mutex
	quotaUsed                            int64
	filesMu                              sync.Mutex
	files                                []*p1File
	binaryInfo                           os.FileInfo
}

// This mirrors only the existing public JSON file representation from
// core/direct_lan_metadata.go. It is never assigned to a Core or live owner.
type p1LANFile struct {
	Version               int                         `json:"version"`
	Identity              directlan.Identity          `json:"identity"`
	Selection             core.DirectLANSelection     `json:"selection"`
	Revision              string                      `json:"revision"`
	PreviousLocalEndpoint string                      `json:"previous_local_endpoint"`
	ObservedAt            string                      `json:"observed_at"`
	Peers                 []endpointmeta.PeerRecord   `json:"peers"`
	PendingChange         *endpointmeta.PendingChange `json:"pending_change,omitempty"`
}

func p1CanonicalHex(s string, n int) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == n && hex.EncodeToString(b) == s
}
func (f *p1Fixture) readManifest() {
	path := os.Getenv("SOBALINK_RESOURCE_PROCESS_MANIFEST")
	data, info, err := p1ReadNoFollow(path, 16384, false)
	f.require(err == nil && info != nil, "manifest_read")
	var m p1Manifest
	f.decode(data, &m, 16384)
	var fields map[string]json.RawMessage
	f.decode(data, &fields, 16384)
	f.require(len(fields) == 14, "manifest_fields")
	for _, name := range []string{"schemaVersion", "scenario", "binary", "binarySHA256", "sourceCommit", "sourceTree", "sourceManifestSHA256", "assetSHA256", "dependencyManifestSHA256", "toolchainSHA256", "target", "selector", "tags", "cgoEnabled"} {
		_, present := fields[name]
		f.require(present, "manifest_field_missing")
	}
	f.require(m.SchemaVersion == 1 && m.Scenario == 1 && m.Selector == p1Selector && m.Tags == p1Tags && !m.CGOEnabled && p1CanonicalHex(m.SourceCommit, 20) && p1CanonicalHex(m.SourceTree, 20), "manifest_schema")
	for _, s := range []string{m.BinarySHA256, m.SourceManifestSHA256, m.AssetSHA256, m.DependencyManifestSHA256, m.ToolchainSHA256} {
		f.require(p1CanonicalHex(s, 32), "manifest_digest")
	}
	f.require(m.Target == "linux-amd64" || m.Target == "linux-arm64", "manifest_target")
	f.manifest = m
	f.checkBinary()
}
func (f *p1Fixture) checkBinary() {
	file, err := p1OpenNoFollow(f.manifest.Binary, false)
	f.require(err == nil, "binary_open")
	before, err := file.Stat()
	if err != nil {
		_ = file.Close()
		f.require(false, "binary_stat")
	}
	if !before.Mode().IsRegular() || before.Size() <= 0 || before.Size() > 256<<20 || before.Mode().Perm()&0111 == 0 {
		_ = file.Close()
		f.require(false, "binary_size")
	}
	hash := sha256.New()
	n, readErr := io.Copy(hash, io.LimitReader(file, (256<<20)+1))
	after, statErr := file.Stat()
	closeErr := file.Close()
	current, pathErr := os.Lstat(f.manifest.Binary)
	f.require(readErr == nil && statErr == nil && closeErr == nil && pathErr == nil && n == before.Size() && p1SameStable(before, after) && p1SameStable(after, current) && hex.EncodeToString(hash.Sum(nil)) == f.manifest.BinarySHA256, "binary_identity")
	if f.binaryInfo == nil {
		f.binaryInfo = before
	} else {
		f.require(p1SameStable(f.binaryInfo, before), "binary_replaced")
	}
}

func p1OpenNoFollow(path string, directory bool) (*os.File, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || len(path) > 512 || strings.Contains(path, "\\") {
		return nil, errors.New("invalid fixed path")
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(parts) > 32 {
		return nil, errors.New("path depth")
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	for i, name := range parts {
		if name == "" || name == "." || name == ".." {
			_ = unix.Close(fd)
			return nil, errors.New("invalid path part")
		}
		flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK
		if i < len(parts)-1 || directory {
			flags |= unix.O_DIRECTORY
		}
		if i == len(parts)-1 && !directory {
			var stat unix.Stat_t
			if e := unix.Fstatat(fd, name, &stat, unix.AT_SYMLINK_NOFOLLOW); e != nil {
				_ = unix.Close(fd)
				return nil, e
			}
			if stat.Mode&unix.S_IFMT != unix.S_IFREG {
				_ = unix.Close(fd)
				return nil, errors.New("not regular")
			}
		}
		next, e := unix.Openat(fd, name, flags, 0)
		closed := unix.Close(fd)
		if e != nil {
			return nil, e
		}
		if closed != nil {
			_ = unix.Close(next)
			return nil, closed
		}
		fd = next
	}
	return os.NewFile(uintptr(fd), path), nil
}
func p1SameStable(a, b os.FileInfo) bool {
	return a != nil && b != nil && os.SameFile(a, b) && a.Size() == b.Size() && a.ModTime() == b.ModTime() && a.Mode() == b.Mode()
}
func p1Private(info os.FileInfo, directory bool) bool {
	if info == nil {
		return false
	}
	s, ok := info.Sys().(*syscall.Stat_t)
	if !ok || s.Uid != uint32(os.Geteuid()) {
		return false
	}
	if directory {
		return info.IsDir() && info.Mode().Perm() == 0700
	}
	return info.Mode().IsRegular() && info.Mode().Perm() == 0600 && s.Nlink == 1
}
func p1ReadNoFollow(path string, limit int, private bool) ([]byte, os.FileInfo, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, nil, err
	}
	if !before.Mode().IsRegular() || before.Size() < 0 || before.Size() > int64(limit) || private && !p1Private(before, false) {
		return nil, nil, errors.New("invalid bounded file")
	}
	file, err := p1OpenNoFollow(path, false)
	if err != nil {
		return nil, nil, err
	}
	opened, e1 := file.Stat()
	if e1 != nil || !opened.Mode().IsRegular() || !p1SameStable(before, opened) {
		closed := file.Close()
		if closed != nil {
			return nil, nil, closed
		}
		return nil, nil, errors.New("opened file changed")
	}
	raw, e2 := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	after, e3 := file.Stat()
	e4 := file.Close()
	current, e5 := os.Lstat(path)
	if e1 != nil || e2 != nil || e3 != nil || e4 != nil || e5 != nil || len(raw) > limit || !p1SameStable(before, opened) || !p1SameStable(opened, after) || !p1SameStable(after, current) {
		return nil, nil, errors.New("unstable bounded file")
	}
	return raw, current, nil
}
func p1Identity(info os.FileInfo) pm.DirectoryIdentity {
	s, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return pm.DirectoryIdentity{}
	}
	var result pm.DirectoryIdentity
	result.Kind = pm.UnixDirectoryIdentity
	result.DeviceOrVolume = uint64(s.Dev)
	binary.BigEndian.PutUint64(result.FileID[:8], uint64(s.Ino))
	return result
}
func p1Path(value string) pm.PathValue {
	var p pm.PathValue
	if len(value) <= len(p.Bytes) {
		p.Length = uint16(len(value))
		copy(p.Bytes[:], value)
	}
	return p
}
func p1RoleIndex(role pm.OwnerRole) int {
	switch role {
	case pm.RoleC0, pm.RoleC1, pm.RoleC2:
		return 0
	case pm.RoleA0:
		return 1
	case pm.RoleB0:
		return 2
	}
	return -1
}
func (f *p1Fixture) roleDir(role pm.OwnerRole) string {
	return filepath.Join(f.root, "state", []string{"c", "a", "b"}[p1RoleIndex(role)])
}
func (f *p1Fixture) seed() {
	f.quotaUsed = p1ProductReserve
	root, err := os.MkdirTemp("", "p1-")
	f.require(err == nil, "root_create")
	f.root = root
	info, err := os.Lstat(root)
	f.require(err == nil && p1Private(info, true) && len(filepath.Join(root, "state", "c", "control.sock")) <= 100, "root_private")
	f.rootInfo = info
	rootFile, openErr := p1OpenNoFollow(root, true)
	f.require(openErr == nil, "root_open")
	f.rootHandle = f.retain(rootFile)
	opened, statErr := rootFile.Stat()
	f.require(statErr == nil && os.SameFile(info, opened), "root_handle_identity")
	for _, name := range []string{"state", "state/c", "state/a", "state/b", "inputs", "evidence", "receipts", "home", "tmp", "config", "cache", "appdata", "localappdata"} {
		f.require(os.Mkdir(filepath.Join(root, name), 0700) == nil, "directory_create")
	}
	_, err = rand.Read(f.fixtureID[:])
	f.require(err == nil && f.fixtureID != [16]byte{}, "fixture_random")
	f.identities = [3]directlan.Identity{{Seed: strings.Repeat("61", 32)}, {Seed: strings.Repeat("62", 32)}, {Seed: strings.Repeat("63", 32)}}
	sort.Slice(f.identities[:], func(i, j int) bool { return f.identities[i].PublicKey() < f.identities[j].PublicKey() })
	excluded := []uint16{54543, 54544, 54545}
	for i := range f.reservations {
		r, e := testfixture.ReserveLoopbackTCPUDP(netip.MustParseAddr("127.0.0.1"), excluded...)
		f.require(e == nil, "endpoint_reservation")
		f.reservations[i] = r
		f.endpoints[i] = r.Endpoint()
		excluded = append(excluded, r.Endpoint().Port())
	}
	roles := []pm.OwnerRole{pm.RoleC0, pm.RoleA0, pm.RoleB0}
	for i, role := range roles {
		dir := f.roleDir(role)
		f.roleInfo[i], err = os.Lstat(dir)
		f.require(err == nil && p1Private(f.roleInfo[i], true), "role_private")
		roleFile, openErr := p1OpenNoFollow(dir, true)
		f.require(openErr == nil, "role_open")
		f.roleHandles[i] = f.retain(roleFile)
		opened, statErr := roleFile.Stat()
		f.require(statErr == nil && os.SameFile(f.roleInfo[i], opened), "role_handle_identity")
		peers := []int{0}
		if i == 0 {
			peers = []int{1, 2}
		}
		saved := p1LANFile{Version: 4, Identity: f.identities[i], Selection: core.DirectLANSelection{Listen: f.endpoints[i].String(), Prefixes: []string{"127.0.0.0/8"}}, Revision: "1", PreviousLocalEndpoint: f.endpoints[i].String(), ObservedAt: time.Unix(f.epoch-1, 0).UTC().Format(time.RFC3339Nano), Peers: []endpointmeta.PeerRecord{}}
		directPeers := []directlan.Peer{}
		for _, j := range peers {
			wire := endpointmeta.PeerWire{Key: f.identities[j].PublicKey(), Name: "synthetic-peer", Endpoint: f.endpoints[j].String(), TunnelKey: f.identities[j].TunnelKey()}
			saved.Peers = append(saved.Peers, endpointmeta.PeerRecord{Peer: wire, Revision: "1"})
			directPeers = append(directPeers, directlan.Peer{Key: wire.Key, Name: wire.Name, Endpoint: f.endpoints[j], TunnelKey: wire.TunnelKey})
		}
		cfg := directlan.Config{Identity: saved.Identity, Listen: f.endpoints[i], AllowedPrefixes: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}, Peers: directPeers}
		f.require(cfg.Validate() == nil, "seed_direct_config")
		f.validateLAN(saved, i, false)
		profile := core.Profile{Version: 1, Settings: core.Settings{Locale: "en", Theme: "system", Network: "direct-lan", Hostname: []string{"synthetic-controller", "synthetic-target-a", "synthetic-target-b"}[i]}, Peers: []core.Trust{}, Services: []core.ServiceSpec{}}
		if i == 0 {
			profile.Services = []core.ServiceSpec{{ID: "synthetic-service", Name: "Synthetic service", Direction: "forward", Network: "tcp", Ports: "8080", Lifetime: "until-stopped", LoopbackHost: "127.0.0.1", PeerID: f.identities[1].PublicKey()}}
		}
		f.seedJSON(filepath.Join(dir, "direct-lan.json"), saved, 64<<10)
		f.seedJSON(filepath.Join(dir, "sobalink.json"), profile, 16<<10)
	}
	f.wanted = [2]resource.Settings{{TransferConcurrentFiles: capacity.Limited(3), TransferConcurrentPerPeer: capacity.Limited(1)}, {TransferConcurrentFiles: capacity.Limited(4), TransferConcurrentPerPeer: capacity.Limited(2)}}
}
func (f *p1Fixture) seedJSON(path string, v any, max int) {
	raw, err := json.Marshal(v)
	f.require(err == nil && len(raw) < max, "seed_encoding")
	f.writeNew(path, append(raw, '\n'), false)
}
func (f *p1Fixture) charge(n int) bool {
	f.quotaMu.Lock()
	defer f.quotaMu.Unlock()
	if n < 0 || int64(n) > p1RootLimit-f.quotaUsed {
		return false
	}
	f.quotaUsed += int64(n)
	return true
}
func (f *p1Fixture) writeNew(path string, raw []byte, charge bool) {
	if charge {
		f.require(f.charge(len(raw)), "aggregate_quota")
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	f.require(err == nil, "private_write_open")
	n, e1 := file.Write(raw)
	e2 := file.Sync()
	e3 := file.Close()
	f.require(n == len(raw) && e1 == nil && e2 == nil && e3 == nil, "private_write")
}
func (f *p1Fixture) readPrivate(role pm.OwnerRole, name string, max int, optional bool) []byte {
	dir := f.roleDir(role)
	current, err := os.Lstat(dir)
	f.require(err == nil && os.SameFile(current, f.roleInfo[p1RoleIndex(role)]) && p1Private(current, true), "role_replaced")
	handle := f.roleHandles[p1RoleIndex(role)]
	opened, statErr := handle.file.Stat()
	f.require(statErr == nil && os.SameFile(opened, f.roleInfo[p1RoleIndex(role)]), "role_handle_changed")
	raw, err := p1ReadRelative(handle.file, name, max)
	current, statErr = os.Lstat(dir)
	f.require(statErr == nil && os.SameFile(current, f.roleInfo[p1RoleIndex(role)]), "role_path_changed")
	if optional && errors.Is(err, os.ErrNotExist) {
		return nil
	}
	f.require(err == nil, "private_read")
	return raw
}
func (f *p1Fixture) protectedFiles() [3][6]p1FileSnapshot {
	var out [3][6]p1FileSnapshot
	for i, role := range []pm.OwnerRole{pm.RoleC1, pm.RoleA0, pm.RoleB0} {
		for j, name := range []string{"sobalink.json", "direct-lan.json", "capacity.json", "resource-state/state.json", "resource-grants/state.json", "resource-groups/state.json"} {
			limit := []int{16 << 10, 64 << 10, 64 << 10, 256 << 10, 32 << 10, 1 << 20}[j]
			raw := f.readPrivate(role, name, limit, true)
			if raw != nil {
				out[i][j] = p1FileSnapshot{true, sha256.Sum256(raw), len(raw)}
			}
		}
	}
	return out
}
func (f *p1Fixture) validateLAN(saved p1LANFile, i int, managed bool) {
	local := endpointmeta.PeerWire{Key: f.identities[i].PublicKey(), TunnelKey: f.identities[i].TunnelKey(), Endpoint: f.endpoints[i].String()}
	model := endpointmeta.Snapshot{Version: saved.Version, Revision: saved.Revision, LocalPeer: local, LocalScope: endpointmeta.Scope{Family: "ipv4", Prefixes: []string{"127.0.0.0/8"}}, PreviousLocalEndpoint: saved.PreviousLocalEndpoint, ObservedAt: saved.ObservedAt, Peers: saved.Peers, PendingChange: saved.PendingChange}
	f.require(saved.Version == 4 && saved.Identity == f.identities[i] && saved.Selection.Listen == local.Endpoint && reflect.DeepEqual(saved.Selection.Prefixes, model.LocalScope.Prefixes) && saved.PendingChange == nil && model.ValidateAt(time.Now()) == nil, "lan_state")
	count := 1
	if i == 0 {
		count = 2
	}
	f.require(len(saved.Peers) == count, "lan_star")
	for _, peer := range saved.Peers {
		f.require(peer.Peer.Key != local.Key && peer.UpgradePending == nil && peer.PairRevocation == nil, "lan_peer")
		if !managed {
			f.require(peer.PairContext == nil && !peer.ContextConfirmed && peer.EndpointState == nil, "seed_authority")
		}
	}
}
func (f *p1Fixture) writeSelections() {
	f.selection = f.makeSelection(f.wanted)
	f.unusedSelection = f.makeSelection([2]resource.Settings{{TransferConcurrentFiles: capacity.Limited(5), TransferConcurrentPerPeer: capacity.Limited(2)}, {TransferConcurrentFiles: capacity.Limited(6), TransferConcurrentPerPeer: capacity.Limited(3)}})
	for i, value := range []resourcegroup.Selection{f.selection, f.unusedSelection} {
		raw, err := json.Marshal(value)
		f.require(err == nil && len(raw) <= resourcegroup.MaxSelectionBytes, "selection_encode")
		decoded, err := resourcegroup.DecodeSelection(raw)
		f.require(err == nil && reflect.DeepEqual(decoded, value), "selection_decode")
		f.writeNew(filepath.Join(f.root, "inputs", []string{"selection-applied.json", "selection-unused.json"}[i]), raw, true)
	}
}
func (f *p1Fixture) makeSelection(wanted [2]resource.Settings) resourcegroup.Selection {
	template, err := resourcegroup.NewTemplate(wanted[0])
	f.require(err == nil, "template")
	s := resourcegroup.Selection{SchemaVersion: 1, Template: template, Members: []resourcegroup.Member{}}
	for i, g := range f.grants {
		m := resourcegroup.Member{PeerKey: f.identities[i+1].PublicKey(), Selector: resourcegrant.ManagementSelector{ProtocolVersion: 2, Target: g.Record.Target, GrantID: g.Record.ID, GrantRevision: g.Record.Revision}}
		if i == 1 {
			m.Override = &resourcegroup.Override{TransferConcurrentFiles: &wanted[i].TransferConcurrentFiles, TransferConcurrentPerPeer: &wanted[i].TransferConcurrentPerPeer}
		}
		s.Members = append(s.Members, m)
	}
	return s
}
func (f *p1Fixture) removeSuccessfulRoot() bool {
	if f.root == "" {
		return false
	}
	current, err := os.Lstat(f.root)
	if err != nil || !os.SameFile(current, f.rootInfo) || !p1Private(current, true) {
		return false
	}
	return os.RemoveAll(f.root) == nil
}

// Reads only fixed relative names from an already retained directory identity.
// Intermediate descriptors are owned locally; every close result is checked.
func p1ReadRelative(base *os.File, name string, limit int) (raw []byte, result error) {
	if base == nil || filepath.IsAbs(name) || filepath.Clean(name) != name || strings.Contains(name, "\\") {
		return nil, errors.New("invalid relative file")
	}
	parts := strings.Split(name, "/")
	if len(parts) > 6 {
		return nil, errors.New("relative depth")
	}
	fd, err := unix.FcntlInt(base.Fd(), unix.F_DUPFD_CLOEXEC, 3)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := unix.Close(fd); err != nil {
			raw = nil
			result = err
		}
	}()
	for _, part := range parts[:len(parts)-1] {
		if part == "" || part == "." || part == ".." {
			return nil, errors.New("invalid relative part")
		}
		next, e := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
		if e != nil {
			return nil, e
		}
		old := fd
		fd = next
		if e = unix.Close(old); e != nil {
			return nil, e
		}
		var st unix.Stat_t
		if e = unix.Fstat(fd, &st); e != nil {
			return nil, e
		}
		if st.Mode&unix.S_IFMT != unix.S_IFDIR || st.Mode&0777 != 0700 || st.Uid != uint32(os.Geteuid()) {
			return nil, errors.New("unsafe relative directory")
		}
	}
	leaf := parts[len(parts)-1]
	if leaf == "" || leaf == "." || leaf == ".." {
		return nil, errors.New("invalid relative leaf")
	}
	var before, after unix.Stat_t
	if e := unix.Fstatat(fd, leaf, &before, unix.AT_SYMLINK_NOFOLLOW); e != nil {
		return nil, e
	}
	if before.Mode&unix.S_IFMT != unix.S_IFREG || before.Mode&0777 != 0600 || before.Nlink != 1 || before.Uid != uint32(os.Geteuid()) || before.Size < 0 || before.Size > int64(limit) {
		return nil, errors.New("unsafe relative file")
	}
	child, e := unix.Openat(fd, leaf, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if e != nil {
		return nil, e
	}
	file := os.NewFile(uintptr(child), "p1-bound-file")
	opened, e1 := file.Stat()
	if e1 != nil || !p1Private(opened, false) {
		e2 := file.Close()
		if e2 != nil {
			return nil, e2
		}
		return nil, errors.New("relative opened type")
	}
	raw, e2 := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	closedInfo, e3 := file.Stat()
	e4 := file.Close()
	e5 := unix.Fstatat(fd, leaf, &after, unix.AT_SYMLINK_NOFOLLOW)
	identity, ok := opened.Sys().(*syscall.Stat_t)
	if e2 != nil || e3 != nil || e4 != nil || e5 != nil || !ok || uint64(identity.Dev) != uint64(before.Dev) || identity.Ino != before.Ino || identity.Size != before.Size || identity.Mtim.Sec != before.Mtim.Sec || identity.Mtim.Nsec != before.Mtim.Nsec || !p1SameStable(opened, closedInfo) || len(raw) > limit || before.Dev != after.Dev || before.Ino != after.Ino || before.Size != after.Size || before.Mtim != after.Mtim || before.Mode != after.Mode || before.Uid != after.Uid || before.Nlink != after.Nlink {
		return nil, errors.New("unstable relative file")
	}
	return raw, nil
}

// Two stable-boundary inventories detect unexpected reachable persistence. This
// is a closed namespace assertion, not a periodic disk-quota implementation.
func (f *p1Fixture) inventoryDirectory(path string, max int) []os.DirEntry {
	file, err := p1OpenNoFollow(path, true)
	f.require(err == nil, "inventory_open")
	before, e1 := file.Stat()
	entries, e2 := file.ReadDir(max + 1)
	after, e3 := file.Stat()
	e4 := file.Close()
	current, e5 := os.Lstat(path)
	f.require(e1 == nil && (e2 == nil || e2 == io.EOF) && e3 == nil && e4 == nil && e5 == nil && len(entries) <= max && p1Private(before, true) && p1SameStable(before, after) && p1SameStable(after, current), "inventory_binding")
	return entries
}
func (f *p1Fixture) inventoryAtomic(role pm.OwnerRole, name string, limit int) {
	for _, entry := range f.inventoryDirectory(filepath.Join(f.roleDir(role), name), 2) {
		leaf := entry.Name()
		f.require(leaf == "owner.lock" || leaf == "snapshot", "atomic_namespace_entry")
		bound := limit
		if leaf == "owner.lock" {
			bound = 64
		}
		raw := f.readPrivate(role, filepath.Join(name, leaf), bound, false)
		if leaf == "owner.lock" {
			f.require(string(raw) == "sobalink atomic persistence v1\n", "atomic_namespace_marker")
		}
	}
}
func (f *p1Fixture) assertFileInventory() {
	for _, role := range []pm.OwnerRole{pm.RoleC0, pm.RoleA0, pm.RoleB0} {
		dir := f.roleDir(role)
		for _, entry := range f.inventoryDirectory(dir, 10) {
			name := entry.Name()
			path := filepath.Join(dir, name)
			switch name {
			case "sobalink.json", "direct-lan.json", "capacity.json", "process.lock":
				max := 64 << 10
				if name == "sobalink.json" {
					max = 16 << 10
				}
				if name == "process.lock" {
					max = 64
				}
				f.readPrivate(role, name, max, false)
			case "control.sock":
				info, err := os.Lstat(path)
				f.require(err == nil && info.Mode()&os.ModeType == os.ModeSocket && info.Mode().Perm() == 0600, "control_namespace")
			case ".sobalink-atomic-v1":
				f.inventoryAtomic(role, name, 64<<10)
			case "resource-state", "resource-grants", "resource-groups":
				limit := 256 << 10
				if name == "resource-grants" {
					limit = 32 << 10
				}
				if name == "resource-groups" {
					limit = 1 << 20
				}
				for _, child := range f.inventoryDirectory(path, 2) {
					switch child.Name() {
					case "state.json":
						f.readPrivate(role, filepath.Join(name, "state.json"), limit, false)
					case ".sobalink-atomic-v1":
						f.inventoryAtomic(role, filepath.Join(name, child.Name()), limit)
					default:
						f.require(false, "sidecar_namespace_entry")
					}
				}
			default:
				f.require(false, "unreviewed_product_file")
			}
		}
	}
	for _, name := range []string{"home", "tmp", "config", "cache", "appdata", "localappdata"} {
		f.require(len(f.inventoryDirectory(filepath.Join(f.root, name), 0)) == 0, "synthetic_home_not_empty")
	}
}
