package transport

import "sync"

const defaultUDPPolicyQueuedBytes = 1 << 20
const defaultUDPQueuedBytes = 16 << 20
const defaultUDPSessions = 512

// UDPBudget bounds one policy across all its materialized ports. The zero
// value uses the shared default controller. Budgets always follow live limits.
type UDPBudget struct {
	controller            *Controller
	mu                    sync.Mutex
	sessions, queuedBytes int64
}

func NewUDPBudget() *UDPBudget { return defaultController.NewUDPBudget() }
func (c *Controller) NewUDPBudget() *UDPBudget {
	return &UDPBudget{controller: controllerOrDefault(c)}
}
func (b *UDPBudget) resources() *Controller { return controllerOrDefault(b.controller) }
func (b *UDPBudget) reserveSession() bool {
	controller := b.resources()
	limits := controller.limits()
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.sessions >= limits.UDPPerPolicy || !controller.reserveSession(limits.UDPSessions) {
		return false
	}
	b.sessions++
	return true
}
func (b *UDPBudget) releaseSession() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sessions--
	b.resources().releaseSession()
}
func (b *UDPBudget) reserveBytes(n int) bool {
	controller := b.resources()
	limits := controller.limits()
	b.mu.Lock()
	defer b.mu.Unlock()
	if n < 0 || limits.UDPPolicyQueuedBytes < b.queuedBytes || int64(n) > limits.UDPPolicyQueuedBytes-b.queuedBytes || !controller.reserveBytes(n, limits.UDPQueuedBytes) {
		return false
	}
	b.queuedBytes += int64(n)
	return true
}
func (b *UDPBudget) releaseBytes(n int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.queuedBytes -= int64(n)
	b.resources().releaseBytes(n)
}

// Usage includes queued and in-flight datagram payloads and queue metadata.
func (b *UDPBudget) Usage() (sessions, queuedBytes int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sessions, b.queuedBytes
}

func (t *udpTable) resourceBudget() *UDPBudget {
	t.budgetOnce.Do(func() {
		t.budget = t.cfg.Budget
		if t.budget == nil {
			t.budget = NewUDPBudget()
		}
	})
	return t.budget
}
func (t *udpTable) reserveBytes(n int) bool {
	t.bytesMu.Lock()
	defer t.bytesMu.Unlock()
	if !t.resourceBudget().reserveBytes(n) {
		return false
	}
	t.queuedBytes += int64(n)
	return true
}
func (t *udpTable) releaseBytes(n int) {
	t.bytesMu.Lock()
	defer t.bytesMu.Unlock()
	t.queuedBytes -= int64(n)
	t.resourceBudget().releaseBytes(n)
}
