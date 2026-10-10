//go:build resource_process_native

package processmodel

import (
	"encoding/binary"
	"strings"
	"testing"
)

func TestCanonicalIdentifiers(t *testing.T) {
	runText := "0123456789abcdef0123456789abcdef"
	operationText := runText + runText
	run, ok := ParseRunID(runText)
	if !ok || run[0] != 1 || run[15] != 0xef {
		t.Fatal("canonical run ID was not decoded")
	}
	operation, ok := ParseOperationID(operationText)
	if !ok || operation[0] != 1 || operation[31] != 0xef {
		t.Fatal("canonical operation ID was not decoded")
	}
	for _, value := range []string{"", runText[:31], runText + "0", strings.ToUpper(runText), "g" + runText[1:], " " + runText[1:]} {
		if got, ok := ParseRunID(value); ok || got != [16]byte{} {
			t.Error("invalid run ID retained input or was accepted")
		}
	}
	for _, value := range []string{"", operationText[:63], operationText + "0", strings.ToUpper(operationText), "g" + operationText[1:]} {
		if got, ok := ParseOperationID(value); ok || got != [32]byte{} {
			t.Error("invalid operation ID retained input or was accepted")
		}
	}
	if value, ok := ParseRunID(strings.Repeat("0", 32)); !ok || value != [16]byte{} {
		t.Fatal("canonical zero run ID rejected")
	}
}

func allObservations() []Observation {
	run, operation, digest := [16]byte{1}, [32]byte{2}, [32]byte{3}
	values := []Observation{
		{Kind: GroupAccepted, RunID: run},
		{Kind: GroupIntent, Action: Apply, RunID: run, OperationID: operation},
		{Kind: ProviderAdmitted, Action: Apply, OperationID: operation},
		{Kind: IPCConnectAttempt}, {Kind: IPCConnectCompleted}, {Kind: MaintenancePassed},
		{Kind: Barrier, BarrierOrdinal: 1}, {Kind: Barrier, BarrierOrdinal: 32},
	}
	for kind := ManagementClientInvoked; kind <= ManagementFrameWritten; kind++ {
		for action := Inspect; action <= Status; action++ {
			v := Observation{Kind: kind, Action: action}
			if action == Apply || action == Status {
				v.OperationID = operation
			}
			values = append(values, v)
		}
	}
	for command := ControlLimits; command <= GroupStatus; command++ {
		values = append(values, Observation{Kind: LocalCommand, Command: command, RequestDigest: digest}, Observation{Kind: IPCRequestDispatched, Command: command})
	}
	for outcome := Success; outcome <= Failure; outcome++ {
		values = append(values, Observation{Kind: EntryReturned, Outcome: outcome})
	}
	return values
}

func TestEventCodecRoundTrip(t *testing.T) {
	for _, value := range allObservations() {
		event := Event{Sequence: 1, Observation: value}
		encoded, ok := EncodeEventBatch(Owner, []Event{event})
		if !ok {
			t.Fatalf("valid observation rejected: kind %d", value.Kind)
		}
		decoded, ok := DecodeEventBatch(Owner, encoded.Bytes[:encoded.Length])
		if !ok || decoded.Count != 1 || decoded.Events[0] != event {
			t.Fatalf("event round trip failed: kind %d", value.Kind)
		}
		encoded.Bytes[4+16] ^= 1
		if decoded.Events[0] != event {
			t.Fatal("decoded event aliases input")
		}
	}
	var events [MaxBatchEvents]Event
	for i := range events {
		events[i] = Event{Sequence: uint64(i + 1), Observation: Observation{Kind: MaintenancePassed}}
	}
	encoded, ok := EncodeEventBatch(Owner, events[:])
	if !ok || int(encoded.Length) != MaxEventPayload {
		t.Fatal("exact maximum batch rejected")
	}
	events[0].Observation.Kind = 255
	decoded, ok := DecodeEventBatch(Owner, encoded.Bytes[:encoded.Length])
	if !ok || decoded.Count != MaxBatchEvents || decoded.Events[0].Observation.Kind != MaintenancePassed {
		t.Fatal("encoder retained caller input")
	}
	cliEvent := Event{Sequence: MaxCLIEvents, Observation: Observation{Kind: Barrier, BarrierOrdinal: 4}}
	if encoded, ok := EncodeEventBatch(CLI, []Event{cliEvent}); !ok {
		t.Fatal("CLI exact sequence/barrier bound rejected")
	} else if decoded, ok := DecodeEventBatch(CLI, encoded.Bytes[:encoded.Length]); !ok || decoded.Events[0] != cliEvent {
		t.Fatal("CLI round trip failed")
	}
}

func TestEventCodecRejectsMalformed(t *testing.T) {
	base := Event{Sequence: 1, Observation: Observation{Kind: GroupAccepted, RunID: [16]byte{1}}}
	encoded, ok := EncodeEventBatch(Owner, []Event{base})
	if !ok {
		t.Fatal("valid base rejected")
	}
	for length := 0; length < int(encoded.Length); length++ {
		if _, ok := DecodeEventBatch(Owner, encoded.Bytes[:length]); ok {
			t.Fatalf("truncation accepted at %d", length)
		}
	}
	for _, mutation := range []struct {
		offset int
		value  byte
	}{
		{0, 255}, {1, 0}, {2, 1}, {3, 1}, {4 + 7, 0}, {4 + 8, 255},
		{4 + 9, byte(Apply)}, {4 + 10, byte(ControlLimits)}, {4 + 11, byte(Success)},
		{4 + 12, 1}, {4 + 13, 1}, {4 + 14, 1}, {4 + 15, 1},
		{4 + 32, 1}, {4 + 64, 1}, {4 + 103, 1},
	} {
		bad := encoded
		bad.Bytes[mutation.offset] = mutation.value
		if _, ok := DecodeEventBatch(Owner, bad.Bytes[:bad.Length]); ok {
			t.Errorf("noncanonical field accepted at %d", mutation.offset)
		}
	}
	if _, ok := DecodeEventBatch(Owner, encoded.Bytes[:encoded.Length+1]); ok {
		t.Error("trailing byte accepted")
	}
	if _, ok := DecodeEventBatch(Owner, make([]byte, MaxEventPayload+1)); ok {
		t.Error("oversized batch accepted")
	}
	if _, ok := DecodeEventBatch(Mode(0), encoded.Bytes[:encoded.Length]); ok {
		t.Error("unknown mode accepted")
	}
	for _, sequence := range []uint64{0, MaxOwnerEvents + 1, ^uint64(0)} {
		bad := encoded
		binary.BigEndian.PutUint64(bad.Bytes[4:12], sequence)
		if _, ok := DecodeEventBatch(Owner, bad.Bytes[:bad.Length]); ok {
			t.Error("invalid sequence accepted")
		}
	}
	for _, events := range [][]Event{nil, {base, base}, {base, {Sequence: 3, Observation: base.Observation}}, make([]Event, MaxBatchEvents+1)} {
		if _, ok := EncodeEventBatch(Owner, events); ok {
			t.Error("invalid batch encoded")
		}
	}
	invalid := []Observation{
		{Kind: GroupIntent}, {Kind: ProviderAdmitted, Action: Inspect},
		{Kind: ManagementClientInvoked}, {Kind: ManagementFrameWritten, Action: Action(255)},
		{Kind: ManagementFrameAttempted, Action: Inspect, OperationID: [32]byte{1}},
		{Kind: IPCConnectAttempt, Command: ControlLimits}, {Kind: LocalCommand},
		{Kind: IPCRequestDispatched, Command: Command(255)},
		{Kind: IPCRequestDispatched, Command: LocalStatus, RequestDigest: [32]byte{1}},
		{Kind: MaintenancePassed, RunID: [16]byte{1}}, {Kind: Barrier}, {Kind: Barrier, BarrierOrdinal: 33},
		{Kind: EntryReturned}, {Kind: EntryReturned, Outcome: Outcome(255)}, {Kind: Kind(255)},
	}
	for _, value := range invalid {
		if _, ok := EncodeEventBatch(Owner, []Event{{Sequence: 1, Observation: value}}); ok {
			t.Errorf("invalid observation encoded: kind %d", value.Kind)
		}
	}
	for _, value := range []Observation{{Kind: GroupAccepted}, {Kind: MaintenancePassed}, {Kind: Barrier, BarrierOrdinal: 5}} {
		event := Event{Sequence: 1, Observation: value}
		if _, ok := EncodeEventBatch(CLI, []Event{event}); ok {
			t.Error("CLI encoded owner-only observation")
		}
		ownerBytes, ok := EncodeEventBatch(Owner, []Event{event})
		if !ok {
			t.Fatal("owner-only observation invalid in Owner mode")
		}
		if _, ok := DecodeEventBatch(CLI, ownerBytes.Bytes[:ownerBytes.Length]); ok {
			t.Error("CLI decoded owner-only observation")
		}
	}
}
