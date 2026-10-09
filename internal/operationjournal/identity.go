package operationjournal

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/webkaz-labs/sobalink/internal/resource"
	"github.com/webkaz-labs/sobalink/internal/resourcegrant"
)

// CurrentBinding must be captured by a future authenticated, owned coordinator.
// Validation cannot establish that these inputs are current or authorized.
// The nonce is the existing per-owned-boot identifier salt, not a credential.
type CurrentBinding struct {
	Scope               RemoteOperationScope
	AuthorizingRevision uint64
	IssuanceNonce       string
	ManagedGeneration   string
}

func (b CurrentBinding) Validate() error {
	if b.Scope.Validate() != nil || !validRevision(b.AuthorizingRevision) || !resource.ValidID(b.IssuanceNonce) || !resource.ValidDigest(b.ManagedGeneration) {
		return ErrInvalid
	}
	return nil
}
func (r RemoteOperationRecord) binding() CurrentBinding {
	return CurrentBinding{r.Scope, r.AuthorizingRevision, r.IssuanceNonce, r.ManagedGeneration}
}
func operationID(b CurrentBinding, sequence uint64) (string, error) {
	if b.Validate() != nil || sequence == 0 {
		return "", ErrInvalid
	}
	// This fixed struct and Scope's fixed order/tags are a versioned encoding.
	data, err := json.Marshal(struct {
		Domain              string               `json:"domain"`
		Scope               RemoteOperationScope `json:"scope"`
		AuthorizingRevision uint64               `json:"authorizingRevision"`
		ManagedGeneration   string               `json:"managedGeneration"`
		Sequence            uint64               `json:"sequence"`
	}{"sobalink/resource-operation/remote-management/v1", b.Scope, b.AuthorizingRevision, b.ManagedGeneration, sequence})
	if err != nil {
		return "", ErrInvalid
	}
	salt, err := hex.DecodeString(b.IssuanceNonce)
	if err != nil {
		return "", ErrInvalid
	}
	mac := hmac.New(sha256.New, salt)
	_, _ = mac.Write(data)
	return hex.EncodeToString(mac.Sum(nil)), nil
}
func remoteRequestHash(r RemoteOperationRecord) string {
	data, _ := json.Marshal(struct {
		Domain              string                               `json:"domain"`
		Sequence            uint64                               `json:"sequence"`
		Scope               RemoteOperationScope                 `json:"scope"`
		AuthorizingRevision uint64                               `json:"authorizingRevision"`
		IssuanceNonce       string                               `json:"issuanceNonce"`
		ManagedGeneration   string                               `json:"managedGeneration"`
		Action              string                               `json:"action"`
		Request             resourcegrant.ManagementApplyRequest `json:"request"`
	}{"sobalink/resource-operation/remote-request/v1", r.Sequence, r.Scope, r.AuthorizingRevision, r.IssuanceNonce, r.ManagedGeneration, resourcegrant.ApplyAction, r.Request})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// NewRemoteIntent constructs evidence only. It does not reserve a journal slot,
// authorize a request, publish an intent, or call a provider.
func NewRemoteIntent(current CurrentBinding, sequence uint64, request resourcegrant.ManagementApplyRequest) (TaggedRecord, error) {
	r := RemoteOperationRecord{Sequence: sequence, Scope: current.Scope, AuthorizingRevision: current.AuthorizingRevision, IssuanceNonce: current.IssuanceNonce, ManagedGeneration: current.ManagedGeneration, Request: cloneApply(request), Phase: "intent", Outcome: resource.UnknownOutcome()}
	r.RequestHash = remoteRequestHash(r)
	tagged := TaggedRecord{Kind: RemoteKind, Remote: &r}
	if tagged.Validate(current.Scope.Target.ResourceID, sequence) != nil {
		return TaggedRecord{}, ErrInvalid
	}
	return tagged, nil
}
