package nearby

import (
	"errors"
	"net"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

type nativeUDPWriteFixture struct {
	steps                 []string
	deadline              time.Time
	deadlineErr, writeErr error
	n                     int
	destination           *net.UDPAddr
	data                  []byte
}

func (f *nativeUDPWriteFixture) SetWriteDeadline(deadline time.Time) error {
	f.steps = append(f.steps, "deadline")
	f.deadline = deadline
	return f.deadlineErr
}
func (f *nativeUDPWriteFixture) WriteToUDP(data []byte, destination *net.UDPAddr) (int, error) {
	f.steps = append(f.steps, "write")
	f.data, f.destination = append([]byte(nil), data...), destination
	return f.n, f.writeErr
}

func TestNativeScopedUDPSendContract(t *testing.T) {
	data := packQuery()
	for _, tc := range []struct {
		name                              string
		n                                 int
		writeErr, deadlineErr, bindingErr error
		stage, kind                       string
		steps                             []string
	}{
		{"complete", len(data), nil, nil, nil, "none", "none", []string{"binding", "deadline", "write"}},
		{"short", len(data) - 1, nil, nil, nil, "socket_write", "short_payload", []string{"binding", "deadline", "write"}},
		{"zero", 0, nil, nil, nil, "socket_write", "short_payload", []string{"binding", "deadline", "write"}},
		{"oversized_count", len(data) + 1, nil, nil, nil, "socket_write", "short_payload", []string{"binding", "deadline", "write"}},
		{"error_with_count", len(data), errors.New("synthetic private error"), nil, nil, "socket_write", "other", []string{"binding", "deadline", "write"}},
		{"deadline_failed", 0, nil, errors.New("synthetic private deadline"), nil, "socket_write_deadline", "none", []string{"binding", "deadline"}},
		{"binding_changed", 0, nil, nil, ErrScopeChanged, "none", "none", []string{"binding"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &nativeUDPWriteFixture{n: tc.n, writeErr: tc.writeErr, deadlineErr: tc.deadlineErr}
			before := time.Now()
			err := writeScopedUDPPacket(f, data, func() error { f.steps = append(f.steps, "binding"); return tc.bindingErr })
			after := time.Now()
			if socketFailureStage(err) != tc.stage || nativeSendFailureKind(err) != tc.kind || !reflect.DeepEqual(f.steps, tc.steps) {
				t.Fatal("send contract changed or retried")
			}
			if tc.bindingErr != nil && !errors.Is(err, tc.bindingErr) {
				t.Fatal("lost binding failure")
			}
			if !f.deadline.IsZero() && (f.deadline.Before(before.Add(time.Second)) || f.deadline.After(after.Add(time.Second))) {
				t.Fatal("write deadline changed")
			}
			if f.destination != nil && (f.destination.String() != "224.0.0.251:5353" || f.destination.Zone != "" || !reflect.DeepEqual(f.data, data)) {
				t.Fatal("destination or payload changed")
			}
		})
	}
	for _, size := range []int{0, 11, MaxDatagramBytes + 1} {
		f := &nativeUDPWriteFixture{}
		if err := writeScopedUDPPacket(f, make([]byte, size), func() error { t.Fatal("invalid payload checked binding"); return nil }); !errors.Is(err, ErrInvalidPacket) || len(f.steps) != 0 {
			t.Fatal("invalid payload reached socket")
		}
	}
}

func TestNativePlatformSendAndIngressBoundaries(t *testing.T) {
	for file, fragments := range map[string][]string{
		"native_windows.go": {"return writeScopedUDPPacket(t.conn, data, func() error", "currentBindingWithPolicy(t.scope, &t.ifi, t.policy)", "winReceiveControl(oob[:nn], strconv.IntSize/8)", "flags&(windows.MSG_TRUNC|windows.MSG_CTRUNC)", "windows.IP_MULTICAST_IF, selected", "Interface: selected", "configureWindowsMulticast(scope,", "{windows.IP_MULTICAST_TTL, 255}"},
		"native_linux.go":   {"Src: net.IP(scope.LocalAddress.AsSlice()), IfIndex: scope.InterfaceIndex"},
		"native_darwin.go":  {"return sourceOnlySendControl(scope)", "unix.IP_BOUND_IF, index"},
		"native_unix.go":    {"return writeNativePacket(t.sender, data, nativeSendControl(t.scope))", "sender.WriteTo(data, cm,"},
	} {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, fragment := range fragments {
			if !strings.Contains(string(data), fragment) {
				t.Fatalf("%s lost required platform contract", file)
			}
		}
		if file == "native_windows.go" && strings.Contains(string(data), "WriteMsgUDP(") {
			t.Fatal("Windows restored ambiguous byte-count path")
		}
	}
	acceptance, err := os.ReadFile("native_acceptance_test.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(acceptance), "runPlatformSendProbe(") {
		t.Fatal("redundant post-failure send probe was restored")
	}
}
