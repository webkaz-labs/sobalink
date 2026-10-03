//go:build !linux && !darwin && !windows

package diskspace

import "os"

func available(*os.File) (uint64, error) { return 0, ErrUnknown }

func nativeNoSpace(error) bool { return false }
