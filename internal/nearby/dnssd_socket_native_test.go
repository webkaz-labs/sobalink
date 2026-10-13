//go:build darwin && cgo

package nearby

import (
	"context"
	"net"
	"os/exec"
	"time"

	"github.com/webkaz-labs/sobalink/internal/nearby/dnssdprobe"
)

func dnsServiceSocketReadback(socket *dnssdLegacySocket, scope Scope) *dnssdSocketReadback {
	result := dnssdReadback([7]int{2, 2, 2, 2, 2, 2, 2})
	raw, err := socket.conn.SyscallConn()
	if err != nil {
		return result
	}
	if raw.Control(func(fd uintptr) {
		result = dnssdReadback([7]int(dnssdprobe.Readback(fd, scope.LocalAddress.As4(), socket.local.Port(), scope.InterfaceIndex)))
	}) != nil {
		return dnssdReadback([7]int{2, 2, 2, 2, 2, 2, 2})
	}
	return result
}

// No shell, host lookup, table dump, mutation, additional interface or endpoint.
// The child is retained by dnssdRouteOwner until Command.Run/Wait physically joins.
func runDNSServiceRouteLookup(parent context.Context, name string) dnssdRouteResult {
	ctx, cancel := context.WithTimeout(parent, time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/sbin/route", "-n", "get", "-ifscope", name, "224.0.0.251")
	cmd.Env = []string{"LC_ALL=C", "PATH=/usr/bin:/bin:/usr/sbin:/sbin"}
	cmd.WaitDelay = 100 * time.Millisecond
	output := &dnssdRouteBuffer{cancel: cancel}
	cmd.Stdout, cmd.Stderr = output, output
	err := cmd.Run()
	joined := cmd.Process == nil || cmd.ProcessState != nil
	result := dnssdRouteResult{Status: "unavailable", OwnerJoined: joined}
	output.mu.Lock()
	defer output.mu.Unlock()
	if output.overflow {
		result.Status = "output_limit"
		return result
	}
	if ctx.Err() != nil {
		result.Status = "timeout"
		return result
	}
	if err != nil {
		return result
	}
	result = dnssdParseRoute(output.data, name)
	result.OwnerJoined = joined
	return result
}

type dnssdNativeCWriter struct {
	socket     *dnssdLegacySocket
	scope      Scope
	ctx        context.Context
	deadline   time.Time
	binding    func() error
	result     *dnssdLegacyResult
	scopeError error
}

func (w *dnssdNativeCWriter) SetWriteDeadline(deadline time.Time) error {
	return w.socket.conn.SetWriteDeadline(deadline)
}

// The interface name is shared with the historical Go seam, but this method
// performs exactly one native C sendto; it never calls UDPConn.WriteToUDP.
func (w *dnssdNativeCWriter) WriteToUDP(data []byte, destination *net.UDPAddr) (n int, err error) {
	if destination == nil || destination.AddrPort().Addr() != multicast4 || destination.Port != mdnsPort || len(data) < 12 || len(data) > 512 {
		return 0, ErrInvalidPacket
	}
	if err = w.binding(); err != nil {
		w.scopeError = err
		return 0, err
	}
	if w.ctx.Err() != nil || !time.Now().Before(w.deadline) {
		return 0, context.DeadlineExceeded
	}
	raw, err := w.socket.conn.SyscallConn()
	if err != nil {
		return 0, err
	}
	var sendErr error
	err = raw.Control(func(fd uintptr) {
		// SetWriteDeadline cannot cancel direct libc calls. C also checks
		// O_NONBLOCK and every mandatory option before its single sendto.
		if w.ctx.Err() != nil || !time.Now().Before(w.deadline) {
			sendErr = context.DeadlineExceeded
			return
		}
		var snapshot dnssdprobe.Snapshot
		n, w.result.NativeSendAttempted, snapshot, sendErr = dnssdprobe.Send(fd, data, w.scope.LocalAddress.As4(), w.socket.local.Port(), w.scope.InterfaceIndex)
		w.result.SocketReadback = dnssdReadback([7]int(snapshot))
	})
	if err != nil {
		return n, err
	}
	return n, sendErr
}
func dnsServiceReadbackFailure(result *dnssdLegacyResult) bool {
	switch result.SocketReadback.state() {
	case "matched":
		return false
	case "unavailable":
		result.Status, result.Reason = "unavailable", "legacy_readback_unavailable"
		result.FailureStage, result.FailureKind = "legacy_readback", "unavailable"
	default:
		result.Status, result.Reason = "failed", "socket_readback_mismatch"
		result.FailureStage, result.FailureKind = "legacy_readback", "mismatch"
	}
	result.LegacySerializationVerified = false
	return true
}
