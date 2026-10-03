package core

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"time"
)

// A revision is a bounded, immutable local review snapshot, not a credential.
// Every field is compared with a new identity-authenticated peer response.
// Keeping the reviewed expiry allows a lease renewal without accepting a
// shorter grant. No device name, local target, owner, or allowed-peer list is
// exposed through this peer metadata.
type discoveryReview struct {
	Version   int           `json:"version"`
	PeerID    string        `json:"peerId"`
	Service   RemoteService `json:"service"`
	CheckedAt time.Time     `json:"checkedAt"`
}

func discoveryReviewError() error {
	return &localCommandError{"discovery_review_changed", "shared service changed or its review is stale; refresh and review the current service before connecting"}
}

func validServicePurpose(purpose string) bool {
	switch purpose {
	case "", "generic", "custom", "web", "ssh", "db", "postgres", "ai", "local-ai", "desktop", "rustdesk":
		return true
	}
	return false
}

func freshDiscoveryCheck(checked, now time.Time) bool {
	return !checked.IsZero() && !checked.After(now.Add(5*time.Second)) && now.Sub(checked) <= 15*time.Second
}

func encodeDiscoveryReview(peerID string, service RemoteService, checked time.Time) string {
	b, _ := json.Marshal(discoveryReview{Version: 1, PeerID: peerID, Service: service, CheckedAt: checked})
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeDiscoveryReview(revision string) (discoveryReview, error) {
	var review discoveryReview
	// The enclosing local command/profile budgets already bound this encoding.
	// Do not impose a smaller arbitrary range or token-size limit here.
	if len(revision) == 0 {
		return review, discoveryReviewError()
	}
	b, err := base64.RawURLEncoding.DecodeString(revision)
	if err != nil {
		return review, discoveryReviewError()
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	// A saved review may have an old deadline after the same grant was renewed.
	// Decode the reviewed value without applying live-response expiry checks;
	// the freshly authenticated response is validated separately before use.
	type historicalService RemoteService
	var wire struct {
		Version   int               `json:"version"`
		PeerID    string            `json:"peerId"`
		Service   historicalService `json:"service"`
		CheckedAt time.Time         `json:"checkedAt"`
	}
	if err := d.Decode(&wire); err != nil {
		return review, discoveryReviewError()
	}
	review = discoveryReview{Version: wire.Version, PeerID: wire.PeerID, Service: RemoteService(wire.Service), CheckedAt: wire.CheckedAt}
	if d.Decode(new(any)) != io.EOF || review.Version != 1 || review.CheckedAt.IsZero() || !validServicePurpose(review.Service.Purpose) {
		return review, discoveryReviewError()
	}
	return review, nil
}

func (r discoveryReview) matchesRequest(spec ServiceSpec, effectivePorts string) bool {
	return r.PeerID == spec.PeerID && r.Service.ID == spec.ServiceID &&
		r.Service.Purpose == spec.Purpose && r.Service.Network == spec.Network && r.Service.Ports == effectivePorts &&
		r.Service.Application == "unverified" && validReviewedLifetime(r.Service)
}

func validReviewedLifetime(s RemoteService) bool {
	return s.Lifetime == "until-revoked" && s.ExpiresAt.IsZero() ||
		(s.Lifetime == "finite" || s.Lifetime == "") && !s.ExpiresAt.IsZero()
}

func sameDiscoveredGrant(reviewed, current RemoteService) bool {
	if validateRemote(current) != nil || !validReviewedLifetime(reviewed) {
		return false
	}
	return reviewed.ID == current.ID && reviewed.Purpose == current.Purpose &&
		reviewed.Network == current.Network && reviewed.Ports == current.Ports &&
		reviewed.Lifetime == current.Lifetime && !current.ExpiresAt.Before(reviewed.ExpiresAt)
}

func validateSavedDiscoveryReview(spec ServiceSpec) error {
	if spec.ServiceID == "" && spec.ServiceRevision == "" {
		return nil
	}
	if spec.Direction != "forward" {
		return errors.New("only outbound connections may follow a shared service")
	}
	review, err := decodeDiscoveryReview(spec.ServiceRevision)
	if err != nil || review.PeerID != spec.PeerID || review.Service.ID != spec.ServiceID ||
		review.Service.Network != spec.Network || review.Service.Purpose != spec.Purpose || !validReviewedLifetime(review.Service) {
		return discoveryReviewError()
	}
	return nil
}
