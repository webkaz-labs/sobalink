//go:build linux

package main

import (
	"context"
	"errors"

	"golang.org/x/sys/unix"
)

type nativeUpgradeProcess struct{ fd int }

func observeUpgradeProcess(pid int) (upgradeProcessObserver, error) {
	if pid <= 0 {
		return nil, errors.New("invalid process identity")
	}
	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		return nil, err
	}
	return &nativeUpgradeProcess{fd: fd}, nil
}
func (p *nativeUpgradeProcess) Close() error { return unix.Close(p.fd) }
func (p *nativeUpgradeProcess) Wait(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		fds := []unix.PollFd{{Fd: int32(p.fd), Events: unix.POLLIN}}
		_, err := unix.Poll(fds, 100)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return err
		}
		if fds[0].Revents&unix.POLLIN != 0 {
			return nil
		}
		if fds[0].Revents&(unix.POLLERR|unix.POLLNVAL) != 0 {
			return errors.New("process exit observation failed")
		}
	}
}
