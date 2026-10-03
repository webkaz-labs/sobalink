package control

import (
	"errors"
	"fmt"
	"os"
	"testing"
)

func TestUnavailablePortableMissingStateDoesNotHidePermission(t *testing.T) {
	for _, err := range []error{os.ErrNotExist, fmt.Errorf("missing state: %w", os.ErrNotExist)} {
		if !Unavailable(err) {
			t.Fatal("missing state not recognized", err)
		}
	}
	for _, err := range []error{nil, os.ErrPermission, fmt.Errorf("permission: %w", os.ErrPermission), errors.New("other failure")} {
		if Unavailable(err) {
			t.Fatal("unrelated failure hidden", err)
		}
	}
}
