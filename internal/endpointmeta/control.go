package endpointmeta

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
)

// Request and Reply are closed sets of canonical values; these codecs do not
// dispatch handlers, authenticate a connection, or perform protocol negotiation.
type Request interface {
	wireValue
	request()
}
type Reply interface {
	wireValue
	reply()
}

type PairRequest struct {
	Version     int      `json:"version"`
	Operation   string   `json:"operation"`
	Token       string   `json:"token"`
	Peer        PeerWire `json:"peer"`
	JoinerNonce string   `json:"joiner_nonce"`
	JoinerScope Scope    `json:"joiner_scope"`
}

func (PairRequest) request() {}
func (r PairRequest) validate() error {
	if r.Version != 2 || r.Operation != "pair" || r.Peer.validate() != nil || !r.JoinerScope.Contains(r.Peer.Endpoint) {
		return ErrInvalid
	}
	if _, err := rawBytes(r.Token, 32); err != nil {
		return err
	}
	_, err := rawBytes(r.JoinerNonce, 32)
	return err
}

type PrepareRequest struct {
	Version            int    `json:"version"`
	Operation          string `json:"operation"`
	Sender             string `json:"sender"`
	Recipient          string `json:"recipient"`
	SenderTunnelKey    string `json:"sender_tunnel_key"`
	RecipientTunnelKey string `json:"recipient_tunnel_key"`
	SenderNonce        string `json:"sender_nonce"`
	SenderEndpoint     string `json:"sender_endpoint"`
	RecipientEndpoint  string `json:"recipient_endpoint"`
	SenderScope        Scope  `json:"sender_scope"`
}

func (PrepareRequest) request() {}
func (r PrepareRequest) validate() error {
	if r.Version != 2 || r.Operation != "pair-context-prepare" || !validHex(r.Sender) || !validHex(r.Recipient) || r.Sender == r.Recipient || !validTunnel(r.SenderTunnelKey) || !validTunnel(r.RecipientTunnelKey) || r.SenderTunnelKey == r.RecipientTunnelKey || !r.SenderScope.Contains(r.SenderEndpoint) || !r.SenderScope.Contains(r.RecipientEndpoint) {
		return ErrInvalid
	}
	_, err := rawBytes(r.SenderNonce, 32)
	return err
}

type BoundRequest struct {
	Version     int    `json:"version"`
	Operation   string `json:"operation"`
	PairBinding string `json:"pair_binding"`
}

func (BoundRequest) request() {}
func (r BoundRequest) validate() error {
	if r.Version != 2 || !validHex(r.PairBinding) {
		return ErrInvalid
	}
	switch r.Operation {
	case "pair-context-commit", "pair-context-status", "session":
		return nil
	}
	return ErrInvalid
}
func (Envelope) request() {}

type PairReply struct {
	Version     int         `json:"version"`
	Operation   string      `json:"operation"`
	OK          bool        `json:"ok"`
	Peer        PeerWire    `json:"peer"`
	PairContext PairContext `json:"pair_context"`
	PairBinding string      `json:"pair_binding"`
}

func (PairReply) reply() {}
func (r PairReply) validate() error {
	b, err := r.PairContext.Binding()
	if err != nil || r.Version != 2 || r.Operation != "pair" || !r.OK || r.Peer.validate() != nil || r.PairBinding != b || r.Peer.Key != r.PairContext.HostKey || r.Peer.TunnelKey != r.PairContext.HostTunnelKey || r.Peer.Endpoint != r.PairContext.HostEndpoint {
		return ErrInvalid
	}
	return nil
}

type PrepareReply struct {
	Version     int         `json:"version"`
	Operation   string      `json:"operation"`
	OK          bool        `json:"ok"`
	PairContext PairContext `json:"pair_context"`
	PairBinding string      `json:"pair_binding"`
}

func (PrepareReply) reply() {}
func (r PrepareReply) validate() error {
	b, err := r.PairContext.Binding()
	if err != nil || r.Version != 2 || r.Operation != "pair-context-prepare" || !r.OK || r.PairBinding != b || r.PairContext.HostKey >= r.PairContext.JoinerKey {
		return ErrInvalid
	}
	return nil
}

type ContextReply struct {
	Version     int    `json:"version"`
	Operation   string `json:"operation"`
	OK          bool   `json:"ok"`
	PairBinding string `json:"pair_binding"`
	State       string `json:"state"`
}

func (ContextReply) reply() {}
func (r ContextReply) validate() error {
	if r.Version != 2 || !r.OK || !validHex(r.PairBinding) {
		return ErrInvalid
	}
	if r.Operation == "pair-context-commit" && r.State == "committed" {
		return nil
	}
	if r.Operation == "pair-context-status" && (r.State == "prepared" || r.State == "committed") {
		return nil
	}
	return ErrInvalid
}

type SessionReply struct {
	Version     int    `json:"version"`
	Operation   string `json:"operation"`
	OK          bool   `json:"ok"`
	PairBinding string `json:"pair_binding"`
}

func (SessionReply) reply() {}
func (r SessionReply) validate() error {
	if r.Version != 2 || r.Operation != "session" || !r.OK || !validHex(r.PairBinding) {
		return ErrInvalid
	}
	return nil
}

type UpdateReply struct {
	Version      int    `json:"version"`
	Operation    string `json:"operation"`
	OK           bool   `json:"ok"`
	PairBinding  string `json:"pair_binding"`
	Sequence     string `json:"sequence"`
	UpdateDigest string `json:"update_digest"`
	Outcome      string `json:"outcome"`
}

func (UpdateReply) reply() {}
func (r UpdateReply) validate() error {
	if r.Version != 2 || r.Operation != "endpoint-update" || !r.OK || !validHex(r.PairBinding) || !validHex(r.UpdateDigest) {
		return ErrInvalid
	}
	if _, err := sequence(r.Sequence, false); err != nil {
		return err
	}
	switch r.Outcome {
	case "applied", "withdrawn", "accepted_inactive", "already_applied", "review_required", "saved_pending_activation":
		return nil
	}
	return ErrInvalid
}

type ErrorReply struct {
	Version   int    `json:"version"`
	Operation string `json:"operation"`
	OK        bool   `json:"ok"`
	Code      string `json:"code"`
}

func (ErrorReply) reply() {}
func (r ErrorReply) validate() error {
	if r.Version != 2 || r.OK || !operation(r.Operation) {
		return ErrInvalid
	}
	if r.Operation == "unknown" && r.Code != "unsupported_operation" {
		return ErrInvalid
	}
	switch r.Code {
	case "unsupported_operation", "pair_rejected", "pair_context_required", "context_review_required", "context_mismatch", "context_expired", "identity_mismatch", "policy_denied", "authority_inactive", "session_role", "stale_generation", "stale_sequence", "sequence_conflict", "invalid_update", "capacity", "recovery_required", "unavailable", "cancelled":
		return nil
	}
	return ErrInvalid
}

func operation(s string) bool {
	switch s {
	case "pair", "pair-context-prepare", "pair-context-commit", "pair-context-status", "session", "endpoint-update", "unknown":
		return true
	}
	return false
}

func ParseRequest(b []byte) (Request, error) {
	if len(b) > MaxFrameBytes {
		return nil, ErrCapacity
	}
	if bytes.HasPrefix(b, []byte(`{"update":`)) {
		var e Envelope
		if err := decode(b, &e); err != nil {
			return nil, err
		}
		return e, nil
	}
	var h struct {
		Operation string `json:"operation"`
	}
	if !bytes.HasPrefix(b, []byte(`{"version":2,"operation":`)) || json.Unmarshal(b, &h) != nil {
		return nil, ErrInvalid
	}
	var r Request
	switch h.Operation {
	case "pair":
		r = &PairRequest{}
	case "pair-context-prepare":
		r = &PrepareRequest{}
	case "pair-context-commit", "pair-context-status", "session":
		r = &BoundRequest{}
	default:
		return nil, ErrInvalid
	}
	if err := decode(b, r); err != nil {
		return nil, err
	}
	return r, nil
}

// ParseReply requires the fixed operation that the caller sent. Matching the
// echoed context/binding and authenticated connection remains a separate check.
func ParseReply(b []byte, expectedOperation string) (Reply, error) {
	if len(b) > MaxFrameBytes {
		return nil, ErrCapacity
	}
	if !operation(expectedOperation) || expectedOperation == "unknown" {
		return nil, ErrInvalid
	}
	var h struct {
		Operation string `json:"operation"`
		OK        bool   `json:"ok"`
	}
	if json.Unmarshal(b, &h) != nil || h.Operation != expectedOperation {
		return nil, ErrInvalid
	}
	var r Reply
	if !h.OK {
		r = &ErrorReply{}
	} else {
		switch expectedOperation {
		case "pair":
			r = &PairReply{}
		case "pair-context-prepare":
			r = &PrepareReply{}
		case "pair-context-commit", "pair-context-status":
			r = &ContextReply{}
		case "session":
			r = &SessionReply{}
		case "endpoint-update":
			r = &UpdateReply{}
		}
	}
	if err := decode(b, r); err != nil {
		return nil, err
	}
	return r, nil
}

// MatchPairTranscript verifies all context fields against the exact invitation
// and join request. It does not authenticate either party or commit pairing.
func MatchPairTranscript(i Invitation, request PairRequest, reply PairReply) error {
	if i.validate() != nil || request.validate() != nil || reply.validate() != nil {
		return ErrInvalid
	}
	p := reply.PairContext
	if request.Token != i.Token || request.Peer.Key != i.RecipientKey || reply.Peer != i.Host || p.HostKey != i.Host.Key || p.HostTunnelKey != i.Host.TunnelKey || p.HostEndpoint != i.Host.Endpoint || p.HostNonce != i.HostNonce || p.JoinerKey != request.Peer.Key || p.JoinerTunnelKey != request.Peer.TunnelKey || p.JoinerEndpoint != request.Peer.Endpoint || p.JoinerNonce != request.JoinerNonce {
		return ErrIdentity
	}
	hs, _ := Encode(p.HostScope)
	ihs, _ := Encode(i.HostScope)
	js, _ := Encode(p.JoinerScope)
	rjs, _ := Encode(request.JoinerScope)
	if !bytes.Equal(hs, ihs) || !bytes.Equal(js, rjs) {
		return ErrIdentity
	}
	return nil
}

// Frame returns a bounded in-memory frame. ReadFrame rejects a second frame or
// trailing bytes; no stream reader, socket, or request dispatch is provided.
func Frame(v wireValue) ([]byte, error) {
	b, err := Encode(v)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 4+len(b))
	binary.BigEndian.PutUint32(out, uint32(len(b)))
	copy(out[4:], b)
	return out, nil
}

func ReadFrame(b []byte) ([]byte, error) {
	if len(b) < 4 {
		return nil, ErrInvalid
	}
	n := binary.BigEndian.Uint32(b[:4])
	if n > MaxFrameBytes {
		return nil, ErrCapacity
	}
	if n == 0 || uint64(n)+4 != uint64(len(b)) {
		return nil, ErrInvalid
	}
	return bytes.Clone(b[4:]), nil
}
