package endpointmeta

import (
	"crypto/ed25519"
	"time"
)

// SaveModel is supplied only by an isolated model harness. A nil error means
// confirmed durable publication; published describes an uncertain error result.
// Production stores must keep their existing exclusive owner and AtomicWrite.
type SaveModel func([]byte) (published bool, err error)

// ExportModel saves the entire synthetic snapshot before returning newly issued
// bytes. An error returns no bytes and a recovery outcome, including when the
// replacement was published. The input recovery latch cannot be bypassed here.
func ExportModel(model SaveResolution, remoteKey string, u UpdateBody, key ed25519.PrivateKey, now time.Time, budget int, save SaveModel) (SaveResolution, []byte, error) {
	s := model.Snapshot
	if model.Recovery || !model.Durable || s.PendingChange != nil {
		return model, nil, ErrRecovery
	}
	i, err := recordForKey(s, remoteKey)
	if err != nil {
		return model, nil, err
	}
	r := s.Peers[i]
	if r.PairContext == nil || r.EndpointState == nil || !r.ContextConfirmed {
		return model, nil, ErrReview
	}
	if !fitsExport(s, remoteKey, u, now, budget) {
		return model, nil, ErrCapacity
	}
	if err := s.ValidateAt(now); err != nil {
		return model, nil, err
	}
	if save == nil {
		return model, nil, ErrInvalid
	}
	next := cloneSnapshot(s)
	state := next.Peers[i].EndpointState
	seq, err := nextCounter(state.IssuedHighwater)
	if err != nil {
		return model, nil, err
	}
	if u.Sequence != seq || u.Issuer != s.LocalPeer.Key || u.Recipient != remoteKey {
		return model, nil, ErrIdentity
	}
	if u.Operation == "set" && (u.Endpoint != s.LocalPeer.Endpoint || u.PriorEndpoint != s.PreviousLocalEndpoint) || u.Operation == "withdraw" && u.PriorEndpoint != s.LocalPeer.Endpoint {
		return model, nil, ErrPolicy
	}
	e, err := Sign(u, key)
	if err != nil {
		return model, nil, err
	}
	if err := Inspect(e, *r.PairContext, remoteKey, now); err != nil {
		return model, nil, err
	}
	state.IssuedVersion = 1
	state.IssuedHighwater = seq
	state.IssuedProof = &e
	next.Revision, err = nextCounter(s.Revision)
	if err != nil {
		return model, nil, err
	}
	next.Peers[i].Revision = next.Revision
	next.Peers[i].UpgradePending = nil
	next.ObservedAt = now.UTC().Format(time.RFC3339Nano)
	b, err := EncodeSnapshot(next, budget)
	if err != nil {
		return model, nil, err
	}
	published, saveError := save(b)
	result := ResolveSave(s, next, published, saveError)
	if saveError != nil {
		return result, nil, ErrRecovery
	}
	wire, err := Encode(e)
	return result, wire, err
}

func ReexportModel(model SaveResolution, remoteKey string) ([]byte, error) {
	s := model.Snapshot
	if model.Recovery || !model.Durable || s.PendingChange != nil {
		return nil, ErrRecovery
	}
	if err := s.Validate(); err != nil {
		return nil, err
	}
	i, err := recordForKey(s, remoteKey)
	if err != nil {
		return nil, err
	}
	r := s.Peers[i]
	if r.EndpointState == nil || r.EndpointState.IssuedProof == nil || !r.ContextConfirmed {
		return nil, ErrReview
	}
	return Encode(*r.EndpointState.IssuedProof)
}
