package core

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/resourcegrant"
	"github.com/webkaz-labs/sobalink/internal/webui"
)

func TestResourceGrantLocalDispatcherDoesNotCacheReviews(t *testing.T) {
	c, peer := resourceGrantFixture(t)
	call := func(name string, input any) (any, error) {
		raw, _ := json.Marshal(input)
		return c.Command(context.Background(), webui.Command{RequestID: "same-local-grant-request", Name: name, Payload: raw})
	}
	input := resourceGrantInputs{Target: resourceTarget(c), PeerKey: peer, ExpiresAt: time.Now().Add(time.Hour).Unix(), Actions: []string{resourcegrant.Inspect}, Fields: []string{resourcegrant.FilesField, resourcegrant.PerPeerField}}
	first, err := call("resource.grant.preview", input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := call("resource.grant.preview", input)
	if err != nil {
		t.Fatal(err)
	}
	a, b := first.(resourceGrantReview), second.(resourceGrantReview)
	if a.Grant.ID == b.Grant.ID {
		t.Fatal("preview was cached")
	}
	if _, err := call("resource.grant.confirm", resourceGrantConfirmation{Review: a, Confirm: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := call("resource.grant.confirm", resourceGrantConfirmation{Review: b, Confirm: true}); err == nil {
		t.Fatal("stale review replayed cached success")
	}
	observed, err := call("resource.grant.inspect", grantInspectInput(c))
	if err != nil || len(observed.(resourceGrantLocalView).Records) != 1 {
		t.Fatal("inspection used another command's cached result")
	}
	if c.resourceGrants.fence != nil || c.node != nil {
		t.Fatal("local dispatcher activated runtime")
	}
}
