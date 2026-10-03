package core

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strconv"

	"github.com/webkaz-labs/sobalink/internal/config"
)

// RustDeskMetadata is public application configuration, never a signing key or
// credential. Its role references bind the helper to four exact saved forwards.
type RustDeskMetadata struct {
	PublicKey          string `json:"publicKey"`
	IDServiceID        string `json:"idServiceId"`
	HeartbeatServiceID string `json:"heartbeatServiceId"`
	NATServiceID       string `json:"natServiceId"`
	RelayServiceID     string `json:"relayServiceId"`
}

type RustDeskSetup struct {
	Name           string `json:"name"`
	Backend        string `json:"backend"`
	IDPeerID       string `json:"idPeerId"`
	RelayPeerID    string `json:"relayPeerId"`
	PublicKey      string `json:"publicKey"`
	IDPort         int    `json:"idPort"`
	RelayPort      int    `json:"relayPort"`
	LocalIDPort    int    `json:"localIdPort"`
	LocalRelayPort int    `json:"localRelayPort"`
	LoopbackHost   string `json:"loopbackHost"`
	Lifetime       string `json:"lifetime"`
	TTLSeconds     int    `json:"ttlSeconds"`
}

type RustDeskSetupRequest struct {
	Configuration    RustDeskSetup `json:"configuration"`
	ExpectedRevision string        `json:"expectedRevision,omitempty"`
}

type RustDeskSetupReview struct {
	Configuration  RustDeskSetup          `json:"configuration"`
	Group          ServiceGroup           `json:"group"`
	Services       []ServiceSpec          `json:"services"`
	Revision       string                 `json:"revision"`
	Saved          bool                   `json:"saved"`
	Applied        bool                   `json:"applied"`
	ClientSettings RustDeskClientSettings `json:"clientSettings"`
}

type RustDeskRoleSettings struct {
	Role          string `json:"role"`
	ServiceID     string `json:"serviceId"`
	Network       string `json:"network"`
	PeerID        string `json:"peerId"`
	RemotePort    int    `json:"remotePort"`
	LocalEndpoint string `json:"localEndpoint"`
	Lifetime      string `json:"lifetime"`
	TTLSeconds    int    `json:"ttlSeconds"`
	Status        string `json:"status"`
	ListenerReady bool   `json:"listenerReady"`
}

type RustDeskClientSettings struct {
	Group          string                 `json:"group"`
	IDServer       string                 `json:"idServer"`
	RelayServer    string                 `json:"relayServer"`
	PublicKey      string                 `json:"publicKey"`
	Proxy          string                 `json:"proxy"`
	UDPEnabled     bool                   `json:"udpEnabled"`
	RemoteIDSuffix string                 `json:"remoteIdSuffix"`
	Application    string                 `json:"application"`
	Roles          []RustDeskRoleSettings `json:"roles"`
	Notices        []ClientNotice         `json:"notices"`
}

func normalizeRustDeskSetup(in RustDeskSetup) (RustDeskSetup, error) {
	if in.Name == "" {
		in.Name = "rustdesk"
	}
	// The suffixes are part of valid saved service names, not a separate limit.
	if !config.ValidName(in.Name + "-heartbeat") {
		return in, &localCommandError{"rustdesk_name_invalid", "choose a valid group name short enough for the -heartbeat service suffix"}
	}
	if in.Backend != "tailnet" && in.Backend != "lan" {
		return in, &localCommandError{"service_backend_required", "choose tailnet or lan explicitly"}
	}
	if in.RelayPeerID == "" {
		in.RelayPeerID = in.IDPeerID
	}
	if !config.ValidPeerID(in.IDPeerID) || !config.ValidPeerID(in.RelayPeerID) {
		return in, &localCommandError{"rustdesk_peer_invalid", "select the immutable ID-server and relay peer IDs"}
	}
	if err := validateRustDeskPublicKey(in.PublicKey); err != nil {
		return in, err
	}
	if in.IDPort == 0 {
		in.IDPort = 21116
	}
	if in.RelayPort == 0 {
		in.RelayPort = 21117
	}
	if in.LocalIDPort == 0 {
		in.LocalIDPort = 32116
	}
	if in.LocalRelayPort == 0 {
		in.LocalRelayPort = 32117
	}
	if in.IDPort < 1025 || in.IDPort > 65535 || in.LocalIDPort < 1025 || in.LocalIDPort > 65535 || in.RelayPort < 1024 || in.RelayPort > 65535 || in.LocalRelayPort < 1024 || in.LocalRelayPort > 65535 {
		return in, &localCommandError{"rustdesk_port_invalid", "ID ports must be 1025..65535, and relay ports 1024..65535; NAT uses ID port minus one"}
	}
	if in.LocalRelayPort == in.LocalIDPort || in.LocalRelayPort == in.LocalIDPort-1 {
		return in, &localCommandError{"rustdesk_port_overlap", "local relay must differ from local ID and NAT ports"}
	}
	if in.IDPeerID == in.RelayPeerID && (in.RelayPort == in.IDPort || in.RelayPort == in.IDPort-1) {
		return in, &localCommandError{"rustdesk_port_overlap", "ID, NAT and relay TCP ports must differ on the same peer"}
	}
	var err error
	in.LoopbackHost, err = serviceLoopback(in.LoopbackHost)
	if err != nil {
		return in, err
	}
	if in.Lifetime == "" && in.TTLSeconds == 0 {
		in.Lifetime = "until-stopped"
	}
	in.Lifetime, err = serviceLifetime(in.Lifetime, in.TTLSeconds, "forward")
	return in, err
}

func validateRustDeskPublicKey(publicKey string) error {
	key, err := base64.StdEncoding.DecodeString(publicKey)
	if err != nil || len(key) != 32 {
		return &localCommandError{"rustdesk_key_invalid", "RustDesk public key must be base64 encoding of 32 bytes"}
	}
	return nil
}

func rustDeskRoleSpecs(in RustDeskSetup) []ServiceSpec {
	roles := []struct {
		role, protocol, peer string
		remote, local        int
	}{
		{"nat", "tcp", in.IDPeerID, in.IDPort - 1, in.LocalIDPort - 1},
		{"id", "tcp", in.IDPeerID, in.IDPort, in.LocalIDPort},
		{"heartbeat", "udp", in.IDPeerID, in.IDPort, in.LocalIDPort},
		{"relay", "tcp", in.RelayPeerID, in.RelayPort, in.LocalRelayPort},
	}
	out := make([]ServiceSpec, 0, len(roles))
	for _, role := range roles {
		// Stable IDs keep a repeated preview/save inert and preserve group selectors.
		raw, _ := json.Marshal([]string{"rustdesk", in.Backend, in.Name, role.role})
		sum := sha256.Sum256(raw)
		out = append(out, ServiceSpec{ID: hex.EncodeToString(sum[:16]), Name: in.Name + "-" + role.role, Backend: in.Backend, Direction: "forward", Network: role.protocol, Ports: strconv.Itoa(role.remote), LocalPort: role.local, LoopbackHost: in.LoopbackHost, Lifetime: in.Lifetime, TTLSeconds: in.TTLSeconds, PeerID: role.peer, Purpose: "rustdesk"})
	}
	return out
}

func rustDeskGroup(in RustDeskSetup, specs []ServiceSpec) ServiceGroup {
	return ServiceGroup{Name: in.Name, ServiceIDs: []string{specs[0].ID, specs[1].ID, specs[2].ID, specs[3].ID}, RustDesk: &RustDeskMetadata{PublicKey: in.PublicKey, NATServiceID: specs[0].ID, IDServiceID: specs[1].ID, HeartbeatServiceID: specs[2].ID, RelayServiceID: specs[3].ID}}
}

func rustDeskMembers(p Profile, group ServiceGroup) ([]ServiceSpec, error) {
	if group.RustDesk == nil {
		return nil, &localCommandError{"rustdesk_group_required", "select a saved RustDesk group"}
	}
	m := group.RustDesk
	if err := validateRustDeskPublicKey(m.PublicKey); err != nil {
		return nil, err
	}
	ids := []string{m.NATServiceID, m.IDServiceID, m.HeartbeatServiceID, m.RelayServiceID}
	if len(group.ServiceIDs) != len(ids) {
		return nil, &localCommandError{"rustdesk_group_inconsistent", "RustDesk metadata requires the four exact role services; explicitly detach metadata before changing the group"}
	}
	members := make([]ServiceSpec, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		if id == "" || seen[id] || !slices.Contains(group.ServiceIDs, id) {
			return nil, &localCommandError{"rustdesk_group_inconsistent", "RustDesk group role references are missing or duplicated"}
		}
		seen[id] = true
		i := slices.IndexFunc(p.Services, func(s ServiceSpec) bool { return s.ID == id })
		if i < 0 {
			return nil, &localCommandError{"rustdesk_group_inconsistent", "RustDesk group has a missing saved service"}
		}
		members = append(members, p.Services[i])
	}
	return members, nil
}

func validateRustDeskGroup(p Profile, group ServiceGroup) error {
	if group.RustDesk == nil {
		return nil
	}
	members, err := rustDeskMembers(p, group)
	if err != nil {
		return err
	}
	nat, id, heartbeat, relay := members[0], members[1], members[2], members[3]
	idPort, idErr := strconv.Atoi(id.Ports)
	relayPort, relayErr := strconv.Atoi(relay.Ports)
	setup, err := normalizeRustDeskSetup(RustDeskSetup{Name: group.Name, Backend: id.Backend, IDPeerID: id.PeerID, RelayPeerID: relay.PeerID, PublicKey: group.RustDesk.PublicKey, IDPort: idPort, RelayPort: relayPort, LocalIDPort: id.LocalPort, LocalRelayPort: relay.LocalPort, LoopbackHost: id.LoopbackHost, Lifetime: id.Lifetime, TTLSeconds: id.TTLSeconds})
	if idErr != nil || relayErr != nil || err != nil || idPort == 0 || relayPort == 0 || id.LocalPort == 0 || relay.LocalPort == 0 {
		return &localCommandError{"rustdesk_group_inconsistent", "RustDesk ID and relay must retain explicit valid ports, peer IDs and public key"}
	}
	expected := rustDeskRoleSpecs(setup)
	for i, s := range []ServiceSpec{nat, id, heartbeat, relay} {
		want := expected[i]
		// IDs/names may be preserved by an import; every transport-bearing field
		// must still match the role. This metadata never changes inbound grants.
		want.ID, want.Name = s.ID, s.Name
		if !reflect.DeepEqual(s, want) {
			return &localCommandError{"rustdesk_group_inconsistent", "RustDesk role peer, protocol, port mapping or lifetime changed; explicitly detach metadata before editing individual rules"}
		}
	}
	return nil
}

func (c *Core) rustDeskSetupReview(in RustDeskSetup) (RustDeskSetupReview, Profile, error) {
	var review RustDeskSetupReview
	in, err := normalizeRustDeskSetup(in)
	if err != nil {
		return review, Profile{}, err
	}
	p := c.profileCopy()
	specs := rustDeskRoleSpecs(in)
	for i := range specs {
		specs[i], err = c.normalizeDefinition(specs[i])
		if err != nil {
			return review, Profile{}, err
		}
	}
	group := rustDeskGroup(in, specs)
	next := cloneProfile(p)
	saved := false
	if index := slices.IndexFunc(p.Groups, func(g ServiceGroup) bool { return g.Name == group.Name }); index >= 0 {
		existing := p.Groups[index]
		members, memberErr := rustDeskMembers(p, existing)
		if memberErr != nil || !reflect.DeepEqual(existing, group) || !reflect.DeepEqual(members, specs) {
			return review, Profile{}, &localCommandError{"rustdesk_group_conflict", "a different saved group uses this name; choose another name or explicitly review its existing definitions"}
		}
		saved = true
	} else {
		for _, s := range specs {
			if slices.ContainsFunc(p.Services, func(existing ServiceSpec) bool { return existing.ID == s.ID || existing.Name == s.Name }) {
				return review, Profile{}, &localCommandError{"service_name_conflict", "a saved service already uses a RustDesk role name or ID; choose another group name"}
			}
		}
		next.Services = append(next.Services, specs...)
		next.Groups = append(next.Groups, group)
	}
	if err := validateProfile(next); err != nil {
		return review, Profile{}, err
	}
	if err := c.validateGroupCapacity(next, p); err != nil {
		return review, Profile{}, err
	}
	if len(next.Services) > len(p.Services) && int64(len(next.Services)) > c.limit("logical", "savedServices") {
		return review, Profile{}, &localCommandError{"service_capacity", "four RustDesk rules exceed savedServices capacity; review capacity settings"}
	}
	encoded, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return review, Profile{}, err
	}
	if int64(len(encoded))+1 > c.limit("resources", "profileBytes") {
		return review, Profile{}, &localCommandError{"profile_capacity", "RustDesk setup exceeds the profile storage budget"}
	}
	raw, _ := json.Marshal(struct {
		Profile       Profile
		Configuration RustDeskSetup
	}{p, in})
	hash := sha256.Sum256(raw)
	settings, err := c.rustDeskSettings(next, group)
	if err != nil {
		return review, Profile{}, err
	}
	if !saved {
		for i := range settings.Roles {
			settings.Roles[i].Status = "planned"
			settings.Roles[i].ListenerReady = false
		}
	}
	review = RustDeskSetupReview{Configuration: in, Group: group, Services: specs, Revision: hex.EncodeToString(hash[:]), Saved: saved, ClientSettings: settings}
	return review, next, nil
}

func (c *Core) rustDeskSettings(p Profile, group ServiceGroup) (RustDeskClientSettings, error) {
	var settings RustDeskClientSettings
	if err := validateRustDeskGroup(p, group); err != nil {
		return settings, err
	}
	members, err := rustDeskMembers(p, group)
	if err != nil {
		return settings, err
	}
	settings = RustDeskClientSettings{Group: group.Name, IDServer: config.Address(members[1].LoopbackHost, members[1].LocalPort), RelayServer: config.Address(members[3].LoopbackHost, members[3].LocalPort), PublicKey: group.RustDesk.PublicKey, Proxy: "", UDPEnabled: true, RemoteIDSuffix: "/r", Application: "unverified", Roles: []RustDeskRoleSettings{}, Notices: rustDeskNotices()}
	roles := []string{"nat", "id", "heartbeat", "relay"}
	for i, s := range members {
		port, _ := strconv.Atoi(s.Ports)
		status, ready := c.clientServiceState(s.ID)
		c.mu.RLock()
		if active := c.active[s.ID]; active != nil {
			s.Lifetime, s.TTLSeconds = active.spec.Lifetime, active.spec.TTLSeconds
		}
		c.mu.RUnlock()
		settings.Roles = append(settings.Roles, RustDeskRoleSettings{Role: roles[i], ServiceID: s.ID, Network: s.Network, PeerID: s.PeerID, RemotePort: port, LocalEndpoint: config.Address(s.LoopbackHost, s.LocalPort), Lifetime: s.Lifetime, TTLSeconds: s.TTLSeconds, Status: status, ListenerReady: ready})
	}
	return settings, nil
}

func (c *Core) clientHelperCommand(ctx context.Context, name string, raw json.RawMessage) (any, error) {
	switch name {
	case "rustdesk.preview", "rustdesk.save":
		var in RustDeskSetupRequest
		if err := decodePayload(raw, &in); err != nil {
			return nil, err
		}
		review, next, err := c.rustDeskSetupReview(in.Configuration)
		if err != nil {
			return nil, err
		}
		if name == "rustdesk.preview" {
			return review, nil
		}
		if in.ExpectedRevision == "" || in.ExpectedRevision != review.Revision {
			return nil, &localCommandError{"rustdesk_revision_conflict", "profile or RustDesk setup changed; preview all four rules and public key again"}
		}
		if review.Saved {
			return review, nil
		}
		saveErr := c.saveProfile(next)
		if !atomicPublished(saveErr) {
			return nil, saveErr
		}
		c.mu.Lock()
		c.profile = next
		c.mu.Unlock()
		review.Saved, review.Applied = true, true
		for i := range review.ClientSettings.Roles {
			review.ClientSettings.Roles[i].Status = "saved"
			review.ClientSettings.Roles[i].ListenerReady = false
		}
		return review, saveErr
	case "rustdesk.settings":
		var in struct {
			Group string `json:"group"`
		}
		if err := decodePayload(raw, &in); err != nil {
			return nil, err
		}
		p := c.profileCopy()
		for _, group := range p.Groups {
			if group.Name == in.Group {
				return c.rustDeskSettings(p, group)
			}
		}
		return nil, &localCommandError{"group_not_found", "saved RustDesk group no longer exists"}
	case "client.settings":
		var in ClientSettingsRequest
		if err := decodePayload(raw, &in); err != nil {
			return nil, err
		}
		return c.clientSettings(ctx, in)
	}
	return nil, errors.New("unknown client helper command")
}

func rustDeskNotices() []ClientNotice {
	return []ClientNotice{
		{"rustdesk_backup", "Back up existing RustDesk server and proxy settings before changing them; stopping soba does not restore them.", "RustDesk のサーバー・プロキシ設定を変更する前に控えを保存してください。soba を停止しても元の設定には戻りません。"},
		{"rustdesk_same_relay", "Use the same local relay address and port at every participating endpoint. Mixed profiles are unverified.", "参加するすべての端末で同じローカルリレーアドレスとポートを使ってください。異なる構成の混在は未検証です。"},
		{"rustdesk_forward_settings", "Keep RustDesk proxy blank and UDP enabled. Connect with remote-ID/r. All four forwarding rules and remote server permissions are required.", "RustDesk のプロキシは空欄、UDP は有効にし、接続先を remote-ID/r にしてください。4つの転送設定と接続先サーバーへの許可が必要です。"},
		{"rustdesk_socks_role", "SOCKS CONNECT-only mode cannot register a controlled RustDesk 1.4.9 endpoint with OSS server 1.1.16; other client roles need separate validation.", "SOCKS CONNECT のみの構成では RustDesk 1.4.9 の被操作端末を OSS サーバー 1.1.16 に登録できません。他のクライアント用途は個別の検証が必要です。"},
		{"application_unverified", "Saved settings and ready listeners do not verify RustDesk screen sharing or input control. No RustDesk application settings were changed.", "保存済み設定や待受の準備完了は RustDesk の画面表示・操作の成功を示しません。RustDesk アプリの設定は変更していません。"},
	}
}

// Keep formatted endpoint construction centralized for local API consumers.
func clientPortText(first, last int) string {
	if first == last {
		return strconv.Itoa(first)
	}
	return fmt.Sprintf("%d-%d", first, last)
}
