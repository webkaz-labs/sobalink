package core

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/config"
)

func TestOutboxPayloadDirectoryHasCertifiedPrivatePermissions(t *testing.T) {
	c := outboxFixture(t)
	review := outboxPrepareFixture(t, c, "private-payload")
	o := c.outbox
	payload := o.state.Payloads[o.prepared[review.OutboxID].intent.PayloadID]
	parent := filepath.Join(o.path(), "payloads")
	parentInfo, err := o.root.Lstat("payloads")
	if err != nil {
		t.Fatal(err)
	}
	// This invokes the strict native DACL check on Windows: protected inheritance,
	// exactly one current-user full-access ACE, correct owner, and no reparse point.
	child, err := config.OpenPrivateChildDirectoryBound(parent, payload.ID, parentInfo)
	if err != nil {
		t.Fatal(err)
	}
	identity, identityErr := outboxNativeIdentity(child)
	closeErr := child.Close()
	if identityErr != nil || closeErr != nil || identity != payload.RootIdentity {
		t.Fatal("payload native ownership differs from its certified directory", identityErr, closeErr)
	}
}

func TestOutboxPayloadCreationRefusesExistingPrivateDirectory(t *testing.T) {
	c := outboxFixture(t)
	o := c.outbox
	if err := config.SecureChildDirectory(o.path(), "payloads"); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(o.path(), "payloads")
	parentInfo, err := o.root.Lstat("payloads")
	if err != nil {
		t.Fatal(err)
	}
	payload := &outboxPayload{ID: randomID(), Marker: randomID(), FileIdentities: map[string]string{}}
	foreign, err := config.CreatePrivateChildDirectoryBound(parent, payload.ID, parentInfo)
	if err != nil {
		t.Fatal(err)
	}
	before, err := outboxNativeIdentity(foreign)
	if err != nil {
		_ = foreign.Close()
		t.Fatal(err)
	}
	if err := foreign.Close(); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(parent, payload.ID, "foreign.txt")
	if err := os.WriteFile(sentinel, []byte("synthetic foreign content"), 0600); err != nil {
		t.Fatal(err)
	}
	err = o.withOwnedLocked(func() error {
		o.state.Payloads[payload.ID] = payload
		return o.createPayloadLocked(payload)
	})
	if !errors.Is(err, os.ErrExist) {
		t.Fatalf("existing private directory was not refused: %v", err)
	}
	reopened, err := config.OpenPrivateChildDirectoryBound(parent, payload.ID, parentInfo)
	if err != nil {
		t.Fatal(err)
	}
	after, identityErr := outboxNativeIdentity(reopened)
	closeErr := reopened.Close()
	if identityErr != nil || closeErr != nil || before != after {
		t.Fatal("existing directory was replaced", identityErr, closeErr)
	}
	content, err := os.ReadFile(sentinel)
	if err != nil || string(content) != "synthetic foreign content" {
		t.Fatal("existing content was changed", err)
	}
	if _, err := os.Lstat(filepath.Join(parent, payload.ID, "owner")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("existing directory was assigned an outbox ownership marker", err)
	}
}

func TestOutboxRepeatedInitializationKeepsOriginalOwner(t *testing.T) {
	c := outboxFixture(t)
	original := c.outbox
	if err := c.initializeOutbox(false); err == nil {
		t.Fatal("startup replaced a retained coordinator")
	}
	if c.outbox != original {
		t.Fatal("original root owner was abandoned")
	}
	if _, err := original.root.Stat("."); err != nil {
		t.Fatal("original root was closed before its owner joined", err)
	}
}
