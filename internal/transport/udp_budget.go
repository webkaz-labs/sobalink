package transport

import "sync"

const maxUDPQueuedBytes = 1 << 20
const maxTotalUDPQueuedBytes = 16 << 20
const maxTotalUDPSessions = 512

// Byte limits complement packet counts: an allowed sender using large
// datagrams and many source ports cannot reserve gigabytes of queue storage.
var udpMemory = byteBudget{limit: maxTotalUDPQueuedBytes}
var udpSessionSlots = make(chan struct{}, maxTotalUDPSessions)

type byteBudget struct {
	mu          sync.Mutex
	used, limit int
}

func (b *byteBudget) reserve(n int) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if n < 0 || n > b.limit-b.used {
		return false
	}
	b.used += n
	return true
}
func (b *byteBudget) release(n int) { b.mu.Lock(); b.used -= n; b.mu.Unlock() }
func (b *byteBudget) usage() int    { b.mu.Lock(); defer b.mu.Unlock(); return b.used }
func (t *udpTable) reserveBytes(n int) bool {
	t.bytesMu.Lock()
	defer t.bytesMu.Unlock()
	if n < 0 || n > maxUDPQueuedBytes-t.queuedBytes {
		return false
	}
	if !udpMemory.reserve(n) {
		return false
	}
	t.queuedBytes += n
	return true
}
func (t *udpTable) releaseBytes(n int) {
	t.bytesMu.Lock()
	t.queuedBytes -= n
	udpMemory.release(n)
	t.bytesMu.Unlock()
}
