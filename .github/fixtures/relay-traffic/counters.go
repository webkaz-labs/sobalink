package lanlink

import (
	"errors"
	"io"
	"sync/atomic"
	"time"
)

// Count only writes at one boundary of the TCP proxy. Counting both the read
// and write of the same copied stream would double-count the same relay leg.
type trafficCountWriter struct {
	destination io.Writer
	bytes       *atomic.Uint64
}

func (w trafficCountWriter) Write(p []byte) (int, error) {
	n, err := w.destination.Write(p)
	if n < 0 || n > len(p) {
		return 0, errors.New("invalid relay stream write count")
	}
	w.bytes.Add(uint64(n))
	return n, err
}

type trafficCounters struct {
	toRelay, fromRelay, opened, dialFailures atomic.Uint64
	active                                   atomic.Int64
}

type trafficSnapshot struct {
	toRelay, fromRelay, opened, dialFailures uint64
	active                                   int64
}

func (c *trafficCounters) snapshot() trafficSnapshot {
	return trafficSnapshot{c.toRelay.Load(), c.fromRelay.Load(), c.opened.Load(), c.dialFailures.Load(), c.active.Load()}
}

type trafficPhase struct {
	Name                  string   `json:"name"`
	ObservationSeconds    float64  `json:"observation_seconds"`
	PayloadSeconds        float64  `json:"payload_seconds"`
	UsefulSentBytes       uint64   `json:"useful_sent_bytes"`
	UsefulReceivedBytes   uint64   `json:"useful_received_bytes"`
	UsefulTotalBytes      uint64   `json:"useful_total_bytes"`
	RelayToBytes          uint64   `json:"relay_to_bytes"`
	RelayFromBytes        uint64   `json:"relay_from_bytes"`
	RelayTotalBytes       uint64   `json:"relay_total_bytes"`
	TwoLegPayloadBytes    uint64   `json:"two_leg_payload_bytes"`
	RelayExcessBytes      int64    `json:"relay_excess_bytes"`
	RelayPerUsefulByte    *float64 `json:"relay_per_useful_byte"`
	RelayPerTwoLegPayload *float64 `json:"relay_per_two_leg_payload"`
	RelayBytesPerSecond   float64  `json:"relay_bytes_per_second"`
	UsefulBytesPerSecond  float64  `json:"useful_bytes_per_second"`
	ConnectionsOpened     uint64   `json:"connections_opened"`
	ActiveStart           int64    `json:"active_connections_start"`
	ActiveEnd             int64    `json:"active_connections_end"`
	DialFailures          uint64   `json:"dial_failures"`
	Admissions            uint64   `json:"relay_admissions"`
}

func summarizeTraffic(name string, before, after trafficSnapshot, observed, payload time.Duration, sent, received, admissions uint64) (trafficPhase, error) {
	if observed <= 0 || payload < 0 || payload > observed || after.toRelay < before.toRelay || after.fromRelay < before.fromRelay || after.opened < before.opened || after.dialFailures < before.dialFailures {
		return trafficPhase{}, errors.New("invalid relay observation interval")
	}
	p := trafficPhase{Name: name, ObservationSeconds: observed.Seconds(), PayloadSeconds: payload.Seconds(), UsefulSentBytes: sent, UsefulReceivedBytes: received,
		UsefulTotalBytes: sent + received, RelayToBytes: after.toRelay - before.toRelay, RelayFromBytes: after.fromRelay - before.fromRelay,
		ConnectionsOpened: after.opened - before.opened, ActiveStart: before.active, ActiveEnd: after.active,
		DialFailures: after.dialFailures - before.dialFailures, Admissions: admissions}
	p.RelayTotalBytes = p.RelayToBytes + p.RelayFromBytes
	p.TwoLegPayloadBytes = 2 * p.UsefulTotalBytes
	p.RelayExcessBytes = int64(p.RelayTotalBytes) - int64(p.TwoLegPayloadBytes)
	p.RelayBytesPerSecond = float64(p.RelayTotalBytes) / p.ObservationSeconds
	if p.UsefulTotalBytes > 0 {
		perUseful := float64(p.RelayTotalBytes) / float64(p.UsefulTotalBytes)
		perTwoLeg := float64(p.RelayTotalBytes) / float64(p.TwoLegPayloadBytes)
		p.RelayPerUsefulByte, p.RelayPerTwoLegPayload = &perUseful, &perTwoLeg
		if payload > 0 {
			p.UsefulBytesPerSecond = float64(p.UsefulTotalBytes) / payload.Seconds()
		}
	}
	return p, nil
}

// A repeatable payload generator uses fixed memory regardless of transfer size.
type trafficPattern struct{ offset uint64 }

func (p *trafficPattern) Read(dst []byte) (int, error) {
	for i := range dst {
		dst[i] = byte((p.offset + uint64(i)) % 251)
	}
	p.offset += uint64(len(dst))
	return len(dst), nil
}
