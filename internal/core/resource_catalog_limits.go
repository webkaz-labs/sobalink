package core

import (
	"encoding/json"
	"math"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/resourcecatalog"
)

func resourceCatalogLimits(policy capacity.Policy, envelopeAllowance int64) (resourcecatalog.Limits, error) {
	if policy.Validate() != nil || envelopeAllowance < 0 {
		return resourcecatalog.Limits{}, resourceCatalogCapacity()
	}
	control, err := localLimitsFor(policy)
	if err != nil {
		return resourcecatalog.Limits{}, resourceCatalogCapacity()
	}
	rows, bytes := policy.Number("resources", "pageEntries"), min(policy.Number("resources", "pageBytes"), control.ResponseBytes)
	if rows <= 0 || rows > int64(math.MaxInt) || rows > capacity.MaxJSONInteger || bytes <= envelopeAllowance || bytes-envelopeAllowance > int64(math.MaxInt) || bytes > capacity.MaxJSONInteger {
		return resourcecatalog.Limits{}, resourceCatalogCapacity()
	}
	n := int(bytes - envelopeAllowance)
	l := resourcecatalog.Limits{MaxSources: 5, MaxRemoteTargets: 1, MaxRows: int(rows), MaxPageRows: int(rows), MaxPages: 1, MaxBytes: n, MaxPageBytes: n, MaxStringBytes: n}
	if l.Validate() != nil {
		return resourcecatalog.Limits{}, resourceCatalogCapacity()
	}
	return l, nil
}

// The raw snapshot is inserted verbatim into both local success wrappers.
// Encoding a null placeholder computes their actual framing, including the
// limits' decimal digits and the terminating newline written by both servers.
func resourceCatalogEnvelopeAllowance(l resourcecatalog.Limits) (int64, error) {
	local, err := json.Marshal(struct {
		SchemaVersion int                       `json:"schemaVersion"`
		Limits        resourceCatalogViewLimits `json:"limits"`
		Snapshot      json.RawMessage           `json:"snapshot"`
	}{1, resourceCatalogViewOf(l), json.RawMessage("null")})
	if err != nil {
		return 0, resourceCatalogCapacity()
	}
	ipc, err := json.Marshal(struct {
		Data json.RawMessage `json:"data"`
	}{json.RawMessage(local)})
	if err != nil {
		return 0, resourceCatalogCapacity()
	}
	web, err := json.Marshal(struct {
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
	}{true, json.RawMessage(local)})
	if err != nil {
		return 0, resourceCatalogCapacity()
	}
	return int64(max(len(ipc), len(web)) - len("null") + 1), nil
}

func resourceCatalogPinnedLimits(policy capacity.Policy) (resourcecatalog.Limits, error) {
	initial, err := resourceCatalogLimits(policy, 0)
	if err != nil {
		return initial, err
	}
	allowance, err := resourceCatalogEnvelopeAllowance(initial)
	if err != nil {
		return resourcecatalog.Limits{}, err
	}
	l, err := resourceCatalogLimits(policy, allowance)
	if err != nil {
		return l, err
	}
	actual, err := resourceCatalogEnvelopeAllowance(l)
	// Smaller decimal budgets cannot increase the envelope. Verify rather than
	// relying on a guessed constant subtraction or a retained-transfer scan.
	if err != nil || actual > allowance {
		return resourcecatalog.Limits{}, resourceCatalogCapacity()
	}
	return l, nil
}

// One request-wide capture budget is consumed monotonically in canonical source
// order. No provider gets a fresh MaxRows/MaxBytes allocation. Reservation covers
// row framing, scalar headers and worst-case JSON string escaping before copies;
// C1 separately enforces the exact encoded aggregate and failure metadata.
// This is a bounded logical allocation budget, not a hard process-RSS claim.
type resourceCatalogBudget struct{ rows, bytes int }

func (b *resourceCatalogBudget) reserve(strings ...string) bool {
	return b.reserveWithExtra(0, strings...)
}

func (b *resourceCatalogBudget) reserveWithExtra(extra int, strings ...string) bool {
	if b == nil || b.rows <= 0 || b.bytes < 1024 {
		return false
	}
	n := 1024
	if extra < 0 || extra > (b.bytes-n)/6 {
		return false
	}
	n += 6 * extra
	for _, value := range strings {
		if len(value) > (b.bytes-n)/6 {
			return false
		}
		n += 6 * len(value)
	}
	b.rows--
	b.bytes -= n
	return true
}

func (b *resourceCatalogBudget) rowCapacity() int {
	if b == nil || b.bytes < 1024 {
		return 0
	}
	return min(b.rows, b.bytes/1024)
}

func resourceCatalogBudgetFor(id, scope string, selections []resourcecatalog.Selection, l resourcecatalog.Limits) (*resourceCatalogBudget, error) {
	b, err := resourcecatalog.NewBuilder(id, scope, selections, l)
	if err != nil {
		return nil, resourceCatalogCapacity()
	}
	for _, s := range selections {
		if b.Fail(s, "unavailable", 253402300799000) != nil {
			return nil, resourceCatalogCapacity()
		}
	}
	snapshot, err := b.Finish()
	if err != nil {
		return nil, resourceCatalogCapacity()
	}
	encoded, err := resourcecatalog.EncodeSnapshot(snapshot, l)
	if err != nil {
		return nil, resourceCatalogCapacity()
	}
	// Reserve a revision digest and exact-total field for each future success.
	remaining := l.MaxBytes - len(encoded) - len(selections)*128
	if remaining < 0 {
		return nil, resourceCatalogCapacity()
	}
	return &resourceCatalogBudget{l.MaxRows, remaining}, nil
}
