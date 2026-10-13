//go:build darwin && cgo

package nearby

/*
#cgo CFLAGS: -std=c11
#cgo LDFLAGS: -ldns_sd
#include <stdlib.h>
#include "dnssd_bridge_darwin.h"
*/
import "C"

import (
	"context"
	"sync"
	"unsafe"
)

func newDarwinRecordProvider(ctx context.Context, cfg Config, policy discoveryPolicy) (recordProvider, error) {
	return newDNSServiceProvider(ctx, cfg, policy, newNativeDNSServiceBackend, currentBindingWithPolicy)
}

type nativeDNSServiceBackend struct {
	mu      sync.Mutex
	closing bool
	calls   sync.WaitGroup
	once    sync.Once
	owner   *C.struct_soba_dnssd_owner
}

func newNativeDNSServiceBackend(reg dnssdRegistration) (dnssdBackend, error) {
	if reg.InterfaceIndex == 0 {
		return nil, ErrInvalidScope
	}
	var cfg C.struct_soba_dnssd_config
	cfg.interface_index = C.uint32_t(reg.InterfaceIndex)
	for i, record := range reg.Records {
		if len(record.Name) == 0 || len(record.Name) >= C.SOBA_DNSSD_NAME || len(record.Data) > C.SOBA_DNSSD_BYTES {
			return nil, ErrInvalidAdvertisement
		}
		item := &cfg.records[i]
		for j, ch := range []byte(record.Name) {
			item.name[j] = C.char(ch)
		}
		item._type = C.uint16_t(record.Type)
		item.length = C.uint16_t(len(record.Data))
		for j, ch := range record.Data {
			item.data[j] = C.uchar(ch)
		}
	}
	owner := C.soba_dnssd_create(&cfg)
	if owner == nil {
		return nil, dnssdFailure{"allocation", "unavailable"}
	}
	return &nativeDNSServiceBackend{owner: owner}, nil
}
func (b *nativeDNSServiceBackend) begin() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closing {
		return false
	}
	b.calls.Add(1)
	return true
}
func (b *nativeDNSServiceBackend) Start() error {
	if !b.begin() {
		return context.Canceled
	}
	defer b.calls.Done()
	return dnssdNativeError(C.soba_dnssd_start(b.owner))
}
func (b *nativeDNSServiceBackend) Query(name string, typ uint16) error {
	if !b.begin() {
		return context.Canceled
	}
	defer b.calls.Done()
	if len(name) == 0 || len(name) >= C.SOBA_DNSSD_NAME {
		return ErrInvalidPacket
	}
	nativeName := C.CString(name)
	defer C.free(unsafe.Pointer(nativeName))
	return dnssdNativeError(C.soba_dnssd_query(b.owner, nativeName, C.uint16_t(typ)))
}
func (b *nativeDNSServiceBackend) Next() (dnssdEvent, error) {
	if !b.begin() {
		return dnssdEvent{}, context.Canceled
	}
	defer b.calls.Done()
	var event C.struct_soba_dnssd_event
	if err := dnssdNativeError(C.soba_dnssd_next(b.owner, &event)); err != nil {
		return dnssdEvent{}, err
	}
	// C copied into this caller-owned bounded struct while holding its ring lock.
	// The Go result owns new buffers; Close cannot invalidate it.
	return dnssdEvent{InterfaceIndex: uint32(event.interface_index), Name: C.GoString(&event.name[0]), Type: uint16(event._type), Class: uint16(event.class_), Data: C.GoBytes(unsafe.Pointer(&event.data[0]), C.int(event.length)), TTL: uint32(event.ttl), Removed: event.removed != 0}, nil
}
func (b *nativeDNSServiceBackend) Close() error {
	b.once.Do(func() {
		b.mu.Lock()
		b.closing = true
		b.mu.Unlock()
		C.soba_dnssd_stop(b.owner)
		// No Go lock is held across daemon teardown or queue/call joins.
		C.soba_dnssd_join(b.owner)
		b.calls.Wait()
		C.soba_dnssd_free(b.owner)
	})
	return nil
}
func dnssdNativeError(r C.struct_soba_dnssd_result) error {
	if r.kind == 0 {
		return nil
	}
	if r.kind == 7 {
		return context.Canceled
	}
	if r.kind == 6 {
		return ErrReceiveBudget
	}
	stages := [...]string{"unknown", "create_connection", "schedule", "registration", "query", "callback", "admission"}
	kinds := [...]string{"none", "unavailable", "permission_denied", "name_conflict", "invalid_argument", "unsupported"}
	stage, kind := "unknown", "unavailable"
	if int(r.stage) > 0 && int(r.stage) < len(stages) {
		stage = stages[int(r.stage)]
	}
	if int(r.kind) > 0 && int(r.kind) < len(kinds) {
		kind = kinds[int(r.kind)]
	}
	return dnssdFailure{stage, kind}
}
