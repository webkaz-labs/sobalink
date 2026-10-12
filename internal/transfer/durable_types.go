package transfer

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
)

// Durable v1 is deliberately separate from immediate transfer routes. A lease
// never supplies peer authority: each operation also binds its verified caller.
const (
	DurableVersion      = 1
	DurableCapability   = "durable_receipt_v1"
	DurableOfferPath    = "/v1/durable-transfers/offer"
	DurableStatusPath   = "/v1/durable-transfers/status"
	DurableFilePath     = "/v1/durable-transfers/file"
	DurableStartPath    = "/v1/durable-transfers/start"
	DurableLeaseHeader  = "X-Sobalink-Durable-Lease"
	DurableReceiptGrace = 24 * time.Hour
	DurableClockSkew    = 5 * time.Minute
	DurableMaxLifetime  = 7 * 24 * time.Hour
)

var (
	ErrDurableConflict     = errors.New("durable identity conflicts")
	ErrDurableUnknownLease = errors.New("durable lease is unknown")
	ErrDurableRecovery     = errors.New("durable receive recovery is required")
	ErrDurableExpired      = errors.New("durable transmission deadline passed")
	ErrDurableClock        = errors.New("durable clock needs repair")
	ErrDurableCapacity     = errors.New("durable receive capacity reached")
	ErrDurableStorage      = errors.New("durable storage is unsupported")
	ErrDurablePermission   = errors.New("durable permission changed")
)

type DurableOffer struct {
	Version     int       `json:"version"`
	DeliveryID  string    `json:"deliveryId"`
	RecipientID string    `json:"recipientId"`
	Digest      string    `json:"digest"`
	Manifest    Manifest  `json:"manifest"`
	CreatedAt   time.Time `json:"createdAt"`
	SentAt      time.Time `json:"sentAt"`
	ExpiresAt   time.Time `json:"expiresAt"`
}

type DurableLease struct {
	Version       int       `json:"version"`
	DeliveryID    string    `json:"deliveryId"`
	Digest        string    `json:"digest"`
	ReceiverEpoch string    `json:"receiverEpoch"`
	Token         string    `json:"token"`
	RecipientID   string    `json:"recipientId"`
	ExpiresAt     time.Time `json:"expiresAt"`
	ReceiptUntil  time.Time `json:"receiptUntil"`
}

type DurableRequest struct {
	Lease DurableLease `json:"lease"`
}

type DurableReceipt struct {
	Lease   DurableLease `json:"lease"`
	State   BatchState   `json:"state"`
	Files   []FileAck    `json:"files"`
	SavedAt time.Time    `json:"savedAt"`
}

type DurableStatus struct {
	Lease       DurableLease    `json:"lease"`
	State       BatchState      `json:"state"`
	Receipt     *DurableReceipt `json:"receipt,omitempty"`
	ReceiverNow time.Time       `json:"receiverNow"`
	ReasonCode  string          `json:"reasonCode,omitempty"`
}

// DurableDigest has fixed-field canonical JSON encoding. Entry order and exact
// paths are immutable; equivalent timestamps use UTC. The digest binds the
// recipient and original deadline, never a receiver-selected local destination.
func DurableDigest(offer DurableOffer) (string, error) {
	if offer.Version != DurableVersion || offer.DeliveryID != offer.Manifest.ID || !validID(offer.DeliveryID) || offer.RecipientID == "" || len(offer.RecipientID) > 512 {
		return "", ErrInvalidManifest
	}
	offer.Digest = ""
	offer.SentAt = time.Time{}
	offer.CreatedAt, offer.ExpiresAt = offer.CreatedAt.UTC(), offer.ExpiresAt.UTC()
	data, err := json.Marshal(offer)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:]), nil
}

func DurableErrorCode(err error) string {
	for _, known := range []struct {
		err  error
		code string
	}{
		{ErrDurableConflict, "durable_conflict"}, {ErrDurableUnknownLease, "durable_unknown_lease"},
		{ErrDurableExpired, "durable_expired"},
		{ErrDurableClock, "durable_clock"}, {ErrDurableCapacity, "durable_capacity"},
		{ErrDurableStorage, "durable_unsupported_storage"}, {ErrDurablePermission, "durable_permission"},
		{ErrDurableRecovery, "durable_recovery_required"},
	} {
		if errors.Is(err, known.err) {
			return known.code
		}
	}
	return ErrorCode(err)
}
