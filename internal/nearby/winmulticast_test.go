package nearby

import (
	"errors"
	"reflect"
	"testing"
)

func TestWindowsMulticastIndexEncodingAndBounds(t *testing.T) {
	for index, want := range map[int][4]byte{1: {0, 0, 0, 1}, 255: {0, 0, 0, 255}, 256: {0, 0, 1, 0}, 0x123456: {0, 0x12, 0x34, 0x56}, 0xffffff: {0, 255, 255, 255}} {
		got, err := windowsMulticastIndex(index)
		if err != nil || got != want {
			t.Fatal("interface index changed byte order or value")
		}
	}
	for _, index := range []int{-1, 0, 0x1000000, int(^uint(0) >> 1)} {
		if _, err := windowsMulticastIndex(index); !errors.Is(err, ErrUnsupported) {
			t.Fatal("default or unrepresentable interface accepted")
		}
		scope := testScope()
		scope.InterfaceIndex = index
		err := configureWindowsMulticast(scope, windowsMulticastOps{
			setInterface: func([4]byte) error { t.Fatal("invalid index set interface"); return nil },
			joinGroup:    func([4]byte, [4]byte) error { t.Fatal("invalid index joined group"); return nil },
		})
		if !errors.Is(err, ErrUnsupported) {
			t.Fatal("invalid index opened multicast scope")
		}
	}
}

func TestWindowsMulticastOptionsDistinguishDuplicateLocalAddresses(t *testing.T) {
	for _, index := range []int{7, 8} {
		scope := testScope()
		scope.InterfaceIndex = index
		var steps []string
		var sent, joined, group [4]byte
		if err := configureWindowsMulticast(scope, windowsMulticastOps{
			setInterface: func(value [4]byte) error { steps = append(steps, "interface"); sent = value; return nil },
			joinGroup: func(g, value [4]byte) error {
				steps = append(steps, "membership")
				group, joined = g, value
				return nil
			},
		}); err != nil {
			t.Fatal(err)
		}
		want := [4]byte{0, 0, 0, byte(index)}
		if sent != want || joined != want || group != multicast4.As4() || !reflect.DeepEqual(steps, []string{"interface", "membership"}) {
			t.Fatal("duplicate local address selected wrong interface or widened membership")
		}
	}
	for _, fail := range []string{"interface", "membership"} {
		var steps []string
		failure := errors.New("synthetic private option failure")
		err := configureWindowsMulticast(testScope(), windowsMulticastOps{
			setInterface: func([4]byte) error {
				steps = append(steps, "interface")
				if fail == "interface" {
					return failure
				}
				return nil
			},
			joinGroup: func([4]byte, [4]byte) error { steps = append(steps, "membership"); return failure },
		})
		want := []string{"interface"}
		if fail == "membership" {
			want = append(want, "membership")
		}
		if !errors.Is(err, failure) || !reflect.DeepEqual(steps, want) {
			t.Fatal("option failure was swallowed or retried")
		}
	}
}
