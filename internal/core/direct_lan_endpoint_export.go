package core

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"time"

	"github.com/webkaz-labs/sobalink/internal/directlan"
	"github.com/webkaz-labs/sobalink/internal/endpointmeta"
)

// No identity, sequence, context, endpoint or scope can be supplied by a local
// caller. Only the intended peer and explicit outgoing lifetime are choices.
type directLANEndpointExportInput struct {
	PeerID           string `json:"peerId"`
	Operation        string `json:"operation,omitempty"`
	Lifetime         string `json:"lifetime,omitempty"`
	Expires          string `json:"expires,omitempty"`
	ExpectedRevision string `json:"expectedRevision,omitempty"`
}

type directLANEndpointExportReview struct {
	Revision               string             `json:"revision"`
	PeerID                 string             `json:"peerId"`
	PairBinding            string             `json:"pairBinding"`
	Operation              string             `json:"operation"`
	Sequence               string             `json:"sequence"`
	Endpoint               string             `json:"endpoint"`
	PriorEndpoint          string             `json:"priorEndpoint"`
	RecipientScope         endpointmeta.Scope `json:"recipientScope"`
	ScopeDigest            string             `json:"scopeDigest"`
	Issued                 string             `json:"issued,omitempty"`
	Lifetime               string             `json:"lifetime"`
	Expires                string             `json:"expires"`
	ProofDigest            string             `json:"proofDigest,omitempty"`
	Reexport               bool               `json:"reexport"`
	EndpointUpdatesEnabled bool               `json:"endpointUpdatesEnabled"`
}

type directLANEndpointExportResult struct {
	Saved                  bool   `json:"saved"`
	Reexport               bool   `json:"reexport"`
	StoreRevision          string `json:"storeRevision"`
	Sequence               string `json:"sequence"`
	ProofDigest            string `json:"proofDigest"`
	Update                 string `json:"update"`
	EndpointUpdatesEnabled bool   `json:"endpointUpdatesEnabled"`
}

func decodeDirectLANEndpointExportInput(raw json.RawMessage) (directLANEndpointExportInput, error) {
	var input directLANEndpointExportInput
	if len(raw) > endpointmeta.MaxFrameBytes {
		return input, endpointmeta.ErrCapacity
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&input) != nil || d.Decode(new(any)) != io.EOF {
		return input, endpointmeta.ErrInvalid
	}
	return input, nil
}

// Core.op and the profile lifecycle are held by executeCommand. This command
// never constructs a Node, signs during preview, or enables managed transport.
func (c *Core) directLANEndpointExportCommand(ctx context.Context, name string, raw json.RawMessage) (any, error) {
	input, err := decodeDirectLANEndpointExportInput(raw)
	if err != nil {
		return nil, directLANEndpointError(err)
	}
	preview := name == "direct-lan.endpoint.export.preview" || name == "direct-lan.endpoint.reexport.preview"
	reexport := name == "direct-lan.endpoint.reexport" || name == "direct-lan.endpoint.reexport.preview"
	if !preview && name != "direct-lan.endpoint.export" && name != "direct-lan.endpoint.reexport" {
		return nil, directLANEndpointError(endpointmeta.ErrInvalid)
	}
	if reexport && (input.Operation != "" || input.Lifetime != "" || input.Expires != "") {
		return nil, directLANEndpointError(endpointmeta.ErrInvalid)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.mu.RLock()
	stopped := c.node == nil && c.attemptedNetwork == "" && !c.closing && c.ctx.Err() == nil
	process, store := c.lanStartNonce, c.directLAN
	c.mu.RUnlock()
	if !stopped {
		return nil, &lanCommandError{"network_restart_required", "stop soba and start with --offline before exporting saved endpoint proofs; live endpoint following is not enabled"}
	}
	if store == nil {
		return nil, &lanCommandError{"direct_lan_setup_required", "configure direct LAN before exporting a saved endpoint proof"}
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	result, err := store.endpointExportLocked(ctx, process, input, preview, reexport, time.Now())
	return result, directLANEndpointError(err)
}

func (s *directLANStore) endpointExportLocked(ctx context.Context, process string, input directLANEndpointExportInput, preview, reexport bool, now time.Time, publishers ...func(endpointmeta.Snapshot) (endpointmeta.SaveResolution, error)) (any, error) {
	m, err := s.endpointModelLocked(now, false)
	if err != nil {
		return nil, err
	}
	r, err := endpointRecord(*m, input.PeerID)
	if err != nil {
		return nil, err
	}
	// Issuing increments the peer revision. It cannot clear a pending context
	// transcript or relabel its captured revision to make validation pass.
	if r.UpgradePending != nil {
		return nil, directLANEndpointContextRequired()
	}
	if s.reviewRevision == ^uint64(0) {
		return nil, endpointmeta.ErrCapacity
	}
	budget, err := s.endpointBudgetLocked()
	if err != nil {
		return nil, err
	}
	options := endpointmeta.ExportOptions{Operation: input.Operation, Lifetime: input.Lifetime, Expires: input.Expires}
	var body endpointmeta.UpdateBody
	var envelope endpointmeta.Envelope
	if reexport {
		if r.EndpointState.IssuedProof == nil {
			return nil, endpointmeta.ErrReview
		}
		envelope = *r.EndpointState.IssuedProof
		body = envelope.Update
	} else {
		body, err = endpointmeta.PrepareExport(*m, input.PeerID, options, now, budget)
		if err != nil {
			return nil, err
		}
	}
	scope := r.PairContext.JoinerScope
	if m.LocalPeer.Key == r.PairContext.JoinerKey {
		scope = r.PairContext.HostScope
	}
	review := directLANEndpointExportReview{PeerID: input.PeerID, PairBinding: body.PairBinding, Operation: body.Operation,
		Sequence: body.Sequence, Endpoint: body.Endpoint, PriorEndpoint: body.PriorEndpoint, RecipientScope: scope,
		ScopeDigest: body.ScopeDigest, Lifetime: body.Lifetime, Expires: body.Expires, Reexport: reexport}
	if reexport {
		review.Issued = body.Issued
		review.ProofDigest, _ = envelope.Digest()
	}
	expected := input.ExpectedRevision
	input.ExpectedRevision = ""
	// New issuance captures its Issued time only at apply. Review still binds
	// exact outgoing intent and every derived state field, including sequence.
	review.Revision = privateRevision(struct {
		Purpose, Process, FileDigest string
		StoreRevision                uint64
		State                        directLANState
		Input                        directLANEndpointExportInput
		Review                       directLANEndpointExportReview
	}{"direct-lan-endpoint-export-v1", process, s.fileDigest, s.reviewRevision, s.state, input, review})
	if preview {
		return review, nil
	}
	if expected == "" || expected != review.Revision {
		return nil, directLANEndpointReviewChanged()
	}
	issueTime := time.Now()
	if err := s.observeEndpointTimeLocked(issueTime); err != nil {
		return nil, err
	}
	if err := m.ValidateAt(issueTime); err != nil {
		s.recovery = true
		return nil, directlan.ErrRecovery
	}
	next := *m
	var deadline time.Time
	if !reexport {
		body, err = endpointmeta.PrepareExport(*m, input.PeerID, options, issueTime, budget)
		if err != nil {
			return nil, err
		}
		envelope, err = s.state.Identity.SignEndpointUpdate(body)
		if err != nil {
			return nil, err
		}
		next, err = endpointmeta.ProposeIssued(*m, input.PeerID, envelope, issueTime, budget)
		if err != nil {
			return nil, err
		}
		if body.Lifetime == "finite" {
			absolute, _ := time.Parse(time.RFC3339Nano, body.Expires)
			deadline = issueTime.Add(absolute.Sub(issueTime.UTC()))
		}
	}
	if err := s.preflightEndpointSnapshotLocked(next, false); err != nil {
		return nil, err
	}
	// Encoding remains private until a confirmed save. Re-export signs nothing
	// and republishes the exact unchanged snapshot/proof, including ObservedAt.
	text, err := envelope.Text()
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	publicationTime := time.Now()
	if err := s.observeEndpointTimeLocked(publicationTime); err != nil {
		return nil, err
	}
	if !reexport {
		if !deadline.IsZero() && !publicationTime.Before(deadline) {
			return nil, endpointmeta.ErrExpired
		}
		if err := endpointmeta.Inspect(envelope, *r.PairContext, input.PeerID, publicationTime); err != nil {
			return nil, err
		}
	}
	s.preserveIssuedEndpointCandidateLocked(next, issueTime)
	var saved endpointmeta.SaveResolution
	if len(publishers) == 0 {
		saved, err = s.saveEndpointSnapshotLocked(next)
	} else if len(publishers) == 1 && publishers[0] != nil {
		saved, err = publishers[0](next)
	} else {
		err = endpointmeta.ErrReview
	}
	s.pruneIssuedEndpointDeadlinesLocked(time.Now())
	if err != nil {
		return nil, err
	}
	if !saved.Durable {
		return nil, directlan.ErrRecovery
	}
	completed := time.Now()
	if err := s.observeEndpointTimeLocked(completed); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !reexport {
		if !deadline.IsZero() && !completed.Before(deadline) {
			return nil, endpointmeta.ErrExpired
		}
		if err := endpointmeta.Inspect(envelope, *r.PairContext, input.PeerID, completed); err != nil {
			return nil, err
		}
	}
	digest, _ := envelope.Digest()
	return directLANEndpointExportResult{Saved: true, Reexport: reexport, StoreRevision: saved.Snapshot.Revision,
		Sequence: envelope.Update.Sequence, ProofDigest: digest, Update: text}, nil
}
