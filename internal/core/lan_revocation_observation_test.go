//go:build lanlink_integration

package core

import (
	"errors"
	"fmt"
	"io"
	"net"
	"testing"
)

type privateRevocationTimeout struct{}

func (privateRevocationTimeout) Error() string {
	panic("diagnostics must never render transport errors")
}
func (privateRevocationTimeout) Timeout() bool   { return true }
func (privateRevocationTimeout) Temporary() bool { return false }

func TestRevocationReadResultClosedCategories(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"success", nil, "none"},
		{"eof", io.EOF, "eof"},
		{"wrapped_eof", fmt.Errorf("synthetic-private: %w", io.EOF), "eof"},
		{"closed", net.ErrClosed, "closed"},
		{"timeout", privateRevocationTimeout{}, "timeout"},
		{"other", errors.New("synthetic-private-address-and-token"), "other"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := revocationReadResult(tc.err); got != tc.want {
				t.Fatalf("closed category = %q; want %q", got, tc.want)
			}
		})
	}
}
