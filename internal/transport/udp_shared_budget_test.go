package transport

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"
	"testing/synctest"
	"time"
)

func TestUDPSharedByteBudgetAcrossPorts(t *testing.T) {
	baseline := defaultController.Usage().UDPQueuedBytes
	budget := NewUDPBudget()
	a := &udpTable{cfg: UDPConfig{Budget: budget}}
	b := &udpTable{cfg: UDPConfig{Budget: budget}}
	if !a.reserveBytes(defaultUDPPolicyQueuedBytes/2) || !b.reserveBytes(defaultUDPPolicyQueuedBytes/2) {
		t.Fatal("shared byte capacity unavailable")
	}
	defer a.releaseBytes(defaultUDPPolicyQueuedBytes / 2)
	defer b.releaseBytes(defaultUDPPolicyQueuedBytes / 2)
	if a.reserveBytes(1) || b.reserveBytes(1) {
		t.Fatal("per-port tables amplified one policy's queue cap")
	}
	if sessions, bytes := budget.Usage(); sessions != 0 || bytes != defaultUDPPolicyQueuedBytes {
		t.Fatalf("shared accounting: sessions %d bytes %d", sessions, bytes)
	}
	if defaultController.Usage().UDPQueuedBytes != baseline+defaultUDPPolicyQueuedBytes {
		t.Fatal("process budget was not retained")
	}
	independent := &udpTable{cfg: UDPConfig{Budget: NewUDPBudget()}}
	if !independent.reserveBytes(1) {
		t.Fatal("independent policy incorrectly shared cap")
	}
	independent.releaseBytes(1)
}

func TestUDPSharedQueueDropsAndRepeatedStopRelease(t *testing.T) {
	baseline := defaultController.Usage().UDPQueuedBytes
	budget := NewUDPBudget()
	var sessions []*udpSession
	for range 2 {
		ctx, cancel := context.WithCancel(t.Context())
		table := &udpTable{cfg: UDPConfig{Budget: budget, QueueSize: 1}}
		s := &udpSession{ctx: ctx, cancel: cancel, table: table, ready: make(chan struct{}, 1)}
		sessions = append(sessions, s)
		s.offer([]byte("one"))
		s.offer([]byte("two"))
	}
	if _, bytes := budget.Usage(); bytes != int64(2*packetStorage(3)) {
		t.Fatalf("queue overflow leaked shared bytes: %d", bytes)
	}
	for _, s := range sessions {
		s.stop()
		s.stop()
	}
	if _, bytes := budget.Usage(); bytes != 0 || defaultController.Usage().UDPQueuedBytes != baseline {
		t.Fatal("repeated stop leaked or underflowed shared/process byte accounting")
	}
}

func TestUDPSharedReservationRollsBackWhenProcessMemoryFull(t *testing.T) {
	if !defaultController.reserveBytes(defaultUDPQueuedBytes, defaultUDPQueuedBytes) {
		t.Fatal("process budget not empty")
	}
	defer defaultController.releaseBytes(defaultUDPQueuedBytes)
	budget := NewUDPBudget()
	table := &udpTable{cfg: UDPConfig{Budget: budget}}
	if table.reserveBytes(1) {
		t.Fatal("process byte cap bypassed")
	}
	if sessions, bytes := budget.Usage(); sessions != 0 || bytes != 0 || table.queuedBytes != 0 {
		t.Fatal("failed global reservation retained policy/table reservation")
	}
}

// sharedUDPTestServer exercises the production admission and cleanup paths
// using channel-backed packets and canceled mock dials, without OS sockets.
func sharedUDPTestServer(t *testing.T, budget *UDPBudget, dial Dialer, validate func(context.Context, string, string) error) (*Server, *virtualPacketSocket) {
	t.Helper()
	local := &virtualPacketSocket{incoming: make(chan virtualDatagram), outgoing: make(chan virtualDatagram), closed: make(chan struct{})}
	server := startServer(t.Context(), local, local.LocalAddr(), func(s *Server) {
		table := &udpTable{sessions: make(map[netip.AddrPort]*udpSession), server: s, local: local, cfg: UDPConfig{Budget: budget, Target: "127.0.0.1:9", MaxSessions: defaultMaxUDPSessions, QueueSize: defaultUDPQueueSize, IdleTimeout: time.Minute, DialTimeout: 10 * time.Second, WriteTimeout: time.Second, Validate: validate}, dial: dial}
		table.readLocal()
	})
	return server, local
}

func TestUDPSharedMappingCapAcrossMaterializedPorts(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		beforeSlots, beforeBytes := defaultController.Usage().UDPSessions, defaultController.Usage().UDPQueuedBytes
		budget := NewUDPBudget()
		dial := func(ctx context.Context, _, _ string) (net.Conn, error) { <-ctx.Done(); return nil, ctx.Err() }
		a, localA := sharedUDPTestServer(t, budget, dial, nil)
		defer closeServer(t, a)
		b, localB := sharedUDPTestServer(t, budget, dial, nil)
		defer closeServer(t, b)
		for _, local := range []*virtualPacketSocket{localA, localB} {
			for p := 1; p <= 128; p++ {
				local.incoming <- virtualDatagram{data: []byte("x"), address: netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), uint16(p))}
			}
		}
		synctest.Wait()
		if sessions, _ := budget.Usage(); sessions != defaultMaxUDPSessions {
			t.Fatalf("got %d shared sessions", sessions)
		}
		localB.incoming <- virtualDatagram{data: []byte("extra"), address: netip.MustParseAddrPort("127.0.0.1:5000")}
		synctest.Wait()
		if sessions, bytes := budget.Usage(); sessions != 256 || bytes != int64(256*packetStorage(1)) {
			t.Fatalf("257th mapping admitted or queued: %d sessions, %d bytes", sessions, bytes)
		}
		if defaultController.Usage().UDPSessions != beforeSlots+256 {
			t.Fatal("process reservation rollback failed on shared-cap rejection")
		}
		closeServer(t, a)
		if sessions, bytes := budget.Usage(); sessions != 128 || bytes != int64(128*packetStorage(1)) {
			t.Fatalf("closing one port did not release exactly its reservations: %d,%d", sessions, bytes)
		}
		localB.incoming <- virtualDatagram{data: []byte("x"), address: netip.MustParseAddrPort("127.0.0.1:5000")}
		synctest.Wait()
		if sessions, _ := budget.Usage(); sessions != 129 {
			t.Fatal("released shared mapping capacity was not reusable")
		}
		closeServer(t, b)
		if sessions, bytes := budget.Usage(); sessions != 0 || bytes != 0 || defaultController.Usage().UDPSessions != beforeSlots || defaultController.Usage().UDPQueuedBytes != beforeBytes {
			t.Fatal("shared/process resources leaked on multiport shutdown")
		}
	})
}

func TestUDPSharedFailuresReleaseAllResources(t *testing.T) {
	for _, phase := range []string{"dial", "validate", "write", "cancel"} {
		t.Run(phase, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				beforeSlots, beforeBytes := defaultController.Usage().UDPSessions, defaultController.Usage().UDPQueuedBytes
				budget := NewUDPBudget()
				for range 4 {
					var validate func(context.Context, string, string) error
					if phase == "validate" {
						validate = func(context.Context, string, string) error { return errors.New("revoked") }
					}
					dial := func(ctx context.Context, _, _ string) (net.Conn, error) {
						if phase == "dial" {
							return nil, errors.New("refused")
						}
						if phase == "cancel" {
							<-ctx.Done()
							return nil, ctx.Err()
						}
						a, b := net.Pipe()
						b.Close()
						return a, nil
					}
					server, local := sharedUDPTestServer(t, budget, dial, validate)
					local.incoming <- virtualDatagram{data: []byte("queued"), address: netip.MustParseAddrPort("127.0.0.1:12000")}
					synctest.Wait()
					closeServer(t, server)
					if sessions, bytes := budget.Usage(); sessions != 0 || bytes != 0 || defaultController.Usage().UDPSessions != beforeSlots || defaultController.Usage().UDPQueuedBytes != beforeBytes {
						t.Fatalf("%s leaked shared/process resources: %d sessions, %d bytes", phase, sessions, bytes)
					}
				}
			})
		})
	}
}
