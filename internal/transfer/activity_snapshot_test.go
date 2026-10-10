package transfer

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestActivitySnapshotScalarAllowlist(t *testing.T) {
	m := &Manager{batches: map[string]*batchState{"synthetic-batch": {value: Batch{ID: "synthetic-batch", Peer: Peer{ID: "synthetic-peer", Generation: 7}, State: Receiving, Destination: "synthetic-private-path", TotalBytes: 20, CompletedBytes: 5, Files: []FileStatus{{Entry: Entry{Path: "synthetic-secret-name"}, Error: "synthetic-private-error"}}}}}}
	got, err := m.ActivitySnapshot(context.Background(), 1)
	if err != nil || len(got) != 1 || got[0] != (ActivitySummary{"synthetic-batch", "synthetic-peer", Receiving, 20, 5}) {
		t.Fatal("unexpected scalar observation", err)
	}
	typeOf := reflect.TypeOf(ActivitySummary{})
	for _, forbidden := range []string{"Files", "Destination", "Error", "Generation", "Manifest", "Policy"} {
		if _, ok := typeOf.FieldByName(forbidden); ok {
			t.Fatal("private field in activity allowlist", forbidden)
		}
	}
	got[0].State = Completed
	if m.batches["synthetic-batch"].value.State != Receiving {
		t.Fatal("caller mutated manager")
	}
}

func TestActivitySnapshotLimitsClosedAndCancellation(t *testing.T) {
	m := &Manager{batches: map[string]*batchState{"a": {}, "b": {}}}
	for _, limit := range []int{-1, 0, 1} {
		if rows, err := m.ActivitySnapshot(context.Background(), limit); !errors.Is(err, ErrLimit) || rows != nil {
			t.Fatal("pre-allocation row bound not enforced")
		}
	}
	m.closed = true
	if _, err := m.ActivitySnapshot(context.Background(), 2); !errors.Is(err, ErrClosed) {
		t.Fatal("closed manager disclosed")
	}
	m.closed = false
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if rows, err := m.ActivitySnapshot(ctx, 2); !errors.Is(err, context.Canceled) || rows != nil {
		t.Fatal("cancelled capture returned data")
	}
}

func TestActivitySnapshotEmptyAndDeterministic(t *testing.T) {
	m := &Manager{batches: map[string]*batchState{}}
	rows, err := m.ActivitySnapshot(context.Background(), 2)
	if err != nil || rows == nil || len(rows) != 0 {
		t.Fatal("empty is not complete empty")
	}
	m.batches["b"] = &batchState{value: Batch{ID: "b"}}
	m.batches["a"] = &batchState{value: Batch{ID: "a"}}
	rows, err = m.ActivitySnapshot(context.Background(), 2)
	if err != nil || rows[0].ID != "a" || rows[1].ID != "b" {
		t.Fatal("order is not stable")
	}
}
