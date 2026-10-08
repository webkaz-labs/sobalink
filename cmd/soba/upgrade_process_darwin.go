//go:build darwin

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
	fd, err := unix.Kqueue()
	if err != nil {
		return nil, err
	}
	unix.CloseOnExec(fd)
	_, err = unix.Kevent(fd, []unix.Kevent_t{{Ident: uint64(pid), Filter: unix.EVFILT_PROC, Flags: unix.EV_ADD | unix.EV_ENABLE | unix.EV_ONESHOT, Fflags: unix.NOTE_EXIT}}, nil, nil)
	if err != nil {
		unix.Close(fd)
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
		var events [1]unix.Kevent_t
		timeout := unix.Timespec{Nsec: 100000000}
		n, err := unix.Kevent(p.fd, nil, events[:], &timeout)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return err
		}
		if n != 0 {
			if events[0].Flags&unix.EV_ERROR != 0 {
				return errors.New("process exit observation failed")
			}
			if events[0].Fflags&unix.NOTE_EXIT != 0 {
				return nil
			}
		}
	}
}
