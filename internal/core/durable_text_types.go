package core

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/transfer"
)

const (
	DurableTextVersion     = 1
	DurableTextCapability  = "durable_text_v1"
	DurableTextOfferPath   = "/v1/durable-messages/offer"
	DurableTextDeliverPath = "/v1/durable-messages/deliver"
	DurableTextStatusPath  = "/v1/durable-messages/status"
	messageStoreVersion    = 1
	messageStoreHardBytes  = 16 << 20
	messageStoreMaxRows    = 1024
	messageActiveLimit     = 32
	messageActivePeerLimit = 8
	messageReceiptReserve  = 4096
	messageHardTextBytes   = (messageStoreHardBytes - messageReceiptReserve) / 6
	textMaximumLifetime    = 7 * 24 * time.Hour
	textReceiptGrace       = 24 * time.Hour
	textClockSkew          = 5 * time.Minute
)

// Durable text never downgrades to the immediate-message route.
type DurableTextOffer struct {
	Version     int       `json:"version"`
	MessageID   string    `json:"messageId"`
	RecipientID string    `json:"recipientId"`
	TextBytes   int64     `json:"textBytes"`
	TextSHA256  string    `json:"textSHA256"`
	CreatedAt   time.Time `json:"createdAt"`
	ExpiresAt   time.Time `json:"expiresAt"`
	SentAt      time.Time `json:"sentAt"`
	Digest      string    `json:"digest"`
}
type DurableTextDeliver struct {
	Lease transfer.DurableLease `json:"lease"`
	Text  string                `json:"text"`
}
type DurableTextReceipt struct {
	Lease      transfer.DurableLease `json:"lease"`
	MessageID  string                `json:"messageId"`
	TextBytes  int64                 `json:"textBytes"`
	TextSHA256 string                `json:"textSHA256"`
	SavedAt    time.Time             `json:"savedAt"`
}
type DurableTextStatus struct {
	Lease       transfer.DurableLease `json:"lease"`
	State       string                `json:"state"`
	Receipt     *DurableTextReceipt   `json:"receipt,omitempty"`
	ReceiverNow time.Time             `json:"receiverNow"`
	ReasonCode  string                `json:"reasonCode,omitempty"`
}
type TextOutboxRecord struct {
	Version         int                 `json:"version"`
	Kind            string              `json:"kind"`
	OutboxID        string              `json:"outboxId"`
	RequestID       string              `json:"requestId"`
	MessageID       string              `json:"messageId"`
	PeerID          string              `json:"peerId"`
	ProcessID       string              `json:"processId,omitempty"`
	ReviewRevision  string              `json:"reviewRevision"`
	Admission       string              `json:"admission"`
	Status          string              `json:"status"`
	DeliveryOutcome string              `json:"deliveryOutcome"`
	ReasonCode      string              `json:"reasonCode"`
	Text            string              `json:"text,omitempty"`
	TextBytes       int64               `json:"textBytes"`
	SHA256          string              `json:"sha256"`
	CreatedAt       time.Time           `json:"createdAt"`
	ExpiresAt       time.Time           `json:"expiresAt"`
	UpdatedAt       time.Time           `json:"updatedAt"`
	Receipt         *DurableTextReceipt `json:"receipt,omitempty"`
}

// View.Text and View.Receipt are omitted in persisted rows. Text and Receipt
// below are the sole copies; messageQueueViewLocked adds them to a cloned view.
type messageOutgoing struct {
	View             TextOutboxRecord       `json:"view"`
	Identity         string                 `json:"identity"`
	Network          string                 `json:"network"`
	SenderID         string                 `json:"senderId"`
	Trust            Trust                  `json:"trust"`
	Signature        string                 `json:"signature"`
	Offer            DurableTextOffer       `json:"offer"`
	Lease            *transfer.DurableLease `json:"lease,omitempty"`
	Receipt          *DurableTextReceipt    `json:"receipt,omitempty"`
	Text             string                 `json:"text,omitempty"`
	BodyRetained     bool                   `json:"bodyRetained"`
	Visible          bool                   `json:"visible"`
	Attempted        bool                   `json:"attempted"`
	ExposurePossible bool                   `json:"exposurePossible"`
	Cancelled        bool                   `json:"cancelled"`
	NoMoreSend       bool                   `json:"noMoreSend"`
	// Active may become false only after a durable NoMoreSend fence and physical
	// owner join. A terminal label alone cannot release an admission slot.
	Active bool `json:"active"`
}
type messageIncoming struct {
	PeerID          string                `json:"peerId"`
	Network         string                `json:"network"`
	TrustGeneration uint64                `json:"trustGeneration"`
	RevocationEpoch string                `json:"revocationEpoch"`
	Offer           DurableTextOffer      `json:"offer"`
	Lease           transfer.DurableLease `json:"lease"`
	State           string                `json:"state"`
	Text            string                `json:"text,omitempty"`
	BodyRetained    bool                  `json:"bodyRetained"`
	Visible         bool                  `json:"visible"`
	Receipt         *DurableTextReceipt   `json:"receipt,omitempty"`
}
type messageEnvelope struct {
	Version   int                         `json:"version"`
	Identity  string                      `json:"identity"`
	Epoch     string                      `json:"epoch"`
	HighWater time.Time                   `json:"highWater"`
	Immediate []Message                   `json:"immediate"`
	Outgoing  map[string]*messageOutgoing `json:"outgoing"`
	Incoming  map[string]*messageIncoming `json:"incoming"`
}

func messageStoreError(code string) error {
	return &localCommandError{code, "message storage requires review; preserve the original message ID and its retained delivery evidence"}
}
func textRequestCreated(id string) (time.Time, bool) {
	parts := strings.Split(id, "_")
	if len(parts) != 3 || parts[0] != "mt1" || len(parts[1]) != 13 || len(parts[2]) != 32 {
		return time.Time{}, false
	}
	ms, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || ms < 1_000_000_000_000 || strconv.FormatInt(ms, 10) != parts[1] || !canonicalHex(parts[2], 16) {
		return time.Time{}, false
	}
	created := time.UnixMilli(ms).UTC()
	if created.Add(textMaximumLifetime+textReceiptGrace+textClockSkew).UnixMilli() > 9_999_999_999_999 {
		return time.Time{}, false
	}
	return created, true
}
func textRequestFresh(id string, now time.Time) bool {
	created, ok := textRequestCreated(id)
	return ok && !created.Before(now.Add(-textClockSkew)) && !created.After(now.Add(textClockSkew))
}
func textRequestHorizon(id string) (time.Time, bool) {
	created, ok := textRequestCreated(id)
	if !ok {
		return time.Time{}, false
	}
	return created.Add(textMaximumLifetime + textReceiptGrace + textClockSkew), true
}
func newTextRequestID(now time.Time) string {
	return "mt1_" + strconv.FormatInt(now.UTC().UnixMilli(), 10) + "_" + randomID()
}
func canonicalHex(value string, size int) bool {
	b, err := hex.DecodeString(value)
	return err == nil && len(b) == size && hex.EncodeToString(b) == value
}
func textSHA256(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}
func durableTextDigest(offer DurableTextOffer) (string, error) {
	created, ok := textRequestCreated(offer.MessageID)
	if !ok || offer.Version != DurableTextVersion || !config.ValidPeerID(offer.RecipientID) || offer.TextBytes <= 0 || offer.TextBytes > messageHardTextBytes || !canonicalHex(offer.TextSHA256, 32) || !offer.CreatedAt.Equal(created) || offer.ExpiresAt.Sub(created) < time.Minute || offer.ExpiresAt.After(created.Add(textMaximumLifetime)) {
		return "", messageStoreError("message_invalid")
	}
	offer.Digest = ""
	offer.SentAt = time.Time{}
	offer.CreatedAt, offer.ExpiresAt = offer.CreatedAt.UTC(), offer.ExpiresAt.UTC()
	data, err := json.Marshal(offer)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append([]byte("sobalink/durable-text/v1\x00"), data...))
	return hex.EncodeToString(sum[:]), nil
}
func textLeaseMatches(offer DurableTextOffer, lease transfer.DurableLease) bool {
	horizon, ok := textRequestHorizon(offer.MessageID)
	digest, err := durableTextDigest(offer)
	return ok && err == nil && digest == offer.Digest && lease.Version == DurableTextVersion && lease.DeliveryID == offer.MessageID && lease.RecipientID == offer.RecipientID && lease.Digest == offer.Digest && canonicalHex(lease.ReceiverEpoch, 16) && canonicalHex(lease.Token, 16) && lease.ExpiresAt.Equal(offer.ExpiresAt) && lease.ReceiptUntil.Equal(horizon)
}
func validateTextReceipt(offer DurableTextOffer, lease *transfer.DurableLease, receipt *DurableTextReceipt) error {
	if lease == nil || receipt == nil || !textLeaseMatches(offer, *lease) || !sameTextLease(*lease, receipt.Lease) || receipt.MessageID != offer.MessageID || receipt.TextBytes != offer.TextBytes || receipt.TextSHA256 != offer.TextSHA256 || receipt.SavedAt.IsZero() || receipt.SavedAt.Before(offer.CreatedAt.Add(-textClockSkew)) || receipt.SavedAt.After(lease.ReceiptUntil) {
		return messageStoreError("message_receipt_invalid")
	}
	return nil
}
func sameTextLease(a, b transfer.DurableLease) bool {
	return a.Version == b.Version && a.DeliveryID == b.DeliveryID && a.Digest == b.Digest && a.ReceiverEpoch == b.ReceiverEpoch && a.Token == b.Token && a.RecipientID == b.RecipientID && a.ExpiresAt.Equal(b.ExpiresAt) && a.ReceiptUntil.Equal(b.ReceiptUntil)
}
