package core

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/directlan"
	"github.com/webkaz-labs/sobalink/internal/operationjournal"
	"github.com/webkaz-labs/sobalink/internal/resource"
	"github.com/webkaz-labs/sobalink/internal/resourcegrant"
	"github.com/webkaz-labs/sobalink/internal/webui"
)

// Source-stage tests use inert Core models and owned TempDir files only. They
// cannot mint a concrete transport capability, start a listener, or activate a
// real grant. End-to-end provider success needs the separate transport gate.
func managementModelBinding(c *Core) operationjournal.CurrentBinding {
	return operationjournal.CurrentBinding{Scope: operationjournal.RemoteOperationScope{Target: resourceTarget(c), Relationship: resourcegrant.Relationship{Backend: resourcegrant.Backend, TargetKey: strings.Repeat("a", 64), PeerKey: strings.Repeat("b", 64), PairBinding: strings.Repeat("c", 64)}, GrantID: strings.Repeat("d", 32), ScopeVersion: operationjournal.ScopeVersion}, AuthorizingRevision: 1, IssuanceNonce: c.resourceNonce, ManagedGeneration: strings.Repeat("e", 64)}
}
func managementModelRequest(binding operationjournal.CurrentBinding, action string) resourcegrant.ManagementRequest {
	return resourcegrant.ManagementRequest{ManagementSelector: resourcegrant.ManagementSelector{ProtocolVersion: resourcegrant.ManagementProtocolVersion, Target: binding.Scope.Target, GrantID: binding.Scope.GrantID, GrantRevision: binding.AuthorizingRevision}, Action: action}
}
func managementModelIntent(t *testing.T, c *Core) (operationjournal.CurrentBinding, resourcegrant.ManagementRequest, operationjournal.TaggedRecord) {
	t.Helper()
	binding := managementModelBinding(c)
	journal, err := managementJournalView(c.resourceState)
	if err != nil {
		t.Fatal(err)
	}
	id, err := operationjournal.CurrentRemoteID(journal, binding)
	if err != nil {
		t.Fatal(err)
	}
	request := managementModelRequest(binding, resourcegrant.ApplyAction)
	request.Apply = &resourcegrant.ManagementApplyRequest{OperationID: id, BaseRevision: strings.Repeat("1", 64), ReviewRevision: strings.Repeat("2", 64), Settings: resource.Settings{TransferConcurrentFiles: capacity.Limited(3), TransferConcurrentPerPeer: capacity.Default()}}
	intent, err := operationjournal.NewRemoteIntent(binding, *journal.HighWater+1, *request.Apply)
	if err != nil {
		t.Fatal(err)
	}
	return binding, request, intent
}
func saveManagementModelIntent(t *testing.T, c *Core, owner *config.Lock, intent operationjournal.TaggedRecord) {
	t.Helper()
	err := scopedStorageBinding(c, owner, func(b *resourcePathBinding) error {
		if err := c.migrateResourceJournalBound(intent, b); err != nil {
			return err
		}
		next, err := operationjournal.WithIntent(*c.resourceState.scoped, intent)
		if err != nil {
			return err
		}
		pending := scopedResourceEnvelope(next)
		if err := c.writeResourceEnvelopeBound(pending, b); err != nil {
			return err
		}
		c.resourceState = pending
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestManagementMissingConcreteAuthorityPrecedesHistory(t *testing.T) {
	// The poisoned journal would fail or panic if history were consulted. No
	// grant, runtime, or caller-created zero capability can authorize access.
	c := &Core{ctx: context.Background(), resourceState: resourceEnvelope{}}
	for _, capability := range []*directlan.ManagedManagement{nil, {}} {
		if _, err := c.authorizeManagementBound(nil, capability, resourcegrant.ManagementRequest{}, nil); err == nil {
			t.Fatal("missing concrete authority accepted")
		}
	}
}

func TestManagementPreviewAndStatusNeverMigrate(t *testing.T) {
	c, owner := resourceFixture(t)
	binding := managementModelBinding(c)
	settings := resource.Settings{TransferConcurrentFiles: capacity.Limited(3), TransferConcurrentPerPeer: capacity.Default()}
	request := managementModelRequest(binding, resourcegrant.PreviewAction)
	request.Preview = &resourcegrant.ManagementPreviewRequest{Settings: settings}
	before, err := os.ReadFile(resourceStatePath(c.dir))
	if err != nil {
		t.Fatal(err)
	}
	writes := 0
	c.atomicWrite = func(string, []byte) error { writes++; return errors.New("read-only operation attempted publication") }
	var first resourcegrant.ManagementReply
	err = scopedStorageBinding(c, owner, func(b *resourcePathBinding) error {
		var err error
		first, err = c.managementOperationBound(nil, nil, request, binding, b)
		if err != nil {
			return err
		}
		second, err := c.managementOperationBound(nil, nil, request, binding, b)
		if err != nil || !reflect.DeepEqual(first, second) {
			t.Fatal("preview reserved or changed a token", err)
		}
		status := managementModelRequest(binding, resourcegrant.StatusAction)
		status.Status = &resourcegrant.ManagementStatusRequest{OperationID: first.Preview.OperationID}
		missing, err := c.managementOperationBound(nil, nil, status, binding, b)
		if err != nil || missing.Unavailable == nil || missing.Operation != nil {
			t.Fatal("nonexistent preview exposed evidence", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(resourceStatePath(c.dir))
	if writes != 0 || c.resourceState.scoped != nil || *c.resourceState.HighWater != 0 || string(before) != string(after) {
		t.Fatal("read path migrated, reserved, or published")
	}
}

func TestManagementRetainedOriginalBindingAndScopedStatus(t *testing.T) {
	c, owner := resourceFixture(t)
	binding, request, intent := managementModelIntent(t, c)
	saveManagementModelIntent(t, c, owner, intent)
	err := scopedStorageBinding(c, owner, func(b *resourcePathBinding) error {
		outcome := resource.Outcome{Status: "canceled", Configuration: "not_attempted", Accounting: "not_attempted", Transfer: "not_attempted"}
		if _, err := c.completeManagementBound(request, intent, outcome, b); err != nil {
			return err
		}
		// A retained original hash wins before new selector/boot/epoch/base tests.
		binding.AuthorizingRevision++
		binding.IssuanceNonce = strings.Repeat("f", 32)
		binding.ManagedGeneration = strings.Repeat("0", 64)
		request.GrantRevision = binding.AuthorizingRevision
		c.atomicWrite = func(string, []byte) error {
			t.Error("retained evidence attempted a write")
			return errors.New("forbidden")
		}
		replayed, err := c.managementOperationBound(nil, nil, request, binding, b)
		if err != nil || replayed.Operation == nil || !*replayed.Operation.EvidenceDurable || replayed.Operation.Outcome != outcome {
			t.Fatal("retained original identity was reinterpreted", err)
		}
		status := managementModelRequest(binding, resourcegrant.StatusAction)
		status.Status = &resourcegrant.ManagementStatusRequest{OperationID: request.Apply.OperationID}
		foreign := binding
		foreign.Scope.Relationship.PairBinding = strings.Repeat("9", 64)
		unknown, err := c.managementOperationBound(nil, nil, status, foreign, b)
		if err != nil || unknown.Unavailable == nil || unknown.Operation != nil {
			t.Fatal("foreign scope exposed evidence", err)
		}
		status.Status.OperationID = strings.Repeat("8", 64)
		missing, err := c.managementOperationBound(nil, nil, status, binding, b)
		if err != nil || missing.Unavailable == nil || missing.Operation != nil {
			t.Fatal("missing evidence classification changed", err)
		}
		request.Apply.Settings.TransferConcurrentFiles = capacity.Limited(4)
		if _, err := c.managementOperationBound(nil, nil, request, binding, b); err == nil {
			t.Fatal("retained operation accepted different payload")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	retained := c.resourceState.scoped.Records[0].Remote
	if retained.AuthorizingRevision != intent.Remote.AuthorizingRevision || retained.IssuanceNonce != intent.Remote.IssuanceNonce || retained.ManagedGeneration != intent.Remote.ManagedGeneration {
		t.Fatal("status or replay rewrote original evidence")
	}
}

func TestManagementAdmissionDenialCompletesDespiteCancellation(t *testing.T) {
	c, owner := resourceFixture(t)
	_, request, intent := managementModelIntent(t, c)
	saveManagementModelIntent(t, c, owner, intent)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c.ctx = ctx
	provider := false
	c.atomicWrite = func(path string, data []byte) error {
		if path != resourceStatePath(c.dir) {
			provider = true
			return errors.New("provider must not run")
		}
		return config.AtomicWrite(path, data)
	}
	err := scopedStorageBinding(c, owner, func(b *resourcePathBinding) error {
		reply, err := c.finishManagementIntentBound(nil, nil, request, intent, c.capacityPolicy(), b)
		if err != nil || reply.Operation == nil || !*reply.Operation.EvidenceDurable || reply.Operation.Outcome.Status != "canceled" || reply.Operation.Outcome.Configuration != "not_attempted" {
			t.Fatal("denied intent lacked durable canceled evidence", err)
		}
		return nil
	})
	if err != nil || provider || c.resourceFrozen {
		t.Fatal("denied admission invoked provider or lost completion", err)
	}
}

func TestManagementCompletionUncertaintyRetainsUnknown(t *testing.T) {
	for _, published := range []bool{false, true} {
		t.Run(map[bool]string{false: "unpublished", true: "published"}[published], func(t *testing.T) {
			c, owner := resourceFixture(t)
			binding, request, intent := managementModelIntent(t, c)
			saveManagementModelIntent(t, c, owner, intent)
			c.atomicWrite = func(path string, data []byte) error {
				if published {
					if err := config.AtomicWrite(path, data); err != nil {
						return err
					}
					return config.ErrAtomicCommitted
				}
				return errors.New("synthetic completion failure")
			}
			err := scopedStorageBinding(c, owner, func(b *resourcePathBinding) error {
				outcome := resource.Outcome{Status: "applied", Configuration: "durable", Accounting: "not_required", Transfer: "not_required"}
				reply, err := c.completeManagementBound(request, intent, outcome, b)
				if err != nil || reply.Operation == nil || *reply.Operation.EvidenceDurable || reply.Operation.Outcome != resource.UnknownOutcome() {
					t.Fatal("uncertainty acknowledged as terminal", err)
				}
				return nil
			})
			if err != nil || !c.resourceFrozen || c.resourceState.scoped.Records[0].Remote.Phase != "intent" {
				t.Fatal("uncertainty lost pinned intent", err)
			}
			if (c.resourceUncertainWrite != nil) != published {
				t.Fatal("uncertain and known-nonpublished writes have incorrect candidates")
			}
			status := managementModelRequest(binding, resourcegrant.StatusAction)
			status.Status = &resourcegrant.ManagementStatusRequest{OperationID: request.Apply.OperationID}
			err = scopedStorageBinding(c, owner, func(b *resourcePathBinding) error {
				reply, err := c.managementOperationBound(nil, nil, status, binding, b)
				if err != nil || reply.Operation == nil || *reply.Operation.EvidenceDurable || reply.Operation.Outcome != resource.UnknownOutcome() {
					t.Fatal("exact self-publication was denied or promoted to durable result", err)
				}
				return nil
			})
			if err != nil || c.resourceRemoteEvidenceDenied {
				t.Fatal("exact uncertainty candidate failed owned reread", err)
			}
		})
	}
}

func TestManagementLocalClientRejectsCommandActionConfusion(t *testing.T) {
	c, _ := resourceFixture(t)
	binding := managementModelBinding(c)
	request := managementModelRequest(binding, resourcegrant.Inspect)
	input := resourcegrant.RemoteManagementInput{PeerKey: binding.Scope.Relationship.PeerKey, Request: request}
	raw := scopedStorageJSON(t, input)
	for _, name := range []string{"resource.remote.management.preview", "resource.remote.management.apply", "resource.remote.management.operation.status"} {
		_, err := c.Command(context.Background(), webui.Command{RequestID: "same-management-request", Name: name, Payload: raw})
		var coded interface{ ErrorCode() string }
		if !errors.As(err, &coded) || coded.ErrorCode() != "resource_grant_invalid" {
			t.Fatal("typed command became a dispatch bridge", name, err)
		}
	}
	if len(c.requests) != 0 {
		t.Fatal("remote operation retained local request-history evidence")
	}
}

func TestManagementFreshApplyCannotReachMigrationWithoutCapability(t *testing.T) {
	c, owner := resourceFixture(t)
	binding := managementModelBinding(c)
	previewRequest := managementModelRequest(binding, resourcegrant.PreviewAction)
	previewRequest.Preview = &resourcegrant.ManagementPreviewRequest{Settings: resource.Settings{TransferConcurrentFiles: capacity.Limited(3), TransferConcurrentPerPeer: capacity.Default()}}
	writes := 0
	c.atomicWrite = func(string, []byte) error { writes++; return errors.New("missing capability reached publication") }
	err := scopedStorageBinding(c, owner, func(b *resourcePathBinding) error {
		preview, err := c.managementOperationBound(nil, nil, previewRequest, binding, b)
		if err != nil {
			return err
		}
		request := managementModelRequest(binding, resourcegrant.ApplyAction)
		p := preview.Preview
		request.Apply = &resourcegrant.ManagementApplyRequest{OperationID: p.OperationID, BaseRevision: p.BaseRevision, ReviewRevision: p.ReviewRevision, Settings: p.Requested}
		if _, err := c.managementOperationBound(nil, nil, request, binding, b); err == nil {
			t.Fatal("fresh apply accepted invented local authority")
		}
		return nil
	})
	if err != nil || writes != 0 || c.resourceState.scoped != nil || *c.resourceState.HighWater != 0 {
		t.Fatal("unauthorized fresh apply reached migration or intent", err)
	}
}

func TestManagementOwnedJournalChangeFreezesNewEvidence(t *testing.T) {
	c, owner := resourceFixture(t)
	if err := os.WriteFile(resourceStatePath(c.dir), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	err := scopedStorageBinding(c, owner, func(b *resourcePathBinding) error { return c.currentManagementJournalBound(b) })
	if err == nil || !c.resourceFrozen || !c.resourceRemoteEvidenceDenied {
		t.Fatal("replaced evidence was certified")
	}
	if err := scopedStorageBinding(c, owner, func(b *resourcePathBinding) error { return c.currentManagementJournalBound(b) }); err == nil {
		t.Fatal("later frozen read reclassified old memory as certified evidence")
	}
}

func TestManagementFrozenReadDetectsLaterJournalMutation(t *testing.T) {
	for _, mutation := range []string{"missing", "corrupt", "different_valid_completion", "older_valid_state"} {
		t.Run(mutation, func(t *testing.T) {
			c, owner := resourceFixture(t)
			older := c.resourceState.clone()
			binding, request, intent := managementModelIntent(t, c)
			saveManagementModelIntent(t, c, owner, intent)
			c.atomicWrite = func(path string, data []byte) error {
				if err := config.AtomicWrite(path, data); err != nil {
					return err
				}
				return config.ErrAtomicCommitted
			}
			err := scopedStorageBinding(c, owner, func(b *resourcePathBinding) error {
				_, err := c.completeManagementBound(request, intent, resource.Outcome{Status: "applied", Configuration: "durable", Accounting: "not_required", Transfer: "not_required"}, b)
				return err
			})
			if err != nil || !c.resourceFrozen || c.resourceUncertainWrite == nil {
				t.Fatal("uncertainty fixture unavailable", err)
			}
			path := resourceStatePath(c.dir)
			switch mutation {
			case "missing":
				err = os.Remove(path)
			case "corrupt":
				err = config.AtomicWrite(path, []byte("{}"))
			case "different_valid_completion":
				other, completeErr := operationjournal.Complete(*c.resourceState.scoped, intent, resource.Outcome{Status: "canceled", Configuration: "not_attempted", Accounting: "not_attempted", Transfer: "not_attempted"})
				if completeErr != nil {
					t.Fatal(completeErr)
				}
				err = config.AtomicWrite(path, scopedStorageJSON(t, other))
			case "older_valid_state":
				err = config.AtomicWrite(path, scopedStorageJSON(t, older))
			}
			if err != nil {
				t.Fatal(err)
			}
			status := managementModelRequest(binding, resourcegrant.StatusAction)
			status.Status = &resourcegrant.ManagementStatusRequest{OperationID: request.Apply.OperationID}
			err = scopedStorageBinding(c, owner, func(b *resourcePathBinding) error {
				_, err := c.managementOperationBound(nil, nil, status, binding, b)
				return err
			})
			if err == nil || !c.resourceRemoteEvidenceDenied {
				t.Fatal("frozen memory hid changed owned journal")
			}
			// Restoring a matching intent does not clear the terminal read denial.
			if err := config.AtomicWrite(path, scopedStorageJSON(t, c.resourceState)); err != nil {
				t.Fatal(err)
			}
			if err := scopedStorageBinding(c, owner, func(b *resourcePathBinding) error { return c.currentManagementJournalBound(b) }); err == nil {
				t.Fatal("restoration cleared read-denial latch without owned reopen")
			}
		})
	}
}

func TestManagementSuccessfulJournalWriteClearsUncertainCandidates(t *testing.T) {
	c, owner := resourceFixture(t)
	older := c.resourceState.clone()
	_, _, intent := managementModelIntent(t, c)
	c.atomicWrite = func(path string, data []byte) error {
		if err := config.AtomicWrite(path, data); err != nil {
			return err
		}
		return config.ErrAtomicCommitted
	}
	err := scopedStorageBinding(c, owner, func(b *resourcePathBinding) error { return c.migrateResourceJournalBound(intent, b) })
	if err == nil || !c.resourceFrozen || c.resourceUncertainWrite == nil {
		t.Fatal("uncertain migration fixture unavailable")
	}
	c.atomicWrite = nil
	// Exercise the central writer directly: any later successful publication
	// must retire the old allowance, even if a separate freeze remains set.
	err = scopedStorageBinding(c, owner, func(b *resourcePathBinding) error { return c.writeResourceEnvelopeBound(c.resourceState, b) })
	if err != nil || c.resourceUncertainWrite != nil {
		t.Fatal("successful publication retained stale candidate", err)
	}
	if err := config.AtomicWrite(resourceStatePath(c.dir), scopedStorageJSON(t, older)); err != nil {
		t.Fatal(err)
	}
	if err := scopedStorageBinding(c, owner, func(b *resourcePathBinding) error { return c.currentManagementJournalBound(b) }); err == nil || !c.resourceRemoteEvidenceDenied {
		t.Fatal("stale uncertainty candidate admitted later rollback")
	}
}

func TestManagementLocalCompletionUncertaintyUsesExactCandidate(t *testing.T) {
	c, owner := resourceFixture(t)
	binding, remote, remoteIntent := managementModelIntent(t, c)
	saveManagementModelIntent(t, c, owner, remoteIntent)
	if err := scopedStorageBinding(c, owner, func(b *resourcePathBinding) error {
		_, err := c.completeManagementBound(remote, remoteIntent, resource.Outcome{Status: "canceled", Configuration: "not_attempted", Accounting: "not_attempted", Transfer: "not_attempted"}, b)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	local := resourceApplyRequest(t, c, 5)
	journalWrites := 0
	c.atomicWrite = func(path string, data []byte) error {
		if err := config.AtomicWrite(path, data); err != nil {
			return err
		}
		if path == resourceStatePath(c.dir) {
			journalWrites++
			if journalWrites == 2 {
				return config.ErrAtomicCommitted
			}
		}
		return nil
	}
	result := resourceCall(t, c, "resource.apply", local).(resource.Operation)
	if !c.resourceFrozen || c.resourceUncertainWrite == nil || result.EvidenceDurable || result.Outcome != resource.UnknownOutcome() {
		t.Fatal("local result uncertainty was not conservatively retained")
	}
	status := managementModelRequest(binding, resourcegrant.StatusAction)
	status.Status = &resourcegrant.ManagementStatusRequest{OperationID: remote.Apply.OperationID}
	if err := scopedStorageBinding(c, owner, func(b *resourcePathBinding) error {
		reply, err := c.managementOperationBound(nil, nil, status, binding, b)
		if err != nil || reply.Operation == nil || !*reply.Operation.EvidenceDurable || reply.Operation.Outcome.Status != "canceled" {
			t.Fatal("exact local self-result candidate hid unrelated prior certified remote evidence", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	last := c.resourceState.scoped.Records[len(c.resourceState.scoped.Records)-1].Local
	if last == nil || last.Phase != "intent" || last.Outcome != resource.UnknownOutcome() {
		t.Fatal("remote reread promoted uncertain local completion")
	}
}

func TestManagementKnownNonpublicationDoesNotAllowAttemptedResult(t *testing.T) {
	c, owner := resourceFixture(t)
	_, request, intent := managementModelIntent(t, c)
	saveManagementModelIntent(t, c, owner, intent)
	outcome := resource.Outcome{Status: "applied", Configuration: "durable", Accounting: "not_required", Transfer: "not_required"}
	c.atomicWrite = func(string, []byte) error { return errors.New("known nonpublication") }
	if err := scopedStorageBinding(c, owner, func(b *resourcePathBinding) error {
		_, err := c.completeManagementBound(request, intent, outcome, b)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if !c.resourceFrozen || c.resourceUncertainWrite != nil {
		t.Fatal("known nonpublication established an attempted-result allowance")
	}
	attempted, err := operationjournal.Complete(*c.resourceState.scoped, intent, outcome)
	if err != nil {
		t.Fatal(err)
	}
	if err := config.AtomicWrite(resourceStatePath(c.dir), scopedStorageJSON(t, attempted)); err != nil {
		t.Fatal(err)
	}
	if err := scopedStorageBinding(c, owner, func(b *resourcePathBinding) error { return c.currentManagementJournalBound(b) }); err == nil || !c.resourceRemoteEvidenceDenied {
		t.Fatal("unpublished attempted result was accepted as an owned uncertainty candidate")
	}
}
