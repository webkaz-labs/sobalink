package nearby

import (
	"context"
	"net/netip"
	"time"
)

// Observations distinguish real socket metadata from an OS DNS-SD API. Neither
// provenance authenticates a peer or grants permission to connect.
type observationKind uint8

const (
	rawPacketObservation observationKind = iota + 1
	osDNSServiceObservation
)

type recordKind uint8

const (
	ptrRecord recordKind = iota + 1
	srvRecord
	txtRecord
	aRecord
)
const (
	maxObservationRecords  = 16
	maxDNSQuestions        = 16
	maxRecordBytes         = 1200
	maxRecordLifetimeBytes = 512 * 1024
	maxObservationQueue    = 64
)

type discoveryRecord struct {
	Kind                   recordKind
	Name                   string
	Target                 string
	Priority, Weight, Port uint16
	TXT                    []string
	Address                netip.Addr
	TTL                    time.Duration
	Removed                bool
}
type recordObservation struct {
	Kind           observationKind
	InterfaceIndex int
	Source         netip.AddrPort // Absent for OS observations. Never synthesized.
	Records        []discoveryRecord
	query          bool // Only a raw provider may mark an exact local question.
}

// Close is once-owned and must physically join all workers and callbacks. A
// factory returns ownership before potentially blocking asynchronous API work.
type recordProvider interface {
	ReadObservation() (recordObservation, error)
	Close() error
}
type recordProviderFactory func(context.Context, Config, discoveryPolicy) (recordProvider, error)
