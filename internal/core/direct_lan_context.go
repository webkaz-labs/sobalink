package core

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"reflect"
	"time"

	"github.com/webkaz-labs/sobalink/internal/directlan"
	"github.com/webkaz-labs/sobalink/internal/endpointmeta"
)

// This closed, private metadata boundary has no command, authentication,
// network, or startup caller. Transcript values are data, not verified evidence.
// Any later adapter must add its independently reviewed claim/response owner;
// neither an admission nor a local durable result permits a wire success.
type contextOperation uint8

const (
	contextPrepare contextOperation = iota + 1
	contextResume
	contextRecordInbound
	contextRecordOutbound
	contextCommit
	contextConfirmCommit
	contextConfirmStatus
	contextConfirmUpdate
	contextRepublish
)

// Exported field names support a canonical private digest only. The type is
// unexported and is never decoded from a management request or persisted.
type contextInputs struct {
	Operation    contextOperation
	PeerKey      string
	PeerRevision string
	OwnNonce     string
	Deadline     string
	Prepare      endpointmeta.PrepareRequest
	Bound        endpointmeta.BoundRequest
	Update       endpointmeta.Envelope
	Resume       endpointmeta.ContextResumeReview
}

// Completion data is deliberately separate from the earlier local admission.
// A future owner can arm inbound work before reading a request, or capture the
// exact original outbound request before waiting for its reply. These values
// alone authenticate nothing and are accepted by no production caller.
type contextTranscript struct {
	Prepare   endpointmeta.PrepareRequest
	Prepared  endpointmeta.PrepareReply
	Bound     endpointmeta.BoundRequest
	Committed endpointmeta.ContextReply
}

func (t contextTranscript) frozen(in contextInputs) (contextTranscript, error) {
	allowed := contextTranscript{}
	var err error
	switch in.Operation {
	case contextRecordInbound:
		allowed.Prepare = t.Prepare
		_, err = endpointmeta.Encode(t.Prepare)
	case contextRecordOutbound:
		allowed.Prepared = t.Prepared
		_, err = endpointmeta.Encode(t.Prepared)
	case contextCommit:
		allowed.Bound = t.Bound
		_, err = endpointmeta.Encode(t.Bound)
		if t.Bound != in.Bound {
			return contextTranscript{}, endpointmeta.ErrIdentity
		}
	case contextConfirmCommit, contextConfirmStatus:
		allowed.Committed = t.Committed
		_, err = endpointmeta.Encode(t.Committed)
	}
	if err != nil {
		return contextTranscript{}, err
	}
	if !reflect.DeepEqual(t, allowed) {
		return contextTranscript{}, endpointmeta.ErrInvalid
	}
	data, err := json.Marshal(t)
	if err != nil || len(data) > 4*endpointmeta.MaxFrameBytes {
		return contextTranscript{}, endpointmeta.ErrCapacity
	}
	var copy contextTranscript
	if err := json.Unmarshal(data, &copy); err != nil {
		return contextTranscript{}, err
	}
	return copy, nil
}

func (in contextInputs) frozen() (contextInputs, error) {
	// Bound scalars before JSON allocation. Wire variants use Encode's
	// nonallocating wire-size preflight before validation or deep copying.
	r := in.Resume
	if len(in.PeerKey) > 64 || len(in.PeerRevision) > 20 || len(in.OwnNonce) > 43 || len(in.Deadline) > 30 ||
		len(r.PeerKey) > 64 || len(r.PeerRevision) > 20 || len(r.ProposalDigest) > 64 ||
		len(r.OldDeadline) > 30 || len(r.NewDeadline) > 30 || len(r.Revision) > 64 {
		return contextInputs{}, endpointmeta.ErrCapacity
	}
	allowed := contextInputs{Operation: in.Operation, PeerKey: in.PeerKey}
	switch in.Operation {
	case contextPrepare:
		allowed.PeerRevision, allowed.OwnNonce, allowed.Deadline = in.PeerRevision, in.OwnNonce, in.Deadline
	case contextResume:
		allowed.Resume = in.Resume
		if in.Resume.PeerKey != in.PeerKey {
			return contextInputs{}, endpointmeta.ErrIdentity
		}
	case contextRecordInbound:
	case contextRecordOutbound:
		allowed.Prepare = in.Prepare
		if _, err := endpointmeta.Encode(in.Prepare); err != nil {
			return contextInputs{}, err
		}
	case contextCommit:
		allowed.Bound = in.Bound
		if in.Bound.Operation != "pair-context-commit" {
			return contextInputs{}, endpointmeta.ErrInvalid
		}
	case contextConfirmCommit, contextConfirmStatus:
		allowed.Bound = in.Bound
		operation := "pair-context-commit"
		if in.Operation == contextConfirmStatus {
			operation = "pair-context-status"
		}
		if in.Bound.Operation != operation {
			return contextInputs{}, endpointmeta.ErrInvalid
		}
	case contextConfirmUpdate:
		allowed.Update = in.Update
		if _, err := endpointmeta.Encode(in.Update); err != nil {
			return contextInputs{}, err
		}
	case contextRepublish:
	default:
		return contextInputs{}, endpointmeta.ErrInvalid
	}
	if in.PeerKey == "" || !reflect.DeepEqual(in, allowed) {
		return contextInputs{}, endpointmeta.ErrInvalid
	}
	if in.Operation == contextCommit || in.Operation == contextConfirmCommit || in.Operation == contextConfirmStatus {
		if _, err := endpointmeta.Encode(in.Bound); err != nil {
			return contextInputs{}, err
		}
	}
	data, err := json.Marshal(in)
	if err != nil || len(data) > 4*endpointmeta.MaxFrameBytes {
		return contextInputs{}, endpointmeta.ErrCapacity
	}
	var copy contextInputs
	if err := json.Unmarshal(data, &copy); err != nil {
		return contextInputs{}, err
	}
	return copy, nil
}

func contextPeer(m endpointmeta.Snapshot, key string) (int, error) {
	for i := range m.Peers {
		if m.Peers[i].Peer.Key == key {
			return i, nil
		}
	}
	return -1, endpointmeta.ErrIdentity
}

// Core.op and the existing exclusive profile lifecycle are prerequisites of
// these private helpers. Migration must have completed through its existing
// reviewed path first; a context review never silently migrates a legacy file.
func (c *Core) contextStoreLocked() (*directLANStore, string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.node != nil || c.attemptedNetwork != "" || c.closing || c.ctx == nil || c.ctx.Err() != nil || c.lanStartNonce == "" {
		return nil, "", directLANMetadataUnavailable()
	}
	if c.directLAN == nil {
		return nil, "", directLANEndpointContextRequired()
	}
	return c.directLAN, c.lanStartNonce, nil
}

func (c *Core) reviewContextLocked(in contextInputs) (contextAdmission, error) {
	s, process, err := c.contextStoreLocked()
	if err != nil {
		return contextAdmission{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.captureContextAdmissionLocked(process, in, time.Now())
}

func (c *Core) applyContextLocked(ctx context.Context, admission contextAdmission, transcript contextTranscript) (contextSaveResult, error) {
	s, process, err := c.contextStoreLocked()
	if err != nil {
		return contextSaveResult{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.applyContextTransitionLocked(ctx, process, admission, transcript, time.Now())
}

// A preparation's nonce is owner-generated once. An exact re-review reuses the
// saved or provisional value, including after an unpublished failed save.
func (s *directLANStore) contextPreparationInputsLocked(m endpointmeta.Snapshot, key, deadline string) (contextInputs, error) {
	i, err := contextPeer(m, key)
	if err != nil {
		return contextInputs{}, err
	}
	r := m.Peers[i]
	in := contextInputs{Operation: contextPrepare, PeerKey: key, PeerRevision: r.Revision, Deadline: deadline}
	if r.UpgradePending != nil {
		in.OwnNonce = r.UpgradePending.OwnNonce
	} else if w, ok := s.contextWindows[key]; ok {
		in.OwnNonce = w.nonce
	} else {
		var nonce [32]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return contextInputs{}, err
		}
		in.OwnNonce = base64.RawURLEncoding.EncodeToString(nonce[:])
	}
	return in, nil
}

// Only this dispatch selects the reducer. No caller supplies a Snapshot to a
// context publisher, and an inbound commit can never select confirmation.
func (s *directLANStore) reduceContextLocked(in contextInputs, transcript contextTranscript, m endpointmeta.Snapshot, now time.Time, budget int) (endpointmeta.ContextTransition, error) {
	window := s.contextWindowLocked(m, in.PeerKey)
	switch in.Operation {
	case contextPrepare:
		return endpointmeta.PrepareContextUpgrade(m, in.PeerKey, in.PeerRevision, in.OwnNonce, in.Deadline, now, budget)
	case contextResume:
		return endpointmeta.ResumePreparedContext(m, in.Resume, now, budget)
	case contextRecordInbound:
		return endpointmeta.RecordInboundContextPrepare(m, in.PeerKey, transcript.Prepare, window, now, budget)
	case contextRecordOutbound:
		return endpointmeta.RecordOutboundContextPrepare(m, in.PeerKey, in.Prepare, transcript.Prepared, window, now, budget)
	case contextCommit:
		if _, err := endpointmeta.Encode(in.Bound); err != nil {
			return endpointmeta.ContextTransition{}, err
		}
		return endpointmeta.CommitPreparedContext(m, in.PeerKey, in.Bound.PairBinding, window, now, budget)
	case contextConfirmCommit, contextConfirmStatus:
		return endpointmeta.ConfirmContextCommit(m, in.PeerKey, in.Bound, transcript.Committed, now, budget)
	case contextConfirmUpdate:
		// This remains the independently Inspect-checked proof route. It does
		// not call an endpoint receive reducer or consume a proof/high-water.
		return endpointmeta.ConfirmContextFromUpdate(m, in.PeerKey, in.Update, now, budget)
	case contextRepublish:
		i, err := contextPeer(m, in.PeerKey)
		if err != nil || m.Peers[i].PairContext == nil {
			return endpointmeta.ContextTransition{}, endpointmeta.ErrReview
		}
		return endpointmeta.ContextTransition{Snapshot: *cloneDirectLANMetadata(&m)}, nil
	}
	return endpointmeta.ContextTransition{}, directlan.ErrRecovery
}
