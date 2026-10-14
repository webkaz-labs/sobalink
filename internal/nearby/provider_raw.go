package nearby

import (
	"context"
	"time"
)

// rawRecordProvider retains the actual socket source. OS providers never use
// this adapter and cannot manufacture a packet's destination/hop-limit proof.
type rawRecordProvider struct {
	transport Transport
	cfg       Config
	policy    discoveryPolicy
	window    time.Time
	parsed    int
}

func newRawRecordProvider(_ context.Context, cfg Config, policy discoveryPolicy, open TransportFactory) (recordProvider, error) {
	if open == nil {
		return nil, ErrUnsupported
	}
	tr, err := open(cfg.Scope)
	if tr == nil {
		return nil, err
	}
	return &rawRecordProvider{transport: tr, cfg: cfg, policy: policy, window: cfg.Clock.Now()}, err
}
func (p *rawRecordProvider) ReadObservation() (recordObservation, error) {
	packet, err := p.transport.ReadPacket()
	if err != nil {
		return recordObservation{}, err
	}
	// Every ReadObservation, including rejected packets, consumes the Session's
	// receive budget. Rate-limit before decoding or record/string allocations.
	now := p.cfg.Clock.Now()
	if now.Sub(p.window) >= time.Second {
		p.window = now
		p.parsed = 0
	}
	if p.parsed >= MaxParsedPerSecond {
		return recordObservation{}, nil
	}
	p.parsed++
	if !validatePacketWithPolicy(p.cfg.Scope, packet, p.policy) {
		return recordObservation{}, nil
	}
	records, query, err := decodeRecordPacket(packet.Data, p.cfg.Advertisement)
	if err != nil {
		return recordObservation{}, nil
	}
	return recordObservation{Kind: rawPacketObservation, InterfaceIndex: packet.InterfaceIndex, Source: packet.Source, Records: records, query: query}, nil
}
func (p *rawRecordProvider) Close() error                  { return p.transport.Close() }
func (p *rawRecordProvider) WritePacket(data []byte) error { return p.transport.WritePacket(data) }
