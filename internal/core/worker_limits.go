package core

import (
	"errors"
	"github.com/webkaz-labs/sobalink/internal/backendworker"
	"github.com/webkaz-labs/sobalink/internal/capacity"
	"math"
)

func selectedWorkerLimits(p capacity.Policy) (backendworker.Limits, error) {
	frame, requests, handles := p.Number("resources", "workerFrameBytes"), p.Number("resources", "workerRequests"), p.Number("resources", "workerHandles")
	if frame > math.MaxUint32 || requests > int64(math.MaxInt) || handles > int64(math.MaxInt) {
		return backendworker.Limits{}, errors.New("worker budget exceeds the protocol or platform representation")
	}
	limits := backendworker.Limits{FrameBytes: int(frame), Requests: int(requests), Handles: int(handles)}
	return limits, limits.Validate()
}
