package core

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/webkaz-labs/sobalink/internal/directlan"
	"github.com/webkaz-labs/sobalink/internal/endpointmeta"
)

// Eight total attempts and seven fixed waits. The existing operation/proof
// context is never replaced or extended; many operations therefore stop earlier.
var endpointDeliveryBackoff = [...]time.Duration{250 * time.Millisecond, 500 * time.Millisecond, time.Second, 2 * time.Second, 4 * time.Second, 4 * time.Second, 4 * time.Second}

// Every joined cause must be transient. In particular a wire cleanup failure
// tagged ErrRetirementIncomplete must not be hidden behind an earlier EOF.
func endpointDeliveryRetryable(err error) bool {
	if err == nil {
		return false
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		children := joined.Unwrap()
		if len(children) == 0 {
			return false
		}
		for _, child := range children {
			if !endpointDeliveryRetryable(child) {
				return false
			}
		}
		return true
	}
	switch err {
	case directlan.ErrUnavailable, directlan.ErrCapacity, io.EOF, io.ErrUnexpectedEOF:
		return true
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		return endpointDeliveryRetryable(wrapped.Unwrap())
	}
	return false
}
func waitEndpointDeliveryRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return ctx.Err()
	}
}

// Generic finite retry bookkeeping; tests exercise counts/time/cancellation,
// never native transports or fake generation publication.
func runEndpointDeliveryAttempts(ctx context.Context, attempt func(context.Context) (*endpointmeta.UpdateReply, error), wait func(context.Context, time.Duration) error) (*endpointmeta.UpdateReply, int, error) {
	if ctx == nil || attempt == nil || wait == nil {
		return nil, 0, endpointmeta.ErrInvalid
	}
	for count := 1; count <= len(endpointDeliveryBackoff)+1; count++ {
		if err := ctx.Err(); err != nil {
			return nil, count - 1, err
		}
		reply, err := attempt(ctx)
		if err == nil {
			if reply == nil {
				return nil, count, endpointmeta.ErrInvalid
			}
			return reply, count, nil
		}
		if !endpointDeliveryRetryable(err) || count > len(endpointDeliveryBackoff) {
			return nil, count, err
		}
		if err = wait(ctx, endpointDeliveryBackoff[count-1]); err != nil {
			return nil, count, err
		}
	}
	return nil, 0, errors.New("unreachable endpoint delivery retry state")
}

func (c *Core) deliverEndpointWithRetry(ctx context.Context, b *directLANBackend, receipt *contextPublicationReceipt, target *directlan.EndpointDelivery, proof endpointmeta.Envelope) (*endpointmeta.UpdateReply, int, error) {
	return runEndpointDeliveryAttempts(ctx, func(attempt context.Context) (*endpointmeta.UpdateReply, error) {
		if err := c.endpointDeliveryReceiptCurrent(attempt, b, receipt, proof.Update.Recipient); err != nil {
			return nil, err
		}
		// Target includes the exact receipt epoch. Dial/TLS remain bounded by
		// the original context; post-connect revalidation rejects a changed epoch
		// before endpoint frame output. A joined watcher then covers frame I/O.
		return b.Node.DeliverEndpointUpdate(attempt, target, proof)
	}, waitEndpointDeliveryRetry)
}

// Short nonwaiting admission only. No Core/store lock crosses network I/O or
// backoff, and no current receipt is reconstructed from a read or flag.
func (c *Core) endpointDeliveryReceiptCurrent(ctx context.Context, b *directLANBackend, receipt *contextPublicationReceipt, key string) error {
	if ctx == nil || ctx.Err() != nil {
		if ctx != nil {
			return ctx.Err()
		}
		return endpointmeta.ErrInvalid
	}
	if !c.op.TryLock() {
		return directlan.ErrUnavailable
	}
	defer c.op.Unlock()
	if c.ctx.Err() != nil {
		return c.ctx.Err()
	}
	if b == nil || b.Node == nil || receipt == nil || c.endpointBackendLocked() != b {
		return endpointmeta.ErrReview
	}
	owner := b.currentCompletion()
	if owner == nil || owner.receipt != receipt || !owner.coreCurrent(key) || !owner.activationCurrent() {
		return endpointmeta.ErrReview
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}
