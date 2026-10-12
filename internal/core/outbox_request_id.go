package core

import (
	"encoding/hex"
	"strconv"
	"strings"
	"time"
)

const outboxRequestClockSkew = 5 * time.Minute
const outboxRequestMaximumLifetime = 7 * 24 * time.Hour
const outboxRequestReceiptGrace = 24 * time.Hour

// Request keys include their immutable admission epoch. Once the hard horizon
// passes, even a forgotten key is permanently inadmissible as new work. This
// permits bounded terminal metadata cleanup without an ever-growing tombstone
// set. Retained uncertain records remain queryable by their original key.
func outboxRequestCreated(id string) (time.Time, bool) {
	parts := strings.Split(id, "_")
	if len(parts) != 3 || parts[0] != "ob1" || len(parts[1]) != 13 || len(parts[2]) != 32 {
		return time.Time{}, false
	}
	millis, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || millis < 1_000_000_000_000 || strconv.FormatInt(millis, 10) != parts[1] {
		return time.Time{}, false
	}
	nonce, err := hex.DecodeString(parts[2])
	if err != nil || len(nonce) != 16 || hex.EncodeToString(nonce) != parts[2] {
		return time.Time{}, false
	}
	created := time.UnixMilli(millis).UTC()
	// Keep checked arithmetic and the wire format inside the representable
	// thirteen-digit epoch interval, including the complete retention horizon.
	if created.Add(outboxRequestMaximumLifetime+outboxRequestReceiptGrace+outboxRequestClockSkew).UnixMilli() > 9_999_999_999_999 {
		return time.Time{}, false
	}
	return created, true
}
func outboxRequestFresh(id string, now time.Time) bool {
	created, ok := outboxRequestCreated(id)
	return ok && !created.Before(now.Add(-outboxRequestClockSkew)) && !created.After(now.Add(outboxRequestClockSkew))
}
func outboxRequestHorizon(id string) (time.Time, bool) {
	created, ok := outboxRequestCreated(id)
	if !ok {
		return time.Time{}, false
	}
	return created.Add(outboxRequestMaximumLifetime + outboxRequestReceiptGrace + outboxRequestClockSkew), true
}
func outboxRequestRetired(id string, now time.Time) bool {
	until, ok := outboxRequestHorizon(id)
	return ok && !now.Before(until)
}
func newOutboxRequestID(now time.Time) string {
	return "ob1_" + strconv.FormatInt(now.UTC().UnixMilli(), 10) + "_" + randomID()
}
