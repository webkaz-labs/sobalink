package core

import (
	"encoding/json"
	"strings"

	"github.com/webkaz-labs/sobalink/internal/control"
	"github.com/webkaz-labs/sobalink/internal/messageframe"
	"github.com/webkaz-labs/sobalink/internal/resourcegroup"
)

func resourceGroupRequestFits(name string, payload json.RawMessage, limits control.Limits) bool {
	if limits.Validate() != nil {
		return false
	}
	// Reserve the actual command shape and worst escaping for the independently
	// bounded request ID. IPC then wraps the whole command as a JSON string.
	command, err := json.Marshal(struct {
		RequestID string          `json:"requestId"`
		Name      string          `json:"name"`
		Payload   json.RawMessage `json:"payload"`
	}{strings.Repeat("\x00", messageframe.RequestIDBytes), name, payload})
	if err != nil || int64(len(command)+1) > limits.CommandBytes {
		return false
	}
	request, err := json.Marshal(control.Request{Command: string(command)})
	return err == nil && int64(len(request)+1) <= limits.RequestBytes
}

func resourceGroupResponseFits(value any, limits control.Limits, reserve int) bool {
	if limits.Validate() != nil || reserve < 0 {
		return false
	}
	data, err := json.Marshal(value)
	if err != nil || len(data)+reserve > resourcegroup.MaxLocalResponseBytes {
		return false
	}
	ipc, err := json.Marshal(control.Response{Data: data})
	if err != nil || int64(len(ipc)+1+reserve) > limits.ResponseBytes {
		return false
	}
	web, err := json.Marshal(struct {
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
	}{true, data})
	return err == nil && int64(len(web)+1+reserve) <= limits.ResponseBytes
}

func (c *Core) resourceGroupResultFits(value any, reserve int) bool {
	limits, err := localLimitsFor(c.capacityPolicy())
	return err == nil && resourceGroupResponseFits(value, limits, reserve)
}

func (c *Core) resourceGroupReserveResponse(run resourceGroupRecord) error {
	worst, err := resourceGroupWorstRecord(run)
	if err != nil {
		return resourceGroupError("capacity")
	}
	view, err := resourceGroupRunView(worst, resourcegroup.ActivityRefreshing)
	// All 21 scalar summary fields are always present. Two-digit counters
	// and boolean spelling differences can grow by fewer than 64 bytes.
	// The 128-byte reserve also covers wrapper/newline and activity differences.
	if err != nil || !c.resourceGroupResultFits(view, 128) {
		return resourceGroupError("capacity")
	}
	return nil
}
