package main

import (
	"errors"
	"reflect"
	"testing"

	"golang.org/x/sys/windows"
)

func TestQRConsoleModeIsScopedAndRestored(t *testing.T) {
	for _, original := range []uint32{0, windows.ENABLE_WRAP_AT_EOL_OUTPUT, windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING | windows.ENABLE_PROCESSED_OUTPUT} {
		var written []uint32
		restore, err := prepareQRConsole(
			func(mode *uint32) error { *mode = original; return nil },
			func(mode uint32) error { written = append(written, mode); return nil },
		)
		if err != nil {
			t.Fatal(err)
		}
		restore()
		updated := original | windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING | windows.ENABLE_PROCESSED_OUTPUT
		var want []uint32
		if updated != original {
			want = []uint32{updated, original}
		}
		if !reflect.DeepEqual(written, want) {
			t.Fatalf("modes written %v, want %v", written, want)
		}
	}
}

func TestQRConsoleModeFailureUsesSafeFallback(t *testing.T) {
	failure := errors.New("unsupported mode")
	for _, failRead := range []bool{false, true} {
		writes := 0
		restore, err := prepareQRConsole(
			func(mode *uint32) error {
				if failRead {
					return failure
				}
				*mode = 0
				return nil
			},
			func(uint32) error { writes++; return failure },
		)
		if err == nil || restore != nil || failRead && writes != 0 || !failRead && writes != 1 {
			t.Fatalf("failed console mode setup: restore %v, err %v, writes %d", restore != nil, err, writes)
		}
	}
}
