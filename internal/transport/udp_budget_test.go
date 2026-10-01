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
	b := &byteBudget{limit: 10}
	if !b.reserve(8) || b.reserve(3) {
		t.Fatal("budget not enforced")
	}
	b.release(8)
	if b.usage() != 0 || !b.reserve(10) || b.reserve(1) {
		t.Fatal("budget not restored")
	}
	b.release(10)
}
func TestUDPQueueBudgetReleasedOnDropAndStop(t *testing.T) {
	baseline := udpMemory.usage()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	table := &udpTable{}
	s := &udpSession{ctx: ctx, cancel: cancel, table: table, queue: make(chan []byte, 64), lastActive: time.Now()}
	packet := make([]byte, maxDatagramSize)
	for range 64 {
		s.offer(packet)
	}
	if len(s.queue) != maxUDPQueuedBytes/len(packet) {
		t.Fatal("byte limit not enforced", len(s.queue))
	}
	if table.queuedBytes > maxUDPQueuedBytes {
		t.Fatal("per-rule budget exceeded")
	}
	s.stop()
	s.stop()
	if len(s.queue) != 0 || table.queuedBytes != 0 || udpMemory.usage() != baseline {
		t.Fatal("queued storage not released", table.queuedBytes, udpMemory.usage(), baseline)
	}
}
func TestUDPQueueCountDropReleasesBytes(t *testing.T) {
	baseline := udpMemory.usage()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	table := &udpTable{}
	s := &udpSession{ctx: ctx, cancel: cancel, table: table, queue: make(chan []byte, 1)}
	s.offer([]byte("one"))
	s.offer([]byte("two"))
	if table.queuedBytes != 3 {
		t.Fatal("dropped packet reservation leaked", table.queuedBytes)
	}
	s.stop()
	if udpMemory.usage() != baseline {
		t.Fatal("global budget leaked")
	}
}
func TestUDPProcessBudgetAcrossRules(t *testing.T) {
	// Do not run in parallel: temporarily fill the process-wide pool and release
	// every reservation even when an assertion fails.
	tables := []*udpTable{}
	defer func() {
		for _, table := range tables {
			table.releaseBytes(maxUDPQueuedBytes)
		}
	}()
	for range maxTotalUDPQueuedBytes / maxUDPQueuedBytes {
		table := &udpTable{}
		if !table.reserveBytes(maxUDPQueuedBytes) {
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
		baseline := len(udpSessionSlots)
		if baseline != 0 {
			t.Fatal("other sessions remain", baseline)
		}
		for range maxTotalUDPSessions - 1 {
			udpSessionSlots <- struct{}{}
		}
		defer func() {
			for range maxTotalUDPSessions - 1 {
				<-udpSessionSlots
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
		if len(udpSessionSlots) != maxTotalUDPSessions {
			t.Fatal("slot not held")
		}
		closeServer(t, v.server)
		if len(udpSessionSlots) != maxTotalUDPSessions-1 {
			t.Fatal("session slot leaked")
		}
	})
}
func TestUDPFailuresRestoreProcessResources(t *testing.T) {
	for _, phase := range []string{"dial", "validate", "write"} {
		t.Run(phase, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				beforeBytes, beforeSlots := udpMemory.usage(), len(udpSessionSlots)
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
					if udpMemory.usage() != beforeBytes || len(udpSessionSlots) != beforeSlots {
						t.Fatal("resource accounting leaked", phase, udpMemory.usage(), len(udpSessionSlots))
					}
				}
			})
		})
	}
}
