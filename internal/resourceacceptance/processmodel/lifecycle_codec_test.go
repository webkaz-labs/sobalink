//go:build resource_process_native

package processmodel

import "testing"

func TestLifecycleCodecClosedShapes(t *testing.T) {
	if EventSize != 104 {
		t.Fatal("lifecycle extension changed wire width")
	}
	for kind := OwnerLockBound; kind <= OwnersClosed; kind++ {
		value := Observation{Kind: kind}
		if kind == OwnersClosed {
			value.Outcome = Success
		}
		event := Event{Sequence: 1, Observation: value}
		encoded, ok := EncodeEventBatch(Owner, []Event{event})
		if !ok || int(encoded.Length) != 4+EventSize {
			t.Fatal("canonical lifecycle event rejected")
		}
		decoded, ok := DecodeEventBatch(Owner, encoded.Bytes[:encoded.Length])
		if !ok || decoded.Count != 1 || decoded.Events[0] != event {
			t.Fatal("lifecycle event changed in roundtrip")
		}
		if _, ok := EncodeEventBatch(CLI, []Event{event}); ok {
			t.Fatal("CLI encoded an owner lifecycle event")
		}
		if _, ok := DecodeEventBatch(CLI, encoded.Bytes[:encoded.Length]); ok {
			t.Fatal("CLI decoded an owner lifecycle event")
		}
		for _, offset := range []int{9, 10, 12, 13, 14, 15, 16, 32, 64, 96} {
			bad := encoded
			bad.Bytes[4+offset] = 1
			if _, ok := DecodeEventBatch(Owner, bad.Bytes[:bad.Length]); ok {
				t.Fatal("lifecycle event accepted a noncanonical foreign field")
			}
		}
		if kind != OwnersClosed {
			bad := encoded
			bad.Bytes[4+11] = byte(Success)
			if _, ok := DecodeEventBatch(Owner, bad.Bytes[:bad.Length]); ok {
				t.Fatal("lifecycle event accepted an unused outcome")
			}
		}
	}
	for _, outcome := range []Outcome{0, TypedError, 255} {
		if _, ok := EncodeEventBatch(Owner, []Event{{Sequence: 1, Observation: Observation{Kind: OwnersClosed, Outcome: outcome}}}); ok {
			t.Fatal("owner closure accepted an invalid outcome")
		}
	}
	if _, ok := EncodeEventBatch(Owner, []Event{{Sequence: 1, Observation: Observation{Kind: OwnersClosed, Outcome: Failure}}}); !ok {
		t.Fatal("actual failed close cannot be represented")
	}
}

func TestLifecycleEventsShareCategoryAndStreamBounds(t *testing.T) {
	r := newRecorder(t, Owner)
	for i := 0; i < MaxLifecycleEvents; i++ {
		r.Record(Observation{Kind: WebOpened})
	}
	if p := r.PrefixThrough(MaxLifecycleEvents); !p.Complete || p.LifecycleCount != MaxLifecycleEvents {
		t.Fatal("lifecycle category was not counted")
	}
	r.Record(Observation{Kind: IPCReady})
	if p := r.PrefixThrough(MaxLifecycleEvents); p.Complete || p.Failures&Overflow == 0 {
		t.Fatal("lifecycle extension escaped recorder bound")
	}
	launch, key := [16]byte{1}, [32]byte{2}
	d, ok := NewDecoder(Owner, launch, key)
	if !ok {
		t.Fatal("decoder setup failed")
	}
	feedDecoderEvents(t, d, launch, key, Observation{Kind: WebOpened}, MaxLifecycleEvents)
	frame, ok := EncodeEventFrame(Owner, launch, key, d.nextFrame, []Event{{Sequence: MaxLifecycleEvents + 1, Observation: Observation{Kind: IPCReady}}})
	if !ok {
		t.Fatal("individually valid event could not be encoded")
	}
	if _, ok := d.Decode(frame.Bytes[:frame.Length]); ok || !d.Failed() {
		t.Fatal("new lifecycle kind escaped cumulative decoder bound")
	}
}
