package core

import (
	"context"
	"math"
	"time"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/transfer"
)

func policyOrDefault(p capacity.Policy) capacity.Policy {
	if p.Version == 0 {
		return capacity.Defaults()
	}
	return p
}

func finiteInt(n int64) int { return int(min(n, int64(^uint(0)>>1))) }

// A logical unlimited choice removes that admission rule, while the selected
// finite metadata and payload budgets continue to bound actual retained work.
func transferLimitsFor(p capacity.Policy, reserved string) transfer.Limits {
	p = policyOrDefault(p)
	metadata := p.Number("resources", "transferManifestBytes")
	retained := p.Number("resources", "transferMetadataBytes")
	bytes := p.Number("resources", reserved)
	pathBytes := min(p.Number("logical", "pathBytes"), metadata)
	return transfer.Limits{
		MaxEntries:       finiteInt(min(p.Number("logical", "batchEntries"), max(1, metadata/512))),
		MaxPathBytes:     finiteInt(pathBytes),
		MaxDepth:         finiteInt(min(p.Number("logical", "pathDepth"), max(1, pathBytes/2+pathBytes%2))),
		MaxManifestBytes: metadata, MaxMetadataBytes: retained,
		DiskReserveBytes: p.Number("resources", "diskReserveBytes"),
		MaxFileBytes:     min(p.Number("logical", "fileBytes"), bytes),
		MaxBatchBytes:    min(p.Number("logical", "batchBytes"), bytes), MaxReservedBytes: bytes,
		MaxBatches: finiteInt(min(p.Number("logical", "transferHistoryEntries"), max(1, retained/512))),
		// Trust records are already constrained by the separately finite profile
		// budget. Do not reapply a lowered logical count to existing bindings.
		MaxPeers:             finiteInt(p.Number("resources", "profileBytes")),
		MaxPendingBatches:    finiteInt(p.Number("resources", "transferPending")),
		MaxPendingPerPeer:    finiteInt(p.Number("resources", "transferPendingPerPeer")),
		MaxConcurrentFiles:   finiteInt(p.Number("resources", "transferConcurrentFiles")),
		MaxConcurrentPerPeer: finiteInt(p.Number("resources", "transferConcurrentPerPeer")),
	}
}

func receiveTransferLimits(p capacity.Policy) transfer.Limits {
	return transferLimitsFor(p, "receiveReservedBytes")
}

func (c *Core) transferLimits() transfer.Limits {
	return transferLimitsFor(c.capacityPolicy(), "transferSpoolBytes")
}

func transferJSONBytes(metadata int64) int64 {
	// Policies are finite JSON-safe integers; saturating still protects direct
	// library tests and future callers from overflowing a framing calculation.
	if metadata > (math.MaxInt64-1)/6 {
		return math.MaxInt64 - 1
	}
	return 6 * metadata
}

func (c *Core) receiveManifestJSONBytes() int64 {
	metadata := c.limit("resources", "transferManifestBytes")
	if c.transfers != nil {
		metadata = max(metadata, c.transfers.LargestManifestBytes())
	}
	return transferJSONBytes(metadata)
}

func (c *Core) transferResponseJSONBytes() int64 {
	metadata := c.limit("resources", "transferManifestBytes")
	c.mu.RLock()
	for _, batch := range c.outgoing {
		batch.mu.Lock()
		metadata = max(metadata, transfer.ManifestMetadataBytes(batch.Manifest))
		batch.mu.Unlock()
	}
	c.mu.RUnlock()
	// File status contains both the offered path and saved relative name.
	return 2 * transferJSONBytes(metadata)
}

func (c *Core) operationContext(parent context.Context, key string) (context.Context, context.CancelFunc) {
	p := c.capacityPolicy()
	if p.Logical[key].Mode == "unlimited" {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, time.Duration(p.Number("logical", key))*time.Second)
}

// MessageTextBytes is the effective decoded-text admission budget used by the
// local transports and authenticated peer endpoint.
func (c *Core) MessageTextBytes() int64 {
	p := c.capacityPolicy()
	return min(p.Number("logical", "messageBytes"), p.Number("resources", "messageTextBytes"))
}

func receiveAccountingLimits(p capacity.Policy) transfer.AccountingLimits {
	p = policyOrDefault(p)
	return transfer.AccountingLimits{
		MaxBytes:   p.Number("resources", "transferMetadataBytes"),
		MaxEntries: p.Number("resources", "stagingInventoryEntries"),
		MaxDepth:   p.Number("resources", "stagingInventoryDepth"),
		// Absolute local paths belong to the private metadata budget, not the
		// sender-relative logical path choice.
		MaxPathBytes: p.Number("resources", "transferMetadataBytes"),
	}
}
