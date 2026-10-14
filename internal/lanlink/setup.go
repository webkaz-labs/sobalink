package lanlink

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/netip"
	"time"
	"unicode"
)

// Connection setup is a transport-independent, short-lived exchange. None of
// these types or functions opens a socket, saves state, or grants local consent.
const (
	SetupVersion            = 1
	SetupRequestVersion     = 2
	MaxSetupRequestLifetime = 20 * time.Minute
	MaxSetupLifetime        = 120 * time.Second
	MaxSetupPayload         = 16 << 10

	SetupActionHelloProof       = "hello-proof"
	SetupActionApprove          = "approve"
	SetupActionReject           = "reject"
	SetupActionInvitation       = "invitation"
	SetupActionPaired           = "paired"
	SetupActionCancel           = "cancel"
	SetupActionConnectedReceipt = "connected-receipt"

	setupHelloDomain      = "sobalink connection setup hello v1"
	setupHelloProofDomain = "sobalink connection setup hello proof v1"
	setupPlanDomain       = "sobalink connection setup plan v1"
	setupMessageDomain    = "sobalink connection setup message v1"
)

var ErrSetup = errors.New("connection setup is invalid, changed, expired or belongs to another session")

// SetupScope binds the selected numeric scope and an opaque local-consent
// revision. Interface indexes are local implementation details and never appear
// on the wire. The caller owns that private binding and must revalidate actual
// interface ownership and availability before activating an allowed bind.
type SetupScope struct {
	LocalAddress    netip.Addr   `json:"local_address"`
	Prefix          netip.Prefix `json:"prefix"`
	ConsentRevision string       `json:"consent_revision"`
}

func (s SetupScope) Validate() error {
	a, p := s.LocalAddress, s.Prefix
	if !validKey(s.ConsentRevision) || !setupLocalAddress(a) || !p.IsValid() || p != p.Masked() || p.Bits() == 0 || p.Addr().Is4In6() || p.Addr().Is4() != a.Is4() || !p.Contains(a) {
		return ErrSetup
	}
	last := prefixLast(p)
	if !(p.Addr().IsPrivate() && last.IsPrivate()) && !(p.Addr().IsLoopback() && last.IsLoopback()) {
		return ErrSetup
	}
	return nil
}

func setupLocalAddress(a netip.Addr) bool {
	return a.IsValid() && !a.Is4In6() && a.Zone() == "" && !a.IsUnspecified() && !a.IsMulticast() && (a.IsPrivate() || a.IsLoopback())
}

// SetupParticipant is public metadata for one explicit, expiring local session.
// Alias is user-chosen display text, never identity. Relay is an exact candidate
// offered by this participant; it does not authorize hosting or relay use.
type SetupParticipant struct {
	Protocol     int    `json:"protocol,omitempty"`
	Mode         string `json:"mode,omitempty"`
	Verification string `json:"verification,omitempty"`

	SessionID    string         `json:"session_id"`
	Key          string         `json:"key"`
	Alias        string         `json:"alias"`
	Bootstrap    netip.AddrPort `json:"bootstrap"`
	Scope        SetupScope     `json:"scope"`
	AllowHost    bool           `json:"allow_host"`
	ExistingHost bool           `json:"existing_host"`
	Relay        *TrustedRelay  `json:"relay"`
	Expires      time.Time      `json:"expires"`
}

// NewSetupSessionID supplies 256 random bits. Reuse is never a restart or retry
// mechanism: a new setup owner must create a fresh ID and obtain fresh consent.
func NewSetupSessionID() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func (p SetupParticipant) validate() error {
	if p.Protocol == 0 {
		if p.Mode != "" || p.Verification != "" {
			return ErrSetup
		}
	} else if p.Protocol != SetupRequestVersion || (p.Mode != "request" && p.Mode != "receive" && p.Mode != "outbound") || (p.Verification != "tofu" && p.Verification != "full-sha256") {
		return ErrSetup
	}
	if p.Mode == "outbound" && (p.Bootstrap.Port() != 0 || p.AllowHost || p.ExistingHost || p.Relay != nil) {
		return ErrSetup
	}
	if p.Protocol == SetupRequestVersion && p.Mode != "receive" && p.AllowHost {
		return ErrSetup
	}

	if !validKey(p.SessionID) || !validSetupAlias(p.Key, p.Alias) || p.Scope.Validate() != nil || p.Expires.IsZero() || !p.Bootstrap.IsValid() || (!setupPort(p.Bootstrap.Port()) && !(p.Protocol == SetupRequestVersion && p.Mode == "outbound" && p.Bootstrap.Port() == 0)) || p.Bootstrap.Addr() != p.Scope.LocalAddress {
		return ErrSetup
	}
	if p.Relay != nil && (p.Relay.Validate() != nil || !setupPort(p.Relay.Address.Port()) || p.Relay.Address.Addr() != p.Scope.LocalAddress || p.Relay.Address == p.Bootstrap) {
		return ErrSetup
	}
	return nil
}

func validSetupAlias(publicKey, alias string) bool {
	if !validPeer(Peer{Key: publicKey, Name: alias}) {
		return false
	}
	// Match the public device-card display boundary without an import cycle.
	for _, r := range alias {
		if unicode.Is(unicode.Cf, r) || r == '\u2028' || r == '\u2029' {
			return false
		}
	}
	return true
}

func setupPort(port uint16) bool {
	return port >= 1024 && (port < 54543 || port > 54545)
}

func (p SetupParticipant) Validate(now time.Time) error {
	if p.validate() != nil || now.IsZero() || !p.Expires.After(now) || p.Expires.After(now.Add(p.maxLifetime())) {
		return ErrSetup
	}
	return nil
}

func (p SetupParticipant) maxLifetime() time.Duration {
	if p.Protocol == SetupRequestVersion {
		return MaxSetupRequestLifetime
	}
	return MaxSetupLifetime
}

func setupTranscriptVersion(participants [2]SetupParticipant) int {
	if participants[0].Protocol == SetupRequestVersion {
		return SetupRequestVersion
	}
	return SetupVersion
}
func setupDomain(v int, v1 string) string {
	if v == SetupRequestVersion {
		return v1[:len(v1)-1] + "2"
	}
	return v1
}

func cloneSetupParticipant(p SetupParticipant) SetupParticipant {
	p.Expires = p.Expires.UTC()
	if p.Relay != nil {
		r := *p.Relay
		p.Relay = &r
	}
	return p
}

func validateSetupParticipants(participants [2]SetupParticipant) error {
	a, b := participants[0], participants[1]
	if a.validate() != nil || b.validate() != nil || a.Key >= b.Key || a.SessionID == b.SessionID || a.Protocol != b.Protocol {
		return ErrSetup
	}
	if a.Protocol == SetupRequestVersion && (a.Verification != b.Verification || (a.Mode == "receive") == (b.Mode == "receive")) {
		return ErrSetup
	}
	for _, participant := range participants {
		if !participant.Scope.Prefix.Contains(a.Bootstrap.Addr()) || !participant.Scope.Prefix.Contains(b.Bootstrap.Addr()) {
			return ErrSetup
		}
	}
	return nil
}

// SetupHello authenticates the selected participants before a host role can be
// elected. It binds host offers but does not select or authorize one of them.
type SetupHello struct {
	Version      int                 `json:"version"`
	Domain       string              `json:"domain"`
	Backend      string              `json:"backend"`
	Participants [2]SetupParticipant `json:"participants"`
}

func NewSetupHello(a, b SetupParticipant) (SetupHello, error) {
	a, b = cloneSetupParticipant(a), cloneSetupParticipant(b)
	if a.Key > b.Key {
		a, b = b, a
	}
	v := setupTranscriptVersion([2]SetupParticipant{a, b})
	h := SetupHello{Version: v, Domain: setupDomain(v, setupHelloDomain), Backend: "lan", Participants: [2]SetupParticipant{a, b}}
	if h.validate() != nil {
		return SetupHello{}, ErrSetup
	}
	return h, nil
}

func (h SetupHello) validate() error {
	if h.Version != setupTranscriptVersion(h.Participants) || h.Domain != setupDomain(h.Version, setupHelloDomain) || h.Backend != "lan" || validateSetupParticipants(h.Participants) != nil {
		return ErrSetup
	}
	return nil
}

func (h SetupHello) Validate(now time.Time) error {
	if h.validate() != nil || h.Participants[0].Validate(now) != nil || h.Participants[1].Validate(now) != nil {
		return ErrSetup
	}
	return nil
}

func (h SetupHello) Canonical() ([]byte, error) {
	if h.validate() != nil {
		return nil, ErrSetup
	}
	for i := range h.Participants {
		h.Participants[i] = cloneSetupParticipant(h.Participants[i])
	}
	raw, err := json.Marshal(h)
	if err != nil || len(raw) > maxPairPlaintext {
		return nil, ErrSetup
	}
	return raw, nil
}

func (h SetupHello) Fingerprint() (string, error) {
	raw, err := h.Canonical()
	if err != nil {
		return "", err
	}
	return digest(raw), nil
}

type setupHelloProof struct {
	Version            int       `json:"version"`
	Domain             string    `json:"domain"`
	Action             string    `json:"action"`
	Sender             string    `json:"sender"`
	Recipient          string    `json:"recipient"`
	SenderSessionID    string    `json:"sender_session_id"`
	RecipientSessionID string    `json:"recipient_session_id"`
	Fingerprint        string    `json:"fingerprint"`
	Expires            time.Time `json:"expires"`
}

func setupHelloBinding(identity Identity, hello SetupHello, senderKey string, now time.Time) (setupHelloProof, error) {
	if identity.Validate() != nil || hello.Validate(now) != nil {
		return setupHelloProof{}, ErrSetup
	}
	sender, recipient := hello.Participants[0], hello.Participants[1]
	if sender.Key != senderKey {
		sender, recipient = recipient, sender
	}
	if sender.Key != senderKey || identity.PublicKey() != sender.Key && identity.PublicKey() != recipient.Key {
		return setupHelloProof{}, ErrSetup
	}
	fingerprint, err := hello.Fingerprint()
	if err != nil {
		return setupHelloProof{}, ErrSetup
	}
	expires := sender.Expires
	if recipient.Expires.Before(expires) {
		expires = recipient.Expires
	}
	return setupHelloProof{
		Version: hello.Version, Domain: setupDomain(hello.Version, setupHelloProofDomain), Action: SetupActionHelloProof,
		Sender: sender.Key, Recipient: recipient.Key,
		SenderSessionID: sender.SessionID, RecipientSessionID: recipient.SessionID,
		Fingerprint: fingerprint, Expires: expires.UTC(),
	}, nil
}

func SealSetupHello(identity Identity, hello SetupHello, now time.Time) ([]byte, error) {
	if identity.Validate() != nil {
		return nil, ErrSetup
	}
	m, err := setupHelloBinding(identity, hello, identity.PublicKey(), now)
	if err != nil {
		return nil, err
	}
	frame, _, err := sealMessage(identity, m.Recipient, m)
	if err != nil {
		return nil, ErrSetup
	}
	return frame, nil
}

// OpenSetupHello proves possession of the selected peer key for this exact
// pair of live sessions. It does not approve a host, a plan, or a local action.
func OpenSetupHello(identity Identity, hello SetupHello, expectedSender string, frame []byte, now time.Time) error {
	want, err := setupHelloBinding(identity, hello, expectedSender, now)
	if err != nil || want.Recipient != identity.PublicKey() || len(frame) == 0 || len(frame) > maxPairMessage {
		return ErrSetup
	}
	var m setupHelloProof
	plain, err := openMessage(identity, frame, expectedSender, &m)
	if err != nil {
		return ErrSetup
	}
	// Comparing the complete expected encoding also rejects duplicate keys,
	// case aliases, missing fields and alternate timestamp encodings.
	canonical, err := json.Marshal(want)
	if err != nil || !bytes.Equal(plain, canonical) {
		return ErrSetup
	}
	return nil
}

// SetupPlan is the complete reviewed transcript. Participants are ordered by
// their full public keys. Revision belongs to the caller's current session;
// validation alone neither remembers earlier revisions nor prevents replay.
type SetupPlan struct {
	RequestID    string `json:"request_id,omitempty"`
	Verification string `json:"verification,omitempty"`

	Version      int                 `json:"version"`
	Domain       string              `json:"domain"`
	Backend      string              `json:"backend"`
	Participants [2]SetupParticipant `json:"participants"`
	Revision     uint64              `json:"revision"`
	Issued       time.Time           `json:"issued"`
	Expires      time.Time           `json:"expires"`
	HostKey      string              `json:"host_key"`
	Relay        TrustedRelay        `json:"relay"`
}

func NewSetupPlan(a, b SetupParticipant, revision uint64, hostKey string, relay TrustedRelay, issued, expires time.Time) (SetupPlan, error) {
	a, b = cloneSetupParticipant(a), cloneSetupParticipant(b)
	if a.Key > b.Key {
		a, b = b, a
	}
	v := setupTranscriptVersion([2]SetupParticipant{a, b})
	p := SetupPlan{
		Version: v, Domain: setupDomain(v, setupPlanDomain), Backend: "lan",
		Participants: [2]SetupParticipant{a, b}, Revision: revision,
		Issued: issued.UTC(), Expires: expires.UTC(), HostKey: hostKey, Relay: relay,
	}
	if v == SetupRequestVersion {
		p.Verification = a.Verification
		requester := a
		if requester.Mode == "receive" {
			requester = b
		}
		p.RequestID = requester.SessionID
	}
	if p.Validate(issued) != nil {
		return SetupPlan{}, ErrSetup
	}
	return p, nil
}

func (p SetupPlan) validate() error {
	a, b := p.Participants[0], p.Participants[1]
	if p.Version != setupTranscriptVersion(p.Participants) || p.Domain != setupDomain(p.Version, setupPlanDomain) || p.Backend != "lan" || p.Revision == 0 || validateSetupParticipants(p.Participants) != nil {
		return ErrSetup
	}
	if !finiteRouteLifetime(p.Issued, p.Expires) || p.Expires.Sub(p.Issued) > a.maxLifetime() || p.Expires.After(a.Expires) || p.Expires.After(b.Expires) || p.Relay.Validate() != nil {
		return ErrSetup
	}
	if p.Version == SetupVersion && (p.RequestID != "" || p.Verification != "") {
		return ErrSetup
	}
	if p.Version == SetupRequestVersion {
		requester := a
		if requester.Mode == "receive" {
			requester = b
		}
		if p.RequestID != requester.SessionID || p.Verification != a.Verification {
			return ErrSetup
		}
	}
	host, ok := p.Participant(p.HostKey)
	if !ok || !host.AllowHost || host.Relay == nil || *host.Relay != p.Relay || p.Version == SetupRequestVersion && host.Mode != "receive" {
		return ErrSetup
	}
	// Neither participant's explicitly selected prefix is expanded by a plan.
	for _, participant := range p.Participants {
		if !participant.Scope.Prefix.Contains(a.Bootstrap.Addr()) || !participant.Scope.Prefix.Contains(b.Bootstrap.Addr()) || !participant.Scope.Prefix.Contains(p.Relay.Address.Addr()) {
			return ErrSetup
		}
	}
	return nil
}

func (p SetupPlan) Validate(now time.Time) error {
	if p.validate() != nil || now.IsZero() || p.Issued.After(now) || !p.Expires.After(now) || p.Participants[0].Validate(now) != nil || p.Participants[1].Validate(now) != nil {
		return ErrSetup
	}
	return nil
}

// Participant returns an independent copy; changing it cannot edit the plan.
func (p SetupPlan) Participant(publicKey string) (SetupParticipant, bool) {
	for _, participant := range p.Participants {
		if participant.Key == publicKey {
			return cloneSetupParticipant(participant), true
		}
	}
	return SetupParticipant{}, false
}

// Canonical emits ordered JSON with UTC times. It validates structure but not
// current liveness, so callers must still Validate with their current time.
func (p SetupPlan) Canonical() ([]byte, error) {
	if p.validate() != nil {
		return nil, ErrSetup
	}
	for i := range p.Participants {
		p.Participants[i] = cloneSetupParticipant(p.Participants[i])
	}
	p.Issued, p.Expires = p.Issued.UTC(), p.Expires.UTC()
	raw, err := json.Marshal(p)
	if err != nil || len(raw) > maxPairPlaintext {
		return nil, ErrSetup
	}
	return raw, nil
}

// Fingerprint is the full 256-bit SHA-256 of the canonical transcript, never a
// short authentication string. Both users must compare the complete fingerprint
// out of band, or scan the complete value in a trusted QR workflow in v1 or
// v2 full-sha256 mode. v2 tofu is explicitly weaker: this digest binds a plan,
// and does not establish independent physical-peer authentication. An in-band
// echo proves no independent agreement and must never grant local approval.
func (p SetupPlan) Fingerprint() (string, error) {
	raw, err := p.Canonical()
	if err != nil {
		return "", err
	}
	return digest(raw), nil
}

type setupMessage struct {
	Version            int       `json:"version"`
	Domain             string    `json:"domain"`
	Action             string    `json:"action"`
	Sender             string    `json:"sender"`
	Recipient          string    `json:"recipient"`
	SenderSessionID    string    `json:"sender_session_id"`
	RecipientSessionID string    `json:"recipient_session_id"`
	Revision           uint64    `json:"revision"`
	Fingerprint        string    `json:"fingerprint"`
	Expires            time.Time `json:"expires"`
	Payload            []byte    `json:"payload"`
}

func validSetupPayload(action string, payload []byte) bool {
	switch action {
	case SetupActionInvitation:
		return len(payload) > 0 && len(payload) <= MaxSetupPayload
	case SetupActionHelloProof, SetupActionApprove, SetupActionPaired, SetupActionCancel, SetupActionConnectedReceipt, SetupActionReject:
		return len(payload) == 0
	default:
		return false
	}
}

func setupMessageBinding(identity Identity, plan SetupPlan, senderKey, action string, now time.Time) (setupMessage, error) {
	if identity.Validate() != nil || plan.Validate(now) != nil || action == SetupActionReject && plan.Version != SetupRequestVersion || (!validSetupPayload(action, nil) && action != SetupActionInvitation) {
		return setupMessage{}, ErrSetup
	}
	sender, ok := plan.Participant(senderKey)
	if !ok {
		return setupMessage{}, ErrSetup
	}
	recipient := plan.Participants[0]
	if recipient.Key == sender.Key {
		recipient = plan.Participants[1]
	}
	if own := identity.PublicKey(); own != sender.Key && own != recipient.Key {
		return setupMessage{}, ErrSetup
	}
	fingerprint, err := plan.Fingerprint()
	if err != nil {
		return setupMessage{}, ErrSetup
	}
	return setupMessage{
		Version: plan.Version, Domain: setupDomain(plan.Version, setupMessageDomain), Action: action,
		Sender: sender.Key, Recipient: recipient.Key,
		SenderSessionID: sender.SessionID, RecipientSessionID: recipient.SessionID,
		Revision: plan.Revision, Fingerprint: fingerprint, Expires: plan.Expires.UTC(),
	}, nil
}

// SealSetupMessage authenticates metadata to the other plan participant using
// the existing pairing box primitive. It does not attest that this action was
// locally approved; the session owner must enforce its own consent and phases.
func SealSetupMessage(identity Identity, plan SetupPlan, action string, payload []byte, now time.Time) ([]byte, error) {
	if identity.Validate() != nil || !validSetupPayload(action, payload) {
		return nil, ErrSetup
	}
	m, err := setupMessageBinding(identity, plan, identity.PublicKey(), action, now)
	if err != nil {
		return nil, err
	}
	if len(payload) > 0 {
		m.Payload = bytes.Clone(payload)
	}
	frame, _, err := sealMessage(identity, m.Recipient, m)
	if err != nil {
		return nil, ErrSetup
	}
	return frame, nil
}

// OpenSetupMessage accepts only the caller's exact current plan, remote sender
// and expected action. Returned bytes are authenticated peer data, not local
// consent, pairing success, or permission to start a listener. The caller owns
// phase/idempotency checks and must reject stale sessions even after rollback.
func OpenSetupMessage(identity Identity, plan SetupPlan, expectedSender, expectedAction string, frame []byte, now time.Time) ([]byte, error) {
	want, err := setupMessageBinding(identity, plan, expectedSender, expectedAction, now)
	if err != nil || want.Recipient != identity.PublicKey() || len(frame) == 0 || len(frame) > maxPairMessage {
		return nil, ErrSetup
	}
	var m setupMessage
	plain, err := openMessage(identity, frame, expectedSender, &m)
	if err != nil || !validSetupPayload(expectedAction, m.Payload) {
		return nil, ErrSetup
	}
	if len(m.Payload) > 0 {
		want.Payload = m.Payload
	}
	// Bind every header field to the locally expected transcript in its exact
	// encoding; received data supplies only the separately bounded payload.
	canonical, err := json.Marshal(want)
	if err != nil || !bytes.Equal(plain, canonical) {
		return nil, ErrSetup
	}
	return bytes.Clone(m.Payload), nil
}

// A v2 pending cancellation is bound to both complete participants even before
// a relay plan exists. It can discard an exact request, never create consent.
func SealSetupCancellation(identity Identity, hello SetupHello, now time.Time) ([]byte, error) {
	if hello.Version != SetupRequestVersion {
		return nil, ErrSetup
	}
	m, err := setupHelloBinding(identity, hello, identity.PublicKey(), now)
	if err != nil {
		return nil, err
	}
	m.Action = SetupActionCancel
	frame, _, err := sealMessage(identity, m.Recipient, m)
	if err != nil {
		return nil, ErrSetup
	}
	return frame, nil
}
func OpenSetupCancellation(identity Identity, hello SetupHello, expectedSender string, frame []byte, now time.Time) error {
	if hello.Version != SetupRequestVersion {
		return ErrSetup
	}
	want, err := setupHelloBinding(identity, hello, expectedSender, now)
	if err != nil || want.Recipient != identity.PublicKey() || len(frame) == 0 || len(frame) > maxPairMessage {
		return ErrSetup
	}
	want.Action = SetupActionCancel
	var m setupHelloProof
	plain, err := openMessage(identity, frame, expectedSender, &m)
	if err != nil {
		return ErrSetup
	}
	canonical, err := json.Marshal(want)
	if err != nil || !bytes.Equal(plain, canonical) {
		return ErrSetup
	}
	return nil
}
