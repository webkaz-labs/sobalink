package core

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/directlan"
	"github.com/webkaz-labs/sobalink/internal/resourcegrant"
)

func resourceManagementEpochFixture() (*Core, resourceManagementOrigin) {
	c := &Core{ctx: context.Background(), resourceIdentity: strings.Repeat("a", 32), resourceNonce: strings.Repeat("b", 32), resourceLock: &config.Lock{}}
	backend := &directLANBackend{Node: &directlan.Node{}}
	owner := &managedCompletionOwner{core: c, backend: backend, node: backend.Node, coreDone: c.ctx.Done()}
	epoch := directlan.NewContextEpoch()
	owner.authority.Store(epoch)
	backend.completion, c.node = owner, backend
	return c, resourceManagementOrigin{controllerID: c.resourceIdentity, bootNonce: c.resourceNonce, owner: c.resourceLock, backend: backend, completion: owner, epoch: epoch, peer: &directlan.ResourcePeerCapture{}}
}

func TestResourceManagementOriginalEpochRejectsRenewalBeforeDisclosure(t *testing.T) {
	c, original := resourceManagementEpochFixture()
	if !original.epochCurrent(c) {
		t.Fatal("original live signal rejected")
	}
	original.epoch.Invalidate()
	original.completion.authority.Store(directlan.NewContextEpoch())
	if original.epochCurrent(c) || c.resourceManagementOriginCurrentLocked(original) == nil {
		t.Fatal("same owner renewal revived original disclosure")
	}
	original.completion.authority.Store(original.epoch)
	if original.epochCurrent(c) {
		t.Fatal("A-to-B-to-A restored terminal epoch")
	}
}

func TestResourceManagementOriginalEpochRejectsReplacementOwner(t *testing.T) {
	c, original := resourceManagementEpochFixture()
	replacement := &managedCompletionOwner{core: c, backend: original.backend, node: original.backend.Node, coreDone: c.ctx.Done()}
	replacement.authority.Store(original.epoch)
	original.backend.successor.Store(replacement)
	if original.epochCurrent(c) || c.resourceManagementOriginCurrentLocked(original) == nil {
		t.Fatal("replacement owner substituted same live epoch")
	}
}

func TestResourceManagementOriginMissingOwnershipDenies(t *testing.T) {
	c := &Core{ctx: context.Background()}
	if _, err := c.captureResourceManagementOriginLocked(strings.Repeat("a", 64)); err == nil {
		t.Fatal("missing local identity minted an origin")
	}
	if err := c.resourceManagementOriginCurrentLocked(resourceManagementOrigin{}); err == nil {
		t.Fatal("zero origin passed final disclosure")
	}
	data, err := json.Marshal(resourceManagementOrigin{})
	if err != nil || string(data) != "{}" {
		t.Fatal("private origin exposed serializable fields")
	}
}

func TestResourceManagementCapturedReplyBindsRequestedSettings(t *testing.T) {
	run := groupStoreRecordFixture(t, 1, 1)
	reply := *run.Review.Rows[0].Reply
	member := run.Review.Selection.Members[0]
	request := resourcegrant.ManagementRequest{ManagementSelector: member.Selector, Action: resourcegrant.PreviewAction, Preview: &resourcegrant.ManagementPreviewRequest{Settings: member.Requested}}
	if !resourceManagementCapturedReplyMatches(request, reply) {
		t.Fatal("matching requested settings rejected")
	}
	for _, field := range []string{"target", "grant", "revision", "requested"} {
		t.Run(field, func(t *testing.T) {
			changed := reply
			preview := *reply.Preview
			changed.Preview = &preview
			switch field {
			case "target":
				changed.Target.ResourceID = strings.Repeat("e", 32)
			case "grant":
				changed.GrantID = strings.Repeat("e", 32)
			case "revision":
				changed.GrantRevision--
			case "requested":
				changed.Preview.Requested.TransferConcurrentFiles = capacity.Default()
			}
			if resourceManagementCapturedReplyMatches(request, changed) {
				t.Fatal("mismatched reply accepted", field)
			}
		})
	}
	changed := reply
	preview := *reply.Preview
	changed.Preview = &preview
	changed.Preview.Effective.TransferConcurrentFiles = 1
	if !resourceManagementCapturedReplyMatches(request, changed) {
		t.Fatal("effective projection incorrectly equated with requested settings")
	}
}

func TestResourceManagementOriginRelationshipOrientation(t *testing.T) {
	local := resourcegrant.Relationship{Backend: resourcegrant.Backend, TargetKey: strings.Repeat("a", 64), PeerKey: strings.Repeat("b", 64), PairBinding: strings.Repeat("c", 64)}
	remote := remoteResourceRelationship(local)
	if remote.Validate() != nil || remote.TargetKey != local.PeerKey || remote.PeerKey != local.TargetKey || remote.PairBinding != local.PairBinding || remoteResourceRelationship(remote) != local {
		t.Fatal("outbound grant orientation changed identity or binding")
	}
}
