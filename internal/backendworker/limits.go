package backendworker

import "errors"

// Limits are finite worker resources, independent of logical peer/service/file
// limits. A worker snapshots its reviewed limits at startup; restart applies a
// later budget change. FrameBytes is framing, never a maximum transfer size.
type Limits struct {
	FrameBytes int `json:"frameBytes"`
	Requests   int `json:"requests"`
	Handles    int `json:"handles"`
}

func DefaultLimits() Limits { return Limits{MaxFrame, MaxPending, MaxHandles} }
func (l Limits) Validate() error {
	if l.FrameBytes < 128<<10 || uint64(l.FrameBytes) > uint64(^uint32(0)) || l.Requests < 8 || l.Handles < 1 {
		return errors.New("worker budgets require a 128-KiB to 32-bit frame (large enough for one legal UDP datagram), at least eight concurrent requests, and a positive handle budget")
	}
	return nil
}
func (l Limits) ioBytes() int { return min(MaxIO, (l.FrameBytes-4096)*3/4) }
