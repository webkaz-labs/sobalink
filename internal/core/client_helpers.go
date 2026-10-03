package core

import (
	"context"
	"fmt"
	"net"
	"slices"
	"strconv"

	"github.com/webkaz-labs/sobalink/internal/identity"
	"github.com/webkaz-labs/sobalink/internal/ranges"
)

// ClientNotice codes remain stable for machine consumers; both human languages
// travel with the same notice so CLI and Web cannot lose a safety qualification.
type ClientNotice struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	MessageJA string `json:"messageJa"`
}

type ClientSettingsRequest struct {
	IDs   []string `json:"ids,omitempty"`
	Group string   `json:"group,omitempty"`
}

type ClientSettingsView struct {
	Services    []ClientServiceSettings  `json:"services"`
	RustDesk    []RustDeskClientSettings `json:"rustdesk"`
	Application string                   `json:"application"`
	Notices     []ClientNotice           `json:"notices"`
}

// A mapping is inclusive and compact. LocalFirst+n maps to RemoteFirst+n.
// For shares, local is the application target and remote is the exposed port.
type ClientPortMapping struct {
	LocalFirst  int `json:"localFirst"`
	LocalLast   int `json:"localLast"`
	RemoteFirst int `json:"remoteFirst"`
	RemoteLast  int `json:"remoteLast"`
}

type SSHClientHint struct {
	HostKeyAlias string   `json:"hostKeyAlias"`
	Args         []string `json:"args"`
	Command      string   `json:"command"`
}

type ClientServiceSettings struct {
	ID              string              `json:"id"`
	Name            string              `json:"name"`
	Backend         string              `json:"backend"`
	Direction       string              `json:"direction"`
	Purpose         string              `json:"purpose"`
	Network         string              `json:"network"`
	PeerID          string              `json:"peerId,omitempty"`
	AllowedPeerIDs  []string            `json:"allowedPeerIds,omitempty"`
	LocalHost       string              `json:"localHost"`
	LocalEndpoint   string              `json:"localEndpoint,omitempty"`
	RemoteEndpoints []string            `json:"remoteEndpoints"`
	RemoteHosts     []string            `json:"remoteHosts"`
	Mappings        []ClientPortMapping `json:"mappings"`
	Lifetime        string              `json:"lifetime"`
	TTLSeconds      int                 `json:"ttlSeconds"`
	Status          string              `json:"status"`
	ListenerReady   bool                `json:"listenerReady"`
	Application     string              `json:"application"`
	SSH             *SSHClientHint      `json:"ssh,omitempty"`
	HTTPCandidate   string              `json:"httpCandidate,omitempty"`
	Notices         []ClientNotice      `json:"notices"`
}

func (c *Core) clientServiceState(id string) (string, bool) {
	c.mu.RLock()
	if active := c.active[id]; active != nil {
		view := c.serviceView(active)
		status, _ := view["status"].(string)
		c.mu.RUnlock()
		ready := status == "active" && active.guard() == nil && c.serviceTransportReady(active)
		if status == "active" && !ready {
			status = "reconnecting"
		}
		return status, ready
	}
	status := c.serviceStates[id]
	c.mu.RUnlock()
	if status == "" {
		status = "saved"
	}
	return status, false
}

func (c *Core) clientSettings(ctx context.Context, in ClientSettingsRequest) (ClientSettingsView, error) {
	p := c.profileCopy()
	selected := p.Services
	if in.Group != "" || len(in.IDs) > 0 {
		var err error
		selected, _, err = selectServices(p, serviceSelection{IDs: in.IDs, Group: in.Group})
		if err != nil {
			return ClientSettingsView{}, err
		}
	}
	// Read a running node's identity when available, but never initialize one or
	// probe an application just to print saved settings.
	state, _ := c.current(ctx)
	if err := ctx.Err(); err != nil {
		return ClientSettingsView{}, err
	}
	result := ClientSettingsView{Services: []ClientServiceSettings{}, RustDesk: []RustDeskClientSettings{}, Application: "unverified", Notices: []ClientNotice{
		{"endpoint_not_proxy", "These are service endpoints, not SOCKS proxy addresses.", "これはサービスの接続先であり、SOCKS プロキシのアドレスではありません。"},
		{"application_unverified", "Saved configuration and listener readiness do not prove application success. Keep TLS certificate and SSH host-key verification enabled.", "設定の保存や待受の準備完了はアプリの動作成功を示しません。TLS 証明書・SSH ホスト鍵の検証は有効のままにしてください。"},
	}}
	for _, s := range selected {
		view, err := c.clientServiceSettings(p.Settings.Network, s, state)
		if err != nil {
			return result, err
		}
		result.Services = append(result.Services, view)
	}
	for _, g := range p.Groups {
		if g.RustDesk == nil || in.Group != "" && in.Group != g.Name {
			continue
		}
		if len(in.IDs) > 0 && !slices.ContainsFunc(g.ServiceIDs, func(id string) bool { return slices.Contains(in.IDs, id) }) {
			continue
		}
		settings, err := c.rustDeskSettings(p, g)
		if err != nil {
			return result, err
		}
		result.RustDesk = append(result.RustDesk, settings)
	}
	return result, nil
}

func (c *Core) clientServiceSettings(currentBackend string, s ServiceSpec, state identity.State) (ClientServiceSettings, error) {
	var result ClientServiceSettings
	host, err := serviceLoopback(s.LoopbackHost)
	if err != nil {
		return result, err
	}
	ports, err := ranges.ParseWithLimit(s.Ports, c.limit("logical", "portIntervals"))
	if err != nil {
		return result, err
	}
	var excluded ranges.Set
	if s.ExcludePorts != "" {
		excluded, err = ranges.ParseWithLimit(s.ExcludePorts, c.limit("logical", "portIntervals"))
		if err != nil {
			return result, err
		}
	}
	effective, err := ports.Excluding(excluded)
	if err != nil {
		return result, err
	}
	if s.Direction == "share" {
		reserved, _ := ranges.Parse("54543-54545")
		effective, err = effective.Excluding(reserved)
		if err != nil {
			return result, err
		}
	}
	// Active shares may additionally exclude control ports of the current node.
	c.mu.RLock()
	if active := c.active[s.ID]; active != nil {
		effective = active.effective
		s.Lifetime, s.TTLSeconds = active.spec.Lifetime, active.spec.TTLSeconds
	}
	c.mu.RUnlock()
	status, ready := c.clientServiceState(s.ID)
	result = ClientServiceSettings{ID: s.ID, Name: s.Name, Backend: s.Backend, Direction: s.Direction, Purpose: s.Purpose, Network: s.Network, PeerID: s.PeerID, AllowedPeerIDs: append([]string(nil), s.PeerIDs...), LocalHost: host, RemoteEndpoints: []string{}, RemoteHosts: []string{}, Mappings: []ClientPortMapping{}, Lifetime: s.Lifetime, TTLSeconds: s.TTLSeconds, Status: status, ListenerReady: ready, Application: "unverified", Notices: []ClientNotice{}}
	offset := 0
	for _, span := range effective.Intervals() {
		count := int(span.Last) - int(span.First) + 1
		first, last := int(span.First), int(span.Last)
		if s.LocalPort != 0 {
			first = s.LocalPort + offset
			last = first + count - 1
		}
		result.Mappings = append(result.Mappings, ClientPortMapping{LocalFirst: first, LocalLast: last, RemoteFirst: int(span.First), RemoteLast: int(span.Last)})
		offset += count
	}
	if len(result.Mappings) == 1 && result.Mappings[0].LocalFirst == result.Mappings[0].LocalLast {
		result.LocalEndpoint = net.JoinHostPort(host, strconv.Itoa(result.Mappings[0].LocalFirst))
	}
	backend := s.Backend
	if backend == "" {
		backend = currentBackend
	}
	if state.Snapshot.Running && backend == currentBackend {
		if s.Direction == "share" {
			for _, ip := range state.IPs {
				result.RemoteHosts = append(result.RemoteHosts, ip.String())
				if effective.Count() == 1 {
					result.RemoteEndpoints = append(result.RemoteEndpoints, net.JoinHostPort(ip.String(), effective.String()))
				}
			}
		} else {
			for _, peer := range state.Snapshot.Peers {
				if peer.ID == s.PeerID && !peer.Expired {
					for _, ip := range peer.IPs {
						result.RemoteHosts = append(result.RemoteHosts, ip.String())
						if effective.Count() == 1 {
							result.RemoteEndpoints = append(result.RemoteEndpoints, net.JoinHostPort(ip.String(), effective.String()))
						}
					}
					break
				}
			}
		}
	}
	if len(result.RemoteHosts) == 0 {
		result.Notices = append(result.Notices, ClientNotice{"identity_unavailable", "Current network addresses are unavailable; the saved peer IDs and port mappings remain authoritative.", "現在のネットワークアドレスは取得できません。保存された相手の ID とポート対応を確認してください。"})
	}
	if s.Direction == "forward" && s.Network == "tcp" && result.LocalEndpoint != "" {
		m := result.Mappings[0]
		if s.Purpose == "ssh" {
			// Distinguish immutable identity, backend and destination port. A loopback
			// address or mutable DNS name alone would conflate unrelated SSH servers.
			alias := fmt.Sprintf("sobalink-%s-%s-%d", backend, s.PeerID, m.RemoteFirst)
			args := []string{"ssh", "-o", "HostKeyAlias=" + alias, "-p", strconv.Itoa(m.LocalFirst), "-l", "USER", host}
			result.SSH = &SSHClientHint{HostKeyAlias: alias, Args: args, Command: fmt.Sprintf("ssh -o HostKeyAlias=%s -p %d -l USER %s", alias, m.LocalFirst, host)}
			result.Notices = append(result.Notices, ClientNotice{"ssh_host_key", "Replace USER with your SSH username. Verify the server host key independently; this peer-specific HostKeyAlias may require a new known_hosts entry. Never disable host-key checking.", "USER を SSH のユーザー名に置き換えてください。サーバーのホスト鍵は別の方法でも確認してください。相手固有の HostKeyAlias により known_hosts への新規登録が必要な場合があります。ホスト鍵の検証を無効にしないでください。"})
		}
		if s.Purpose == "web" {
			result.HTTPCandidate = "http://" + result.LocalEndpoint + "/"
			result.Notices = append(result.Notices, ClientNotice{"http_candidate_tls", "HTTP is only a candidate; no HTTP or HTTPS request was tested. For HTTPS preserve the original TLS hostname, SNI, origin and certificate validation; a loopback URL may not match the certificate.", "HTTP URL は候補であり、HTTP/HTTPS の動作は確認していません。HTTPS では元の TLS ホスト名・SNI・オリジン・証明書検証を維持してください。ループバック URL は証明書の名前と一致しない場合があります。"})
		}
	}
	if !ready {
		result.Notices = append(result.Notices, ClientNotice{"listener_not_ready", "These are saved endpoint mappings. Review and start the service separately before using the local endpoint.", "これは保存された接続先の対応です。利用する前に対象を確認し、別の操作でサービスを開始してください。"})
	}
	return result, nil
}
