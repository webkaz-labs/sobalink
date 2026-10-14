package transfer

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/config"
)

func TestDurableAcceptLifecycleBusyPreservesConsent(t *testing.T) {
	for _, kind := range []Kind{File, Directory} {
		for _, phase := range []string{"admission", "save-check", "save-owner"} {
			t.Run(string(kind)+"/"+phase, func(t *testing.T) {
				profile := t.TempDir()
				owner, err := config.AcquireLock(profile)
				if err != nil {
					t.Fatal("synthetic lifecycle owner unavailable")
				}
				defer owner.Close()
				namespace := filepath.Join(profile, "durable")
				if err := os.Mkdir(namespace, 0700); err != nil {
					t.Fatal("synthetic ledger namespace unavailable")
				}
				owned := func(fn func() error) error {
					err := owner.TryWithOwnershipInfo(profile, func(_, _ os.FileInfo) error { return fn() })
					if errors.Is(err, config.ErrLifecycleBusy) {
						return ErrBusy
					}
					return err
				}
				check := func() error { return owned(func() error { return nil }) }
				options := DurableOptions{Path: filepath.Join(namespace, "receiver.json"), Epoch: testDurableEpoch, Initialize: true, OwnerCheck: check, OwnerOperation: owned}
				m, err := NewManager(Options{Limits: Limits{DiskReserveBytes: 1}, Durable: &options})
				if err != nil {
					t.Fatal("synthetic durable manager unavailable")
				}
				defer m.Close()
				if m.BindPeer(testDurablePeer) != nil || m.BindDurableRelationship(testDurablePeer, "synthetic", "epoch-one") != nil {
					t.Fatal("synthetic durable relationship unavailable")
				}
				entry := durableTestEntry()
				if kind == Directory {
					entry = Entry{ID: "empty", Path: "empty/directory", Kind: Directory}
				}
				offer := durableTestOffer(t, m, "accept-busy", entry)
				r := m.durable.record(offer.Lease.DeliveryID)
				originalOffer, originalLease := r.Offer, r.Lease
				originalOffer.Manifest.Entries = append([]Entry(nil), r.Offer.Manifest.Entries...)
				destination := t.TempDir()
				parent, err := openDestination(destination)
				if err != nil {
					t.Fatal("synthetic destination unavailable")
				}
				defer parent.Close()
				identity, err := durableIdentity(parent, ".", true)
				if err != nil {
					t.Fatal("synthetic destination identity unavailable")
				}
				marshal := func(v any) []byte {
					raw, err := json.Marshal(v)
					if err != nil {
						t.Fatal("synthetic state encoding failed")
					}
					return raw
				}
				read := func() []byte {
					raw, err := os.ReadFile(options.Path)
					if err != nil {
						t.Fatal("synthetic ledger read failed")
					}
					return raw
				}
				state := func() []byte {
					var copy durableSnapshot
					if json.Unmarshal(marshal(m.durable.state), &copy) != nil {
						t.Fatal("synthetic state copy failed")
					}
					// restoreConfirmed clones an empty directory slice as nil.
					// Compare its meaning while separately requiring byte-exact disk
					// and certified snapshots; never normalize nonempty inventories.
					for _, record := range copy.Records {
						if len(record.Directories) == 0 {
							record.Directories = nil
						}
					}
					return marshal(copy)
				}
				before, disk := state(), read()
				confirmed := marshal(m.durable.confirmed)
				var release func()
				hold := func() {
					entered, finish := make(chan struct{}), make(chan struct{})
					joined := make(chan error, 1)
					go func() {
						joined <- owner.WithOwnershipInfo(profile, func(_, _ os.FileInfo) error { close(entered); <-finish; return nil })
					}()
					<-entered
					release = func() {
						close(finish)
						if err := <-joined; err != nil {
							t.Error("synthetic owner join failed")
						}
					}
				}
				defer func() {
					if release != nil {
						release()
					}
				}()
				checks, callbacks := 0, 0
				candidate := false
				observeCandidate := func() {
					candidate = r.State == Accepted && r.Destination == destination && r.DestinationIdentity == identity && r.Root == "" && !r.Started
				}
				m.durable.ownerCheck = func() error {
					checks++
					if phase == "save-check" && checks == 2 {
						observeCandidate()
						hold()
					}
					return check()
				}
				m.durable.ownerOperation = func(fn func() error) error {
					if phase == "save-owner" {
						observeCandidate()
						hold()
					}
					return owned(func() error { callbacks++; return fn() })
				}
				if phase == "admission" {
					hold()
				}
				batch, err := m.Accept(offer.Lease.DeliveryID, destination)
				if err != ErrBusy || callbacks != 0 || candidate != (phase != "admission") || checks != map[string]int{"admission": 1, "save-check": 2, "save-owner": 2}[phase] {
					t.Fatal("acceptance did not refuse at the selected ownership boundary")
				}
				if batch.State != Pending || batch.Destination != "" || !bytes.Equal(before, state()) || !bytes.Equal(disk, read()) || !bytes.Equal(confirmed, marshal(m.durable.confirmed)) || m.durable.blocked != nil || len(m.durable.pendingConfirmed) != 0 {
					t.Fatal("busy acceptance published or retained tentative consent")
				}
				if entries, err := os.ReadDir(destination); err != nil || len(entries) != 0 {
					t.Fatal("refused acceptance created output")
				}
				release()
				release = nil
				m.durable.ownerCheck, m.durable.ownerOperation = check, owned
				// This is one explicit second acceptance after ownership is released,
				// not a retry loop. The same delivery and destination remain selected.
				batch, err = m.Accept(offer.Lease.DeliveryID, destination)
				if err != nil || batch.State != Accepted || batch.Destination != destination || r.DestinationIdentity != identity || r.Root != "" || r.Started || !reflect.DeepEqual(r.Offer, originalOffer) || r.Lease != originalLease {
					t.Fatal("later explicit acceptance changed the original delivery or destination")
				}
				acceptedDisk := read()
				if _, err := m.Accept(offer.Lease.DeliveryID, t.TempDir()); !errors.Is(err, ErrState) || errors.Is(err, ErrBusy) || !bytes.Equal(acceptedDisk, read()) {
					t.Fatal("different destination was accepted or classified as busy")
				}
				if _, err := m.Accept(offer.Lease.DeliveryID, destination); err != nil || !bytes.Equal(acceptedDisk, read()) {
					t.Fatal("same accepted destination was not idempotent")
				}
				entries, err := os.ReadDir(destination)
				if err != nil || len(entries) != 0 {
					t.Fatal("acceptance created output before authenticated lease start")
				}
				if actual, err := durableIdentity(parent, ".", true); err != nil || actual != identity {
					t.Fatal("acceptance replaced the original destination")
				}
			})
		}
	}
}

func TestDurableAcceptUncertainPublicationIsNotBusy(t *testing.T) {
	m := durableTestManager(t, t.TempDir(), true)
	s := durableTestOffer(t, m, "accept-uncertain", durableTestEntry())
	destination := t.TempDir()
	before, err := os.ReadFile(m.durable.path)
	if err != nil {
		t.Fatal("synthetic ledger unavailable")
	}
	m.durable.fault = func(stage string) error {
		if stage == "after_ledger" {
			return errors.New("synthetic uncertain publication")
		}
		return nil
	}
	_, err = m.Accept(s.Lease.DeliveryID, destination)
	if !errors.Is(err, ErrDurableRecovery) || !errors.Is(err, config.ErrAtomicCommitted) || errors.Is(err, ErrBusy) || DurableErrorCode(err) != "durable_recovery_required" || m.durable.blocked == nil {
		t.Fatal("uncertain acceptance was treated as transient ownership contention")
	}
	after, readErr := os.ReadFile(m.durable.path)
	if readErr != nil || bytes.Equal(before, after) {
		t.Fatal("uncertain fixture did not reach actual ledger publication")
	}
	batch, err := m.Get(s.Lease.DeliveryID)
	if err != nil || batch.State != Pending || batch.Destination != "" {
		t.Fatal("uncertified acceptance escaped its confirmed projection")
	}
	if _, err := m.Accept(s.Lease.DeliveryID, destination); !errors.Is(err, ErrDurableRecovery) || errors.Is(err, ErrBusy) {
		t.Fatal("uncertain acceptance reopened admission")
	}
	entries, err := os.ReadDir(destination)
	if err != nil || len(entries) != 0 {
		t.Fatal("uncertain acceptance created output before lease start")
	}
}
