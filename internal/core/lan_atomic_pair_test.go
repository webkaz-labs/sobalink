package core

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/control"
	"github.com/webkaz-labs/sobalink/internal/lanlink"
	tailcat "github.com/webkaz-labs/sobalink/internal/routecat"
	"tailscale.com/types/key"
)

func TestLANJoinPrivateSaveOutcomeAtCommandBoundary(t *testing.T) {
	for _, outcome := range []string{"committed", "ordinary", "busy", "recovery"} {
		t.Run(outcome, func(t *testing.T) {
			c := openLANTestCore(t)
			if err := c.configureLAN(testLANSelection()); err != nil {
				t.Fatal(err)
			}
			store := c.lanStoreCopy()
			before, err := os.ReadFile(store.path)
			if err != nil {
				t.Fatal(err)
			}
			original := store.copy()
			next := store.copy()
			peer := lanlink.Peer{Key: lanlink.GenerateIdentity().PublicKey(), Name: "paired"}
			privateRole := key.NewNode()
			incomingText, _ := key.NewNode().Public().MarshalText()
			incomingRole := string(incomingText)
			privateText, _ := privateRole.MarshalText()
			next.Trust.Peers = append(next.Trust.Peers, peer)
			next.Remotes = append(next.Remotes, lanlink.RemotePeer{Peer: peer, Address: tailcat.Addr("private-capability"), ClientPrivate: privateRole, IncomingClientKey: strings.TrimPrefix(incomingRole, "nodekey:")})
			pathErr := &os.PathError{Op: "sync", Path: store.path + "/.sobalink-atomic-v1/private-capability/" + string(privateText), Err: os.ErrPermission}
			var sentinel error
			switch outcome {
			case "committed":
				sentinel = config.ErrAtomicCommitted
			case "busy":
				sentinel = config.ErrAtomicBusy
			case "recovery":
				sentinel = config.ErrAtomicRecovery
			}
			calls := 0
			store.write = func(path string, data []byte) error {
				calls++
				if outcome == "committed" {
					if err := config.AtomicWrite(path, data); err != nil {
						return err
					}
				}
				return errors.Join(sentinel, pathErr)
			}
			// Exercise the real lanStore publication contract before the actual command
			// boundary; the fake engine supplies only the authenticated remote outcome.
			saveErr := store.persist(next.Trust, next.Remotes)
			backend, engine := testLANBackend()
			if err := backend.Start(); err != nil {
				t.Fatal(err)
			}
			c.mu.Lock()
			c.node = backend
			c.mu.Unlock()
			engine.pairError = fmt.Errorf("%w: %w", lanlink.ErrRemotePairedLocalSave, saveErr)
			requestID := randomID()
			_, err = command(c, requestID, "lan.join", map[string]any{"invitation": "{}"})
			if err == nil || networkErrorCode(err) != "lan_remote_paired_local_save" {
				t.Fatal("machine code changed", err)
			}
			if sentinel != nil && !errors.Is(err, sentinel) {
				t.Fatal("atomic outcome lost at command boundary", err)
			}
			if outcome != "committed" && errors.Is(err, config.ErrAtomicCommitted) {
				t.Fatal("unwritten save claimed replacement", err)
			}
			var privateCause *os.PathError
			if errors.As(err, &privateCause) || errors.Is(err, os.ErrPermission) {
				t.Fatal("private cause leaked", err)
			}
			for _, secret := range []string{store.path, ".sobalink-atomic-v1", "private-capability", string(privateText), incomingRole} {
				if strings.Contains(err.Error(), secret) {
					t.Fatal("private detail in public error", err)
				}
			}
			after, readErr := os.ReadFile(store.path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if outcome == "committed" {
				if bytes.Equal(before, after) || !jsonEqual(store.copy(), next) || !strings.Contains(err.Error(), "local state was replaced") || !strings.Contains(err.Error(), "both devices") || !strings.Contains(err.Error(), "before restarting") {
					t.Fatal("published outcome or recovery guidance wrong", err)
				}
			} else if !bytes.Equal(before, after) || !jsonEqual(store.copy(), original) || !strings.Contains(err.Error(), "local save was not written") || !strings.Contains(err.Error(), "local saved approvals") || !strings.Contains(err.Error(), "other device") {
				t.Fatal("unpublished outcome or recovery guidance wrong", err)
			}
			reopened, readErr := readLANStore(store.path)
			if readErr != nil || !jsonEqual(reopened.copy(), store.copy()) {
				t.Fatal("saved-store/reopen mismatch", readErr)
			}
			// The existing local IPC envelope retains its two error fields and no data.
			envelope, marshalErr := json.Marshal(control.Response{Code: networkErrorCode(err), Error: err.Error()})
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			var fields map[string]json.RawMessage
			if json.Unmarshal(envelope, &fields) != nil || len(fields) != 2 || fields["code"] == nil || fields["error"] == nil {
				t.Fatal("machine envelope changed", string(envelope))
			}
			_, replayed := command(c, requestID, "lan.join", map[string]any{"invitation": "{}"})
			if replayed.Error() != err.Error() || networkErrorCode(replayed) != networkErrorCode(err) || calls != 1 {
				t.Fatal("command replay retried uncertain save", replayed, calls)
			}
			if sentinel != nil && !errors.Is(replayed, sentinel) {
				t.Fatal("replay erased atomic outcome", replayed)
			}
			t.Logf("outcome=%s code=%s writes=%d diskStoreReopenAgree=true redacted=true error=%q", outcome, networkErrorCode(err), calls, err.Error())
		})
	}
}

func TestLANJoinPrecontactRecoverySurvivesCommandBoundaryAndReplay(t *testing.T) {
	for _, wrapped := range []bool{false, true} {
		t.Run(fmt.Sprintf("wrapped-path-error=%v", wrapped), func(t *testing.T) {
			c := openLANTestCore(t)
			backend, engine := testLANBackend()
			if err := backend.Start(); err != nil {
				t.Fatal(err)
			}
			c.mu.Lock()
			c.node = backend
			c.mu.Unlock()
			cause := error(config.ErrAtomicRecovery)
			pathErr := &os.PathError{Op: "open", Path: "/private/saved/approval/key", Err: os.ErrPermission}
			if wrapped {
				cause = errors.Join(cause, pathErr)
			}
			engine.pairError = cause // pairingRecoveryLatch rejects before contact.
			id := randomID()
			payload := map[string]any{"invitation": "{}"}
			_, firstErr := command(c, id, "lan.join", payload)
			if firstErr == nil || !errors.Is(firstErr, config.ErrAtomicRecovery) || errors.Is(firstErr, config.ErrAtomicCommitted) {
				t.Fatalf("precontact recovery outcome changed: %v", firstErr)
			}
			if networkErrorCode(firstErr) != "" || strings.Contains(firstErr.Error(), "paired") || strings.Contains(firstErr.Error(), pathErr.Path) || errors.Is(firstErr, os.ErrPermission) {
				t.Fatalf("unsafe or inaccurate public error: code=%q error=%v", networkErrorCode(firstErr), firstErr)
			}
			if !strings.Contains(firstErr.Error(), "inspect its saved approval") || !strings.Contains(firstErr.Error(), "before restarting") {
				t.Fatalf("missing recovery guidance: %v", firstErr)
			}
			_, replayErr := command(c, id, "lan.join", payload)
			if replayErr != firstErr || !errors.Is(replayErr, config.ErrAtomicRecovery) {
				t.Fatalf("Core.Command replay changed recovery: first=%v replay=%v", firstErr, replayErr)
			}
			_, laterErr := command(c, randomID(), "lan.join", payload)
			if laterErr == nil || !errors.Is(laterErr, config.ErrAtomicRecovery) || errors.Is(laterErr, config.ErrAtomicCommitted) || strings.Contains(laterErr.Error(), "paired") {
				t.Fatalf("later precontact request claimed earlier commit: %v", laterErr)
			}
		})
	}
}

func TestLANJoinRemoteLocalFailureRetainsOnlyPresentAtomicSentinels(t *testing.T) {
	c := openLANTestCore(t)
	backend, engine := testLANBackend()
	if err := backend.Start(); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	c.node = backend
	c.mu.Unlock()
	private := &os.PathError{Op: "sync", Path: "/private/hidden/role-capability", Err: os.ErrPermission}
	engine.pairError = errors.Join(lanlink.ErrRemotePairedLocalSave, config.ErrAtomicCommitted, config.ErrAtomicBusy, config.ErrAtomicRecovery, private)
	_, err := command(c, randomID(), "lan.join", map[string]any{"invitation": "{}"})
	for _, sentinel := range []error{config.ErrAtomicCommitted, config.ErrAtomicBusy, config.ErrAtomicRecovery} {
		if !errors.Is(err, sentinel) {
			t.Fatal("sentinel lost", sentinel, err)
		}
	}
	var leaked *os.PathError
	if errors.As(err, &leaked) || strings.Contains(err.Error(), "role-capability") {
		t.Fatal("private cause retained", err)
	}
	if networkErrorCode(err) != "lan_remote_paired_local_save" {
		t.Fatal(err)
	}
}
