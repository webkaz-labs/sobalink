package lanlink

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type partialTrafficWriter struct{}

func (partialTrafficWriter) Write(p []byte) (int, error) { return 3, io.ErrShortWrite }

func TestTrafficWriterCountsSuccessfulBytesOnly(t *testing.T) {
	var counted atomic.Uint64
	w := trafficCountWriter{partialTrafficWriter{}, &counted}
	n, err := w.Write([]byte("fixture"))
	if n != 3 || !errors.Is(err, io.ErrShortWrite) || counted.Load() != 3 {
		t.Fatal("partial write was not counted exactly")
	}
}

func TestTrafficWriterCountsEachCopiedByteOnce(t *testing.T) {
	var counted atomic.Uint64
	var destination bytes.Buffer
	n, err := io.Copy(trafficCountWriter{&destination, &counted}, bytes.NewReader(make([]byte, 1024)))
	if err != nil || n != 1024 || counted.Load() != 1024 || destination.Len() != 1024 {
		t.Fatal("copy boundary was double-counted or lost bytes")
	}
}

func TestTrafficWriterParallelCounters(t *testing.T) {
	var counted atomic.Uint64
	var workers sync.WaitGroup
	for range 16 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			w := trafficCountWriter{io.Discard, &counted}
			for range 100 {
				if _, err := w.Write(make([]byte, 64)); err != nil {
					t.Error("count-only writer failed")
				}
			}
		}()
	}
	workers.Wait()
	if counted.Load() != 16*100*64 {
		t.Fatal("parallel bytes were lost")
	}
}

func TestTrafficPhaseAccountsForBothRelayLegsAndEcho(t *testing.T) {
	p, err := summarizeTraffic("bulk", trafficSnapshot{toRelay: 50, fromRelay: 50}, trafficSnapshot{toRelay: 1250, fromRelay: 1250}, 2*time.Second, time.Second, 500, 500, 0)
	if err != nil || p.UsefulTotalBytes != 1000 || p.TwoLegPayloadBytes != 2000 || p.RelayTotalBytes != 2400 || p.RelayExcessBytes != 400 || *p.RelayPerUsefulByte != 2.4 || *p.RelayPerTwoLegPayload != 1.2 {
		t.Fatal("two-hop payload denominator is incorrect")
	}
	if p.RelayBytesPerSecond != 1200 || p.UsefulBytesPerSecond != 1000 {
		t.Fatal("observation and payload durations were conflated")
	}
}

func TestTrafficIdleDoesNotInventPayloadRatios(t *testing.T) {
	p, err := summarizeTraffic("idle", trafficSnapshot{}, trafficSnapshot{toRelay: 40, fromRelay: 60}, 10*time.Second, 0, 0, 0, 0)
	if err != nil || p.RelayPerUsefulByte != nil || p.RelayPerTwoLegPayload != nil || p.RelayBytesPerSecond != 10 {
		t.Fatal("idle measurement must report bytes per second without a payload ratio")
	}
	encoded, err := json.Marshal(p)
	if err != nil || !bytes.Contains(encoded, []byte(`"relay_per_useful_byte":null`)) {
		t.Fatal("undefined idle ratio did not remain null")
	}
}

func TestTrafficPhaseRefusesInvalidCounterIntervals(t *testing.T) {
	if _, err := summarizeTraffic("bad", trafficSnapshot{toRelay: 10}, trafficSnapshot{}, time.Second, 0, 0, 0, 0); err == nil {
		t.Fatal("backward counter accepted")
	}
	if _, err := summarizeTraffic("bad", trafficSnapshot{}, trafficSnapshot{}, time.Second, 2*time.Second, 0, 0, 0); err == nil {
		t.Fatal("payload duration exceeds observation")
	}
}

func TestTrafficPatternIndependentOfReadChunks(t *testing.T) {
	first, second := &trafficPattern{}, &trafficPattern{}
	one := make([]byte, 1234)
	first.Read(one)
	var split bytes.Buffer
	for _, size := range []int{20, 400, 814} {
		part := make([]byte, size)
		second.Read(part)
		split.Write(part)
	}
	if !bytes.Equal(one, split.Bytes()) {
		t.Fatal("synthetic payload depends on read chunking")
	}
}
