package endpointmeta

import (
	"crypto/ed25519"
	"time"
)

// SaveModel is supplied only by an isolated model harness. A nil error means
// confirmed durable publication; published describes an uncertain error result.
// Production stores must keep their existing exclusive owner and AtomicWrite.
type SaveModel func([]byte) (published bool, err error)

// ExportOptions contains the local choices for a new signed statement. Every
// identity, endpoint, sequence and scope field is derived from the snapshot.
type ExportOptions struct {
	Operation string
	Lifetime  string
	Expires   string
}

// PrepareExport returns unsigned private data, not permission to release bytes.
// Production callers still own their exact-file review and durable publication.
func PrepareExport(s Snapshot, remoteKey string, options ExportOptions, now time.Time, budget int) (UpdateBody, error) {
	return prepareExportAt(s, remoteKey, options, now.UTC().Format(time.RFC3339Nano), now, budget)
}

// The model harness historically accepts an explicit earlier Issued time. Keep
// that narrow compatibility here; production PrepareExport always captures now.
func prepareExportAt(s Snapshot, remoteKey string, options ExportOptions, issued string, now time.Time, budget int) (UpdateBody, error) {
	if !activeSnapshotVersion(s.Version) {
		return UpdateBody{}, ErrReview
	}
	if s.PendingChange != nil {
		return UpdateBody{}, ErrRecovery
	}
	i, err := activeRecordForKey(s, remoteKey)
	if err != nil {
		return UpdateBody{}, err
	}
	r := s.Peers[i]
	if r.PairContext == nil || r.EndpointState == nil || !r.ContextConfirmed {
		return UpdateBody{}, ErrReview
	}
	if _, err := nextCounter(s.Revision); err != nil {
		return UpdateBody{}, err
	}
	seq, err := nextCounter(r.EndpointState.IssuedHighwater)
	if err != nil {
		return UpdateBody{}, err
	}
	p := r.PairContext
	remoteScope := p.JoinerScope
	if s.LocalPeer.Key == p.JoinerKey {
		remoteScope = p.HostScope
	}
	scopeDigest, err := remoteScope.Digest()
	if err != nil {
		return UpdateBody{}, err
	}
	u := UpdateBody{Version: 1, Domain: UpdateDomain, PairBinding: r.EndpointState.PairBinding,
		Issuer: s.LocalPeer.Key, Recipient: remoteKey, IssuerTunnelKey: s.LocalPeer.TunnelKey,
		RecipientTunnelKey: r.Peer.TunnelKey, Sequence: seq, Operation: options.Operation,
		ScopeDigest: scopeDigest, Issued: issued, Lifetime: options.Lifetime, Expires: options.Expires}
	switch options.Operation {
	case "set":
		u.Endpoint, u.PriorEndpoint = s.LocalPeer.Endpoint, s.PreviousLocalEndpoint
	case "withdraw":
		u.PriorEndpoint = s.LocalPeer.Endpoint
	default:
		return UpdateBody{}, ErrInvalid
	}
	if !fitsExport(s, remoteKey, u, now, budget) {
		return UpdateBody{}, ErrCapacity
	}
	if err := s.ValidateAt(now); err != nil {
		return UpdateBody{}, err
	}
	if _, err := Encode(u); err != nil {
		return UpdateBody{}, err
	}
	if !p.HostScope.Contains(u.PriorEndpoint) || !p.JoinerScope.Contains(u.PriorEndpoint) ||
		u.Operation == "set" && (!p.HostScope.Contains(u.Endpoint) || !p.JoinerScope.Contains(u.Endpoint)) {
		return UpdateBody{}, ErrPolicy
	}
	if !current(u.Issued, u.Lifetime, u.Expires, now) {
		return UpdateBody{}, ErrExpired
	}
	return u, nil
}

func matchIssuedBody(got, expected UpdateBody) error {
	if got.Endpoint != expected.Endpoint || got.PriorEndpoint != expected.PriorEndpoint {
		return ErrPolicy
	}
	if got != expected {
		return ErrIdentity
	}
	return nil
}

// ProposeIssued verifies the exact body derived at the caller's captured time.
// It preserves received authority and returns only a candidate for local saving.
// Core additionally rejects a target UpgradePending before entering this path.
func ProposeIssued(s Snapshot, remoteKey string, e Envelope, now time.Time, budget int) (Snapshot, error) {
	expected, err := PrepareExport(s, remoteKey, ExportOptions{e.Update.Operation, e.Update.Lifetime, e.Update.Expires}, now, budget)
	if err != nil {
		return Snapshot{}, err
	}
	return proposeIssued(s, remoteKey, e, expected, now, budget)
}

func proposeIssued(s Snapshot, remoteKey string, e Envelope, expected UpdateBody, now time.Time, budget int) (Snapshot, error) {
	if !activeSnapshotVersion(s.Version) {
		return Snapshot{}, ErrReview
	}
	if err := matchIssuedBody(e.Update, expected); err != nil {
		return Snapshot{}, err
	}
	i, err := activeRecordForKey(s, remoteKey)
	if err != nil {
		return Snapshot{}, err
	}
	if err := Inspect(e, *s.Peers[i].PairContext, remoteKey, now); err != nil {
		return Snapshot{}, err
	}
	next := cloneSnapshot(s)
	state := next.Peers[i].EndpointState
	state.IssuedVersion, state.IssuedHighwater, state.IssuedProof = 1, e.Update.Sequence, &e
	next.Revision, err = nextCounter(s.Revision)
	if err != nil {
		return Snapshot{}, err
	}
	next.Peers[i].Revision = next.Revision
	// Preserve the established model contract. Production requires absence of
	// this transcript, so issuing a proof cannot silently remove one there.
	next.Peers[i].UpgradePending = nil
	next.ObservedAt = now.UTC().Format(time.RFC3339Nano)
	if _, err := EncodeSnapshot(next, budget); err != nil {
		return Snapshot{}, err
	}
	return next, nil
}

// ExportModel saves the entire synthetic snapshot before returning newly issued
// bytes. An error returns no bytes and a recovery outcome, including when the
// replacement was published. The input recovery latch cannot be bypassed here.
func ExportModel(model SaveResolution, remoteKey string, u UpdateBody, key ed25519.PrivateKey, now time.Time, budget int, save SaveModel) (SaveResolution, []byte, error) {
	s := model.Snapshot
	if !activeSnapshotVersion(s.Version) {
		return model, nil, ErrReview
	}
	if model.Recovery || !model.Durable || s.PendingChange != nil {
		return model, nil, ErrRecovery
	}
	// Preserve the harness's nonallocating prospective-budget rejection and
	// missing-target error priority before hashing/deriving any body fields.
	i, err := activeRecordForKey(s, remoteKey)
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
	expected, err := prepareExportAt(s, remoteKey, ExportOptions{u.Operation, u.Lifetime, u.Expires}, u.Issued, now, budget)
	if err != nil {
		return model, nil, err
	}
	if err := matchIssuedBody(u, expected); err != nil {
		return model, nil, err
	}
	if save == nil {
		return model, nil, ErrInvalid
	}
	e, err := Sign(u, key)
	if err != nil {
		return model, nil, err
	}
	next, err := proposeIssued(s, remoteKey, e, expected, now, budget)
	if err != nil {
		return model, nil, err
	}
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
	if !activeSnapshotVersion(s.Version) {
		return nil, ErrReview
	}
	if model.Recovery || !model.Durable || s.PendingChange != nil {
		return nil, ErrRecovery
	}
	if err := s.Validate(); err != nil {
		return nil, err
	}
	i, err := activeRecordForKey(s, remoteKey)
	if err != nil {
		return nil, err
	}
	r := s.Peers[i]
	if r.EndpointState == nil || r.EndpointState.IssuedProof == nil || !r.ContextConfirmed {
		return nil, ErrReview
	}
	return Encode(*r.EndpointState.IssuedProof)
}
