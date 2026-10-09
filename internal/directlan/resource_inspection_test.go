package directlan

import (
	"errors"
	"net"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/resourcegrant"
)

// A structural identity claim is deliberately not a managed flow capability.
// Embedded net.Conn is nil so any accidental I/O would panic this pure test.
type claimedInspectionConnection struct{ net.Conn }

func (claimedInspectionConnection) PeerIdentity() (string, bool) { return "claimed-peer", true }

func TestManagedInspectionRejectsUnownedConnections(t *testing.T) {
	for _, connection := range []net.Conn{nil, claimedInspectionConnection{}, (*flow)(nil), &flow{}} {
		if capability, ok := captureManagedInspection(connection, nil); ok || capability != nil {
			t.Fatal("unowned connection minted authority")
		}
	}
}

func TestManagedInspectionZeroCapabilityDenies(t *testing.T) {
	for _, capability := range []*ManagedInspection{nil, {}} {
		if _, ok := capability.Relationship(); ok {
			t.Fatal("zero capability disclosed a relationship")
		}
		if err := capability.WriteInspection(nil, resourcegrant.InspectRequest{}, resourcegrant.Inspection{}); !errors.Is(err, ErrUntrusted) {
			t.Fatal("zero capability admitted response")
		}
	}
}
