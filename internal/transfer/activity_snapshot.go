package transfer

import (
	"context"
	"sort"
)

// ActivitySummary is the complete scalar allowlist for local activity catalogs.
// It cannot carry files, destinations, errors, receive policies or accounting.
type ActivitySummary struct {
	ID, PeerID                 string
	State                      BatchState
	TotalBytes, CompletedBytes int64
}

// ActivitySnapshot performs no I/O and owns no goroutine. Membership and scalar
// values are captured under the existing manager lock, after the row bound.
// The strings refer to immutable admitted IDs; no manifest is copied or read.
func (m *Manager) ActivitySnapshot(ctx context.Context, maxRows int) ([]ActivitySummary, error) {
	if ctx == nil || maxRows <= 0 {
		return nil, ErrLimit
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if m == nil {
		return nil, ErrClosed
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, ErrClosed
	}
	if len(m.batches) > maxRows {
		return nil, ErrLimit
	}
	out := make([]ActivitySummary, 0, len(m.batches))
	for _, b := range m.batches {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if b == nil {
			return nil, ErrState
		}
		v := &b.value
		out = append(out, ActivitySummary{v.ID, v.Peer.ID, v.State, v.TotalBytes, v.CompletedBytes})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, ctx.Err()
}
