//go:build linux || darwin

package nearby

import (
	"golang.org/x/sys/unix"
	"net"
	"os"
	"testing"
	"time"
)

func TestDNSServiceLegacyWrappedSendErrorClasses(t *testing.T) {
	for _, tc := range []struct {
		err  error
		kind string
	}{
		{unix.EINVAL, "invalid_argument"}, {unix.EADDRNOTAVAIL, "source_unavailable"},
		{unix.ENETUNREACH, "network_unreachable"}, {unix.EHOSTUNREACH, "host_unreachable"},
		{unix.EACCES, "permission_denied"}, {unix.EPERM, "permission_denied"},
		{unix.EOPNOTSUPP, "unsupported"}, {unix.ENOPROTOOPT, "unsupported"},
		{unix.ETIMEDOUT, "timeout"}, {unix.ENOBUFS, "other"},
	} {
		for _, deadlineFailure := range []bool{false, true} {
			wrapped := &net.OpError{Op: "write", Net: "udp4", Err: os.NewSyscallError("sendto", tc.err)}
			w := &dnssdLegacyFakeWriter{sendErr: wrapped}
			wantStage, wantWrites := "legacy_send", 1
			if deadlineFailure {
				w.deadlineErr = wrapped
				wantStage, wantWrites = "legacy_deadline", 0
			}
			stage, kind := writeDNSServiceLegacyQuery(w, []byte{1}, time.Now().Add(time.Second), time.Now())
			if stage != wantStage || kind != tc.kind || w.writes != wantWrites {
				t.Fatalf("fixed class: %s/%s writes=%d", stage, kind, w.writes)
			}
		}
	}
}
