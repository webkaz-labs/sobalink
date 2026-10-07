package core

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
)

// Diagnostics describe one explicit TCP connection attempt. They do not speak
// the target application's protocol, establish TLS, or prove its health.
type ServiceDiagnostic struct {
	ServiceID   string            `json:"serviceId"`
	Port        int               `json:"port"`
	CheckedAt   time.Time         `json:"checkedAt"`
	Code        string            `json:"code"`
	Transport   string            `json:"transport"`
	Application string            `json:"application"`
	NextSteps   map[string]string `json:"nextSteps"`
}
type serviceFailure struct {
	Code      string            `json:"code"`
	At        time.Time         `json:"at"`
	NextSteps map[string]string `json:"nextSteps"`
}

func diagnosticNextSteps(code string) map[string]string {
	en, ja := "Review the service state and retry explicitly.", "サービスの状態を確認して、明示的に再試行してください。"
	switch code {
	case "tcp_reachable":
		en, ja = "TCP accepted a connection. Check the application's protocol, credentials and TLS or host-key verification separately.", "TCP 接続を受け付けました。アプリのプロトコル、認証情報、TLS やホスト鍵の検証は別途確認してください。"
	case "tcp_unreachable":
		en, ja = "Check that the application is listening on the approved port, then check peer reachability and network permissions and retry this TCP check.", "許可したポートでアプリが待ち受けているか確認し、相手への到達状態とネットワークの許可を確認して、この TCP 確認を再実行してください。"
	case "service_start_failed":
		en, ja = "Check current peer identity, network availability, local port conflicts and capacity, then retry the reviewed service explicitly.", "現在の端末 ID、ネットワークの接続、ローカルポートの競合、容量を確認し、確認したサービスを明示的に再開始してください。"
	case "listener_conflict":
		en, ja = "A local bind reported address-in-use. Use service ports NAME_OR_ID to explicitly check alternatives, then review and restart with a chosen port and revision. Proposals are not reservations; actual start rechecks every bind.", "ローカルの待受で使用中のアドレスが確認されました。service ports NAME_OR_ID で候補を明示的に確認し、選んだポートと版を指定して再開始してください。候補は予約ではなく、実際の開始時に再確認します。"
	case "listener_capacity":
		en, ja = "Review the finite listener budget, stop unused work or narrow the selection; changing ports cannot resolve exhausted capacity.", "有限の入口数の上限を確認するか、未使用の動作を停止・選択範囲を縮小してください。容量不足はポートの変更では解消しません。"
	case "listener_permission_denied":
		en, ja = "The operating system denied the bind. Review local permissions; do not automatically elevate privileges or alter security settings.", "OS が待受を拒否しました。ローカルのアクセス権を確認してください。自動的な権限昇格やセキュリティ設定の変更は行いません。"
	case "listener_address_unavailable":
		en, ja = "Review the explicitly selected IPv4 or IPv6 loopback family before retrying.", "明示的に選んだ IPv4 または IPv6 のループバックを確認してから再試行してください。"
	case "listener_unavailable":
		en, ja = "The loopback bind failed for an unclassified reason. Review the local listener configuration before retrying.", "原因を分類できないローカル待受の失敗です。入口の設定を確認してから明示的に再試行してください。"
	case "network_unavailable":
		en, ja = "Reconnect the selected network. Listener readiness does not prove that the application is reachable.", "選択したネットワークを再接続してください。入口の準備完了だけではアプリへの到達を確認できません。"
	case "peer_identity_changed":
		en, ja = "Refresh the current peers and review the exact identity and service scope before starting again.", "現在の相手一覧を更新し、正確な端末 ID とサービスの範囲を確認してから再開始してください。"
	case "service_scope_changed":
		en, ja = "Review the local address, reserved ports and permission scope before starting again.", "ローカルアドレス、予約ポート、許可範囲を確認してから再開始してください。"
	}
	return map[string]string{"en": en, "ja": ja}
}

// Called with c.mu held. This matches the shipped runtime-only diagnostic
// lifetime; successful recovery does not erase the most recent failure.
func (c *Core) recordServiceFailureLocked(id, code string) {
	if c.serviceFailures == nil {
		c.serviceFailures = map[string]serviceFailure{}
	}
	c.serviceFailures[id] = serviceFailure{Code: code, At: time.Now().UTC(), NextSteps: diagnosticNextSteps(code)}
}
func (c *Core) addServiceDiagnosticsLocked(id string, view map[string]any) {
	if failure, ok := c.serviceFailures[id]; ok {
		view["lastFailure"] = failure
	}
	if diagnostic, ok := c.serviceDiagnostics[id]; ok {
		view["diagnostic"] = diagnostic
	}
}

func (c *Core) diagnoseCommand(ctx context.Context, raw json.RawMessage) (any, error) {
	var in struct {
		ServiceID string `json:"serviceId"`
		Port      int    `json:"port"`
		ProbeTCP  bool   `json:"probeTCP"`
	}
	if err := decodePayload(raw, &in); err != nil {
		return nil, err
	}
	if !in.ProbeTCP {
		if in.ServiceID != "" || in.Port != 0 {
			return nil, &localCommandError{"diagnostic_probe_required", "select an explicit TCP check when choosing a service or port"}
		}
		return map[string]any{"services": c.serviceViews(), "proxies": c.proxyViews(), "networkGuidance": c.networkGuidance(), "application": "unverified", "tcpProbePerformed": false, "nextSteps": map[string]string{"en": "Use doctor --service ID --tcp [--port PORT] for one explicit TCP connection check.", "ja": "doctor --service ID --tcp [--port PORT] で、TCP 接続を 1 回明示的に確認できます。"}}, nil
	}
	c.mu.RLock()
	active := c.active[in.ServiceID]
	c.mu.RUnlock()
	if active == nil || !active.permissionActiveAt(time.Now()) {
		return nil, &localCommandError{"diagnostic_service_inactive", "start the reviewed service before testing its TCP transport"}
	}
	if active.spec.Network != "tcp" {
		return nil, &localCommandError{"diagnostic_tcp_only", "a generic UDP probe cannot establish application reachability; select an active TCP service"}
	}
	port := in.Port
	if port == 0 && active.effective.Count() == 1 {
		port = int(active.effective.Intervals()[0].First)
	}
	if port < 1 || port > 65535 || !active.effective.Contains(uint16(port)) {
		return nil, &localCommandError{"diagnostic_port_required", "select exactly one port in this service's effective approved scope"}
	}
	if err := active.guard(); err != nil {
		return nil, &localCommandError{"diagnostic_service_inactive", "service transport is not ready; reconnect the selected network before retrying"}
	}
	check, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	stop := context.AfterFunc(active.ctx, cancel)
	defer stop()
	release, ok := c.serviceResources().AdmitTCP(active.spec.ID, active.spec.PeerID)
	if !ok {
		return nil, &localCommandError{"diagnostic_capacity", "TCP connection capacity reached; retry after an active connection closes"}
	}
	defer release()
	var conn net.Conn
	var err error
	if active.spec.Direction == "forward" {
		conn, err = c.dial(check, active.spec.PeerID, "tcp", port)
	} else {
		targetPort := port
		if active.spec.LocalPort != 0 {
			targetPort = active.spec.LocalPort
		}
		loopback, e := serviceLoopback(active.spec.LoopbackHost)
		if e != nil {
			return nil, e
		}
		dial := c.diagnosticsDial
		if dial == nil {
			dial = (&net.Dialer{}).DialContext
		}
		conn, err = dial(check, "tcp", config.Address(loopback, targetPort))
	}
	if conn != nil {
		_ = conn.Close()
	} else if err == nil {
		err = errors.New("transport returned no connection")
	}
	if check.Err() != nil {
		err = check.Err()
	}
	if active.guard() != nil {
		err = errors.New("service permission changed during check")
	}
	result := ServiceDiagnostic{ServiceID: in.ServiceID, Port: port, CheckedAt: time.Now().UTC(), Code: "tcp_reachable", Transport: "reachable", Application: "unverified"}
	if err != nil {
		result.Code, result.Transport = "tcp_unreachable", "unreachable"
	}
	result.NextSteps = diagnosticNextSteps(result.Code)
	c.mu.Lock()
	if c.serviceDiagnostics == nil {
		c.serviceDiagnostics = map[string]ServiceDiagnostic{}
	}
	c.serviceDiagnostics[in.ServiceID] = result
	if err != nil {
		if c.serviceFailures == nil {
			c.serviceFailures = map[string]serviceFailure{}
		}
		c.serviceFailures[in.ServiceID] = serviceFailure{Code: result.Code, At: result.CheckedAt, NextSteps: diagnosticNextSteps(result.Code)}
	}
	c.mu.Unlock()
	return result, nil
}
