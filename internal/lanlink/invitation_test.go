package lanlink

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func TestInvitationInspectionSharesLocalPairValidation(t *testing.T) {
	host, client, original, _, _, _, _ := pairFixture(t)
	now := original.Expires.Add(-time.Second)
	if err := original.ValidateFor(client.PublicKey(), now); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Invitation){
		"version":         func(i *Invitation) { i.Version++ },
		"recipient":       func(i *Invitation) { i.RecipientKey = host.PublicKey() },
		"missing expiry":  func(i *Invitation) { i.Expires = time.Time{} },
		"boundary expiry": func(i *Invitation) { i.Expires = now },
		"token":           func(i *Invitation) { i.Token = strings.Repeat("!", 43) },
		"capability":      func(i *Invitation) { i.Host.Address = "invalid" },
		"host key":        func(i *Invitation) { i.Host.Peer.Key = client.PublicKey() },
		"host name":       func(i *Invitation) { i.Host.Peer.Name = "bad\nname" },
		"relay address":   func(i *Invitation) { i.Relay.Address = netip.MustParseAddrPort("127.0.0.2:54446") },
		"relay pin":       func(i *Invitation) { i.Relay.CertificateSHA256 = strings.Repeat("b", 64) },
	} {
		t.Run(name, func(t *testing.T) {
			inv := original
			mutate(&inv)
			if inv.ValidateFor(client.PublicKey(), now) == nil {
				t.Fatal("invalid invitation passed local inspection")
			}
		})
	}
	// Inspection must not consume the invitation or grant trust. Cancellation
	// remains a remote fact that only an actual join can discover.
	if len(host.cfg.Trust.invites) != 1 || len(client.PublicPeers()) != 0 {
		t.Fatal("local inspection changed pairing authorization")
	}
	host.CancelInvitation(original.Token)
	if err := original.ValidateFor(client.PublicKey(), now); err != nil {
		t.Fatal("local validation claimed knowledge of remote cancellation")
	}
	expired := original
	expired.Expires = time.Now().Add(-time.Second)
	if err := client.PairInvitation(context.Background(), expired); !errors.Is(err, ErrInvite) {
		t.Fatal("actual join did not revalidate the previously inspected expiry")
	}
}
