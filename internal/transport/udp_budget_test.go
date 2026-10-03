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

func TestUDPByteBudgetRejectsOverLimit(t *testing.T) {
	b := NewController(nil)
	if !b.reserveBytes(8, 10) || b.reserveBytes(3, 10) {
		t.Fatal("budget not enforced")
	}
	b.releaseBytes(8)
	if b.Usage().UDPQueuedBytes != 0 || !b.reserveBytes(10, 10) || b.reserveBytes(1, 10) {
		t.Fatal("budget not restored")
	}
	b.releaseBytes(10)
}
func TestUDPQueueBudgetReleasedOnDropAndStop(t *testing.T) {
	baseline := defaultController.Usage().UDPQueuedBytes
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	table := &udpTable{}
	s := &udpSession{ctx: ctx, cancel: cancel, table: table, lastActive: time.Now()}
	packet := make([]byte, maxDatagramSize)
	for range 64 {
		s.offer(packet)
	}
	if s.queuedPackets != int64(defaultUDPPolicyQueuedBytes/packetStorage(len(packet))) {
		t.Fatal("byte limit not enforced", s.queuedPackets)
	}
	if table.queuedBytes > defaultUDPPolicyQueuedBytes {
		t.Fatal("per-rule budget exceeded")
	}
	s.stop()
	s.stop()
	if s.queuedPackets != 0 || table.queuedBytes != 0 || defaultController.Usage().UDPQueuedBytes != baseline {
		t.Fatal("queued storage not released", table.queuedBytes, defaultController.Usage().UDPQueuedBytes, baseline)
	}
}
func TestUDPQueueCountDropReleasesBytes(t *testing.T) {
	baseline := defaultController.Usage().UDPQueuedBytes
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	table := &udpTable{cfg: UDPConfig{QueueSize: 1}}
	s := &udpSession{ctx: ctx, cancel: cancel, table: table, ready: make(chan struct{}, 1)}
	s.offer([]byte("one"))
	s.offer([]byte("two"))
	if table.queuedBytes != int64(packetStorage(3)) {
		t.Fatal("dropped packet reservation leaked", table.queuedBytes)
	}
	s.stop()
	if defaultController.Usage().UDPQueuedBytes != baseline {
		t.Fatal("global budget leaked")
	}
}
func TestUDPProcessBudgetAcrossRules(t *testing.T) {
	// Do not run in parallel: temporarily fill the process-wide pool and release
	// every reservation even when an assertion fails.
	tables := []*udpTable{}
	defer func() {
		for _, table := range tables {
			table.releaseBytes(defaultUDPPolicyQueuedBytes)
		}
	}()
	for range defaultUDPQueuedBytes / defaultUDPPolicyQueuedBytes {
		table := &udpTable{}
		if !table.reserveBytes(defaultUDPPolicyQueuedBytes) {
			t.Fatal("budget unavailable")
		}
		tables = append(tables, table)
	}
	if (&udpTable{}).reserveBytes(1) {
		t.Fatal("process-wide memory budget bypassed")
	}
}

func TestUDPProcessSessionCapAndRelease(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		baseline := defaultController.Usage().UDPSessions
		if baseline != 0 {
			t.Fatal("other sessions remain", baseline)
		}
		for range defaultUDPSessions - 1 {
			if !defaultController.reserveSession(defaultUDPSessions) {
				t.Fatal("session budget unavailable")
			}
		}
		defer func() {
			for range defaultUDPSessions - 1 {
				defaultController.releaseSession()
			}
		}()
		v := virtualUDPServer(t, time.Minute, 10)
		defer closeServer(t, v.server)
		v.send(netip.MustParseAddrPort("127.0.0.1:10001"), "one")
		first := v.mapping(t)
		defer first.peer.Close()
		v.send(netip.MustParseAddrPort("127.0.0.1:10002"), "two")
		synctest.Wait()
		v.noAdditionalMapping(t)
		if defaultController.Usage().UDPSessions != defaultUDPSessions {
			t.Fatal("slot not held")
		}
		closeServer(t, v.server)
		if defaultController.Usage().UDPSessions != defaultUDPSessions-1 {
			t.Fatal("session slot leaked")
		}
	})
}
func TestUDPFailuresRestoreProcessResources(t *testing.T) {
	for _, phase := range []string{"dial", "validate", "write"} {
		t.Run(phase, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				beforeBytes, beforeSlots := defaultController.Usage().UDPQueuedBytes, defaultController.Usage().UDPSessions
				for range 10 {
					local := &virtualPacketSocket{incoming: make(chan virtualDatagram), outgoing: make(chan virtualDatagram), closed: make(chan struct{})}
					var peer net.Conn
					server := startServer(t.Context(), local, local.LocalAddr(), func(s *Server) {
						table := &udpTable{sessions: map[netip.AddrPort]*udpSession{}, server: s, local: local, cfg: UDPConfig{Target: "example.invalid:9", MaxSessions: 2, QueueSize: 4, IdleTimeout: time.Minute, DialTimeout: time.Second, WriteTimeout: time.Second}, dial: func(context.Context, string, string) (net.Conn, error) {
							if phase == "dial" {
								return nil, errors.New("dial failed")
							}
							a, b := net.Pipe()
							peer = b
							b.Close()
							return a, nil
						}}
						if phase == "validate" {
							table.cfg.Validate = func(context.Context, string, string) error { return errors.New("revoked") }
						}
						table.readLocal()
					})
					local.incoming <- virtualDatagram{data: []byte("queued"), address: netip.MustParseAddrPort("127.0.0.1:12000")}
					synctest.Wait()
					closeServer(t, server)
					if peer != nil {
						peer.Close()
					}
					if defaultController.Usage().UDPQueuedBytes != beforeBytes || defaultController.Usage().UDPSessions != beforeSlots {
						t.Fatal("resource accounting leaked", phase, defaultController.Usage().UDPQueuedBytes, defaultController.Usage().UDPSessions)
					}
				}
			})
		})
	}
}
