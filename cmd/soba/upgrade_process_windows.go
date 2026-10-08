package main

import (
	"context"
	"errors"

	"golang.org/x/sys/windows"
)

type nativeUpgradeProcess struct{ handle windows.Handle }

func observeUpgradeProcess(pid int) (upgradeProcessObserver, error) {
	if pid <= 0 {
		return nil, errors.New("invalid process identity")
	}
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return nil, err
	}
	return &nativeUpgradeProcess{handle: h}, nil
}
func (p *nativeUpgradeProcess) Close() error { return windows.CloseHandle(p.handle) }
func (p *nativeUpgradeProcess) Wait(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		state, err := windows.WaitForSingleObject(p.handle, 100)
		if err != nil {
			return err
		}
		if state == windows.WAIT_OBJECT_0 {
			return nil
		}
		if state != uint32(windows.WAIT_TIMEOUT) {
			return errors.New("process exit observation failed")
		}
	}
}
