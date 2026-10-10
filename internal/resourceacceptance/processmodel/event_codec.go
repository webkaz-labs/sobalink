//go:build resource_process_native

package processmodel

import "encoding/binary"

type EventBatch struct {
	Events [MaxBatchEvents]Event
	Count  uint16
}

type EncodedEvents struct {
	Bytes  [MaxEventPayload]byte
	Length uint16
}

// EncodeEventBatch copies only a bounded, canonical contiguous sequence.
func EncodeEventBatch(mode Mode, events []Event) (EncodedEvents, bool) {
	var encoded EncodedEvents
	if mode.capacity() == 0 || len(events) == 0 || len(events) > MaxBatchEvents {
		return encoded, false
	}
	for i, event := range events {
		if !validEvent(mode, event) || i > 0 && event.Sequence != events[i-1].Sequence+1 {
			return EncodedEvents{}, false
		}
		encodeEvent(encoded.Bytes[4+i*EventSize:4+(i+1)*EventSize], event)
	}
	binary.BigEndian.PutUint16(encoded.Bytes[:2], uint16(len(events)))
	encoded.Length = uint16(4 + len(events)*EventSize)
	return encoded, true
}

// DecodeEventBatch validates length/count before examining fixed-size values.
// No returned value aliases the caller's input bytes.
func DecodeEventBatch(mode Mode, input []byte) (EventBatch, bool) {
	var batch EventBatch
	if mode.capacity() == 0 || len(input) < 4 || len(input) > MaxEventPayload || input[2] != 0 || input[3] != 0 {
		return batch, false
	}
	count := int(binary.BigEndian.Uint16(input[:2]))
	if count == 0 || count > MaxBatchEvents || len(input) != 4+count*EventSize {
		return batch, false
	}
	for i := 0; i < count; i++ {
		event, ok := decodeEvent(mode, input[4+i*EventSize:4+(i+1)*EventSize])
		if !ok || i > 0 && event.Sequence != batch.Events[i-1].Sequence+1 {
			return EventBatch{}, false
		}
		batch.Events[i] = event
	}
	batch.Count = uint16(count)
	return batch, true
}

func encodeEvent(out []byte, event Event) {
	binary.BigEndian.PutUint64(out[:8], event.Sequence)
	v := event.Observation
	out[8], out[9], out[10], out[11] = byte(v.Kind), byte(v.Action), byte(v.Command), byte(v.Outcome)
	copy(out[16:32], v.RunID[:])
	copy(out[32:64], v.OperationID[:])
	copy(out[64:96], v.RequestDigest[:])
	binary.BigEndian.PutUint64(out[96:104], v.BarrierOrdinal)
}

func decodeEvent(mode Mode, input []byte) (Event, bool) {
	var event Event
	if len(input) != EventSize || input[12] != 0 || input[13] != 0 || input[14] != 0 || input[15] != 0 {
		return event, false
	}
	event.Sequence = binary.BigEndian.Uint64(input[:8])
	v := &event.Observation
	v.Kind, v.Action, v.Command, v.Outcome = Kind(input[8]), Action(input[9]), Command(input[10]), Outcome(input[11])
	copy(v.RunID[:], input[16:32])
	copy(v.OperationID[:], input[32:64])
	copy(v.RequestDigest[:], input[64:96])
	v.BarrierOrdinal = binary.BigEndian.Uint64(input[96:104])
	if !validEvent(mode, event) {
		return Event{}, false
	}
	return event, true
}
