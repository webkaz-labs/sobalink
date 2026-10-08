package config

import (
	"errors"
	"golang.org/x/sys/windows"
)

// Only native Windows access/sharing denial can make a live-handle rename
// fixture unavailable. Other filesystem errors must still fail the test.
func resourceOpenHandleRenameDenied(err error) bool {
	return errors.Is(err, windows.ERROR_ACCESS_DENIED) || errors.Is(err, windows.ERROR_SHARING_VIOLATION)
}
