package main

import (
	"context"

	"github.com/webkaz-labs/sobalink/internal/control"
	"github.com/webkaz-labs/sobalink/internal/core"
)

func controlCall(ctx context.Context, dir, command string, v any) error {
	// Check the small, separate capacity file without reading saved identities
	// or private payloads. Invalid selected settings require an explicit repair.
	if _, err := core.ReadLocalControlLimits(dir); err != nil {
		return err
	}
	// Retained in-memory history can exceed a newly lowered admission budget.
	// The protected local server publishes its finite current read envelope.
	var limits control.Limits
	if err := control.Call(ctx, dir, "control.limits", &limits); err != nil {
		return err
	}
	if err := limits.Validate(); err != nil {
		return err
	}
	return control.CallWithLimits(ctx, dir, command, v, limits)
}
